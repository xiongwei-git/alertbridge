package mcpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/xiongwei-git/alertbridge/internal/domain"
	"github.com/xiongwei-git/alertbridge/internal/worker"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/securestore"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

func TestEventsDiscoveryAndValidation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := securestore.New(bytes.Repeat([]byte{1}, 32))
	events, err := NewEvents(db, cipher, "ops", hex.EncodeToString(hashForTest()), "", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	hash := sha256.Sum256([]byte(token))
	h, err := NewWithOAuth(db, hex.EncodeToString(hash[:]), "ops", nil, events)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, params, auth string) map[string]any {
		r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
		r.Header.Set("Authorization", "Bearer "+auth)
		r.Header.Set("Content-Type", "application/json")
		modernTestRequest(t, r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if auth == "bad" {
			if w.Code != 401 {
				t.Fatal(w.Code)
			}
			return nil
		}
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	call("server/discover", `{}`, "bad")
	v := call("server/discover", `{}`, token)
	if v["result"] == nil {
		t.Fatal(v)
	}
	v = call("events/list", `{}`, token)
	if !strings.Contains(toJSON(v), "alert.created") {
		t.Fatal(v)
	}
	for _, params := range []string{
		`{"name":"unknown","arguments":{},"delivery":{"mode":"webhook","url":"https://example.com/cb"}}`,
		`{"name":"alert.created","arguments":{"routing_key":"private"},"delivery":{"mode":"webhook","url":"https://example.com/cb"}}`,
		`{"name":"alert.created","arguments":{},"delivery":{"mode":"webhook","url":"http://127.0.0.1/cb","secret":"bad"}}`,
	} {
		if v := call("events/subscribe", params, token); v["error"] == nil {
			t.Fatal(v)
		}
	}
	if _, err := events.Sender(context.Background(), &store.Delivery{ChannelID: "@mcp-event:missing"}); err == nil {
		t.Fatal("missing subscription accepted")
	}
}
func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func hashForTest() []byte { h := sha256.Sum256([]byte(strings.Repeat("a", 64))); return h[:] }

type eventTransport func(*http.Request) (*http.Response, error)

func (f eventTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type eventFixture struct {
	db     *store.Store
	events *Events
	h      http.Handler
	token  string
	now    time.Time
	path   string
	cipher *securestore.Cipher
}

func newEventFixture(t *testing.T) *eventFixture {
	t.Helper()
	f := &eventFixture{path: filepath.Join(t.TempDir(), "events.db"), token: strings.Repeat("b", 64), now: time.Now().UTC()}
	var err error
	f.db, err = store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Close() })
	f.cipher, _ = securestore.New(bytes.Repeat([]byte{2}, 32))
	f.reset(t)
	return f
}
func (f *eventFixture) reset(t *testing.T) {
	t.Helper()
	sum := sha256.Sum256([]byte(f.token))
	var err error
	f.events, err = NewEvents(f.db, f.cipher, "ops", hex.EncodeToString(sum[:]), "", "callback.example")
	if err != nil {
		t.Fatal(err)
	}
	f.events.now = func() time.Time { return f.now }
	f.h, err = NewWithOAuth(f.db, hex.EncodeToString(sum[:]), "ops", nil, f.events)
	if err != nil {
		t.Fatal(err)
	}
}
func (f *eventFixture) call(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	r := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Content-Type", "application/json")
	modernTestRequest(t, r)
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Mcp-Protocol-Version", EventsProtocolVersion)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func subscriptionFor(secret string) map[string]any {
	return map[string]any{"name": "alert.created", "arguments": map[string]string{"severity": "critical", "routing_key": "ops"}, "delivery": map[string]string{"mode": "webhook", "url": "https://callback.example/events", "secret": secret}}
}
func testSigningSecret(b byte) string {
	return "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// This receiver validates Standard Webhooks independently of the production signer.
func verifyTestSignature(t *testing.T, r *http.Request, body []byte, secret string) {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(r.Header.Get("webhook-id") + "." + r.Header.Get("webhook-timestamp") + "."))
	mac.Write(body)
	expected := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	found := false
	for _, sig := range strings.Fields(r.Header.Get("webhook-signature")) {
		found = found || hmac.Equal([]byte(sig), []byte(expected))
	}
	if !found {
		t.Fatal("invalid Standard Webhooks signature")
	}
	if r.Header.Get("X-MCP-Subscription-Id") == "" {
		t.Fatal("missing subscription header")
	}
}
func mockEventReceiver(t *testing.T, secret string, receive func(*http.Request, []byte) int) *http.Client {
	t.Helper()
	return &http.Client{Transport: eventTransport(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		verifyTestSignature(t, r, body, secret)
		var v map[string]any
		json.Unmarshal(body, &v)
		responseBody := "{}"
		status := 200
		if v["type"] == "verification" {
			responseBody = toJSON(map[string]any{"challenge": v["challenge"]})
		} else if receive != nil {
			status = receive(r, body)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(responseBody)), Header: make(http.Header)}, nil
	})}
}
func (f *eventFixture) accept(t *testing.T, id, route, severity, status, suppressed string) store.AcceptResult {
	t.Helper()
	result, err := f.db.AcceptEvent(context.Background(), store.AcceptParams{ClientID: "test", RawPayload: []byte("{}"), Event: domain.Event{EventID: id, RoutingKey: route, Source: "monitor", Severity: domain.Severity(severity), Status: domain.Status(status), Title: "external title", Message: "external body", OccurredAt: f.now, DedupeKey: id}, Now: f.now, DedupeWindow: time.Minute, SuppressReason: suppressed})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func (f *eventFixture) process(t *testing.T) bool {
	t.Helper()
	w := worker.New(f.db, nil, worker.Config{EventSenderFor: f.events.Sender, RetryDelays: []time.Duration{time.Second}})
	processed, err := w.ProcessOne(context.Background(), f.now)
	if err != nil {
		t.Fatal(err)
	}
	return processed
}
func TestEventsPersistentDeliveryLifecycle(t *testing.T) {
	f := newEventFixture(t)
	secret := testSigningSecret(3)
	calls := 0
	failOnce := true
	var delivered []byte
	var firstID string
	receiver := mockEventReceiver(t, secret, func(r *http.Request, b []byte) int {
		calls++
		if firstID == "" {
			firstID = r.Header.Get("webhook-id")
			delivered = append([]byte{}, b...)
		} else if firstID != r.Header.Get("webhook-id") || !bytes.Equal(delivered, b) {
			t.Fatal("retry changed event ID/body")
		}
		if failOnce {
			failOnce = false
			return 503
		}
		return 204
	})
	f.events.client = receiver
	params := subscriptionFor(secret)
	v := f.call(t, "events/subscribe", params)
	if v["error"] != nil {
		t.Fatal(v)
	}
	id := v["result"].(map[string]any)["id"].(string)
	// Idempotent canonical filter order and bounded verification cache.
	if v2 := f.call(t, "events/subscribe", params); v2["result"].(map[string]any)["id"] != id {
		t.Fatal(v2)
	}
	raw, err := f.db.EventSubscription(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw.Encrypted, []byte(secret)) || bytes.Contains(raw.Encrypted, []byte("callback.example")) {
		t.Fatal("plaintext credentials persisted")
	}
	for _, p := range []struct{ id, route, severity, status, suppressed string }{
		{"private", "private", "critical", "info", ""}, {"low", "ops", "info", "info", ""}, {"suppressed", "ops", "critical", "info", "silence"},
	} {
		f.accept(t, p.id, p.route, p.severity, p.status, p.suppressed)
	}
	if f.process(t) {
		t.Fatal("nonmatching event was queued")
	}
	result := f.accept(t, "matching", "ops", "critical", "info", "")
	duplicate := f.accept(t, "matching", "ops", "critical", "info", "")
	if duplicate.Outcome != store.OutcomeDuplicate || duplicate.Deliveries != result.Deliveries {
		t.Fatal("duplicate contract changed", duplicate)
	}
	if !f.process(t) || calls != 1 {
		t.Fatal("first delivery missing")
	}
	if f.process(t) {
		t.Fatal("retry ignored backoff")
	}
	// Close/reopen both DB and Events runtime; subscription and pending retry survive.
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.reset(t)
	f.events.client = receiver
	f.now = f.now.Add(2 * time.Second)
	if !f.process(t) || calls != 2 {
		t.Fatal("persistent retry missing")
	}
	if f.process(t) {
		t.Fatal("successful delivery repeated")
	}
	var payload map[string]any
	json.Unmarshal(delivered, &payload)
	data := payload["data"].(map[string]any)
	if data["id"] != result.EventID || data["message"] != nil || data["raw_payload"] != nil || payload["cursor"] != nil {
		t.Fatal(payload)
	}
	// Tools still work under the new protocol header.
	if v := f.call(t, "tools/list", map[string]any{}); v["error"] != nil {
		t.Fatal(v)
	}
	if v := f.call(t, "tools/call", map[string]any{"name": "get_alert", "arguments": map[string]string{"id": result.EventID}}); v["error"] != nil {
		t.Fatal(v)
	}
	f.accept(t, "pending-cancel", "ops", "critical", "info", "")
	if v := f.call(t, "events/unsubscribe", params); v["error"] != nil {
		t.Fatal(v)
	}
	f.call(t, "events/unsubscribe", params)
	if !f.process(t) || calls != 2 {
		t.Fatal("cancelled subscription delivered")
	}
}
func TestEventsExpirationRotationAndPermanentErrors(t *testing.T) {
	for _, code := range []int{410, 413, 400} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			f := newEventFixture(t)
			old, newSecret := testSigningSecret(4), testSigningSecret(5)
			f.events.client = mockEventReceiver(t, old, nil)
			params := subscriptionFor(old)
			v := f.call(t, "events/subscribe", params)
			id := v["result"].(map[string]any)["id"].(string)
			f.events.client = mockEventReceiver(t, newSecret, func(r *http.Request, b []byte) int { verifyTestSignature(t, r, b, old); return code })
			params = subscriptionFor(newSecret)
			params["ttlMs"] = int64(1000)
			if v := f.call(t, "events/subscribe", params); v["error"] != nil {
				t.Fatal(v)
			}
			f.accept(t, "one", "ops", "critical", "info", "")
			if !f.process(t) {
				t.Fatal("missing delivery")
			}
			status, err := f.db.GetDeliveryStatus(context.Background(), f.accept(t, "one", "ops", "critical", "info", "").EventID, eventChannelPrefix+id)
			if err != nil || status.Status != "dead" {
				t.Fatal(status, err)
			}
			f.now = f.now.Add(2 * time.Second)
			f.accept(t, "expired", "ops", "critical", "info", "")
			if f.process(t) {
				t.Fatal("expired subscription queued")
			}
		})
	}
}
func TestEventsCallbackFailureAndURLSafety(t *testing.T) {
	f := newEventFixture(t)
	secret := testSigningSecret(6)
	f.events.client = &http.Client{Transport: eventTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"challenge":"wrong"}`))}, nil
	})}
	v := f.call(t, "events/subscribe", subscriptionFor(secret))
	if v["error"].(map[string]any)["code"] != float64(-32015) {
		t.Fatal(v)
	}
	hosts := map[string]bool{"callback.example": true, "127.0.0.1": true}
	for _, u := range []string{"http://callback.example/", "https://user@callback.example/", "https://callback.example:444/", "https://other.example/", "https://127.0.0.1/", "https://callback.example/#secret"} {
		if _, err := callbackURL(u, hosts); err == nil {
			t.Fatal("unsafe callback accepted", u)
		}
	}
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "192.0.2.1", "198.18.0.1", "224.0.0.1", "::1", "fc00::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2002:7f00:1::1", "2001:db8::1"} {
		if publicCallbackIP(netip.MustParseAddr(s)) {
			t.Fatal("non-public IP accepted", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicCallbackIP(netip.MustParseAddr(s)) {
			t.Fatal("public IP rejected", s)
		}
	}
}

func TestEventsOAuthRevocationAndPolicyChange(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	secret := testSigningSecret(7)
	oauth, err := NewOAuth(f.db, "https://alerts.example", "ops", "", func(*http.Request) (string, bool) { return "csrf", true })
	if err != nil {
		t.Fatal(err)
	}
	f.events, err = NewEvents(f.db, f.cipher, "ops", "", "https://alerts.example", "callback.example")
	if err != nil {
		t.Fatal(err)
	}
	f.events.now = func() time.Time { return f.now }
	f.events.client = mockEventReceiver(t, secret, func(*http.Request, []byte) int { t.Fatal("revoked event delivered"); return 204 })
	oauth.now = func() time.Time { return f.now }
	// Exercise the real persisted OAuth grant checks without a UI dependency.
	if err := f.db.SaveOAuthCode(ctx, "code", "challenge", oauth.redirect, oauth.resource, oauth.policy, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ExchangeOAuthCode(ctx, "code", "challenge", oauth.redirect, oauth.resource, oauth.policy, "grant", tokenHash(f.token), "refresh", f.now); err != nil {
		t.Fatal(err)
	}
	f.h, err = NewWithOAuth(f.db, "", "ops", oauth, f.events)
	if err != nil {
		t.Fatal(err)
	}
	v := f.call(t, "events/subscribe", subscriptionFor(secret))
	if v["error"] != nil {
		t.Fatal(v)
	}
	id := v["result"].(map[string]any)["id"].(string)
	result := f.accept(t, "revoked", "ops", "critical", "info", "")
	if err := f.db.RevokeAllOAuth(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.process(t) {
		t.Fatal("pending job missing")
	}
	status, err := f.db.GetDeliveryStatus(ctx, result.EventID, eventChannelPrefix+id)
	if err != nil || status.Status != "dead" {
		t.Fatal(status, err)
	}
}

func TestEventsQueuedAuthorizationChanges(t *testing.T) {
	for _, change := range []string{"expire", "token", "routes", "unsubscribe-before-send"} {
		t.Run(change, func(t *testing.T) {
			f := newEventFixture(t)
			secret := testSigningSecret(8)
			count := 0
			f.events.client = mockEventReceiver(t, secret, func(*http.Request, []byte) int { count++; return 204 })
			params := subscriptionFor(secret)
			v := f.call(t, "events/subscribe", params)
			id := v["result"].(map[string]any)["id"].(string)
			result := f.accept(t, "pending", "ops", "critical", "info", "")
			switch change {
			case "expire":
				f.now = f.now.Add(25 * time.Hour)
			case "token":
				f.events.staticOwner = "static:changed"
			case "routes":
				f.events.staticPolicy = "changed"
			case "unsubscribe-before-send":
				d, err := f.db.ClaimDelivery(context.Background(), f.now, time.Second)
				if err != nil || d == nil {
					t.Fatal(err)
				}
				sender, err := f.events.Sender(context.Background(), d)
				if err != nil {
					t.Fatal(err)
				}
				f.call(t, "events/unsubscribe", params)
				if _, err := sender.Send(context.Background(), d.Event); err == nil {
					t.Fatal("stale sender accepted")
				}
				if count != 0 {
					t.Fatal("cancelled delivered")
				}
				return
			}
			if !f.process(t) {
				t.Fatal("queued job missing")
			}
			status, err := f.db.GetDeliveryStatus(context.Background(), result.EventID, eventChannelPrefix+id)
			if err != nil || status.Status != "dead" || count != 0 {
				t.Fatal(status, err, count)
			}
		})
	}
}

func TestEventsRealLocalTLSCallback(t *testing.T) {
	f := newEventFixture(t)
	secret := testSigningSecret(9)
	received := make(chan map[string]any, 1)
	callback := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		verifyTestSignature(t, r, body, secret)
		var v map[string]any
		if err := json.Unmarshal(body, &v); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if v["type"] == "verification" {
			json.NewEncoder(w).Encode(map[string]any{"challenge": v["challenge"]})
			return
		}
		received <- v
		w.WriteHeader(204)
	}))
	defer callback.Close()
	target, err := url.Parse(callback.URL)
	if err != nil {
		t.Fatal(err)
	}
	trusted := callback.Client()
	// Test-only routing sends the permitted synthetic callback to a trusted local TLS
	// listener. Neither production HTTPS checks nor TLS certificate verification change.
	f.events.client = &http.Client{Transport: eventTransport(func(r *http.Request) (*http.Response, error) {
		local := r.Clone(r.Context())
		u := *r.URL
		u.Host = target.Host
		local.URL = &u
		local.Host = target.Host
		return trusted.Transport.RoundTrip(local)
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ingress := httptest.NewServer(f.h)
	defer ingress.Close()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "events/subscribe", "params": subscriptionFor(secret)})
	req, _ := http.NewRequest("POST", ingress.URL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	modernTestRequest(t, req)
	req.Header.Set("Authorization", "Bearer "+f.token)
	response, err := ingress.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result map[string]any
	json.NewDecoder(response.Body).Decode(&result)
	if response.StatusCode != 200 || result["error"] != nil {
		t.Fatal(response.StatusCode, result)
	}
	event := f.accept(t, "local-tls", "ops", "critical", "info", "")
	if !f.process(t) {
		t.Fatal("no callback job")
	}
	select {
	case v := <-received:
		if v["eventId"] != "evt_"+event.EventID {
			t.Fatal(v)
		}
	case <-time.After(time.Second):
		t.Fatal("TLS callback not received")
	}
}
func TestEventsRecreatedSubscriptionDoesNotReviveOldJobs(t *testing.T) {
	f := newEventFixture(t)
	secret := testSigningSecret(10)
	count := 0
	f.events.client = mockEventReceiver(t, secret, func(*http.Request, []byte) int { count++; return 204 })
	params := subscriptionFor(secret)
	f.call(t, "events/subscribe", params)
	f.accept(t, "old", "ops", "critical", "info", "")
	f.call(t, "events/unsubscribe", params)
	f.call(t, "events/subscribe", params)
	if !f.process(t) || count != 0 {
		t.Fatal("recreated subscription revived old job")
	}
	f.accept(t, "new", "ops", "critical", "info", "")
	if !f.process(t) || count != 1 {
		t.Fatal("new generation not delivered")
	}
}

func TestEventsTTLRefreshAndValidation(t *testing.T) {
	f := newEventFixture(t)
	secret := testSigningSecret(11)
	verifications := 0
	receiver := mockEventReceiver(t, secret, nil)
	f.events.client = &http.Client{Transport: eventTransport(func(r *http.Request) (*http.Response, error) { verifications++; return receiver.Transport.RoundTrip(r) })}
	params := subscriptionFor(secret)
	params["ttlMs"] = int64(2000)
	first := f.call(t, "events/subscribe", params)
	firstResult := first["result"].(map[string]any)
	id := firstResult["id"].(string)
	sub, err := f.db.EventSubscription(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	generation := sub.Generation
	if sub.Expires.UnixMilli() != f.now.Add(2*time.Second).UnixMilli() {
		t.Fatal(sub.Expires, f.now)
	}
	f.now = f.now.Add(time.Second)
	second := f.call(t, "events/subscribe", params)
	if second["result"].(map[string]any)["id"] != id {
		t.Fatal(second)
	}
	sub, err = f.db.EventSubscription(context.Background(), id)
	if err != nil || sub.Generation != generation || sub.Expires.UnixMilli() != f.now.Add(2*time.Second).UnixMilli() || verifications != 1 {
		t.Fatal(sub, err, verifications)
	}
	// Another filter at the same verified destination/secret uses the bounded cache.
	params["arguments"] = map[string]string{"status": "info"}
	f.call(t, "events/subscribe", params)
	if verifications != 1 {
		t.Fatal("callback verification cache not principal/destination scoped")
	}
	for _, ttl := range []any{nil, int64(999999999)} {
		params["ttlMs"] = ttl
		v := f.call(t, "events/subscribe", params)
		if v["error"] != nil {
			t.Fatal(v)
		}
		expiry, _ := time.Parse(time.RFC3339Nano, v["result"].(map[string]any)["refreshBefore"].(string))
		if expiry.Sub(f.now) != 24*time.Hour {
			t.Fatal(expiry)
		}
	}
	for _, ttl := range []any{0, -1, "1000", 1.5} {
		params["ttlMs"] = ttl
		if v := f.call(t, "events/subscribe", params); v["error"] == nil {
			t.Fatal("invalid ttl accepted", ttl)
		}
	}
	params = subscriptionFor(secret)
	params["cursor"] = "old-history"
	if v := f.call(t, "events/subscribe", params); v["error"] == nil {
		t.Fatal("unsupported replay accepted")
	}
	for _, hosts := range []string{"", "*.example", "example.com/path", "example.com:443", "bad\nhost", "-bad.example", "bad..example"} {
		if _, err := NewEvents(f.db, f.cipher, "ops", "", "", hosts); err == nil {
			t.Fatal("invalid callback allowlist accepted", hosts)
		}
	}
	client := eventHTTPClient(map[string]bool{"127.0.0.1": true})
	if response, err := client.Get("https://127.0.0.1/"); err == nil {
		response.Body.Close()
		t.Fatal("production transport dialed loopback")
	}
	if client.CheckRedirect(&http.Request{}, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects enabled")
	}
}

func TestModernProtocolMetadataAndResults(t *testing.T) {
	f := newEventFixture(t)
	params := map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": EventsProtocolVersion, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "modern", "method": "events/list", "params": params})
	r := httptest.NewRequest("POST", "/mcp", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Content-Type", "application/json")
	modernTestRequest(t, r)
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", EventsProtocolVersion)
	r.Header.Set("Mcp-Method", "events/list")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	var v map[string]any
	json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 200 || v["error"] != nil {
		t.Fatalf("modern metadata rejected: %d %s", w.Code, w.Body.String())
	}
	if v["result"].(map[string]any)["resultType"] != "complete" {
		t.Fatal("missing modern resultType", v)
	}
}
