package credentials

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

// failSetKeychain rejects every write, simulating a keychain that is full,
// locked or otherwise unwritable.
type failSetKeychain struct {
	failSet bool
	store   map[string]string
}

func (f *failSetKeychain) Get(k string) (string, error) {
	if v, ok := f.store[k]; ok {
		return v, nil
	}
	return "", ErrKeychainNotFound
}
func (f *failSetKeychain) Set(k, v string) error {
	if f.failSet {
		return errors.New("keychain write failed")
	}
	f.store[k] = v
	return nil
}
func (f *failSetKeychain) Delete(k string) error { delete(f.store, k); return nil }

// keyringNotFoundKeychain returns the REAL error the OS keychain returns for
// a missing entry (keyring.ErrNotFound), never the package sentinel — the
// production keychain provider (backend/sync/keyring_desktop.go) forwards it
// unwrapped.
type keyringNotFoundKeychain struct{}

func (keyringNotFoundKeychain) Get(string) (string, error) { return "", keyring.ErrNotFound }
func (keyringNotFoundKeychain) Set(string, string) error   { return nil }
func (keyringNotFoundKeychain) Delete(string) error        { return nil }

// TestUnlockRejectsWrongMasterPassword locks in the verifier check: a typo'd
// master password must be rejected BEFORE anything is cached in the keychain
// and before the store reports Unlocked. Without a verifier the wrong key
// survived restarts through AutoUnlock and every later save used it.
func TestUnlockRejectsWrongMasterPassword(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	s := New(dir, kc)
	if err := s.Setup(ModeMasterPassword, "hunter2"); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	entry := "master-key/" + s.DirHash()
	if err := kc.Delete(entry); err != nil {
		t.Fatalf("clear cached key: %v", err)
	}

	s2 := New(dir, kc)
	if err := s2.AutoUnlock(); err != nil {
		t.Fatalf("AutoUnlock: %v", err)
	}
	if s2.Unlocked() {
		t.Fatal("expected locked store after cache cleared")
	}

	if err := s2.Unlock("wrong-password"); err == nil {
		t.Fatal("Unlock with a wrong master password must fail")
	}
	if _, ok := kc.store[entry]; ok {
		t.Fatal("wrong password was cached in the keychain")
	}
	if s2.Unlocked() {
		t.Fatal("store reports Unlocked after a failed Unlock")
	}

	if err := s2.Unlock("hunter2"); err != nil {
		t.Fatalf("Unlock with the correct password: %v", err)
	}
	if !s2.Unlocked() {
		t.Fatal("store should be unlocked after a correct Unlock")
	}
}

// TestAutoUnlockRejectsStaleCachedKey: a cached key that cannot open the
// verifier must not be adopted — otherwise a bad key survives restarts and
// every load/save silently mixes keys.
func TestAutoUnlockRejectsStaleCachedKey(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	s := New(dir, kc)
	if err := s.Setup(ModeMasterPassword, "hunter2"); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// Poison the cache with a key that was never used for this vault.
	stale, err := randomKey()
	if err != nil {
		t.Fatalf("randomKey: %v", err)
	}
	kc.store["master-key/"+s.DirHash()] = hex.EncodeToString(stale)

	s2 := New(dir, kc)
	if err := s2.AutoUnlock(); err != nil {
		t.Fatalf("AutoUnlock: %v", err)
	}
	if s2.Unlocked() {
		t.Fatal("AutoUnlock adopted a key that fails the meta verifier")
	}
	// The correct password still unlocks the vault.
	if err := s2.Unlock("hunter2"); err != nil {
		t.Fatalf("Unlock after stale cache: %v", err)
	}
}

// TestAutoUnlockMapsRealKeyringNotFound: the production keychain reports a
// missing entry as keyring.ErrNotFound, never as ErrKeychainNotFound. Only
// the test fake returns the sentinel, so without the mapping KeychainLost
// can never happen in production and the UI dead-ends in NeedsSetup.
func TestAutoUnlockMapsRealKeyringNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := WriteMeta(dir, &Meta{Mode: ModeKeychain}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	s := New(dir, keyringNotFoundKeychain{})
	if err := s.AutoUnlock(); err != nil {
		t.Fatalf("AutoUnlock must treat keyring.ErrNotFound as KeychainLost, got error: %v", err)
	}
	if !s.Status().KeychainLost {
		t.Fatalf("expected KeychainLost, got %+v", s.Status())
	}
}

// TestStoreHasSecretsCoversAIAndTunnels: ai.json and tunnels.json hold the
// same enc:v1 ciphertext under the credential key. Setup must refuse to hand
// out a fresh key over them, otherwise they are orphaned forever.
func TestStoreHasSecretsCoversAIAndTunnels(t *testing.T) {
	for _, tc := range []struct{ file, body string }{
		{"ai.json", `{"models":[{"id":"m1","apiKey":"enc:v1:nonce:blob"}]}`},
		{"tunnels.json", `{"tunnels":[{"id":"t1","upstream":{"pass":"enc:v1:nonce:blob"}}]}`},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.body), 0600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			s := New(dir, newFakeKeychain())
			if !s.storeHasSecrets() {
				t.Fatalf("%s ciphertext not detected by storeHasSecrets", tc.file)
			}
			if err := s.Setup(ModeMasterPassword, "pw"); err == nil {
				t.Fatalf("Setup must refuse while %s still holds ciphertext", tc.file)
			}
		})
	}
}

// TestRekeyWritesMetaBeforeKeychain: credentials.meta is the source of truth.
// If it cannot be written, the keychain must be left untouched — otherwise a
// failed Rekey leaves the keychain holding a key that no meta points at.
func TestRekeyWritesMetaBeforeKeychain(t *testing.T) {
	dir := t.TempDir()
	// A directory where the meta file belongs makes WriteMeta fail.
	if err := os.Mkdir(MetaPath(dir), 0755); err != nil {
		t.Fatalf("prepare meta obstacle: %v", err)
	}

	kc := newFakeKeychain()
	s := New(dir, kc)
	key, err := randomKey()
	if err != nil {
		t.Fatalf("randomKey: %v", err)
	}
	if err := s.Rekey(ModeKeychain, nil, key); err == nil {
		t.Fatal("Rekey must fail when credentials.meta cannot be written")
	}
	if _, ok := kc.store["keychain-key/"+s.DirHash()]; ok {
		t.Fatal("keychain entry written although the meta write failed")
	}
	if s.Unlocked() {
		t.Fatal("store must stay locked after a failed Rekey")
	}
}

// TestRekeyRestoresMetaWhenKeychainFails covers the inverse ordering: once
// meta holds the new mode/salt/verifier, a keychain write failure must roll
// meta back to its previous bytes so the two never diverge.
func TestRekeyRestoresMetaWhenKeychainFails(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	s := New(dir, kc)
	if err := s.Setup(ModeMasterPassword, "hunter2"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	before, err := os.ReadFile(MetaPath(dir))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}

	failing := &failSetKeychain{failSet: true, store: map[string]string{}}
	s2 := New(dir, failing)
	key, err := randomKey()
	if err != nil {
		t.Fatalf("randomKey: %v", err)
	}
	if err := s2.Rekey(ModeKeychain, nil, key); err == nil {
		t.Fatal("Rekey must fail when the keychain rejects the write")
	}
	after, err := os.ReadFile(MetaPath(dir))
	if err != nil {
		t.Fatalf("read meta after failed Rekey: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("meta not restored after keychain failure:\nbefore=%s\nafter=%s", before, after)
	}
}
