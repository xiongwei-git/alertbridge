package channel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

func adapterEvent() domain.Event {
	return domain.Event{EventID: "evt", Source: "gatus", RoutingKey: "infra", Status: domain.StatusFiring, Severity: domain.SeverityCritical, Title: "Node down", Message: "Connection failed", OccurredAt: time.Now().UTC()}
}

func TestTelegramSender(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botsecret/sendMessage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()
	sender := NewTelegramSender(TelegramConfig{BotToken: "secret", ChatID: "123", BaseURL: server.URL, Client: server.Client()})
	if _, err := sender.Send(context.Background(), adapterEvent()); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "123" || !strings.Contains(got["text"].(string), "Node down") {
		t.Fatalf("payload = %+v", got)
	}
}

func TestNtfySender(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("Priority") != "urgent" || !strings.Contains(string(body), "Node down") {
			t.Errorf("headers=%v body=%s", r.Header, body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	sender := NewNtfySender(NtfyConfig{Endpoint: server.URL, Token: "token", Client: server.Client()})
	if _, err := sender.Send(context.Background(), adapterEvent()); err != nil {
		t.Fatal(err)
	}
}

func TestBuildEmailPreventsHeaderInjection(t *testing.T) {
	event := adapterEvent()
	event.Title = "Node down\r\nBcc: attacker@example.com"
	message, from, recipients, err := buildEmail(SMTPConfig{Host: "smtp.example.com", Port: 465, From: "Ops <ops@example.com>", Recipients: []string{"owner@example.com"}, Mode: "tls"}, event)
	if err != nil {
		t.Fatal(err)
	}
	if from != "ops@example.com" || len(recipients) != 1 {
		t.Fatalf("from=%q recipients=%v", from, recipients)
	}
	header := strings.SplitN(string(message), "\r\n\r\n", 2)[0]
	if strings.Contains(header, "\r\nBcc:") {
		t.Fatalf("header injection in message: %s", message)
	}
}
