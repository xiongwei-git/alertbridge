package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const oidcCookie = "__Secure-alertbridge_oidc"

// OIDCConfig is loaded only from a server-side file, never from a browser.
type OIDCConfig struct {
	Issuer           string `json:"issuer"`
	ClientID         string `json:"client_id"`
	ClientSecret     string `json:"client_secret"`
	RedirectURL      string `json:"redirect_url"`
	AdminSubject     string `json:"admin_subject"`
	PasswordFallback bool   `json:"password_fallback"`
}

type oidcAttempt struct {
	binding               [32]byte
	nonce, verifier, next string
	oldSession            [32]byte
	expires               time.Time
}

type OIDC struct {
	cfg      OIDCConfig
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
	mu       sync.Mutex
	attempts map[[32]byte]oidcAttempt
}

func LoadOIDC(ctx context.Context, path string) (*OIDC, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open OIDC configuration")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o007 != 0 || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("OIDC configuration requires a regular file without world access or group write")
	}
	var cfg OIDCConfig
	dec := json.NewDecoder(io.LimitReader(f, 16385))
	dec.DisallowUnknownFields()
	if dec.Decode(&cfg) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid OIDC configuration")
	}
	return NewOIDC(ctx, cfg)
}

func NewOIDC(ctx context.Context, cfg OIDCConfig) (*OIDC, error) {
	issuer, e := url.Parse(cfg.Issuer)
	redirect, e2 := url.Parse(cfg.RedirectURL)
	if e != nil || e2 != nil || !secureURL(issuer) || !secureURL(redirect) || issuer.RawQuery != "" || redirect.RawQuery != "" || redirect.Path != "/auth/oidc/callback" || cfg.ClientID == "" || len(cfg.ClientSecret) < 32 || cfg.AdminSubject == "" {
		return nil, errors.New("invalid OIDC issuer, callback or identity configuration")
	}
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	ctx = oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, errors.New("OIDC discovery failed")
	}
	var discovery struct {
		JWKS string `json:"jwks_uri"`
	}
	if provider.Claims(&discovery) != nil {
		return nil, errors.New("invalid OIDC discovery")
	}
	for _, raw := range []string{provider.Endpoint().AuthURL, provider.Endpoint().TokenURL, discovery.JWKS} {
		u, err := url.Parse(raw)
		if err != nil || !secureURL(u) || u.Host != issuer.Host {
			return nil, errors.New("OIDC endpoints must use the configured HTTPS issuer host")
		}
	}
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInHeader
	return &OIDC{cfg: cfg, oauth: oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL, Endpoint: endpoint, Scopes: []string{oidc.ScopeOpenID, "profile"}}, verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID, SupportedSigningAlgs: []string{oidc.RS256}}), client: client, attempts: make(map[[32]byte]oidcAttempt)}, nil
}

func secureURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

func (h *Handler) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	o := h.cfg.OIDC
	if o == nil {
		http.NotFound(w, r)
		return
	}
	if !o.validHost(r) {
		h.oidcFailure(w, r, "origin")
		return
	}
	now := h.cfg.Now()
	if h.cfg.Database.RecordAdminLoginAttempt(r.Context(), now.UTC(), 10) != nil {
		h.oidcFailure(w, r, "rate_limit")
		return
	}
	state, e := randomToken(32)
	binding, e2 := randomToken(32)
	nonce, e3 := randomToken(32)
	if e != nil || e2 != nil || e3 != nil {
		h.oidcFailure(w, r, "random")
		return
	}
	a := oidcAttempt{binding: sha256.Sum256([]byte(binding)), nonce: nonce, verifier: oauth2.GenerateVerifier(), next: oauthLoginNext(r.URL.Query().Get("next")), expires: now.Add(5 * time.Minute)}
	if cookie, e := r.Cookie(sessionCookie); e == nil {
		a.oldSession = sha256.Sum256([]byte(cookie.Value))
	}
	o.mu.Lock()
	for k, v := range o.attempts {
		if !v.expires.After(now) {
			delete(o.attempts, k)
		}
	}
	if len(o.attempts) >= 256 {
		o.mu.Unlock()
		h.oidcFailure(w, r, "capacity")
		return
	}
	o.attempts[sha256.Sum256([]byte(state))] = a
	o.mu.Unlock()
	h.oidcAttemptCookie(w, binding, 300)
	http.Redirect(w, r, o.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(a.verifier)), http.StatusSeeOther)
}

func (o *OIDC) validHost(r *http.Request) bool {
	u, _ := url.Parse(o.cfg.RedirectURL)
	return r.Host == u.Host
}

func (h *Handler) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	o := h.cfg.OIDC
	if o == nil {
		http.NotFound(w, r)
		return
	}
	if !o.validHost(r) || len(r.URL.RawQuery) > 8192 {
		h.oidcFailure(w, r, "callback")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["state"]) != 1 || len(q["code"]) > 1 || len(q["error"]) > 1 {
		h.oidcFailure(w, r, "parameters")
		return
	}
	cookie, err := r.Cookie(oidcCookie)
	if err != nil || len(cookie.Value) != 43 || len(q.Get("state")) != 43 {
		h.oidcFailure(w, r, "state")
		return
	}
	key := sha256.Sum256([]byte(q.Get("state")))
	binding := sha256.Sum256([]byte(cookie.Value))
	o.mu.Lock()
	a, ok := o.attempts[key]
	if ok && subtle.ConstantTimeCompare(a.binding[:], binding[:]) == 1 {
		delete(o.attempts, key)
	} else {
		ok = false
	}
	o.mu.Unlock()
	h.oidcAttemptCookie(w, "", -1)
	if !ok || !a.expires.After(h.cfg.Now()) {
		h.oidcFailure(w, r, "state")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		h.oidcFailure(w, r, "authorization")
		return
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), o.client), 8*time.Second)
	defer cancel()
	token, err := o.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(a.verifier))
	if err != nil {
		h.oidcFailure(w, r, "exchange")
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		h.oidcFailure(w, r, "id_token")
		return
	}
	id, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		h.oidcFailure(w, r, "verification")
		return
	}
	if subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(a.nonce)) != 1 {
		h.oidcFailure(w, r, "nonce")
		return
	}
	if id.Issuer != o.cfg.Issuer || subtle.ConstantTimeCompare([]byte(id.Subject), []byte(o.cfg.AdminSubject)) != 1 {
		h.oidcFailure(w, r, "identity")
		return
	}
	// Reject a conflicting authorized party even when the audience includes us.
	var claims struct {
		AZP string `json:"azp"`
	}
	if id.Claims(&claims) != nil || (claims.AZP != "" && claims.AZP != o.cfg.ClientID) {
		h.oidcFailure(w, r, "authorized_party")
		return
	}
	session, err := randomToken(32)
	csrf, err2 := randomToken(24)
	if err != nil || err2 != nil {
		h.oidcFailure(w, r, "random")
		return
	}
	hash := sha256.Sum256([]byte(session))
	now := h.cfg.Now().UTC()
	if h.cfg.Database.CreateAdminSession(r.Context(), hash[:], csrf, now.Add(h.cfg.SessionLifetime), now) != nil {
		h.oidcFailure(w, r, "session")
		return
	}
	if a.oldSession != ([32]byte{}) {
		if h.cfg.Database.DeleteAdminSession(r.Context(), a.oldSession[:]) != nil {
			_ = h.cfg.Database.DeleteAdminSession(r.Context(), hash[:])
			h.oidcFailure(w, r, "session_rotation")
			return
		}
	}
	h.setSessionCookie(w, session, int(h.cfg.SessionLifetime.Seconds()))
	next := a.next
	if next == "" {
		next = "/admin/"
	}
	h.cfg.Logger.Info("admin OIDC login succeeded")
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (h *Handler) oidcAttemptCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: value, Path: "/auth/oidc", MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}
func (h *Handler) oidcFailure(w http.ResponseWriter, r *http.Request, category string) {
	h.cfg.Logger.Warn("admin OIDC login failed", "category", category)
	http.Redirect(w, r, "/admin/login?oidc_error=1", http.StatusSeeOther)
}
