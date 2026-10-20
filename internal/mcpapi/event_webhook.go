package mcpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Reserved/non-public ranges beyond IsPrivate/IsGlobalUnicast (including transition networks).
var callbackBlockedRanges = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/96", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicCallbackIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range callbackBlockedRanges {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func validCallbackHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

var errCallbackTimeout = errors.New("callback timed out")

func callbackURL(raw string, hosts map[string]bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") || !hosts[strings.ToLower(u.Hostname())] {
		return nil, errors.New("callback must use HTTPS and an explicitly allowed host on port 443")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicCallbackIP(ip) {
		return nil, errors.New("callback address is not public")
	}
	return u, nil
}
func eventHTTPClient(hosts map[string]bool) *http.Client {
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 * 1024}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" || !hosts[strings.ToLower(host)] {
			return nil, errors.New("callback host unavailable")
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("callback DNS unavailable")
		}
		for _, ip := range addresses {
			if !publicCallbackIP(ip) {
				return nil, errors.New("callback DNS contains non-public address")
			}
		}
		// Dial the validated address directly. TLS still verifies the original URL hostname.
		dialer := net.Dialer{Timeout: 5 * time.Second}
		for _, ip := range addresses {
			conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
		}
		return nil, errors.New("callback connection unavailable")
	}
	return &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func signingKey(secret string) ([]byte, error) {
	if !strings.HasPrefix(secret, "whsec_") {
		return nil, errors.New("invalid signing secret")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil || len(key) < 24 || len(key) > 64 {
		return nil, errors.New("invalid signing secret")
	}
	return key, nil
}
func webhookSignature(secret, id, timestamp string, body []byte) (string, error) {
	key, err := signingKey(secret)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s.%s.", id, timestamp)
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (e *Events) post(ctx context.Context, credentials eventCredentials, subscriptionID, id string, body []byte) (*http.Response, error) {
	if len(body) > 256*1024 {
		return nil, errors.New("event payload too large")
	}
	if _, err := callbackURL(credentials.URL, e.hosts); err != nil {
		return nil, err
	}
	stamp := strconv.FormatInt(e.now().Unix(), 10)
	signature, err := webhookSignature(credentials.Secret, id, stamp, body)
	if err != nil {
		return nil, err
	}
	if credentials.OldSecret != "" && e.now().Before(credentials.RotateUntil) {
		old, err := webhookSignature(credentials.OldSecret, id, stamp, body)
		if err != nil {
			return nil, err
		}
		signature += " " + old
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, credentials.URL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid callback request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("webhook-id", id)
	req.Header.Set("webhook-timestamp", stamp)
	req.Header.Set("webhook-signature", signature)
	req.Header.Set("X-MCP-Subscription-Id", subscriptionID)
	response, err := e.client.Do(req)
	if err != nil {
		var timed net.Error
		if errors.As(err, &timed) && timed.Timeout() {
			return nil, errCallbackTimeout
		}
		return nil, errors.New("callback request failed")
	}
	return response, nil
}
