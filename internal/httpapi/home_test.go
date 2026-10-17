package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestPublicHomeAndRouteIsolation(t *testing.T) {
	h := New(Config{}) // The introduction needs neither credentials nor a database.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "了解项目，无需登录") {
		t.Fatalf("home: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("public home must be HTML without creating a login session")
	}
	_, rest, ok := strings.Cut(w.Body.String(), "<style>")
	style, _, end := strings.Cut(rest, "</style>")
	if !ok || !end {
		t.Fatal("missing embedded styles")
	}
	sum := sha256.Sum256([]byte(style))
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") || strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("home CSP: %s", csp)
	}
	head := httptest.NewRecorder()
	h.ServeHTTP(head, httptest.NewRequest("HEAD", "/?from=test", nil))
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
		t.Fatal("HEAD does not match GET")
	}
	for _, method := range []string{"POST", "PUT", "DELETE", "OPTIONS"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(method, "/", nil))
		if r.Code != 405 || r.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("%s root: %d", method, r.Code)
		}
	}
	for _, path := range []string{"/missing", "/index.html", "/static/service-home.html", "/admin/", "/mcp", "/oauth/token", "/.well-known/oauth-authorization-server"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != 404 {
			t.Fatalf("root captured disabled/unknown path %s: %d", path, r.Code)
		}
	}
	for _, path := range []string{"/api/v1/events", "/api/v1/notifications"} {
		r := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(r, req)
		if r.Code != http.StatusUnauthorized || strings.Contains(r.Header().Get("Content-Type"), "text/html") || strings.Contains(r.Header().Get("Content-Security-Policy"), "style-src") {
			t.Fatalf("API boundary changed %s: %d", path, r.Code)
		}
	}
}
