package importer

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	salt, err := newSalt()
	if err != nil {
		t.Fatalf("newSalt: %v", err)
	}
	key := deriveKey("correct horse", salt, defaultKDFIterations)
	enc, err := encryptField("hunter2", key)
	if err != nil {
		t.Fatalf("encryptField: %v", err)
	}
	if strings.Contains(enc, "hunter2") {
		t.Fatalf("ciphertext leaks plaintext: %q", enc)
	}
	got, err := decryptField(enc, key)
	if err != nil {
		t.Fatalf("decryptField: %v", err)
	}
	if got != "hunter2" {
		t.Fatalf("round trip mismatch: got %q", got)
	}
}

func TestDecryptWrongPasswordFails(t *testing.T) {
	salt, _ := newSalt()
	right := deriveKey("right", salt, defaultKDFIterations)
	wrong := deriveKey("wrong", salt, defaultKDFIterations)
	enc, _ := encryptField("secret", right)
	if _, err := decryptField(enc, wrong); err == nil {
		t.Fatalf("expected error decrypting with wrong password")
	}
}

func TestValidateKDFIterations(t *testing.T) {
	for _, bad := range []int{-1, 0, 9999, 5_000_001} {
		if err := validateKDFIterations(bad); err == nil {
			t.Errorf("iterations=%d must be rejected", bad)
		}
	}
	for _, ok := range []int{10000, 100000, defaultKDFIterations, 5_000_000} {
		if err := validateKDFIterations(ok); err != nil {
			t.Errorf("iterations=%d must be accepted: %v", ok, err)
		}
	}
}

func TestNewIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := newConnectionID()
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
		if !strings.HasPrefix(id, "conn-") {
			t.Fatalf("bad prefix: %q", id)
		}
	}
}
