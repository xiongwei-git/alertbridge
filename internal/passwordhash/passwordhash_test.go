package passwordhash

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	encoded, err := Hash([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "correct horse battery staple") {
		t.Fatal("encoded hash contains the password")
	}
	if err := Validate(encoded); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !Verify([]byte("correct horse battery staple"), encoded) {
		t.Fatal("Verify() rejected the correct password")
	}
	if Verify([]byte("wrong password value"), encoded) {
		t.Fatal("Verify() accepted an incorrect password")
	}
}

func TestVerifyRejectsMalformedOrExcessiveParameters(t *testing.T) {
	values := []string{
		"",
		"not-a-password-hash",
		"$argon2id$v=19$m=999999999,t=3,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=1junk$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=18$m=65536,t=3,p=1$c2FsdA$aGFzaA",
	}
	for _, value := range values {
		if Verify([]byte("password"), value) {
			t.Fatalf("Verify() accepted %q", value)
		}
	}
}
