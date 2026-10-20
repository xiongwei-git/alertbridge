package mcpapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	egress "egresshub/client"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sdkConfig(t *testing.T) (string, egress.Config) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-client"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := x509.MarshalPKCS8PrivateKey(key)
	write := func(n string, b []byte) string {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	c := egress.Config{Address: "1.1.1.1:18444", ServerName: "egresshub.internal", CAFile: write("ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), CertFile: write("client.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), KeyFile: write("client.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k})), Destinations: []string{"callback.example:443"}}
	b, _ := json.Marshal(c)
	return write("client.json", b), c
}
func TestSDKAdapterOptInAndPolicy(t *testing.T) {
	e := &Events{hosts: map[string]bool{"callback.example": true}, client: &http.Client{}}
	original := e.client
	if err := e.ConfigureEgress(""); err != nil || e.client != original {
		t.Fatal("default changed")
	}
	path, cfg := sdkConfig(t)
	if err := e.ConfigureEgress(path); err != nil {
		t.Fatal(err)
	}
	if e.client == original {
		t.Fatal("SDK not installed")
	}
	if r, err := e.client.Get("http://callback.example/"); err == nil {
		r.Body.Close()
		t.Fatal("cleartext accepted")
	}
	old := e.client
	cfg.Destinations = []string{"unauthorized.example:443"}
	raw, _ := json.Marshal(cfg)
	os.WriteFile(path, raw, 0600)
	if err := e.ConfigureEgress(path); err == nil || e.client != old {
		t.Fatal("broader callback policy accepted")
	}
	os.Chmod(path, 0644)
	if err := e.ConfigureEgress(path); err == nil {
		t.Fatal("public config accepted")
	}
}
func TestSDKAdapterRequiresExplicitTargets(t *testing.T) {
	path, cfg := sdkConfig(t)
	cfg.Destinations = nil
	raw, _ := json.Marshal(cfg)
	os.WriteFile(path, raw, 0600)
	e := &Events{hosts: map[string]bool{"callback.example": true}, client: &http.Client{}}
	if err := e.ConfigureEgress(path); err == nil {
		t.Fatal("empty SDK targets accepted")
	}
}
