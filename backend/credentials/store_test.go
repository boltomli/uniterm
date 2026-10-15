package credentials

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	unitsync "github.com/ys-ll/uniterm/backend/sync"
)

type fakeKeychain struct {
	mu    sync.Mutex
	store map[string]string
}

func newFakeKeychain() *fakeKeychain { return &fakeKeychain{store: map[string]string{}} }
func (f *fakeKeychain) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.store[key]; ok {
		return v, nil
	}
	return "", ErrKeychainNotFound
}
func (f *fakeKeychain) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store[key] = value
	return nil
}
func (f *fakeKeychain) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.store, key)
	return nil
}
func (f *fakeKeychain) List() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.store))
	for k := range f.store {
		keys = append(keys, k)
	}
	return keys, nil
}

// transientKeychain reports a non-notfound error on reads, simulating e.g. a
// temporarily denied macOS keychain permission.
type transientKeychain struct{}

func (transientKeychain) Get(string) (string, error) {
	return "", errors.New("keychain temporarily unavailable")
}
func (transientKeychain) Set(string, string) error { return nil }
func (transientKeychain) Delete(string) error      { return nil }
func (transientKeychain) List() ([]string, error)  { return nil, errors.New("keychain temporarily unavailable") }

func TestSetupKeychainMode(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	s := New(dir, kc)
	if err := s.Setup(ModeKeychain, ""); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	st := s.Status()
	if st.Mode != ModeKeychain || !st.Unlocked || st.NeedsSetup || st.KeychainLost {
		t.Fatalf("status = %+v", st)
	}
	// Round-trip encrypt/decrypt.
	enc, err := s.Encrypt("pw")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := s.Decrypt(enc)
	if err != nil || got != "pw" {
		t.Fatalf("Decrypt = %q, %v", got, err)
	}
	// Key persisted to keychain-key/<hash>.
	if len(kc.store["keychain-key/"+s.DirHash()]) == 0 {
		t.Fatalf("keychain key not persisted")
	}
}

func TestSetupMasterPasswordThenUnlock(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	s := New(dir, kc)
	if err := s.Setup(ModeMasterPassword, "hunter2"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	// A fresh store (simulating restart) must unlock from cached key.
	s2 := New(dir, kc)
	if err := s2.AutoUnlock(); err != nil {
		t.Fatalf("AutoUnlock: %v", err)
	}
	if !s2.Status().Unlocked {
		t.Fatalf("expected auto-unlock from cached key")
	}
	// After losing the cache, Unlock with the password re-derives the key.
	s3 := New(dir, kc)
	_ = kc.Delete("master-key/" + s3.DirHash())
	if err := s3.AutoUnlock(); err != nil {
		t.Fatalf("AutoUnlock: %v", err)
	}
	if s3.Status().Unlocked {
		t.Fatalf("expected locked after cache cleared")
	}
	if err := s3.Unlock("hunter2"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if !s3.Status().Unlocked {
		t.Fatalf("expected unlocked after Unlock")
	}
}

func TestAutoUnlockKeychainLost(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	s := New(dir, kc)
	_ = s.Setup(ModeKeychain, "")
	// Simulate a fresh machine: meta exists but keychain key is gone.
	_ = kc.Delete("keychain-key/" + s.DirHash())
	s2 := New(dir, kc)
	if err := s2.AutoUnlock(); err != nil {
		t.Fatalf("AutoUnlock: %v", err)
	}
	if !s2.Status().KeychainLost {
		t.Fatalf("expected KeychainLost, got %+v", s2.Status())
	}
}

// --- Moved data dir: the keychain entry is scoped by data-dir hash, so a
// copied directory misses its entry even though the key itself still sits in
// the OS keychain under the old hash. AutoUnlock must recover the surviving
// key instead of declaring it lost; a dir with nothing to recover stays lost
// and the count of existing ciphertext fields lets the frontend route to the
// recovery dialog instead of first-run mode selection. ---

// copyDataDir mimics the user copying the app dir (meta + config files) to a
// new path. The keychain entry stays scoped to the old path's hash.
func copyDataDir(t *testing.T, from, to string) {
	t.Helper()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, e.Name()), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAutoUnlockRecoversKeyAfterDirMove: a keychain-mode data dir copied to a
// new path must find its old key in the keychain (under the old dir hash),
// re-scope it to the new hash and unlock — no data loss, no lost-key flow.
func TestAutoUnlockRecoversKeyAfterDirMove(t *testing.T) {
	kc := newFakeKeychain()
	oldDir := t.TempDir()
	sOld := New(oldDir, kc)
	sOld.SetSecretFiles([]string{"connections.json"})
	if err := sOld.Setup(ModeKeychain, ""); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	enc, err := sOld.Encrypt("pw")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	body := `{"connections":[{"id":"c1","password":"` + enc + `"}]}`
	if err := os.WriteFile(filepath.Join(oldDir, "connections.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	newDir := t.TempDir()
	copyDataDir(t, oldDir, newDir)
	sNew := New(newDir, kc)
	sNew.SetSecretFiles([]string{"connections.json"})
	if err := sNew.AutoUnlock(); err != nil {
		t.Fatalf("AutoUnlock after move: %v", err)
	}
	if st := sNew.Status(); !st.Unlocked || st.KeychainLost || st.DecryptFailed {
		t.Fatalf("moved dir must recover its key: %+v", st)
	}
	// The recovered key is re-scoped to the new hash (Rekey), so the next
	// start unlocks through the normal path.
	if len(kc.store["keychain-key/"+sNew.DirHash()]) == 0 {
		t.Fatal("recovered key must be re-scoped to the new dir hash")
	}
	if got, err := sNew.Decrypt(enc); err != nil || got != "pw" {
		t.Fatalf("Decrypt after recovery = %q, %v", got, err)
	}
	sNext := New(newDir, kc)
	sNext.SetSecretFiles([]string{"connections.json"})
	if err := sNext.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	if !sNext.Status().Unlocked {
		t.Fatalf("second start after recovery must unlock normally: %+v", sNext.Status())
	}
}

// TestAutoUnlockRecoveryFailsStaysLost: ciphertext the keychain cannot open
// (no entry matches) must stay a lost key — not silently adopted, not reset.
func TestAutoUnlockRecoveryFailsStaysLost(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	salt := []byte("0123456789abcdef")
	seedCiphertext(t, dir, unitsync.DeriveKey("unknown-origin", salt), ModeKeychain, salt)
	// An unrelated surviving entry: must not be adopted.
	h := New(dir, kc).DirHash()
	if err := kc.Set("keychain-key/"+h, hex.EncodeToString(unitsync.DeriveKey("this-machine", salt))); err != nil {
		t.Fatal(err)
	}

	// The scoped entry is missing (copied dir), only the unrelated one exists.
	_ = kc.Delete("keychain-key/" + h)
	s := New(dir, kc)
	s.SetSecretFiles([]string{"connections.json"})
	if err := s.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.Unlocked || !st.KeychainLost {
		t.Fatalf("unmatchable ciphertext must stay keychain-lost: %+v", st)
	}
}

// TestAutoUnlockNoCiphertextDoesNotAdoptKeys: verification passes trivially on
// a ciphertext-free dir, so recovery must not run there — a wrong key would
// silently become the store's key.
func TestAutoUnlockNoCiphertextDoesNotAdoptKeys(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	salt := []byte("0123456789abcdef")
	if err := WriteMeta(dir, &Meta{Mode: ModeKeychain, Salt: salt}); err != nil {
		t.Fatal(err)
	}
	// A foreign key survives in the keychain under some other dir's hash.
	foreign := hex.EncodeToString(unitsync.DeriveKey("foreign", salt))
	if err := kc.Set("keychain-key/other-dir-hash", foreign); err != nil {
		t.Fatal(err)
	}

	s := New(dir, kc)
	if err := s.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.Unlocked || !st.KeychainLost {
		t.Fatalf("must not adopt a foreign key without ciphertext: %+v", st)
	}
	if _, err := kc.Get("keychain-key/" + s.DirHash()); !errors.Is(err, ErrKeychainNotFound) {
		t.Fatalf("recovery must not write a keychain entry: %v", err)
	}
}

// TestAutoUnlockStaleKeyRecoversViaOtherEntry: a scoped-but-stale entry must
// not mask a still-valid key from another dir hash (data replaced by a copy
// from a directory whose key is still in the keychain).
func TestAutoUnlockStaleKeyRecoversViaOtherEntry(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	salt := []byte("0123456789abcdef")
	right := unitsync.DeriveKey("old-dir", salt)
	seedCiphertext(t, dir, right, ModeKeychain, salt)
	// This dir has its own (stale) scoped entry.
	h := New(dir, kc).DirHash()
	if err := kc.Set("keychain-key/"+h, hex.EncodeToString(unitsync.DeriveKey("stale", salt))); err != nil {
		t.Fatal(err)
	}
	// The original dir's key survives under the old hash.
	if err := kc.Set("keychain-key/old-dir-hash", hex.EncodeToString(right)); err != nil {
		t.Fatal(err)
	}

	s := New(dir, kc)
	s.SetSecretFiles([]string{"connections.json"})
	if err := s.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); !st.Unlocked || st.DecryptFailed {
		t.Fatalf("the surviving old-dir key must be adopted: %+v", st)
	}
}

// TestTransientKeychainReadSkipsRecovery: a keychain read failure must surface
// as an AutoUnlock error, never as a lost key (reset flow would wipe secrets).
func TestTransientKeychainReadSkipsRecovery(t *testing.T) {
	dir := t.TempDir()
	salt := []byte("0123456789abcdef")
	seedCiphertext(t, dir, unitsync.DeriveKey("k", salt), ModeKeychain, salt)
	s := New(dir, transientKeychain{})
	s.SetSecretFiles([]string{"connections.json"})
	if err := s.AutoUnlock(); err == nil {
		t.Fatal("transient keychain failure must be reported, not swallowed")
	}
	if st := s.Status(); st.KeychainLost {
		t.Fatalf("transient failure must not look like a lost key: %+v", st)
	}
}

// TestStatusCountsExistingSecrets: the count of in-place ciphertext fields is
// what lets the frontend tell "first run" from "data with lost credentials".
// Unwired stores count nothing (same convention as storeHasSecrets).
func TestStatusCountsExistingSecrets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "connections.json"),
		[]byte(`{"connections":[{"password":"enc:v1:x"},{"password":"enc:v1:y"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ai.json"),
		[]byte(`{"models":[{"apiKey":"enc:v1:z"}]}`), 0600); err != nil {
		t.Fatal(err)
	}

	s := New(dir, newFakeKeychain())
	if st := s.Status(); st.ExistingSecrets != 0 {
		t.Fatalf("unwired store must count nothing: %+v", st)
	}
	s.SetSecretFiles([]string{"connections.json", "ai.json"})
	if st := s.Status(); st.ExistingSecrets != 3 {
		t.Fatalf("ExistingSecrets = %d, want 3", st.ExistingSecrets)
	}
}

// TestStatusMetaLostWithCiphertext: credentials.meta gone but ciphertext
// present — no mode to unlock with, so this is NOT a first run. The frontend
// must be able to see that data survives behind the missing meta.
func TestStatusMetaLostWithCiphertext(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "connections.json"),
		[]byte(`{"connections":[{"password":"enc:v1:x"}]}`), 0600); err != nil {
		t.Fatal(err)
	}

	s := New(dir, newFakeKeychain())
	s.SetSecretFiles([]string{"connections.json"})
	if err := s.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.NeedsSetup || st.Unlocked {
		t.Fatalf("missing meta must look like setup: %+v", st)
	}
	if st.ExistingSecrets != 1 {
		t.Fatalf("ExistingSecrets = %d, want 1 (the frontend routes on it)", st.ExistingSecrets)
	}
}

func TestEncryptWhenLockedFails(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	s := New(dir, kc)
	_ = s.Setup(ModeMasterPassword, "pw")
	_ = kc.Delete("master-key/" + s.DirHash())
	s2 := New(dir, kc)
	_ = s2.AutoUnlock() // locked
	if _, err := s2.Encrypt("x"); err == nil {
		t.Fatalf("Encrypt should fail when locked")
	}
}

// TestStoreHasSecretsUsesWiredFileList: the file list that may carry in-place
// ciphertext is wired by the app from the store's secret-field manifest (it
// cannot be declared here — store imports credentials). An unwired store
// reports nothing.
func TestStoreHasSecretsUsesWiredFileList(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "connections.json"),
		[]byte(`{"connections":[{"password":"enc:v1:x"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ai.json"),
		[]byte(`{"models":[{"apiKey":"enc:v1:y"}]}`), 0600); err != nil {
		t.Fatal(err)
	}

	s := New(dir, newFakeKeychain())
	if s.storeHasSecrets() {
		t.Fatal("an unwired store must not scan for ciphertext")
	}

	s.SetSecretFiles([]string{"connections.json", "ai.json"})
	if !s.storeHasSecrets() {
		t.Fatal("ciphertext in a wired file must be detected")
	}
}

// --- P1: a cached key that cannot decrypt the data must not pass as
// "unlocked" — the state must surface so the user is asked for the master
// password (or offered clearing), exactly like opening after a migration. ---

// seedCiphertext writes a connection file whose password field is encrypted
// under key, and claims master-password mode with the given salt.
func seedCiphertext(t *testing.T, dir string, key []byte, mode string, salt []byte) {
	t.Helper()
	enc, err := EncryptField("secret", key)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"connections":[{"id":"c1","password":"` + enc + `"}]}`
	if mode == ModeKeychain {
		body = `{"connections":[{"id":"c1","password":"` + enc + `"}]}`
	}
	if err := os.WriteFile(filepath.Join(dir, "connections.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteMeta(dir, &Meta{Mode: mode, Salt: salt}); err != nil {
		t.Fatal(err)
	}
}

// TestAutoUnlockStaleCacheKeyDecryptFailed: a cached derived key left over
// from a previous setup cannot open the current data. AutoUnlock must refuse
// to report success, and the right master password must recover — repairing
// the poisoned cache on the way.
func TestAutoUnlockStaleCacheKeyDecryptFailed(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	salt := []byte("0123456789abcdef")
	seedCiphertext(t, dir, unitsync.DeriveKey("right", salt), ModeMasterPassword, salt)

	h := New(dir, kc).DirHash()
	if err := kc.Set("master-key/"+h, hex.EncodeToString(unitsync.DeriveKey("old", salt))); err != nil {
		t.Fatal(err)
	}

	s := New(dir, kc)
	s.SetSecretFiles([]string{"connections.json"})
	if err := s.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.Unlocked || !st.DecryptFailed {
		t.Fatalf("stale cache key must not unlock: %+v", st)
	}

	// The right master password recovers, and the poisoned cache is repaired.
	if err := s.Unlock("right"); err != nil {
		t.Fatalf("unlock with the right password: %v", err)
	}
	if !s.Status().Unlocked {
		t.Fatal("the right password must unlock")
	}
	s2 := New(dir, kc)
	s2.SetSecretFiles([]string{"connections.json"})
	if err := s2.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	if st := s2.Status(); !st.Unlocked || st.DecryptFailed {
		t.Fatalf("cache not repaired by the successful unlock: %+v", s2.Status())
	}
}

// TestUnlockWrongPasswordKeepsCache: a failed unlock must not overwrite the
// cached entry — the old behaviour cached the wrong attempt's key first and
// poisoned a working state.
func TestUnlockWrongPasswordKeepsCache(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	salt := []byte("0123456789abcdef")
	seedCiphertext(t, dir, unitsync.DeriveKey("right", salt), ModeMasterPassword, salt)

	h := New(dir, kc).DirHash()
	stale := hex.EncodeToString(unitsync.DeriveKey("old", salt))
	if err := kc.Set("master-key/"+h, stale); err != nil {
		t.Fatal(err)
	}

	s := New(dir, kc)
	s.SetSecretFiles([]string{"connections.json"})
	if err := s.Unlock("wrong"); err == nil {
		t.Fatal("a wrong master password must fail")
	}
	if v, err := kc.Get("master-key/" + h); err != nil || v != stale {
		t.Fatalf("failed unlock must not touch the cache: %q err=%v", v, err)
	}
}

// TestAutoUnlockStaleKeychainKeyDecryptFailed: the keychain entry exists but
// holds THIS machine's key while the data was encrypted elsewhere — the state
// is the same failed-verification one, not a lost key (the entry is right
// there), and a master password cannot fix it in this mode.
func TestAutoUnlockStaleKeychainKeyDecryptFailed(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	salt := []byte("0123456789abcdef")
	seedCiphertext(t, dir, unitsync.DeriveKey("other-machine", salt), ModeKeychain, salt)

	h := New(dir, kc).DirHash()
	if err := kc.Set("keychain-key/"+h, hex.EncodeToString(unitsync.DeriveKey("this-machine", salt))); err != nil {
		t.Fatal(err)
	}

	s := New(dir, kc)
	s.SetSecretFiles([]string{"connections.json"})
	if err := s.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if st.Unlocked || !st.DecryptFailed {
		t.Fatalf("a key that cannot decrypt must not unlock: %+v", st)
	}
	if st.KeychainLost {
		t.Fatal("the key entry exists — this is not a lost key")
	}
	if err := s.Unlock("anything"); err == nil {
		t.Fatal("keychain mode cannot be unlocked with a password")
	}
}

// TestAutoUnlockWithoutCiphertextTriviallyUnlocks: with no ciphertext in the
// wired files there is nothing a wrong key could break — verification passes
// and behaviour is unchanged (fresh installs, post-reset dirs).
func TestAutoUnlockWithoutCiphertextTriviallyUnlocks(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	salt := []byte("0123456789abcdef")
	if err := WriteMeta(dir, &Meta{Mode: ModeMasterPassword, Salt: salt}); err != nil {
		t.Fatal(err)
	}
	h := New(dir, kc).DirHash()
	if err := kc.Set("master-key/"+h, hex.EncodeToString(unitsync.DeriveKey("whatever", salt))); err != nil {
		t.Fatal(err)
	}

	s := New(dir, kc)
	if err := s.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); !st.Unlocked || st.DecryptFailed {
		t.Fatalf("a dir without ciphertext must unlock trivially: %+v", st)
	}
}

// --- Recovery must follow the recovered key's nature: a master-key cache is
// password-derived (master-password mode survives with its salt), a
// keychain-key entry is a random key no password can derive (mode must
// switch to keychain, otherwise meta lies and the password is locked out
// forever — ChangeMasterPassword/SwitchCredentialMode both verify against
// the derived key). ---

// TestAutoUnlockRecoveryFollowsKeyNature: the rekey-bug remnant state — meta
// says master-password while the ciphertext is under a keychain-key random
// entry. Recovery must switch the mode to keychain and rewrite meta.
func TestAutoUnlockRecoveryFollowsKeyNature(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	salt := []byte("0123456789abcdef")
	key := unitsync.DeriveKey("random-holder", salt)
	seedCiphertext(t, dir, key, ModeMasterPassword, salt)
	if err := kc.Set("keychain-key/some-old-dir", hex.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}

	s := New(dir, kc)
	s.SetSecretFiles([]string{"connections.json"})
	if err := s.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); !st.Unlocked || st.Mode != ModeKeychain {
		t.Fatalf("recovery must switch the mode to keychain: %+v", st)
	}
	m, err := ReadMeta(dir)
	if err != nil || m == nil || m.Mode != ModeKeychain {
		t.Fatalf("meta must be rewritten to keychain: %+v %v", m, err)
	}
	// Restart takes the normal unlock path.
	s2 := New(dir, kc)
	s2.SetSecretFiles([]string{"connections.json"})
	if err := s2.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	if !s2.Status().Unlocked {
		t.Fatalf("restart after mode-switching recovery must unlock: %+v", s2.Status())
	}
}

// TestAutoUnlockMasterPasswordSurvivesViaMasterKeyCache: a copied
// master-password dir finds the old dir's master-key cache. The key is
// password-derived, so master-password mode and its salt survive, and the
// user's password keeps working after the cache is cleared.
func TestAutoUnlockMasterPasswordSurvivesViaMasterKeyCache(t *testing.T) {
	dir := t.TempDir()
	kc := newFakeKeychain()
	salt := []byte("0123456789abcdef")
	key := unitsync.DeriveKey("pw", salt)
	seedCiphertext(t, dir, key, ModeMasterPassword, salt)
	if err := kc.Set("master-key/old-dir-hash", hex.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}

	s := New(dir, kc)
	s.SetSecretFiles([]string{"connections.json"})
	if err := s.AutoUnlock(); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); !st.Unlocked || st.Mode != ModeMasterPassword {
		t.Fatalf("master-key cache must keep master-password mode: %+v", st)
	}
	m, err := ReadMeta(dir)
	if err != nil || m == nil || m.Mode != ModeMasterPassword || string(m.Salt) != string(salt) {
		t.Fatalf("meta mode/salt must be preserved: %+v %v", m, err)
	}
	// The password still derives the key once the cache is gone.
	_ = kc.Delete("master-key/" + s.DirHash())
	s2 := New(dir, kc)
	s2.SetSecretFiles([]string{"connections.json"})
	_ = s2.AutoUnlock()
	if err := s2.Unlock("pw"); err != nil {
		t.Fatalf("password must still derive the key: %v", err)
	}
}
