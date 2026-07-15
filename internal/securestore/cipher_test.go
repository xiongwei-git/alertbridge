package securestore

import (
	"bytes"
	"testing"
)

func TestCipherRoundTripAndTamperDetection(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	cipher, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	first, err := cipher.Encrypt([]byte("client-secret"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := cipher.Encrypt([]byte("client-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("ciphertexts must use unique nonces")
	}
	plain, err := cipher.Decrypt(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "client-secret" {
		t.Fatalf("plaintext = %q", plain)
	}
	first[len(first)-1] ^= 0xff
	if _, err := cipher.Decrypt(first); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestCipherRejectsWrongKeyLength(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("short key was accepted")
	}
}
