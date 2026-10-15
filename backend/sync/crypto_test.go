package sync

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestEncryptDecryptWithAAD_RoundTrip verifies that encryptBytesWithAAD +
// decryptBytesWithAAD round-trip when the same AAD is provided.
func TestEncryptDecryptWithAAD_RoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef") // 32 bytes — AES-256
	plaintext := []byte(`{"theme":"dark"}`)
	aad := []byte("connections.json")

	encoded, err := encryptBytesWithAAD(plaintext, key, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := decryptBytesWithAAD(encoded, key, aad)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, plaintext)
	}
}

// TestEncryptBytesWithAAD_AADMismatchRejected is the cross-file swap guard
// (SYNC-P1-1): an attacker who can paste ciphertext from one file over
// another (e.g. settings.json.enc → connections.json.enc) must fail the
// AAD check on decryption.
func TestEncryptBytesWithAAD_AADMismatchRejected(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := []byte(`{"password":"s3cret"}`)
	connAAD := []byte("connections.json")
	settingsAAD := []byte("settings.json")

	// Encrypt under connections.json AAD.
	encoded, err := encryptBytesWithAAD(plaintext, key, connAAD)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Attempt to decrypt under settings.json AAD — must fail.
	if _, err := decryptBytesWithAAD(encoded, key, settingsAAD); err == nil {
		t.Fatalf("AAD mismatch was accepted — cross-file ciphertext swap is undetected")
	}

	// Sanity: decrypting under the correct AAD still works.
	got, err := decryptBytesWithAAD(encoded, key, connAAD)
	if err != nil {
		t.Fatalf("decrypt with correct AAD: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext mismatch")
	}
}

// TestEncryptBytes_AADBinding ensures sync ciphertexts are bound to their
// file name: a swap across files must fail, while legacy nil-AAD ciphertexts
// (written before SYNC-P1-1 was wired up) still decrypt as a fallback.
func TestEncryptBytes_AADBinding(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := []byte(`{"theme":"dark"}`)

	encoded, err := encryptBytes(plaintext, key, "settings.json")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := decryptBytes(encoded, key, "settings.json", 0)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, plaintext)
	}

	// The same ciphertext under a different file name must fail the AAD check.
	if _, err := decryptBytes(encoded, key, "connections.json", 0); err == nil {
		t.Fatal("ciphertext decrypted under the wrong file name — AAD not enforced")
	}

	// Legacy nil-AAD ciphertexts stay readable via the fallback path in a
	// legacy (version 0) repo.
	legacy, err := encryptBytesWithAAD(plaintext, key, nil)
	if err != nil {
		t.Fatalf("legacy encrypt: %v", err)
	}
	got, err = decryptBytes(legacy, key, "settings.json", 0)
	if err != nil {
		t.Fatalf("legacy decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("legacy round-trip mismatch:\n got %q\nwant %q", got, plaintext)
	}
}

// unlockedPS is a PasswordStore that reports a usable key.
type unlockedPS struct{}

func (unlockedPS) Encrypt(plaintext string) (string, error) { return plaintext, nil }
func (unlockedPS) Decrypt(encoded string) (string, error)   { return encoded, nil }
func (unlockedPS) Unlocked() bool                           { return true }

// lockedPS is a PasswordStore that reports itself locked; used to assert that
// sync refuses to run when the credential store holds no usable key.
type lockedPS struct{}

func (lockedPS) Encrypt(plaintext string) (string, error) { return "", nil }
func (lockedPS) Decrypt(encoded string) (string, error)   { return "", nil }
func (lockedPS) Unlocked() bool                           { return false }

// TestRequireUnlocked verifies sync refuses to run when the credential store is
// locked, but still allows a nil (unwired) or unlocked store.
func TestRequireUnlocked(t *testing.T) {
	s := &SyncService{}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.requireUnlocked(); err != nil {
		t.Fatalf("nil store should be allowed, got %v", err)
	}

	s.passwordStore = unlockedPS{}
	if err := s.requireUnlocked(); err != nil {
		t.Fatalf("unlocked store should be allowed, got %v", err)
	}

	s.passwordStore = lockedPS{}
	if err := s.requireUnlocked(); err == nil {
		t.Fatal("locked store should be refused")
	}
}

func TestEncryptDecryptScopedOnlyTouchesScope(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef")

	for _, name := range []string{"connections.json", "settings.json"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(`{"a":1}`), 0600); err != nil {
			t.Fatal(err)
		}
	}

	files := []string{"connections.json"}
	if err := EncryptConfigFilesScoped(files, src, dest, key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("out-of-scope settings.json must not be encrypted into dest")
	}
	if _, err := os.Stat(filepath.Join(dest, "connections.json")); err != nil {
		t.Fatal("scoped connections.json missing")
	}

	// Round-trip.
	roundTripDest := t.TempDir()
	if err := DecryptConfigFilesScoped(files, dest, roundTripDest, key); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(roundTripDest, "connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("decrypted connections.json = %q, want %q", got, `{"a":1}`)
	}
	if _, err := os.Stat(filepath.Join(roundTripDest, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("out-of-scope settings.json must not be decrypted into round-trip dest")
	}
}

func TestLegacyWrappersStillWork(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(filepath.Join(src, "connections.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := EncryptConfigFiles(src, dest, key); err != nil {
		t.Fatal(err)
	}
	if err := DecryptConfigFiles(dest, t.TempDir(), key); err != nil {
		t.Fatal(err)
	}
}
