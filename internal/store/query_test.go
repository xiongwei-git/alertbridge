package store

import (
	"context"
	"github.com/xiongwei-git/alertbridge/internal/domain"
	"path/filepath"
	"testing"
	"time"
)

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
