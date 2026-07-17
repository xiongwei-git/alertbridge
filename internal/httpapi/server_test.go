package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/auth"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

func TestEventEndpointAcceptsAndRejectsReplay(t *testing.T) {
	handler, now, secret := newTestHandler(t)
	body := validBody(now, "evt-1", "infrastructure")
	first := signedRequest(t, body, now, "nonce-0001", secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, first)
	if response.Code != http.StatusAccepted {
		t.Fatalf("first status = %d body=%s", response.Code, response.Body.String())
	}
	var accepted map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted["outcome"] != "queued" || accepted["event_id"] != "evt-1" {
		t.Fatalf("response = %+v", accepted)
	}

	replay := signedRequest(t, body, now, "nonce-0001", secret)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, replay)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestEventEndpointBoundaryErrors(t *testing.T) {
	handler, now, secret := newTestHandler(t)
	tests := []struct {
		name         string
		body         []byte
		route, nonce string
		want         int
	}{
		{name: "unknown field", body: append(validBody(now, "evt-2", "infrastructure")[:len(validBody(now, "evt-2", "infrastructure"))-1], []byte(`,"surprise":true}`)...), route: "infrastructure", nonce: "nonce-0002", want: 400},
		{name: "forbidden route", body: validBody(now, "evt-3", "proxy"), route: "proxy", nonce: "nonce-0003", want: 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := signedRequest(t, tt.body, now, tt.nonce, secret)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.want {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
		})
	}

	t.Run("tampered signature", func(t *testing.T) {
		body := validBody(now, "evt-4", "infrastructure")
		request := signedRequest(t, body, now, "nonce-0004", secret)
		request.Header.Set("X-Notify-Signature", fmt.Sprintf("%064d", 0))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestSimpleNotificationEndpointReturnsOKForBaotaCompatibility(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "alertbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	plain, publicID, secretHash, err := auth.GenerateBearerToken()
	if err != nil {
		t.Fatal(err)
	}
	token := auth.IngressToken{
		ID: "baota-prod", PublicID: publicID, SecretHash: secretHash, Enabled: true,
		RoutingKey: "infrastructure", Severity: "warning", RateLimitPerMinute: 2,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(Config{
		Database: database,
		BearerVerifier: auth.BearerVerifier{Lookup: func(id string) (auth.IngressToken, bool) {
			return token, id == publicID
		}},
		Routes:          map[string]map[string][]string{"infrastructure": {"warning": {"feishu.ops"}}},
		EnabledChannels: map[string]bool{"feishu.ops": true},
		DedupeWindow:    30 * time.Minute,
		Now:             func() time.Time { return now },
		Logger:          logger,
	})

	body := []byte(`{"title":"宝塔磁盘告警","message":"根分区使用率超过 90%","category":"disk"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/notifications", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+plain)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var accepted acceptedResponse
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	delivery, err := database.ClaimDelivery(context.Background(), now, time.Minute)
	if err != nil || delivery == nil {
		t.Fatalf("delivery = %+v, %v", delivery, err)
	}
	if delivery.Event.Source != "baota-prod" || delivery.Event.RoutingKey != "infrastructure" || delivery.Event.Severity != "warning" || delivery.Event.Status != "info" || delivery.Event.Labels["category"] != "disk" {
		t.Fatalf("simple event = %+v", delivery.Event)
	}
	if accepted.EventID == "" || delivery.Event.EventID != accepted.EventID {
		t.Fatalf("accepted = %+v delivery event=%q", accepted, delivery.Event.EventID)
	}
}

func TestSimpleNotificationEndpointRejectsUnauthorizedInvalidAndLimitedRequests(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "alertbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	plain, publicID, secretHash, err := auth.GenerateBearerToken()
	if err != nil {
		t.Fatal(err)
	}
	token := auth.IngressToken{
		ID: "baota-prod", PublicID: publicID, SecretHash: secretHash, Enabled: true,
		RoutingKey: "infrastructure", Severity: "warning", RateLimitPerMinute: 4,
	}
	handler := New(Config{
		Database: database,
		BearerVerifier: auth.BearerVerifier{Lookup: func(id string) (auth.IngressToken, bool) {
			return token, id == publicID
		}},
		Routes:          map[string]map[string][]string{"infrastructure": {"warning": {"feishu.ops"}}},
		EnabledChannels: map[string]bool{"feishu.ops": true},
		DedupeWindow:    30 * time.Minute,
		Now:             func() time.Time { return now },
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	call := func(authHeader string, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/notifications", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if authHeader != "" {
			request.Header.Set("Authorization", authHeader)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	unauthorized := call("", `{"title":"x","message":"y"}`)
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") != `Bearer realm="alertbridge"` {
		t.Fatalf("unauthorized = %d headers=%v body=%s", unauthorized.Code, unauthorized.Header(), unauthorized.Body.String())
	}
	queryCredential := httptest.NewRequest(http.MethodPost, "/api/v1/notifications?token="+plain, strings.NewReader(`{"title":"x","message":"y"}`))
	queryCredential.Header.Set("Content-Type", "application/json")
	queryResponse := httptest.NewRecorder()
	handler.ServeHTTP(queryResponse, queryCredential)
	if queryResponse.Code != http.StatusUnauthorized {
		t.Fatalf("query credential = %d body=%s", queryResponse.Code, queryResponse.Body.String())
	}
	tooLarge := call("Bearer "+plain, `{"title":"x","message":"`+strings.Repeat("x", 8200)+`"}`)
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body = %d body=%s", tooLarge.Code, tooLarge.Body.String())
	}
	invalid := call("Bearer "+plain, `{"title":"x","message":"y","status":"resolved"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid fields = %d body=%s", invalid.Code, invalid.Body.String())
	}
	invalidCategory := call("Bearer "+plain, `{"title":"x","message":"y","category":"bad\ncategory"}`)
	if invalidCategory.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid category = %d body=%s", invalidCategory.Code, invalidCategory.Body.String())
	}
	valid := call("Bearer "+plain, `{"title":"x","message":"y"}`)
	if valid.Code != http.StatusOK {
		t.Fatalf("valid = %d body=%s", valid.Code, valid.Body.String())
	}
	limited := call("Bearer "+plain, `{"title":"x","message":"y"}`)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "60" {
		t.Fatalf("limited = %d headers=%v body=%s", limited.Code, limited.Header(), limited.Body.String())
	}
}

func TestLegacyHookEndpointsAreGone(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/hooks/grafana/gatus", bytes.NewReader([]byte(`{"status":"firing"}`)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusGone {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`"code":"endpoint_removed"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`/api/v1/events`)) {
		t.Fatalf("response = %s", response.Body.String())
	}
}

func TestSilenceRecordsEventWithoutDelivery(t *testing.T) {
	handler, now, secret := newTestHandlerWithOptions(t, func(cfg *Config) {
		cfg.IsSilenced = func(route, severity string, _ time.Time) bool {
			return route == "infrastructure" && severity == "critical"
		}
	})
	body := validBody(now, "evt-silenced", "infrastructure")
	request := signedRequest(t, body, now, "nonce-silenced", secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !bytes.Contains(response.Body.Bytes(), []byte(`"outcome":"suppressed"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"reason":"silence"`)) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHealthEndpoints(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
		if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("security header = %q", got)
		}
	}
}

func newTestHandler(t *testing.T) (http.Handler, time.Time, []byte) {
	return newTestHandlerWithOptions(t, nil)
}

func newTestHandlerWithOptions(t *testing.T, mutate func(*Config)) (http.Handler, time.Time, []byte) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "alertbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	secret := []byte("0123456789abcdef0123456789abcdef")
	verifier := auth.Verifier{Clients: map[string]auth.Client{"gatus": {
		ID: "gatus", Secret: secret, Enabled: true, AllowedRoutes: map[string]struct{}{"infrastructure": {}}, RateLimitPerMin: 10,
	}}, Tolerance: 5 * time.Minute, Now: func() time.Time { return now }}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{Database: database, Verifier: verifier, Routes: map[string]map[string][]string{
		"infrastructure": {"critical": {"feishu.ops"}, "warning": {"feishu.ops"}, "info": {"feishu.ops"}}, "proxy": {"critical": {"feishu.ops"}},
	}, EnabledChannels: map[string]bool{"feishu.ops": true}, NonceRetention: 10 * time.Minute, DedupeWindow: 30 * time.Minute, BodyLimitBytes: 32 * 1024, Now: func() time.Time { return now }, Logger: logger}
	if mutate != nil {
		mutate(&cfg)
	}
	return New(cfg), now, secret
}

func validBody(now time.Time, eventID, route string) []byte {
	value := map[string]any{"event_id": eventID, "source": "gatus", "routing_key": route, "status": "firing", "severity": "critical", "title": "Node unavailable", "message": "Three checks failed", "occurred_at": now.Format(time.RFC3339), "dedupe_key": "node-1-connectivity", "labels": map[string]string{"node": "node-1"}}
	body, _ := json.Marshal(value)
	return body
}

func signedRequest(t *testing.T, body []byte, now time.Time, nonce string, secret []byte) *http.Request {
	t.Helper()
	timestamp := fmt.Sprint(now.Unix())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/events", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Notify-Client", "gatus")
	request.Header.Set("X-Notify-Timestamp", timestamp)
	request.Header.Set("X-Notify-Nonce", nonce)
	request.Header.Set("X-Notify-Signature", auth.SignHex(secret, timestamp, nonce, body))
	return request
}
