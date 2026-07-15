package channel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

func TestFeishuSenderSendsSignedCard(t *testing.T) {
	now := time.Unix(1784090000, 0)
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer server.Close()
	sender := NewFeishuSender(FeishuConfig{Webhook: server.URL, SigningSecret: []byte("signing-secret"), MessageType: "card", Keyword: "AlertBridge", Now: func() time.Time { return now }, Client: server.Client()})
	event := domain.Event{EventID: "evt-1", Source: "gatus", RoutingKey: "infra", Status: domain.StatusResolved,
		Severity: domain.SeverityCritical, Title: "Proxy *recovered*", Message: "Connection [restored]", OccurredAt: now,
		DedupeKey: "proxy-1", Labels: map[string]string{"node": "hk-1"}}
	started := now.Add(-12 * time.Minute)
	event.IncidentStartedAt = &started
	status, err := sender.Send(context.Background(), event)
	if err != nil || status != http.StatusOK {
		t.Fatalf("Send() = %d, %v", status, err)
	}
	if received["msg_type"] != "interactive" {
		t.Fatalf("msg_type = %v", received["msg_type"])
	}
	if received["timestamp"] != fmt.Sprint(now.Unix()) {
		t.Fatalf("timestamp = %v", received["timestamp"])
	}
	stringToSign := fmt.Sprintf("%d\n%s", now.Unix(), "signing-secret")
	mac := hmac.New(sha256.New, []byte(stringToSign))
	wantSign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if received["sign"] != wantSign {
		t.Fatalf("sign = %v, want %s", received["sign"], wantSign)
	}
	payload, _ := json.Marshal(received)
	if string(payload) == "" || !containsAll(string(payload), `AlertBridge`, `Proxy *recovered*`, `Connection \\[restored\\]`, `12m`) {
		t.Fatalf("payload missing escaped content or duration: %s", payload)
	}
}

func TestFeishuSenderInjectsKeywordIntoText(t *testing.T) {
	var received struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer server.Close()

	sender := NewFeishuSender(FeishuConfig{Webhook: server.URL, MessageType: "text", Keyword: "AlertBridge", Client: server.Client()})
	event := domain.Event{Status: domain.StatusFiring, Severity: domain.SeverityCritical, Title: "Database unavailable", Message: "Connection failed; see AlertBridge runbook", Source: "gatus", OccurredAt: time.Now()}
	if _, err := sender.Send(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(received.Content.Text, "【AlertBridge】") {
		t.Fatalf("text payload = %q, want configured keyword prefix", received.Content.Text)
	}
}

func TestFeishuSenderClassifiesHTTPFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "slow down", http.StatusTooManyRequests) }))
	defer server.Close()
	sender := NewFeishuSender(FeishuConfig{Webhook: server.URL, MessageType: "text", Client: server.Client()})
	event := domain.Event{Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: "test", Message: "test", OccurredAt: time.Now()}
	_, err := sender.Send(context.Background(), event)
	var sendErr *SendError
	if !errors.As(err, &sendErr) || !sendErr.Retryable || sendErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("Send() error = %#v", err)
	}
}

func TestFeishuSenderRejectsAmbiguousSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	sender := NewFeishuSender(FeishuConfig{Webhook: server.URL, MessageType: "text", Client: server.Client()})
	event := domain.Event{Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: "test", Message: "test", OccurredAt: time.Now()}
	_, err := sender.Send(context.Background(), event)
	var sendErr *SendError
	if !errors.As(err, &sendErr) || !sendErr.Retryable {
		t.Fatalf("Send() error = %#v, want retryable ambiguous response", err)
	}
}

func TestFeishuSenderTreatsKeywordRejectionAsPermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":19024,"msg":"Key Words Not Found"}`))
	}))
	defer server.Close()
	sender := NewFeishuSender(FeishuConfig{Webhook: server.URL, MessageType: "text", Client: server.Client()})
	event := domain.Event{Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: "test", Message: "test", OccurredAt: time.Now()}
	_, err := sender.Send(context.Background(), event)
	var sendErr *SendError
	if !errors.As(err, &sendErr) || sendErr.Retryable || sendErr.Message != "Feishu keyword check failed" {
		t.Fatalf("Send() error = %#v, want permanent keyword error", err)
	}
}

func containsAll(value string, values ...string) bool {
	for _, candidate := range values {
		found := false
		for i := 0; i+len(candidate) <= len(value); i++ {
			if value[i:i+len(candidate)] == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
