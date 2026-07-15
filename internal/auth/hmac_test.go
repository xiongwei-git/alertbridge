package auth

import (
	"errors"
	"testing"
	"time"
)

func TestVerifier(t *testing.T) {
	now := time.Unix(1784090000, 0)
	body := []byte(`{"event_id":"evt-1"}`)
	secret := []byte("correct horse battery staple")
	verifier := Verifier{
		Clients: map[string]Client{"gatus-us": {
			ID: "gatus-us", Secret: secret, Enabled: true,
			AllowedRoutes: map[string]struct{}{"infrastructure": {}}, RateLimitPerMin: 60,
		}},
		Tolerance: 5 * time.Minute, Now: func() time.Time { return now },
	}
	headers := Headers{ClientID: "gatus-us", Timestamp: "1784090000", Nonce: "nonce-0001"}
	headers.Signature = SignHex(secret, headers.Timestamp, headers.Nonce, body)
	if _, err := verifier.Verify(headers, body); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	t.Run("tampered body", func(t *testing.T) {
		if _, err := verifier.Verify(headers, append(body, ' ')); !errors.Is(err, ErrInvalidAuth) {
			t.Fatalf("Verify() error = %v, want ErrInvalidAuth", err)
		}
	})
	t.Run("stale timestamp", func(t *testing.T) {
		stale := headers
		stale.Timestamp = "1784089000"
		stale.Signature = SignHex(secret, stale.Timestamp, stale.Nonce, body)
		if _, err := verifier.Verify(stale, body); !errors.Is(err, ErrStaleRequest) {
			t.Fatalf("Verify() error = %v, want ErrStaleRequest", err)
		}
	})
	t.Run("short nonce", func(t *testing.T) {
		bad := headers
		bad.Nonce = "short"
		if _, err := verifier.Verify(bad, body); !errors.Is(err, ErrInvalidAuth) {
			t.Fatalf("Verify() error = %v, want ErrInvalidAuth", err)
		}
	})
}
