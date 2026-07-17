package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

func TestRecordRequestRejectsReplayAndRateLimit(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	if err := store.RecordRequest(ctx, "client-a", "nonce-0001", now.Add(10*time.Minute), now, 1); err != nil {
		t.Fatalf("RecordRequest() error = %v", err)
	}
	if err := store.RecordRequest(ctx, "client-a", "nonce-0001", now.Add(10*time.Minute), now, 1); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay error = %v, want ErrReplay", err)
	}
	if err := store.RecordRequest(ctx, "client-a", "nonce-0002", now.Add(10*time.Minute), now, 1); !errors.Is(err, ErrRateLimit) {
		t.Fatalf("rate error = %v, want ErrRateLimit", err)
	}
}

func TestRecordRateLimitUsesIndependentBuckets(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	if err := database.RecordRequest(ctx, "baota-a", "nonce-0001", now.Add(10*time.Minute), now, 1); err != nil {
		t.Fatalf("HMAC request error = %v", err)
	}
	if err := database.RecordRateLimit(ctx, "bearer:baota-a", now, 1); err != nil {
		t.Fatalf("token must not share the HMAC bucket: %v", err)
	}
	if err := database.RecordRateLimit(ctx, "bearer:baota-b", now, 1); err != nil {
		t.Fatalf("independent token request error = %v", err)
	}
	if err := database.RecordRateLimit(ctx, "bearer:baota-a", now, 1); !errors.Is(err, ErrRateLimit) {
		t.Fatalf("rate error = %v, want ErrRateLimit", err)
	}
	var count int
	if err := database.db.QueryRowContext(ctx, "SELECT request_count FROM rate_windows WHERE client_id=? AND minute_bucket=?", "bearer:baota-a", now.Unix()/60).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rejected requests must not grow the rate counter: count=%d err=%v", count, err)
	}
}

func TestAcceptEventLifecycleAndIdempotency(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	first := testEvent(now, "evt-1", domain.StatusFiring)
	result, err := store.AcceptEvent(ctx, AcceptParams{ClientID: "client-a", Event: first, Targets: []string{"feishu.ops"}, Now: now, DedupeWindow: 30 * time.Minute, RawPayload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("AcceptEvent(first) error = %v", err)
	}
	if result.Outcome != OutcomeQueued || result.Deliveries != 1 {
		t.Fatalf("first result = %+v", result)
	}

	repeat := testEvent(now.Add(time.Minute), "evt-2", domain.StatusFiring)
	result, err = store.AcceptEvent(ctx, AcceptParams{ClientID: "client-a", Event: repeat, Targets: []string{"feishu.ops"}, Now: now.Add(time.Minute), DedupeWindow: 30 * time.Minute, RawPayload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("AcceptEvent(repeat) error = %v", err)
	}
	if result.Outcome != OutcomeSuppressed || result.Reason != "dedupe_window" || result.Deliveries != 0 {
		t.Fatalf("repeat result = %+v", result)
	}

	resolved := testEvent(now.Add(2*time.Minute), "evt-3", domain.StatusResolved)
	result, err = store.AcceptEvent(ctx, AcceptParams{ClientID: "client-a", Event: resolved, Targets: []string{"feishu.ops"}, Now: now.Add(2 * time.Minute), DedupeWindow: 30 * time.Minute, RawPayload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("AcceptEvent(resolved) error = %v", err)
	}
	if result.Outcome != OutcomeQueued || result.IncidentStartedAt == nil || !result.IncidentStartedAt.Equal(now) {
		t.Fatalf("resolved result = %+v", result)
	}

	result, err = store.AcceptEvent(ctx, AcceptParams{ClientID: "client-a", Event: first, Targets: []string{"feishu.ops"}, Now: now.Add(3 * time.Minute), DedupeWindow: 30 * time.Minute, RawPayload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("AcceptEvent(duplicate) error = %v", err)
	}
	if result.Outcome != OutcomeDuplicate || result.EventID == "" || result.Deliveries != 1 {
		t.Fatalf("duplicate result = %+v", result)
	}
}

func TestClaimAndCompleteDelivery(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	event := testEvent(now, "evt-1", domain.StatusInfo)
	accepted, err := store.AcceptEvent(ctx, AcceptParams{ClientID: "client-a", Event: event, Targets: []string{"feishu.ops"}, Now: now, DedupeWindow: time.Minute, RawPayload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := store.ClaimDelivery(ctx, now, 30*time.Second)
	if err != nil || delivery == nil {
		t.Fatalf("ClaimDelivery() = %+v, %v", delivery, err)
	}
	if delivery.Attempts != 1 || delivery.Event.EventID != "evt-1" {
		t.Fatalf("delivery = %+v", delivery)
	}
	if err := store.CompleteFailure(ctx, delivery.ID, delivery.Attempts, now.Add(time.Second), "temporary", 503, false); err != nil {
		t.Fatal(err)
	}
	status, err := store.GetDeliveryStatus(ctx, accepted.EventID, "feishu.ops")
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "retrying" || status.Attempts != 1 {
		t.Fatalf("status = %+v", status)
	}
	delivery, err = store.ClaimDelivery(ctx, now.Add(time.Second), 30*time.Second)
	if err != nil || delivery == nil {
		t.Fatalf("second ClaimDelivery() = %+v, %v", delivery, err)
	}
	if err := store.CompleteSuccess(ctx, delivery.ID, 200, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	status, err = store.GetDeliveryStatus(ctx, accepted.EventID, "feishu.ops")
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "sent" || status.Attempts != 2 {
		t.Fatalf("status = %+v", status)
	}
}

func TestPruneKeepsPendingAndDeletesTerminalEvents(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"pending", "sent"} {
		event := testEvent(now, id, domain.StatusInfo)
		if _, err := database.AcceptEvent(ctx, AcceptParams{ClientID: "client-a", Event: event, Targets: []string{"feishu.ops"}, Now: now, DedupeWindow: time.Minute, RawPayload: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	delivery, err := database.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || delivery == nil {
		t.Fatalf("claim = %+v, %v", delivery, err)
	}
	if err := database.CompleteSuccess(ctx, delivery.ID, 200, now); err != nil {
		t.Fatal(err)
	}
	deleted, err := database.Prune(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("Prune() deleted %d, want 1", deleted)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "alertbridge.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testEvent(at time.Time, id string, status domain.Status) domain.Event {
	return domain.Event{EventID: id, Source: "gatus", RoutingKey: "infrastructure", Status: status,
		Severity: domain.SeverityCritical, Title: "Node unavailable", Message: "Connection failed",
		OccurredAt: at, DedupeKey: "node-1-connectivity", Labels: map[string]string{"node": "node-1"}}
}
