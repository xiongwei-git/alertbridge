package channel

import (
	"crypto/tls"
	"net/http"
	"strings"
	"time"
)

func SecureHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.MaxIdleConns = 10
	transport.MaxIdleConnsPerHost = 2
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

func retryableHTTP(status int) bool {
	return status == 408 || status == 425 || status == 429 || status >= 500
}

func safeHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 200 {
		value = value[:200]
	}
	return value
}
