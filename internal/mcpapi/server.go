// Package mcpapi provides authenticated MCP 2.0 queries and event subscriptions.
package mcpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/store"
)

func New(db *store.Store, digest string, routesCSV string) (http.Handler, error) {
	return NewWithOAuth(db, digest, routesCSV, nil)
}

func NewWithOAuth(db *store.Store, digest string, routesCSV string, oauth *OAuth, eventOptions ...*Events) (http.Handler, error) {
	hash, err := hex.DecodeString(digest)
	if err != nil || (len(hash) != 32 && !(digest == "" && oauth != nil)) {
		return nil, errors.New("MCP token SHA256 must be 64 hexadecimal characters")
	}
	routes := strings.Split(routesCSV, ",")
	valid := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	for i := range routes {
		routes[i] = strings.TrimSpace(routes[i])
		if !valid.MatchString(routes[i]) {
			return nil, errors.New("MCP requires explicit comma-separated routing keys")
		}
	}
	tools, err := configureQueries(db, routes)
	if err != nil {
		return nil, err
	}
	var events *Events
	if len(eventOptions) > 0 {
		events = eventOptions[0]
	}
	if oauth != nil {
		oauth.eventsEnabled = events != nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !modernOriginAllowed(r, oauth) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		// Query-string credentials are deliberately never accepted.
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		sum := sha256.Sum256([]byte(token))
		validToken := len(hash) == 32 && subtle.ConstantTimeCompare(sum[:], hash) == 1
		rateKey := "mcp:" + digest
		owner := "static:" + digest
		if !validToken && oauth != nil && len(token) <= 256 {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			id, e := db.OAuthAccess(ctx, tokenHash(token), oauth.policy, oauth.now())
			cancel()
			validToken = e == nil
			if validToken {
				rateKey = "mcp-oauth:" + id
				owner = "oauth:" + id
			}
		}
		if len(auth) > 256 || !strings.HasPrefix(auth, "Bearer ") || !validToken {
			challenge := `Bearer realm="alertbridge-mcp"`
			if oauth != nil {
				challenge += `, resource_metadata="` + oauth.issuer + `/.well-known/oauth-protected-resource/mcp", scope="alerts:read"`
			}
			w.Header().Set("WWW-Authenticate", challenge)
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if e := db.RecordRateLimit(ctx, rateKey, time.Now().UTC(), 60); e != nil {
			if errors.Is(e, store.ErrRateLimit) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "rate limited", 429)
			} else {
				http.Error(w, "service unavailable", 503)
			}
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, "invalid request body", http.StatusRequestEntityTooLarge)
			return
		}
		serveModern(w, r.WithContext(ctx), owner, body, events, tools, oauth)
	}), nil
}
