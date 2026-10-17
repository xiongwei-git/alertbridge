package admin

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

func TestOIDCFlowSecurity(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	badKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"success", "other_subject", "signature", "issuer", "audience", "expired_token", "nonce", "state", "binding", "expired_attempt", "authorization_error", "missing_token", "exchange_error", "duplicate_parameters", "azp"} {
		t.Run(scenario, func(t *testing.T) {
			h, _ := newTestHandler(t)
			h.cfg.SecureCookie = true
			var nonce, challenge string
			exchanges := 0
			var provider *httptest.Server
			provider = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/jwks" {
					_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
					return
				}
				exchanges++
				if scenario == "exchange_error" {
					http.Error(w, `{"error":"invalid_grant"}`, 400)
					return
				}
				_ = r.ParseForm()
				u, p, ok := r.BasicAuth()
				sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if !ok || u != "alertbridge" || p != strings.Repeat("s", 32) || r.Form.Get("redirect_uri") != "https://example.com/auth/oidc/callback" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
					t.Error("token request lost confidential client or PKCE binding")
				}
				if scenario == "missing_token" {
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "unused", "token_type": "Bearer"})
					return
				}
				claims := map[string]any{"iss": provider.URL, "sub": "registered-subject", "aud": "alertbridge", "nonce": nonce, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
				switch scenario {
				case "other_subject":
					claims["sub"] = "another-user"
				case "issuer":
					claims["iss"] = "https://other.invalid"
				case "audience":
					claims["aud"] = "other-client"
				case "expired_token":
					claims["exp"] = time.Now().Add(-time.Hour).Unix()
				case "nonce":
					claims["nonce"] = "wrong"
				case "azp":
					claims["azp"] = "other-client"
				}
				signingKey := key
				if scenario == "signature" {
					signingKey = badKey
				}
				signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: signingKey, KeyID: "test"}}, nil)
				if e != nil {
					t.Error(e)
					return
				}
				body, _ := json.Marshal(claims)
				signed, e := signer.Sign(body)
				if e != nil {
					t.Error(e)
					return
				}
				raw, e := signed.CompactSerialize()
				if e != nil {
					t.Error(e)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "unused-access-token", "refresh_token": "unused-refresh-token", "token_type": "Bearer", "id_token": raw})
			}))
			defer provider.Close()
			client := provider.Client()
			ctx := oidc.ClientContext(context.Background(), client)
			h.cfg.OIDC = &OIDC{cfg: OIDCConfig{Issuer: provider.URL, ClientID: "alertbridge", AdminSubject: "registered-subject", RedirectURL: "https://example.com/auth/oidc/callback"}, oauth: oauth2.Config{ClientID: "alertbridge", ClientSecret: strings.Repeat("s", 32), RedirectURL: "https://example.com/auth/oidc/callback", Endpoint: oauth2.Endpoint{AuthURL: provider.URL + "/authorize", TokenURL: provider.URL + "/token", AuthStyle: oauth2.AuthStyleInHeader}, Scopes: []string{"openid"}}, verifier: oidc.NewVerifier(provider.URL, oidc.NewRemoteKeySet(ctx, provider.URL+"/jwks"), &oidc.Config{ClientID: "alertbridge", SupportedSigningAlgs: []string{oidc.RS256}}), client: client, attempts: make(map[[32]byte]oidcAttempt)}
			var oldLogin *httptest.ResponseRecorder
			// Enable fallback only for constructing a pre-existing authenticated session.
			h.cfg.OIDC.cfg.PasswordFallback = true
			oldLogin = serve(h, "POST", "/admin/login", url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}, nil)
			old := oldLogin.Result().Cookies()[0]
			h.cfg.OIDC.cfg.PasswordFallback = false
			page := serve(h, "GET", "/admin/login", nil, nil)
			if page.Code != 200 || !strings.Contains(page.Body.String(), "使用 Ted 个人认证服务登录") || strings.Contains(page.Body.String(), `name="password"`) || page.Header().Get("Location") != "" {
				t.Fatal("transition page must await explicit click")
			}
			denied := serve(h, "POST", "/admin/login", url.Values{}, nil)
			if denied.Code != 403 {
				t.Fatal("fallback switch bypass")
			}
			start := serve(h, "GET", "/auth/oidc/login?next=https://evil.invalid", nil, old)
			location, e := url.Parse(start.Header().Get("Location"))
			if e != nil {
				t.Fatal(e)
			}
			q := location.Query()
			nonce = q.Get("nonce")
			challenge = q.Get("code_challenge")
			if start.Code != 303 || q.Get("code_challenge_method") != "S256" || nonce == "" || challenge == "" {
				t.Fatal("missing PKCE/nonce")
			}
			cookie := start.Result().Cookies()[0]
			if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/auth/oidc" {
				t.Fatal("unsafe transaction cookie")
			}
			callback := url.Values{"code": {"one-time-code"}, "state": {q.Get("state")}}
			switch scenario {
			case "state":
				callback.Set("state", strings.Repeat("x", 43))
			case "binding":
				cookie.Value = strings.Repeat("x", 43)
			case "expired_attempt":
				h.cfg.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
			case "authorization_error":
				callback.Set("error", "access_denied")
			case "duplicate_parameters":
				callback.Add("state", "another")
			}
			path := "/auth/oidc/callback?" + callback.Encode()
			result := serve(h, "GET", path, nil, cookie)
			var session *http.Cookie
			for _, c := range result.Result().Cookies() {
				if c.Name == sessionCookie {
					session = c
				}
			}
			if scenario != "success" {
				if session != nil || result.Header().Get("Location") != "/admin/login?oidc_error=1" {
					t.Fatalf("failure granted a session: %d", result.Code)
				}
				return
			}
			if session == nil || session.Value == old.Value || !session.Secure || !session.HttpOnly || strings.Contains(session.Value, "unused") || result.Header().Get("Location") != "/admin/" {
				t.Fatal("session or redirect unsafe")
			}
			if _, ok := h.authenticate(httptest.NewRequest("GET", "/admin/", nil)); ok {
				t.Fatal("unauthenticated request accepted")
			}
			if serve(h, "GET", "/admin/", nil, old).Code != 303 {
				t.Fatal("old session survived rotation")
			}
			dashboard := serve(h, "GET", "/admin/", nil, session)
			if dashboard.Code != 200 {
				t.Fatal("new session rejected")
			}
			if serve(h, "POST", "/admin/logout", url.Values{}, session).Code != 403 {
				t.Fatal("logout missing CSRF accepted")
			}
			logout := serve(h, "POST", "/admin/logout", url.Values{"csrf": {extractCSRF(t, dashboard.Body.String())}}, session)
			if logout.Header().Get("Location") != "/admin/login" || serve(h, "GET", "/admin/", nil, session).Code != 303 {
				t.Fatal("local logout failed")
			}
			replay := serve(h, "GET", path, nil, cookie)
			if replay.Header().Get("Location") != "/admin/login?oidc_error=1" || exchanges != 1 {
				t.Fatal("callback replay exchanged code")
			}
		})
	}
}

func TestOIDCConfigFailsClosed(t *testing.T) {
	for _, cfg := range []OIDCConfig{
		{}, {Issuer: "http://auth.invalid", RedirectURL: "https://example.com/auth/oidc/callback", ClientID: "a", ClientSecret: strings.Repeat("s", 32), AdminSubject: "sub"},
		{Issuer: "https://auth.invalid", RedirectURL: "https://example.com/auth/oidc/callback?next=evil", ClientID: "a", ClientSecret: strings.Repeat("s", 32), AdminSubject: "sub"},
	} {
		if _, err := NewOIDC(context.Background(), cfg); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}
