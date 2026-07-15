package domain

import (
	"testing"
	"time"
)

func BenchmarkEventValidate(b *testing.B) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	event := validEvent(now)
	for b.Loop() {
		if err := event.Validate(now); err != nil {
			b.Fatal(err)
		}
	}
}
