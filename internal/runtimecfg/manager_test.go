package runtimecfg

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/auth"
	"github.com/xiongwei-git/alertbridge/internal/domain"
	"github.com/xiongwei-git/alertbridge/internal/securestore"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

func TestManagerInitializesEmptyAndReloadsDynamicConfiguration(t *testing.T) {
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
	manager, err := New(context.Background(), Options{Database: database, Cipher: cipher, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(manager.Clients()) != 0 || len(manager.IngressTokens()) != 0 || len(manager.Channels()) != 0 || len(manager.Routes()) != 0 {
		t.Fatal("new manager must start without business configuration")
	}
	keyword := "AlertBridge"
	if err := manager.UpsertChannel(context.Background(), ChannelInput{ID: "feishu", Type: "feishu", Enabled: true, Endpoint: server.URL, MessageType: "text", Keyword: &keyword}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReplaceRoute(context.Background(), "infra", "critical", []string{"feishu"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateClient(context.Background(), "seed", true, []string{"infra"}, 60); err != nil {
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
	if len(record.SecretCipher) == 0 {
		t.Fatal("client secret ciphertext is empty")
	}

	secret, err := manager.CreateClient(context.Background(), "grafana", true, []string{"infra"}, 30)
	if err != nil || len(secret) != 64 {
		t.Fatalf("CreateClient() secret len=%d err=%v", len(secret), err)
	}
	if _, ok := manager.LookupClient("grafana"); !ok {
		t.Fatal("created client not available after reload")
	}

	bearer, err := manager.CreateIngressToken(context.Background(), "baota-prod", true, "infra", "critical", 10)
	if err != nil || !strings.HasPrefix(bearer, "abt_") {
		t.Fatalf("CreateIngressToken() = %q, %v", bearer, err)
	}
	verified, err := (auth.BearerVerifier{Lookup: manager.LookupIngressToken}).Verify("Bearer " + bearer)
	if err != nil || verified.ID != "baota-prod" || verified.Severity != "critical" {
		t.Fatalf("verified ingress token = %+v, %v", verified, err)
	}
	tokenRecord, err := database.GetIngressToken(context.Background(), "baota-prod")
	if err != nil || len(tokenRecord.TokenHash) != 32 || bytes.Contains(tokenRecord.TokenHash, []byte(bearer)) {
		t.Fatalf("stored ingress token = %+v, %v", tokenRecord, err)
	}
	rotated, err := manager.RotateIngressToken(context.Background(), "baota-prod")
	if err != nil || rotated == bearer {
		t.Fatalf("RotateIngressToken() = %q, %v", rotated, err)
	}
	if _, err := (auth.BearerVerifier{Lookup: manager.LookupIngressToken}).Verify("Bearer " + bearer); !errors.Is(err, auth.ErrInvalidBearer) {
		t.Fatalf("old bearer error = %v, want invalid", err)
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
	manager, err := New(context.Background(), Options{Database: database, Cipher: cipher, AllowInsecureHTTP: true})
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

func TestChannelEditRoundTripPreservesCredentials(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "edit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cipher, err := securestore.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(context.Background(), Options{Database: database, Cipher: cipher})
	if err != nil {
		t.Fatal(err)
	}
	keyword := "original"
	for _, input := range []ChannelInput{
		{ID: "feishu", Type: "feishu", Enabled: true, Endpoint: "https://open.feishu.cn/open-apis/bot/v2/hook/test", Secret: "signing-secret", MessageType: "text", Keyword: &keyword},
		{ID: "telegram", Type: "telegram", Secret: "bot-secret", ChatID: "-100123"},
		{ID: "ntfy", Type: "ntfy", Endpoint: "https://ntfy.sh/private-topic", Secret: "ntfy-secret"},
		{ID: "smtp", Type: "smtp", SMTPHost: "smtp.example.com", SMTPPort: 587, SMTPUsername: "user", Secret: "smtp-secret", SMTPFrom: "ops@example.com", SMTPRecipients: []string{"owner@example.com"}, SMTPMode: "starttls"},
	} {
		t.Run(input.Type, func(t *testing.T) {
			ctx := context.Background()
			if err := manager.UpsertChannel(ctx, input); err != nil {
				t.Fatal(err)
			}
			record, err := database.GetChannel(ctx, input.ID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := manager.decryptConfig(record.ConfigCipher)
			if err != nil {
				t.Fatal(err)
			}
			edit, err := manager.ChannelForEdit(ctx, input.ID)
			if err != nil {
				t.Fatal(err)
			}
			if edit.Secret != "" || edit.Endpoint != "" {
				t.Fatal("edit exposes credentials")
			}
			if edit.Enabled != input.Enabled || edit.ChatID != input.ChatID || edit.SMTPMode != input.SMTPMode || edit.SMTPPort != input.SMTPPort || edit.MessageType != input.MessageType {
				t.Fatal("edit settings not populated")
			}
			if err := manager.UpsertChannel(ctx, edit); err != nil {
				t.Fatal(err)
			}
			record, err = database.GetChannel(ctx, input.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, err := manager.decryptConfig(record.ConfigCipher)
			if err != nil {
				t.Fatal(err)
			}
			if before.Webhook != after.Webhook || before.SigningSecret != after.SigningSecret || before.BotToken != after.BotToken || before.Token != after.Token || before.SMTPPassword != after.SMTPPassword || before.Endpoint != after.Endpoint || before.Keyword != after.Keyword || strings.Join(before.SMTPRecipients, ",") != strings.Join(after.SMTPRecipients, ",") {
				t.Fatal("edit round trip changed stored configuration")
			}
		})
	}
}
