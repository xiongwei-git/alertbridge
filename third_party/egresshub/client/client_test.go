package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func certificates(t *testing.T) (tls.Certificate, tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	ca, e = x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	makeCert := func(serial int64, client bool) tls.Certificate {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
		if client {
			c.Subject.CommonName = "alertbridge-test"
			c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		} else {
			c.Subject.CommonName = "localhost"
			c.DNSNames = []string{"localhost"}
			c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		d, e := x509.CreateCertificate(rand.Reader, c, ca, &k.PublicKey, key)
		if e != nil {
			t.Fatal(e)
		}
		return tls.Certificate{Certificate: [][]byte{d}, PrivateKey: k}
	}
	return makeCert(2, false), makeCert(3, true), pool
}

func fixture(t *testing.T, reply string, serverName ...string) (Config, *http.Client, *atomic.Int32) {
	t.Helper()
	server, client, roots := certificates(t)
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	key, err := x509.MarshalPKCS8PrivateKey(client.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath := write("client.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: client.Certificate[0]}))
	keyPath := write("client.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	// Use the server leaf as a trust anchor for the gateway SAN test.
	caPath := write("ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate[0]}))
	count := &atomic.Int32{}
	inner := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "https://localhost/final")
			w.WriteHeader(302)
			return
		}
		w.Write([]byte("upstream-ok"))
	}))
	inner.TLS = &tls.Config{Certificates: []tls.Certificate{server}}
	inner.StartTLS()
	t.Cleanup(inner.Close)
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" {
			t.Error("expected CONNECT")
		}
		c, b, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		if reply != "" {
			b.WriteString(reply)
			b.Flush()
			return
		}
		up, err := net.Dial("tcp", inner.Listener.Addr().String())
		if err != nil {
			return
		}
		defer up.Close()
		b.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		b.Flush()
		done := make(chan struct{})
		go func() { io.Copy(up, b); close(done) }()
		io.Copy(c, up)
		c.Close()
		up.Close()
		<-done
	}))
	proxy.TLS = &tls.Config{Certificates: []tls.Certificate{server}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}
	proxy.StartTLS()
	t.Cleanup(proxy.Close)
	cfg := Config{Address: "1.1.1.1:18444", ServerName: "localhost", CAFile: caPath, CertFile: certPath, KeyFile: keyPath, Destinations: []string{"localhost:443", "wrong.example:443"}}
	if len(serverName) > 0 {
		cfg.ServerName = serverName[0]
	}
	cli, err := newHTTPClient(cfg, func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != cfg.Address {
			t.Error("unexpected endpoint")
		}
		return (&net.Dialer{}).DialContext(ctx, network, proxy.Listener.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	cli.Transport.(*httpsOnly).transport.TLSClientConfig.RootCAs = roots
	return cfg, cli, count
}
func TestDoubleTLSAndRedirectPolicy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	_, c, count := fixture(t, "")
	r, err := c.Get("https://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if string(body) != "upstream-ok" {
		t.Fatal(string(body))
	}
	r, err = c.Get("https://localhost/redirect")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 302 || count.Load() != 2 {
		t.Fatal("redirect followed")
	}
	if r, err = c.Get("https://wrong.example/"); err == nil {
		r.Body.Close()
		t.Fatal("inner TLS SAN bypassed")
	}
}
func TestRequestPolicy(t *testing.T) {
	_, c, count := fixture(t, "")
	for _, u := range []string{"http://localhost/", "https://evil.example/", "https://localhost:80/", "https://user:pass@localhost/", "https://127.0.0.1/"} {
		if r, err := c.Get(u); err == nil {
			r.Body.Close()
			t.Fatal("accepted " + u)
		}
	}
	req, _ := http.NewRequest("GET", "https://localhost/", nil)
	req.Host = "evil.example"
	if r, err := c.Do(req); err == nil {
		r.Body.Close()
		t.Fatal("host override accepted")
	}
	if count.Load() != 0 {
		t.Fatal("rejected request reached upstream")
	}
}
func TestRejectedAndMalformedCONNECT(t *testing.T) {
	for _, reply := range []string{"HTTP/1.1 403 Forbidden\r\n\r\n", "HTTP/1.1 2000 Bad\r\n\r\n", "HTTP/1.1 200 OK\r\nX: " + strings.Repeat("a", 8200) + "\r\n\r\n"} {
		_, c, _ := fixture(t, reply)
		if r, err := c.Get("https://localhost/"); err == nil {
			r.Body.Close()
			t.Fatal("invalid CONNECT accepted")
		}
	}
}
func TestConfigBoundaries(t *testing.T) {
	cfg, _, _ := fixture(t, "")
	for _, endpoint := range []string{"127.0.0.1:18444", "10.0.0.1:18444", "gateway.example:18444", "1.1.1.1:0", "1.1.1.1:65536"} {
		bad := cfg
		bad.Address = endpoint
		if _, err := NewHTTPClient(bad); err == nil {
			t.Fatal("invalid endpoint")
		}
	}
	bad := cfg
	bad.Destinations = nil
	if _, err := NewHTTPClient(bad); err == nil {
		t.Fatal("empty policy")
	}
	os.Chmod(cfg.KeyFile, 0644)
	if _, err := NewHTTPClient(cfg); err == nil {
		t.Fatal("public key permissions accepted")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	for _, raw := range []string{`{"unknown":1}`, `{} {}`, `{`} {
		os.WriteFile(path, []byte(raw), 0600)
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	os.Chmod(path, 0644)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("public config accepted")
	}
}
func TestNoFallbackAndCancellation(t *testing.T) {
	cfg, _, _ := fixture(t, "")
	var calls atomic.Int32
	c, err := newHTTPClient(cfg, func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://localhost/", nil)
	if r, err := c.Do(req); err == nil {
		r.Body.Close()
		t.Fatal("unavailable gateway succeeded")
	}
	if calls.Load() != 1 {
		t.Fatal("fallback/retry attempted")
	}
}

func TestGatewaySANVerification(t *testing.T) {
	_, c, _ := fixture(t, "", "wrong.example")
	if r, err := c.Get("https://localhost/"); err == nil {
		r.Body.Close()
		t.Fatal("wrong gateway SAN accepted")
	}
}
func TestPublicAddressPolicy(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "192.0.2.1", "169.254.169.254", "::1", "fd00::1", "2001:db8::1", "::ffff:10.0.0.1"} {
		if public(netip.MustParseAddr(s)) {
			t.Fatal("nonpublic endpoint accepted")
		}
	}
	for _, d := range []string{"*.example:443", "example.com:80", "127.0.0.1:443", "EXAMPLE.com:443", "example.com.:443"} {
		if _, ok := destination(d); ok {
			t.Fatal("invalid destination accepted")
		}
	}
}

func TestClientDoesNotCacheBrokenRotatedCredentials(t *testing.T) {
	cfg, c, count := fixture(t, "")
	r, err := c.Get("https://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	os.WriteFile(cfg.KeyFile, []byte("corrupt-rotated-key"), 0600)
	if r, err := c.Get("https://localhost/"); err == nil {
		r.Body.Close()
		t.Fatal("stale client identity was reused")
	}
	if count.Load() != 1 {
		t.Fatal("invalid generation reached upstream")
	}
}
