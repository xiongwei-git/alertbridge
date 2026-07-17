package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestBearerVerifierAcceptsGeneratedTokenAndRejectsTampering(t *testing.T) {
	plain, publicID, secretHash, err := GenerateBearerToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "abt_") || len(publicID) != 16 || len(secretHash) != 32 {
		t.Fatalf("generated token = %q publicID=%q hashLen=%d", plain, publicID, len(secretHash))
	}
	expected := IngressToken{
		ID:                 "baota-prod",
		PublicID:           publicID,
		SecretHash:         secretHash,
		Enabled:            true,
		RoutingKey:         "infrastructure",
		Severity:           "warning",
		RateLimitPerMinute: 10,
	}
	verifier := BearerVerifier{Lookup: func(candidate string) (IngressToken, bool) {
		if candidate == publicID {
			return expected, true
		}
		return IngressToken{}, false
	}}

	got, err := verifier.Verify("Bearer " + plain)
	if err != nil || got.ID != expected.ID || got.RoutingKey != expected.RoutingKey {
		t.Fatalf("Verify() = %+v, %v", got, err)
	}

	t.Run("tampered secret", func(t *testing.T) {
		last := plain[len(plain)-1]
		replacement := byte('0')
		if last == replacement {
			replacement = '1'
		}
		tampered := plain[:len(plain)-1] + string(replacement)
		if _, err := verifier.Verify("Bearer " + tampered); !errors.Is(err, ErrInvalidBearer) {
			t.Fatalf("Verify() error = %v, want ErrInvalidBearer", err)
		}
	})

	t.Run("unknown token id", func(t *testing.T) {
		parts := strings.Split(plain, "_")
		unknown := "abt_ffffffffffffffff_" + parts[2]
		if _, err := verifier.Verify("Bearer " + unknown); !errors.Is(err, ErrInvalidBearer) {
			t.Fatalf("Verify() error = %v, want ErrInvalidBearer", err)
		}
	})

	t.Run("disabled token", func(t *testing.T) {
		disabled := verifier
		disabled.Lookup = func(string) (IngressToken, bool) {
			value := expected
			value.Enabled = false
			return value, true
		}
		if _, err := disabled.Verify("Bearer " + plain); !errors.Is(err, ErrInvalidBearer) {
			t.Fatalf("Verify() error = %v, want ErrInvalidBearer", err)
		}
	})
}
