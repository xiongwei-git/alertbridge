package domain

import (
	"strings"
	"testing"
	"time"
)

func validEvent(now time.Time) Event {
	return Event{
		EventID: "evt-001", Source: "gatus-hk", RoutingKey: "infrastructure",
		Status: StatusFiring, Severity: SeverityCritical, Title: "Proxy unavailable",
		Message: "Three checks failed.", OccurredAt: now, DedupeKey: "proxy-hk-connectivity",
		Labels: map[string]string{"region": "hong-kong"}, URL: "https://status.example.com/incidents/1",
	}
}

func TestEventValidate(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		mutate  func(*Event)
		wantErr string
	}{
		{name: "valid"},
		{name: "unknown status", mutate: func(e *Event) { e.Status = "broken" }, wantErr: "status"},
		{name: "resolved needs dedupe key", mutate: func(e *Event) { e.Status = StatusResolved; e.DedupeKey = "" }, wantErr: "dedupe_key"},
		{name: "future occurrence rejected", mutate: func(e *Event) { e.OccurredAt = now.Add(6 * time.Minute) }, wantErr: "future"},
		{name: "title line break rejected", mutate: func(e *Event) { e.Title = "down\nspoofed" }, wantErr: "line breaks"},
		{name: "javascript url rejected", mutate: func(e *Event) { e.URL = "javascript:alert(1)" }, wantErr: "absolute HTTP"},
		{name: "too many labels", mutate: func(e *Event) {
			e.Labels = map[string]string{}
			for i := 0; i < 21; i++ {
				e.Labels[string(rune('a'+i))] = "x"
			}
		}, wantErr: "20"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := validEvent(now)
			if tt.mutate != nil {
				tt.mutate(&event)
			}
			err := event.Validate(now)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
