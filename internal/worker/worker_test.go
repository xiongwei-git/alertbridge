package worker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/channel"
	"github.com/xiongwei-git/alertbridge/internal/domain"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

type fakeSender struct {
	calls     int
	fail      bool
	lastEvent domain.Event
}

func (s *fakeSender) Send(_ context.Context, event domain.Event) (int, error) {
	s.calls++
	s.lastEvent = event
	if s.fail {
		return 503, &channel.SendError{Message: "temporary", StatusCode: 503, Retryable: true}
	}
	return 200, nil
}

func TestProcessOneConvertsEventToDisplayTimezone(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "alertbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 15, 17, 55, 36, 0, time.UTC)
	event := domain.Event{EventID: "evt-timezone", Source: "proxymonitor", RoutingKey: "base", Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: "Test", Message: "Test", OccurredAt: now}
	if _, err := db.AcceptEvent(context.Background(), store.AcceptParams{ClientID: "client", Event: event, Targets: []string{"feishu.ops"}, Now: now, DedupeWindow: time.Minute, RawPayload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	displayLocation := time.FixedZone("Asia/Shanghai", 8*60*60)
	w := New(db, map[string]channel.Sender{"feishu.ops": sender}, Config{DisplayLocation: displayLocation})
	if processed, err := w.ProcessOne(context.Background(), now); err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v", processed, err)
	}
	if got := sender.lastEvent.OccurredAt.Format(time.RFC3339); got != "2026-07-16T01:55:36+08:00" {
		t.Fatalf("display occurrence = %q", got)
	}
}

func TestProcessOneRetriesThenSucceeds(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "alertbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	event := domain.Event{EventID: "evt-1", Source: "gatus", RoutingKey: "infra", Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: "Test", Message: "Test", OccurredAt: now}
	accepted, err := db.AcceptEvent(context.Background(), store.AcceptParams{ClientID: "client", Event: event, Targets: []string{"feishu.ops"}, Now: now, DedupeWindow: time.Minute, RawPayload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{fail: true}
	w := New(db, map[string]channel.Sender{"feishu.ops": sender}, Config{LeaseDuration: 30 * time.Second, RetryDelays: []time.Duration{time.Second}, MaxAttempts: 2})
	processed, err := w.ProcessOne(context.Background(), now)
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v", processed, err)
	}
	status, _ := db.GetDeliveryStatus(context.Background(), accepted.EventID, "feishu.ops")
	if status.Status != "retrying" || status.Attempts != 1 {
		t.Fatalf("after failure = %+v", status)
	}
	sender.fail = false
	processed, err = w.ProcessOne(context.Background(), now.Add(time.Second))
	if err != nil || !processed {
		t.Fatalf("ProcessOne(retry) = %v, %v", processed, err)
	}
	status, _ = db.GetDeliveryStatus(context.Background(), accepted.EventID, "feishu.ops")
	if status.Status != "sent" || sender.calls != 2 {
		t.Fatalf("after success = %+v, calls=%d", status, sender.calls)
	}
}

func TestProcessOneDeadLettersPermanentFailure(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "alertbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	event := domain.Event{EventID: "evt-1", Source: "gatus", RoutingKey: "infra", Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: "Test", Message: "Test", OccurredAt: now}
	accepted, err := db.AcceptEvent(context.Background(), store.AcceptParams{ClientID: "client", Event: event, Targets: []string{"missing"}, Now: now, DedupeWindow: time.Minute, RawPayload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	w := New(db, nil, Config{LeaseDuration: time.Second, RetryDelays: []time.Duration{time.Second}, MaxAttempts: 2})
	if _, err := w.ProcessOne(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	status, _ := db.GetDeliveryStatus(context.Background(), accepted.EventID, "missing")
	if status.Status != "dead" {
		t.Fatalf("status = %+v", status)
	}
}
