package mcpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/store"
)

const OAuthClientID = "alertbridge-dots"
const OAuthScope = "alerts:read"

type OAuth struct {
	eventsEnabled                              bool
	db                                         *store.Store
	issuer, resource, redirect, policy, routes string
	session                                    func(*http.Request) (string, bool)
	now                                        func() time.Time
}

func NewOAuth(db *store.Store, publicURL, routes, redirect string, session func(*http.Request) (string, bool)) (*OAuth, error) {
	if db == nil || session == nil {
		return nil, errors.New("MCP OAuth requires database and admin session verifier")
	}
	u, e := url.Parse(publicURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("MCP public URL must be an HTTPS origin")
	}
	if redirect == "" {
		redirect = "https://chatgpt.com/connector_platform_oauth_redirect"
	}
	r, e := url.Parse(redirect)
	if e != nil || r.Scheme != "https" || r.Host == "" || r.User != nil || r.Fragment != "" || r.RawQuery != "" {
		return nil, errors.New("MCP OAuth redirect must be an exact HTTPS callback")
	}
	issuer := strings.TrimSuffix(publicURL, "/")
	resource := issuer + "/mcp"
	return &OAuth{db: db, issuer: issuer, resource: resource, redirect: redirect, policy: tokenHash(resource + "\n" + routes), routes: routes, session: session, now: time.Now}, nil
}
func tokenHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func randomOAuthToken() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func oauthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func oauthError(w http.ResponseWriter, status int, code string) {
	oauthJSON(w, status, map[string]string{"error": code})
}

func (o *OAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'")
	switch r.URL.Path {
	case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
		if r.Method != "GET" {
			oauthError(w, 405, "invalid_request")
			return
		}
		oauthJSON(w, 200, map[string]any{"resource": o.resource, "authorization_servers": []string{o.issuer}, "scopes_supported": []string{OAuthScope}, "bearer_methods_supported": []string{"header"}})
	case "/.well-known/oauth-authorization-server":
		if r.Method != "GET" {
			oauthError(w, 405, "invalid_request")
			return
		}
		oauthJSON(w, 200, map[string]any{"issuer": o.issuer, "authorization_endpoint": o.issuer + "/admin/mcp/authorize", "token_endpoint": o.issuer + "/oauth/token", "revocation_endpoint": o.issuer + "/oauth/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{OAuthScope}, "authorization_response_iss_parameter_supported": true})
	case "/admin/mcp/authorize":
		o.authorize(w, r)
	case "/oauth/token", "/oauth/revoke":
		o.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

var consentTemplate = template.Must(template.New("consent").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>AlertBridge 只读授权</title><h1>允许 Dots 查询告警？</h1><p>客户端：alertbridge-dots</p><p>权限：查询告警、投递状态和活跃故障。无法修改配置或恢复故障。</p><p>{{if .Events}}新告警可通过单独创建的 MCP Events 订阅推送到已验证的回调地址；不允许修改故障。{{end}}</p><p>可见路由：{{.Routes}}</p><p>回调地址：{{.Redirect}}</p><form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><button name="decision" value="allow">允许只读访问</button> <button name="decision" value="deny">取消</button></form></html>`))

func (o *OAuth) authorize(w http.ResponseWriter, r *http.Request) {
	// Chromium applies form-action to the POST's redirect chain as well.
	// Permit only our configured callback, still checked by exact match below.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self' "+o.redirect+"; base-uri 'none'; frame-ancestors 'none'")
	if r.Method != "GET" && r.Method != "POST" {
		oauthError(w, 405, "invalid_request")
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		oauthError(w, 400, "invalid_request")
		return
	}
	q := r.URL.Query()
	for _, k := range []string{"client_id", "redirect_uri", "response_type", "code_challenge", "code_challenge_method", "resource", "scope", "state"} {
		if len(q[k]) > 1 {
			oauthError(w, 400, "invalid_request")
			return
		}
	}
	if q.Get("client_id") != OAuthClientID || q.Get("redirect_uri") != o.redirect || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(q.Get("code_challenge")) || q.Get("resource") != o.resource || q.Get("scope") != OAuthScope || len(q.Get("state")) > 1024 {
		oauthError(w, 400, "invalid_request")
		return
	}
	csrf, ok := o.session(r)
	if !ok {
		http.Redirect(w, r, "/admin/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = consentTemplate.Execute(w, struct {
			Routes, Redirect, CSRF string
			Events                 bool
		}{o.routes, o.redirect, csrf, o.eventsEnabled})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if e := r.ParseForm(); e != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(csrf)) != 1 {
		oauthError(w, 403, "access_denied")
		return
	}
	target, _ := url.Parse(o.redirect)
	out := target.Query()
	out.Set("state", q.Get("state"))
	out.Set("iss", o.issuer)
	if r.PostForm.Get("decision") != "allow" {
		out.Set("error", "access_denied")
	} else {
		code, e := randomOAuthToken()
		if e != nil {
			oauthError(w, 503, "temporarily_unavailable")
			return
		}
		if e = o.db.SaveOAuthCode(r.Context(), tokenHash(code), q.Get("code_challenge"), o.redirect, o.resource, o.policy, o.now()); e != nil {
			oauthError(w, 503, "temporarily_unavailable")
			return
		}
		out.Set("code", code)
	}
	target.RawQuery = out.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (o *OAuth) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		oauthError(w, 405, "invalid_request")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		oauthError(w, 415, "invalid_request")
		return
	}
	if e := o.db.RecordRateLimit(r.Context(), "mcp-oauth-token", o.now(), 60); e != nil {
		w.Header().Set("Retry-After", "60")
		oauthError(w, 429, "slow_down")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if e := r.ParseForm(); e != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	p := r.PostForm
	for _, values := range p {
		if len(values) != 1 {
			oauthError(w, 400, "invalid_request")
			return
		}
	}
	if p.Get("client_id") != OAuthClientID || p.Get("client_secret") != "" || r.Header.Get("Authorization") != "" {
		oauthError(w, 401, "invalid_client")
		return
	}
	if r.URL.Path == "/oauth/revoke" {
		if e := o.db.RevokeOAuthToken(r.Context(), tokenHash(p.Get("token"))); e != nil {
			oauthError(w, 503, "temporarily_unavailable")
			return
		}
		oauthJSON(w, 200, map[string]any{})
		return
	}
	if p.Get("resource") != o.resource || (p.Get("scope") != "" && p.Get("scope") != OAuthScope) {
		oauthError(w, 400, "invalid_target")
		return
	}
	access, e := randomOAuthToken()
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable")
		return
	}
	refresh, e := randomOAuthToken()
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable")
		return
	}
	switch p.Get("grant_type") {
	case "authorization_code":
		verifier := p.Get("code_verifier")
		if !regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`).MatchString(verifier) || p.Get("redirect_uri") != o.redirect {
			oauthError(w, 400, "invalid_grant")
			return
		}
		sum := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(sum[:])
		id, err := randomOAuthToken()
		if err != nil {
			oauthError(w, 503, "temporarily_unavailable")
			return
		}
		e = o.db.ExchangeOAuthCode(r.Context(), tokenHash(p.Get("code")), challenge, o.redirect, o.resource, o.policy, id, tokenHash(access), tokenHash(refresh), o.now())
	case "refresh_token":
		e = o.db.RotateOAuthRefresh(r.Context(), tokenHash(p.Get("refresh_token")), o.policy, tokenHash(access), tokenHash(refresh), o.now())
	default:
		oauthError(w, 400, "unsupported_grant_type")
		return
	}
	if e != nil {
		if errors.Is(e, store.ErrOAuthGrant) {
			oauthError(w, 400, "invalid_grant")
		} else {
			oauthError(w, 503, "temporarily_unavailable")
		}
		return
	}
	oauthJSON(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "refresh_token": refresh, "scope": OAuthScope})
}
