package mcpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/store"
)

func TestOAuthPKCEAndRefresh(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "oauth.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	loggedIn := true
	o, e := NewOAuth(db, "https://alerts.example", "ops", "", func(*http.Request) (string, bool) { return "test-csrf", loggedIn })
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	o.now = func() time.Time { return now }
	h, e := NewWithOAuth(db, "", "ops", o)
	if e != nil {
		t.Fatal(e)
	}
	send := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		var body string
		if form != nil {
			body = form.Encode()
		}
		r := httptest.NewRequest(method, "https://alerts.example"+path, strings.NewReader(body))
		if form != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		w := httptest.NewRecorder()
		o.ServeHTTP(w, r)
		return w
	}
	verifier := strings.Repeat("x", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	authURL := func() string {
		return "/admin/mcp/authorize?" + url.Values{"client_id": {OAuthClientID}, "redirect_uri": {o.redirect}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {challenge}, "scope": {OAuthScope}, "resource": {o.resource}, "state": {"client-state"}}.Encode()
	}
	if w := send("GET", "/.well-known/oauth-authorization-server", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "S256") {
		t.Fatal(w.Body.String())
	}
	if w := send("GET", "/.well-known/oauth-protected-resource/mcp", nil); w.Code != 200 || !strings.Contains(w.Body.String(), o.resource) {
		t.Fatal(w.Body.String())
	}
	loggedIn = false
	if w := send("GET", authURL(), nil); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/admin/login?next=") {
		t.Fatal(w.Code, w.Body.String())
	}
	loggedIn = true
	if w := send("GET", authURL(), nil); w.Code != 200 || !strings.Contains(w.Body.String(), "test-csrf") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("POST", authURL(), url.Values{"csrf": {"wrong"}, "decision": {"allow"}}); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := send("GET", strings.Replace(authURL(), url.QueryEscape(o.redirect), url.QueryEscape("https://evil.example/callback"), 1), nil); w.Code != 400 || w.Header().Get("Location") != "" {
		t.Fatal("redirect accepted")
	}
	code := func() string {
		w := send("POST", authURL(), url.Values{"csrf": {"test-csrf"}, "decision": {"allow"}})
		if w.Code != 303 {
			t.Fatal(w.Code, w.Body.String())
		}
		u, e := url.Parse(w.Header().Get("Location"))
		if e != nil {
			t.Fatal(e)
		}
		if u.Query().Get("iss") != o.issuer || u.Query().Get("state") != "client-state" {
			t.Fatal(u)
		}
		return u.Query().Get("code")
	}
	exchange := func(c, v string) *httptest.ResponseRecorder {
		return send("POST", "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {c}, "code_verifier": {v}, "client_id": {OAuthClientID}, "redirect_uri": {o.redirect}, "resource": {o.resource}})
	}
	c := code()
	if w := exchange(c, strings.Repeat("z", 43)); w.Code != 400 {
		t.Fatal("bad verifier")
	}
	w := exchange(c, verifier)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var tok map[string]any
	if e = json.Unmarshal(w.Body.Bytes(), &tok); e != nil {
		t.Fatal(e)
	}
	if w := exchange(c, verifier); w.Code != 400 {
		t.Fatal("code replay")
	}
	access := func(token string) int {
		r := httptest.NewRequest("POST", "https://alerts.example/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if access(tok["access_token"].(string)) != 200 {
		t.Fatal("access rejected")
	}
	now = now.Add(2 * time.Hour)
	if access(tok["access_token"].(string)) != 401 {
		t.Fatal("expired access accepted")
	}
	refresh := func(token string) *httptest.ResponseRecorder {
		return send("POST", "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {OAuthClientID}, "resource": {o.resource}})
	}
	w = refresh(tok["refresh_token"].(string))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var rotated map[string]any
	json.Unmarshal(w.Body.Bytes(), &rotated)
	if access(rotated["access_token"].(string)) != 200 {
		t.Fatal("rotated access rejected")
	}
	if w := refresh(tok["refresh_token"].(string)); w.Code != 400 {
		t.Fatal("refresh reuse accepted")
	}
	if access(rotated["access_token"].(string)) != 401 {
		t.Fatal("refresh replay did not revoke")
	}
	c = code()
	now = now.Add(6 * time.Minute)
	if w := exchange(c, verifier); w.Code != 400 {
		t.Fatal("expired code accepted")
	}
	c = code()
	w = exchange(c, verifier)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &tok)
	if w := send("POST", "/oauth/revoke", url.Values{"client_id": {OAuthClientID}, "token": {tok["refresh_token"].(string)}}); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if access(tok["access_token"].(string)) != 401 {
		t.Fatal("revocation failed")
	}
	c = code()
	w = exchange(c, verifier)
	json.Unmarshal(w.Body.Bytes(), &tok)
	if e := db.RevokeAllOAuth(t.Context()); e != nil {
		t.Fatal(e)
	}
	if access(tok["access_token"].(string)) != 401 {
		t.Fatal("admin revocation failed")
	}
}

func TestOAuthConfig(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "config.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	session := func(*http.Request) (string, bool) { return "", false }
	for _, origin := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/path", "https://example.com?x=y"} {
		if _, e := NewOAuth(db, origin, "ops", "", session); e == nil {
			t.Fatal(origin)
		}
	}
	if _, e := NewOAuth(db, "https://example.com", "ops", "https://user:pass@evil.example/callback", session); e == nil {
		t.Fatal("unsafe callback")
	}
	if _, e := NewOAuth(db, "https://example.com", "ops", "", session); e != nil {
		t.Fatal(e)
	}
}
