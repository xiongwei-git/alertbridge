package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

func TestQueryAlertsOrderAndDirectionalCursors(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "order.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	// Equal receipt times exercise the ID tie-breaker; occurrence times run backwards.
	for i, id := range []string{"a", "b", "c", "d", "private"} {
		route := "ops"
		if id == "private" {
			route = "private"
		}
		_, err := db.AcceptEvent(ctx, AcceptParams{RawPayload: []byte("{}"), ClientID: "test", Event: domain.Event{EventID: id, Source: "monitor", RoutingKey: route, Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: id, Message: "body", OccurredAt: now.Add(-time.Duration(i) * time.Hour)}, Now: now.Add(time.Duration(i/2) * time.Second), Targets: []string{"channel"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	asc, err := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{})
	if err != nil || len(asc.Alerts) != 4 {
		t.Fatalf("asc: %+v %v", asc, err)
	}
	for i := 1; i < len(asc.Alerts); i++ {
		a, b := asc.Alerts[i-1], asc.Alerts[i]
		if a.ReceivedAt.After(b.ReceivedAt) || (a.ReceivedAt.Equal(b.ReceivedAt) && a.ID >= b.ID) {
			t.Fatal("unstable ascending order")
		}
	}
	var got []string
	cursor := ""
	for page := 0; page < 3; page++ {
		p, err := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Order: "desc", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if p.Checkpoint != "" {
			t.Fatal("descending page exposed incremental checkpoint")
		}
		for _, a := range p.Alerts {
			got = append(got, a.ID)
		}
		if page == 0 {
			if len(p.Alerts) != 2 || p.Alerts[0].ID != asc.Alerts[3].ID {
				t.Fatal("latest page did not start with newest receipt")
			}
			if _, err := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Cursor: p.NextCursor}); err == nil {
				t.Fatal("descending cursor accepted by ascending query")
			}
		}
		cursor = p.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(got) != 4 {
		t.Fatalf("pagination returned %d alerts", len(got))
	}
	for i, id := range got {
		if id != asc.Alerts[len(got)-1-i].ID {
			t.Fatal("descending pagination skipped or duplicated a tied receipt")
		}
	}
	first, err := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Order: "desc", Cursor: first.NextCursor}); err == nil {
		t.Fatal("ascending cursor accepted by descending query")
	}
	// Original untagged cursors retain ascending semantics.
	legacy, _ := json.Marshal(map[string]any{"t": asc.Alerts[0].ReceivedAt.UnixMilli(), "i": asc.Alerts[0].ID})
	p, err := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Cursor: base64.RawURLEncoding.EncodeToString(legacy)})
	if err != nil || len(p.Alerts) != 3 {
		t.Fatalf("legacy cursor: %+v %v", p, err)
	}
	for _, order := range []string{"DESC", "sideways", "desc; DROP TABLE events"} {
		if _, err := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Order: order}); err == nil {
			t.Fatalf("accepted invalid order %q", order)
		}
	}
	p, err = db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Order: "desc", Since: now.Add(time.Second).Format(time.RFC3339Nano), Source: "monitor", Severity: "info", Status: "info"})
	if err != nil || len(p.Alerts) != 2 {
		t.Fatalf("descending filters: %+v %v", p, err)
	}
	p, err = db.QueryAlerts(ctx, nil, AlertQuery{Order: "desc"})
	if err != nil || len(p.Alerts) != 0 || p.Checkpoint != "" {
		t.Fatal("empty scope leaked")
	}
	p, err = db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Order: "desc", Source: "absent"})
	if err != nil || len(p.Alerts) != 0 || p.NextCursor != "" || p.Checkpoint != "" {
		t.Fatal("empty descending page")
	}
	// The historical ascending checkpoint still resumes newly received alerts.
	_, err = db.AcceptEvent(ctx, AcceptParams{RawPayload: []byte("{}"), ClientID: "test", Event: domain.Event{EventID: "new", Source: "monitor", RoutingKey: "ops", Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: "new", Message: "body", OccurredAt: now}, Now: now.Add(time.Hour), Targets: []string{"channel"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err = db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Cursor: asc.Checkpoint})
	if err != nil || len(p.Alerts) != 1 || p.Alerts[0].EventID != "new" {
		t.Fatal("ascending incremental checkpoint regressed")
	}
}

func TestQueryPaginationAndScope(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "c"} {
		_, e := db.AcceptEvent(ctx, AcceptParams{RawPayload: []byte("{}"), ClientID: "test", Event: domain.Event{EventID: id, Source: "monitor", RoutingKey: "ops", Status: domain.StatusInfo, Severity: domain.SeverityInfo, Title: id, Message: "body", OccurredAt: now}, Now: now, Targets: []string{"channel"}})
		if e != nil {
			t.Fatal(e)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	checkpoint := ""
	for i := 0; i < 3; i++ {
		p, e := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Limit: 1, Cursor: cursor})
		if e != nil || len(p.Alerts) != 1 {
			t.Fatalf("%+v %v", p, e)
		}
		if seen[p.Alerts[0].ID] {
			t.Fatal("duplicate page")
		}
		seen[p.Alerts[0].ID] = true
		cursor = p.NextCursor
		checkpoint = p.Checkpoint
	}
	if cursor != "" {
		t.Fatal("expected final page")
	}
	end, e := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Cursor: checkpoint})
	if e != nil || len(end.Alerts) != 0 || end.Checkpoint != checkpoint {
		t.Fatal("checkpoint failed")
	}
	if _, e := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Since: "not-a-time"}); e == nil {
		t.Fatal("invalid since")
	}
	p, e := db.QueryAlerts(ctx, nil, AlertQuery{})
	if e != nil || len(p.Alerts) != 0 {
		t.Fatal("empty scope leaked")
	}
	if _, e := db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Cursor: "bad"}); e == nil {
		t.Fatal("invalid cursor")
	}
	p, e = db.QueryAlerts(ctx, []string{"ops"}, AlertQuery{Source: "absent"})
	if e != nil || len(p.Alerts) != 0 {
		t.Fatal("source filter")
	}
}
