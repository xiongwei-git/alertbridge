// Package client provides a fail-closed HTTPS client over authenticated EgressHub CONNECT.
// The gateway validates destination DNS; the client verifies original-host business TLS.
package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"egresshub/internal/credentials"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"time"
)

// Config contains paths, never private-key contents. Destinations are exact DNS names with :443.
// Client credentials are reloaded per connection from an atomic generation directory.
type Config struct {
	Address      string   `json:"address"`
	ServerName   string   `json:"server_name"`
	CAFile       string   `json:"ca_file"`
	CertFile     string   `json:"cert_file"`
	KeyFile      string   `json:"key_file"`
	Destinations []string `json:"destinations"`
}

// LoadConfig reads a private regular JSON file, rejecting unknown fields and trailing data.
// Relative certificate paths are relative to the process working directory.
func LoadConfig(path string) (Config, error) {
	var c Config
	f, err := privateFile(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return c, errors.New("egresshub: invalid configuration")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Config{}, errors.New("egresshub: trailing configuration data")
	}
	return c, nil
}
func privateFile(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("egresshub: private file unavailable")
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("egresshub: file must be regular and private (0600 or stricter)")
	}
	return f, nil
}

// NewHTTPClient creates an HTTPS-only, exact-destination client. It never uses proxy
// environment variables, follows redirects, falls back to direct access, or retries business requests.
// Do not mutate its Transport or CheckRedirect: these enforce the SDK policy.
func NewHTTPClient(c Config) (*http.Client, error) {
	return newHTTPClient(c, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}

func newHTTPClient(c Config, rawDial func(context.Context, string, string) (net.Conn, error)) (*http.Client, error) {
	h, p, err := net.SplitHostPort(c.Address)
	ip, iperr := netip.ParseAddr(h)
	port, perr := strconv.Atoi(p)
	if err != nil || iperr != nil || !public(ip) || perr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != p {
		return nil, errors.New("egresshub: endpoint must be a public literal IP and port")
	}
	if _, ok := destination(c.ServerName + ":443"); !ok {
		return nil, errors.New("egresshub: invalid gateway server name")
	}
	allowed := map[string]bool{}
	for _, d := range c.Destinations {
		if _, ok := destination(d); !ok {
			return nil, errors.New("egresshub: destinations require exact lowercase DNS names on 443")
		}
		allowed[d] = true
	}
	if len(allowed) == 0 {
		return nil, errors.New("egresshub: destinations required")
	}
	_, err = credentials.Load(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, err
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, errors.New("egresshub: CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("egresshub: invalid CA")
	}
	outer := &tls.Config{RootCAs: roots, GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		cert, err := credentials.Load(c.CertFile, c.KeyFile)
		return &cert, err
	}, ServerName: c.ServerName, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if !allowed[address] || network != "tcp" {
			return nil, errors.New("egresshub: destination rejected")
		}
		deadline := time.Now().Add(5 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		dialCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		raw, err := rawDial(dialCtx, "tcp", c.Address)
		if err != nil {
			return nil, errors.New("egresshub: gateway unavailable")
		}
		conn := tls.Client(raw, outer)
		ok := false
		defer func() {
			if !ok {
				conn.Close()
			}
		}()
		conn.SetDeadline(deadline)
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		defer stop()
		if conn.HandshakeContext(dialCtx) != nil {
			return nil, errors.New("egresshub: gateway TLS authentication failed")
		}
		if _, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", address, address); err != nil {
			return nil, errors.New("egresshub: CONNECT write failed")
		}
		reader := bufio.NewReader(conn)
		header := make([]byte, 0, 256)
		for len(header) < 8192 {
			b, err := reader.ReadByte()
			if err != nil {
				return nil, errors.New("egresshub: CONNECT response unavailable")
			}
			header = append(header, b)
			if len(header) >= 4 && string(header[len(header)-4:]) == "\r\n\r\n" {
				break
			}
		}
		if len(header) >= 8192 {
			return nil, errors.New("egresshub: CONNECT headers too large")
		}
		response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(header)), &http.Request{Method: http.MethodConnect})
		if err != nil || response.StatusCode != 200 {
			return nil, errors.New("egresshub: CONNECT rejected")
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if !stop() {
			return nil, context.Canceled
		}
		conn.SetDeadline(time.Time{})
		ok = true
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return &http.Client{Transport: &httpsOnly{transport: transport, allowed: allowed}, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

type httpsOnly struct {
	transport *http.Transport
	allowed   map[string]bool
}

func (t *httpsOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL == nil || r.URL.Scheme != "https" || r.URL.User != nil || r.URL.Opaque != "" || r.URL.Fragment != "" {
		return nil, errors.New("egresshub: HTTPS URL required")
	}
	authority := r.URL.Host
	if r.URL.Port() == "" {
		authority = net.JoinHostPort(r.URL.Hostname(), "443")
	}
	if !t.allowed[authority] || r.Host != "" && r.Host != r.URL.Host {
		return nil, errors.New("egresshub: destination rejected")
	}
	return t.transport.RoundTrip(r)
}
func (t *httpsOnly) CloseIdleConnections() { t.transport.CloseIdleConnections() }
