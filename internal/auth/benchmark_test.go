package auth

import "testing"

func BenchmarkSign(b *testing.B) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	body := []byte(`{"event_id":"benchmark-event","message":"node unavailable"}`)
	for b.Loop() {
		_ = Sign(secret, "1784090000", "benchmark-nonce", body)
	}
}
