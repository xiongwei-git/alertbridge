package mcpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

func TestReadOnlyMCP(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	now := time.Now().UTC()
	ids := map[string]string{}
	for _, route := range []string{"ops", "private"} {
		a, e := db.AcceptEvent(context.Background(), store.AcceptParams{RawPayload: []byte("{}"), ClientID: "test", Event: domain.Event{EventID: route, Source: "monitor", RoutingKey: route, Status: domain.StatusFiring, Severity: domain.SeverityCritical, Title: "alert", Message: "untrusted", OccurredAt: now, DedupeKey: route}, Targets: []string{"test.channel"}, Now: now, DedupeWindow: time.Minute})
		if e != nil {
			t.Fatal(e)
		}
		ids[route] = a.EventID
	}
	token := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(token))
	h, e := New(db, hex.EncodeToString(sum[:]), "ops")
	if e != nil {
		t.Fatal(e)
	}
	call := func(auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/mcp", bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("wrong", `{}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`} {
		w := call(token, body)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	tool := func(name, args string) map[string]any {
		w := call(token, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var v map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	list := tool("list_alerts", `{}`)
	b, _ := json.Marshal(list)
	if !bytes.Contains(b, []byte(ids["ops"])) || bytes.Contains(b, []byte(ids["private"])) {
		t.Fatal(string(b))
	}
	detail := tool("get_alert", `{"id":"`+ids["ops"]+`"}`)
	b, _ = json.Marshal(detail)
	if !bytes.Contains(b, []byte("test.channel")) {
		t.Fatal(string(b))
	}
	hidden := tool("get_alert", `{"id":"`+ids["private"]+`"}`)
	b, _ = json.Marshal(hidden)
	if !bytes.Contains(b, []byte(`"isError":true`)) {
		t.Fatal(string(b))
	}
	active := tool("list_active_incidents", `{}`)
	b, _ = json.Marshal(active)
	if bytes.Contains(b, []byte("private")) || !bytes.Contains(b, []byte("ops")) {
		t.Fatal(string(b))
	}
	invalid := tool("list_alerts", `{"limit":101}`)
	b, _ = json.Marshal(invalid)
	if !bytes.Contains(b, []byte(`"isError":true`)) {
		t.Fatal(string(b))
	}
	r := httptest.NewRequest(http.MethodGet, "http://localhost/mcp?token="+token, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("origin: %d %s", w.Code, w.Body.String())
	}
	for i := 0; i < 61; i++ {
		w = call(token, `{"jsonrpc":"2.0","id":5,"method":"ping"}`)
	}
	if w.Code != 429 {
		t.Fatalf("rate limit %d", w.Code)
	}
}

func TestConfiguration(t *testing.T) {
	for _, v := range [][2]string{{"bad", "ops"}, {strings.Repeat("a", 64), ""}, {strings.Repeat("a", 64), "*"}} {
		if _, e := New(nil, v[0], v[1]); e == nil {
			t.Fatal("accepted invalid config")
		}
	}
}
