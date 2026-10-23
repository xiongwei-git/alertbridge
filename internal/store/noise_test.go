package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

func TestNoiseLifecycleAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noise.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	accept := func(id string, status domain.Status, severity domain.Severity, occurred, received int, key, want, reason string) {
		t.Helper()
		event := testEvent(start.Add(time.Duration(occurred)*time.Minute), id, status)
		event.Severity = severity
		event.DedupeKey = key
		got, err := db.AcceptEvent(context.Background(), AcceptParams{ClientID: "client-a", Event: event, Now: start.Add(time.Duration(received) * time.Minute), DedupeWindow: 10 * time.Minute, Targets: []string{"offline"}, RawPayload: []byte(`{"original":true}`)})
		if err != nil || string(got.Outcome) != want || got.Reason != reason {
			t.Fatalf("%s: %+v, %v", id, got, err)
		}
	}
	key := "tmdb-check"
	accept("first", domain.StatusFiring, domain.SeverityInfo, 0, 0, key, "queued", "")
	accept("repeat", domain.StatusFiring, domain.SeverityInfo, 1, 1, key, "suppressed", "dedupe_window")
	accept("upgrade", domain.StatusFiring, domain.SeverityCritical, 2, 2, key, "queued", "")
	accept("other", domain.StatusFiring, domain.SeverityWarning, 2, 2, "other-check", "queued", "")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	accept("first", domain.StatusFiring, domain.SeverityInfo, 0, 3, key, "duplicate", "")
	accept("late", domain.StatusResolved, domain.SeverityInfo, 1, 3, key, "suppressed", "stale_event")
	accept("healthy", domain.StatusResolved, domain.SeverityInfo, 4, 4, key, "queued", "")
	accept("flap", domain.StatusFiring, domain.SeverityWarning, 5, 5, key, "queued", "")
	accept("late-fire", domain.StatusFiring, domain.SeverityCritical, 3, 6, key, "suppressed", "stale_event")
	accept("upgrade-again", domain.StatusFiring, domain.SeverityCritical, 6, 6, key, "queued", "")
	accept("recovered", domain.StatusResolved, domain.SeverityInfo, 7, 7, key, "queued", "")
	var count int
	if err := db.db.QueryRow("SELECT COUNT(*) FROM events WHERE length(raw_payload)>0").Scan(&count); err != nil || count != 10 {
		t.Fatalf("raw count=%d err=%v", count, err)
	}
}
