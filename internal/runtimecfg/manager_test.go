package runtimecfg

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/config"
	"github.com/xiongwei-git/alertbridge/internal/domain"
	"github.com/xiongwei-git/alertbridge/internal/securestore"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

func TestManagerSeedsAndReloadsDynamicConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"code":0}`)) }))
	defer server.Close()
	database, err := store.Open(filepath.Join(t.TempDir(), "alertbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cipher, err := securestore.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := config.Config{
		Clients:  map[string]config.ClientConfig{"seed": {Enabled: true, Secret: []byte("0123456789abcdef0123456789abcdef"), AllowedRoutes: []string{"infra"}, RateLimitPerMinute: 60}},
		Channels: map[string]config.ChannelConfig{"feishu": {Type: "feishu", Enabled: true, Webhook: server.URL, MessageType: "text", Keyword: "AlertBridge", AllowedHosts: []string{"127.0.0.1"}}},
		Routes:   map[string]map[string][]string{"infra": {"critical": {"feishu"}}},
	}
	manager, err := New(context.Background(), Options{Database: database, Cipher: cipher, Bootstrap: bootstrap, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := manager.LookupClient("seed")
	if !ok || !client.CanUseRoute("infra") {
		t.Fatalf("client = %+v", client)
	}
	if got := manager.ResolveTargets("infra", "critical"); len(got) != 1 || got[0] != "feishu" {
		t.Fatalf("targets = %v", got)
	}
	sender, ok := manager.Sender("feishu")
	if !ok {
		t.Fatal("seeded sender unavailable")
	}
	if _, err := sender.Send(context.Background(), domain.Event{Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: "test", Message: "test", OccurredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	channels := manager.Channels()
	if len(channels) != 1 || !channels[0].HasKeyword {
		t.Fatalf("channel view = %+v, want configured keyword", channels)
	}
	if err := manager.UpsertChannel(context.Background(), ChannelInput{ID: "feishu", Type: "feishu", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	channels = manager.Channels()
	if len(channels) != 1 || channels[0].Enabled || !channels[0].HasKeyword {
		t.Fatalf("channel view after toggle = %+v, want disabled channel with preserved keyword", channels)
	}

	record, err := database.GetClient(context.Background(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(record.SecretCipher, []byte("0123456789abcdef")) {
		t.Fatal("client secret stored in plaintext")
	}

	secret, err := manager.CreateClient(context.Background(), "grafana", true, []string{"infra"}, 30)
	if err != nil || len(secret) != 64 {
		t.Fatalf("CreateClient() secret len=%d err=%v", len(secret), err)
	}
	if _, ok := manager.LookupClient("grafana"); !ok {
		t.Fatal("created client not available after reload")
	}
}

func TestManagerChannelRouteAndSilenceUpdatesAreImmediate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	database, err := store.Open(filepath.Join(t.TempDir(), "alertbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cipher, _ := securestore.New(bytes.Repeat([]byte{8}, 32))
	bootstrap := config.Config{Clients: map[string]config.ClientConfig{"seed": {Enabled: true, Secret: bytes.Repeat([]byte{'a'}, 32), AllowedRoutes: []string{"infra"}, RateLimitPerMinute: 10}}, Channels: map[string]config.ChannelConfig{}, Routes: map[string]map[string][]string{}}
	manager, err := New(context.Background(), Options{Database: database, Cipher: cipher, Bootstrap: bootstrap, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.UpsertChannel(context.Background(), ChannelInput{ID: "ntfy.phone", Type: "ntfy", Enabled: true, Endpoint: server.URL, Secret: "token"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReplaceRoute(context.Background(), "infra", "warning", []string{"ntfy.phone"}); err != nil {
		t.Fatal(err)
	}
	if got := manager.ResolveTargets("infra", "warning"); len(got) != 1 || got[0] != "ntfy.phone" {
		t.Fatalf("targets = %v", got)
	}
	now := time.Now().UTC()
	if err := manager.CreateSilence(context.Background(), "infra", "warning", now.Add(-time.Minute), now.Add(time.Hour), "maintenance"); err != nil {
		t.Fatal(err)
	}
	if !manager.IsSilenced("infra", "warning", now) || manager.IsSilenced("infra", "critical", now) {
		t.Fatal("silence matching is incorrect")
	}
}
