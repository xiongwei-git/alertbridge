package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

func TestDynamicConfigurationCRUD(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	client := ClientRecord{ID: "gatus", Enabled: true, SecretCipher: []byte("cipher"), AllowedRoutes: []string{"infra"}, RateLimitPerMinute: 60}
	if err := database.UpsertClient(ctx, client, now); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetClient(ctx, "gatus")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || !bytes.Equal(got.SecretCipher, client.SecretCipher) || len(got.AllowedRoutes) != 1 {
		t.Fatalf("client = %+v", got)
	}

	channel := ChannelRecord{ID: "feishu.ops", Type: "feishu", Enabled: true, ConfigCipher: []byte("encrypted")}
	if err := database.UpsertChannel(ctx, channel, now); err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceRoute(ctx, "infra", "critical", []string{"feishu.ops"}, now); err != nil {
		t.Fatal(err)
	}
	routes, err := database.ListRoutes(ctx)
	if err != nil || len(routes) != 1 || routes[0].ChannelID != "feishu.ops" {
		t.Fatalf("routes = %+v, %v", routes, err)
	}

	silenceID, err := database.CreateSilence(ctx, SilenceRecord{RoutingKey: "infra", Severity: "warning", StartsAt: now, EndsAt: now.Add(time.Hour), Reason: "maintenance"}, now)
	if err != nil || silenceID == "" {
		t.Fatalf("CreateSilence() = %q, %v", silenceID, err)
	}
	silences, err := database.ListSilences(ctx)
	if err != nil || len(silences) != 1 {
		t.Fatalf("silences = %+v, %v", silences, err)
	}
	if err := database.DeleteSilence(ctx, silenceID); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteChannel(ctx, "feishu.ops"); err != nil {
		t.Fatal(err)
	}
	routes, err = database.ListRoutes(ctx)
	if err != nil || len(routes) != 0 {
		t.Fatalf("routes after channel delete = %+v, %v", routes, err)
	}
}

func TestAdminSessionAndLoginLimit(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	hash := bytes.Repeat([]byte{1}, 32)
	if err := database.CreateAdminSession(ctx, hash, "csrf-token", now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	session, err := database.GetAdminSession(ctx, hash, now)
	if err != nil || session.CSRFToken != "csrf-token" {
		t.Fatalf("session = %+v, %v", session, err)
	}
	if _, err := database.GetAdminSession(ctx, hash, now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session error = %v", err)
	}
	if err := database.RecordAdminLoginAttempt(ctx, now, 1); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordAdminLoginAttempt(ctx, now, 1); !errors.Is(err, ErrRateLimit) {
		t.Fatalf("login limit error = %v", err)
	}
}

func TestAdminCredentialIsInitializedOnce(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	created, err := database.InitializeAdminCredential(ctx, AdminCredential{Username: "admin", PasswordHash: "$argon2id$first"}, now)
	if err != nil || !created {
		t.Fatalf("first InitializeAdminCredential() = %v, %v", created, err)
	}
	created, err = database.InitializeAdminCredential(ctx, AdminCredential{Username: "attacker", PasswordHash: "$argon2id$second"}, now.Add(time.Minute))
	if err != nil || created {
		t.Fatalf("second InitializeAdminCredential() = %v, %v", created, err)
	}
	credential, err := database.GetAdminCredential(ctx)
	if err != nil || credential.Username != "admin" || credential.PasswordHash != "$argon2id$first" {
		t.Fatalf("credential = %+v, %v", credential, err)
	}
}

func TestDeliveryListingAndDeadRetry(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	event := domain.Event{EventID: "admin-event", Source: "admin", RoutingKey: "infra", Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: "test", Message: "test", OccurredAt: now}
	accepted, err := database.AcceptEvent(ctx, AcceptParams{ClientID: "admin", Event: event, Targets: []string{"missing"}, Now: now, DedupeWindow: time.Minute, RawPayload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := database.ClaimDelivery(ctx, now, time.Second)
	if err != nil || delivery == nil {
		t.Fatalf("delivery = %+v, %v", delivery, err)
	}
	if err := database.CompleteFailure(ctx, delivery.ID, delivery.Attempts, now, "permanent", 400, true); err != nil {
		t.Fatal(err)
	}
	items, total, err := database.ListDeliveries(ctx, "dead", 50, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].EventID != "admin-event" {
		t.Fatalf("deliveries = %+v total=%d err=%v", items, total, err)
	}
	if err := database.RetryDeadDelivery(ctx, items[0].ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	status, err := database.GetDeliveryStatus(ctx, accepted.EventID, "missing")
	if err != nil || status.Status != "pending" || status.Attempts != 0 {
		t.Fatalf("status = %+v, %v", status, err)
	}
}
