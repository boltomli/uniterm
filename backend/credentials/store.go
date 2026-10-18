package credentials

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"

	"github.com/zalando/go-keyring"

	"github.com/ys-ll/uniterm/backend/kdf"
)

// Keychain is the subset of sync.Keychain this package needs.
type Keychain interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
	// List returns the entry names stored under the app's keychain service.
	// Used by recovery to find keys written under another data-dir hash
	// (copied/moved data directory).
	List() ([]string, error)
}

// Status describes the credential store state for the frontend.
type Status struct {
	Mode         string
	Unlocked     bool
	NeedsSetup   bool
	KeychainLost bool
	// DecryptFailed marks the state where a key was recovered but cannot
	// open the data dir's ciphertext (e.g. data restored by copying the
	// dir while a stale key survived in the keychain). The unlock dialog
	// must be shown; in master-password mode the right password recovers
	// the data, in keychain mode the way out is clearing credentials.
	DecryptFailed bool
	// ExistingSecrets counts the in-place enc:v1 values across the wired
	// config files. Lets the frontend tell a true first run from local data
	// whose credentials were lost (copied dir, deleted meta) and route to
	// the recovery dialog instead of mode selection.
	ExistingSecrets int
}

// Store owns the master key used to encrypt/decrypt secret fields. The key
// source is orthogonal to the data directory: keychain mode keeps a random key
// in the OS keychain; master-password mode derives the key from a password and
// caches the derived key in the keychain for auto-unlock.
type Store struct {
	// secretFiles is the wired list of config files that may carry in-place
	// ciphertext (see SetSecretFiles). Read under mu.
	secretFiles   []string
	dataDir       string
	keychain      Keychain
	mu            stdsync.Mutex
	mode          string
	salt          []byte
	key           []byte
	decryptFailed bool
}

func New(dataDir string, kc Keychain) *Store {
	return &Store{dataDir: dataDir, keychain: kc}
}

func (s *Store) DataDir() string { return s.dataDir }

// DirHash returns the hex sha256 of the absolute data dir path, used to scope
// keychain entries per directory.
func (s *Store) DirHash() string {
	abs, _ := filepath.Abs(s.dataDir)
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])
}

func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Mode: s.mode, Unlocked: s.key != nil, DecryptFailed: s.decryptFailed,
		ExistingSecrets: s.countSecretsLocked()}
	if s.mode == "" {
		st.NeedsSetup = true
	} else if s.mode == ModeKeychain && s.key == nil && !s.decryptFailed {
		// KeychainLost means the entry is truly absent. A failed
		// verification (entry present but the key cannot open the data)
		// stays DecryptFailed — the remedies differ: lost offers only
		// clearing, decrypt-failed also asks for the master password.
		st.KeychainLost = true
	}
	return st
}

// AutoUnlock loads credentials.meta and attempts to recover the master key.
// A recovered key is accepted only when it opens the meta verifier — a stale
// keychain entry (e.g. a key cached by an older, unverified Unlock) must not
// be adopted, or every load/save after restart runs under the wrong key.
func (s *Store) AutoUnlock() error {
	meta, err := ReadMeta(s.dataDir)
	if err != nil {
		return err
	}
	if meta == nil {
		return nil // NeedsSetup
	}
	h := s.DirHash()
	var key []byte
	switch meta.Mode {
	case ModeKeychain:
		k, err := s.getKey("keychain-key/" + h)
		if err != nil {
			// Transient read failure (e.g. macOS keychain permission was
			// temporarily denied). NOT the same as a lost key: surfacing it
			// avoids the KeychainLost→reset flow that would wipe stored
			// passwords.
			return err
		}
		if k == nil {
			// The entry is scoped to this data dir's hash — but a copied or
			// moved directory carries its ciphertext while the key still
			// sits in the keychain under the OLD dir's hash. Try the
			// surviving entries before declaring the key lost.
			if rk, entry := s.recoverKey(); rk != nil {
				return s.Rekey(recoveredMode(meta, entry), recoveredSalt(meta, entry), rk)
			}
			s.set(meta.Mode, meta.Salt, nil)
			return nil // KeychainLost (truly missing)
		}
		key = k
	case ModeMasterPassword:
		if k, err := s.getKey("master-key/" + h); err == nil && k != nil {
			key = k
		}
	default:
		return fmt.Errorf("unknown credential mode %q", meta.Mode)
	}
	// A recovered key must actually open the data before it counts as
	// unlocked: keychain entries are scoped by data-dir path, so a stale
	// key from a previous setup can survive a data replacement (restore,
	// copy from another machine) and previously passed as a successful
	// unlock — every field then failed to decrypt on a blank UI with no
	// prompt. On failure the state surfaces as DecryptFailed: the unlock
	// dialog offers the master password (master-password mode) and clearing
	// credentials (the only way out in keychain mode, where no password can
	// re-derive the random key). But before settling on that, give the
	// surviving keychain entries a chance: the data may have been copied
	// here from a directory whose key is still in the keychain.
	if !s.keyOpensVault(key) {
		if rk, entry := s.recoverKey(); rk != nil {
			return s.Rekey(recoveredMode(meta, entry), recoveredSalt(meta, entry), rk)
		}
		// set() clears the flag (a committed key means verified); the flag
		// is therefore set after it, on top of the key-less state.
		s.set(meta.Mode, meta.Salt, nil)
		s.mu.Lock()
		s.decryptFailed = true
		s.mu.Unlock()
		return nil
	}
	s.set(meta.Mode, meta.Salt, key)
	return nil
}

// Setup establishes a NEW mode. Used on first run and after reset.
// It must NEVER orphan existing enc:v1 ciphertext: if the data dir already
// holds encrypted secrets, a bare key swap would make them undecryptable.
//   - keychain: reuse a surviving keychain key when one exists; otherwise only
//     allow a fresh key when there is no existing ciphertext.
//   - master-password: refuse when existing ciphertext is present (the correct
//     path is SwitchCredentialMode, which re-encrypts).
func (s *Store) Setup(mode, masterPassword string) error {
	hasSecrets := s.storeHasSecrets()
	var key, salt []byte
	switch mode {
	case ModeKeychain:
		existing, err := s.getKey("keychain-key/" + s.DirHash())
		if err == nil && existing != nil {
			key = existing // reuse the surviving key so existing ciphertext stays readable
		} else if hasSecrets {
			return errors.New("existing encrypted data but no keychain key; refusing to overwrite")
		} else {
			key, err = randomKey()
			if err != nil {
				return err
			}
		}
	case ModeMasterPassword:
		if masterPassword == "" {
			return errors.New("master password required")
		}
		if hasSecrets {
			return errors.New("existing encrypted data; use switch-credential-mode to re-encrypt instead of setup")
		}
		var err error
		salt, err = kdf.GenerateSalt()
		if err != nil {
			return err
		}
		key = kdf.DeriveKey(masterPassword, salt)
	default:
		return fmt.Errorf("unknown credential mode %q", mode)
	}
	return s.Rekey(mode, salt, key)
}

// Unlock derives the master key from a master password (master-password mode only).
// The derived key is checked against the verifier in credentials.meta BEFORE
// anything is cached: a typo must not produce Unlocked=true, must not be
// written to the keychain (it survived restarts through AutoUnlock), and must
// never become the key new saves are encrypted under.
func (s *Store) Unlock(masterPassword string) error {
	s.mu.Lock()
	mode, salt := s.mode, s.salt
	s.mu.Unlock()
	if mode != ModeMasterPassword {
		return errors.New("not in master-password mode")
	}
	key := kdf.DeriveKey(masterPassword, salt)
	// Verify before caching: the cached entry may hold a key this data
	// cannot be opened with, and a failed attempt must not overwrite it
	// with another one that cannot either. Both checks run: the meta
	// verifier rejects a typo'd password even on a ciphertext-free dir
	// (nothing to scan yet), the data scan catches a stale key that passes
	// the verifier but no longer opens the files.
	if !s.keyOpensVault(key) {
		return ErrWrongMasterPassword
	}
	// Cache the derived key for future auto-unlock.
	if err := s.keychain.Set("master-key/"+s.DirHash(), hex.EncodeToString(key)); err != nil {
		return err
	}
	s.set(mode, salt, key)
	return nil
}

// verifyKey checks key against the verifier stored in credentials.meta.
// Meta written before verifiers existed (Verifier == "") cannot be checked
// and is accepted — there is nothing to compare against, and rejecting would
// lock out every existing vault.
func verifyKey(meta *Meta, key []byte) error {
	if meta == nil || meta.Verifier == "" {
		return nil
	}
	canary, err := DecryptField(meta.Verifier, key)
	if err != nil || canary != VerifyPlaintext {
		return errors.New("master password incorrect")
	}
	return nil
}

func (s *Store) Encrypt(plaintext string) (string, error) {
	key := s.currentKey()
	if key == nil {
		return "", errors.New("credentials locked")
	}
	return EncryptField(plaintext, key)
}

func (s *Store) Decrypt(encoded string) (string, error) {
	key := s.currentKey()
	if key == nil {
		return "", errors.New("credentials locked")
	}
	return DecryptField(encoded, key)
}

func (s *Store) Key() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.key
}

// Unlocked reports whether the store holds a usable key. It is false while
// master-password mode is not yet unlocked, keychain mode has lost its key, or
// the store has never been set up.
func (s *Store) Unlocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.key != nil
}

func (s *Store) SetKey(key []byte) {
	s.mu.Lock()
	s.key = key
	s.mu.Unlock()
}

// Rekey persists the new mode/salt/key/verifier and swaps them into memory.
// Used by Setup and switch-mode orchestration. Ordering is deliberate:
// credentials.meta is written FIRST (atomically) and the keychain second, so
// a failed meta write can no longer leave the keychain holding a key that no
// meta points at; a failed keychain write rolls meta back to its previous
// bytes so the two never diverge.
func (s *Store) Rekey(mode string, salt, key []byte) error {
	entry := "keychain-key/" + s.DirHash()
	if mode == ModeMasterPassword {
		entry = "master-key/" + s.DirHash()
	}
	verifier, err := EncryptField(VerifyPlaintext, key)
	if err != nil {
		return err
	}
	metaPath := MetaPath(s.dataDir)
	prevRaw, prevErr := os.ReadFile(metaPath)
	hadPrev := prevErr == nil

	if err := WriteMeta(s.dataDir, &Meta{Mode: mode, Salt: salt, Verifier: verifier}); err != nil {
		return err
	}
	if err := s.keychain.Set(entry, hex.EncodeToString(key)); err != nil {
		if hadPrev {
			_ = writeFileAtomic(metaPath, prevRaw, 0600)
		} else {
			_ = os.Remove(metaPath)
		}
		return err
	}
	s.set(mode, salt, key)
	return nil
}

// recoveredMode decides the mode a recovered key lands in: a master-key
// cache is password-derived, so master-password mode survives with its salt;
// a keychain-key entry is a random key no password can derive, so the mode
// must follow the key into keychain — keeping master-password there would
// lock the user's (correct) password out forever, since Unlock,
// ChangeMasterPassword and SwitchCredentialMode all verify against the
// derived key.
func recoveredMode(meta *Meta, entry string) string {
	if meta.Mode == ModeMasterPassword && strings.HasPrefix(entry, "master-key/") {
		return ModeMasterPassword
	}
	return ModeKeychain
}

// recoveredSalt keeps the master password's salt when the recovery preserves
// master-password mode; keychain mode carries no salt.
func recoveredSalt(meta *Meta, entry string) []byte {
	if recoveredMode(meta, entry) == ModeMasterPassword {
		return meta.Salt
	}
	return nil
}

// recoverKey tries to survive a moved data dir. Keychain entries are scoped
// by data-dir hash, so a copied directory misses its entry even though the
// key itself still sits in the OS keychain under the old hash. Every entry
// is tried against the local ciphertext; the first one that decrypts is
// adopted along with its entry name, so the caller can tell a master-key
// cache (password-derived) from a keychain-key random key. Only meaningful
// when the dir carries ciphertext — verifyKey accepts any key on a
// ciphertext-free dir, so a wrong key could otherwise be claimed.
// Enumeration failures (platform without List, transient keychain errors)
// simply yield no candidates.
func (s *Store) recoverKey() ([]byte, string) {
	if !s.storeHasSecrets() {
		return nil, ""
	}
	names, err := s.keychain.List()
	if err != nil {
		return nil, ""
	}
	for _, name := range names {
		k, err := s.getKey(name)
		if err != nil || k == nil {
			continue // not found / not a hex key — next candidate
		}
		if s.verifyKey(k) {
			return k, name
		}
	}
	return nil, ""
}

// countSecretsLocked counts the in-place enc:v1 values across the wired
// config files. Mirrors storeHasSecrets' conventions: unwired stores count
// nothing. Caller must hold s.mu.
func (s *Store) countSecretsLocked() int {
	count := 0
	for _, name := range s.secretFiles {
		data, err := os.ReadFile(filepath.Join(s.dataDir, name))
		if err != nil || !bytes.Contains(data, []byte(Prefix)) {
			continue
		}
		var obj interface{}
		if err := json.Unmarshal(data, &obj); err != nil {
			continue
		}
		count += countEncryptedValues(obj)
	}
	return count
}

// countEncryptedValues returns the number of string values carrying the
// in-place ciphertext prefix, walking objects and arrays generically.
func countEncryptedValues(node interface{}) int {
	switch n := node.(type) {
	case map[string]interface{}:
		c := 0
		for _, v := range n {
			if s, ok := v.(string); ok && IsEncrypted(s) {
				c++
			}
			c += countEncryptedValues(v)
		}
		return c
	case []interface{}:
		c := 0
		for _, v := range n {
			c += countEncryptedValues(v)
		}
		return c
	}
	return 0
}

// ClearKeychainCache removes the master-password derived-key cache. Used when
// switching master-password → keychain and on reset.
func (s *Store) ClearKeychainCache() error {
	return s.keychain.Delete("master-key/" + s.DirHash())
}

func (s *Store) currentKey() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.key
}

// keyOpensVault reports whether key both passes the meta verifier (when one
// is recorded) and decrypts the data dir's ciphertext. The verifier check
// works without any ciphertext on disk — a typo'd master password must be
// rejected (and never cached) on a fresh dir; the data scan catches a stale
// key that opens the verifier's era but not the current files. Metas written
// before verifiers existed fall through to the data scan alone.
func (s *Store) keyOpensVault(key []byte) bool {
	if meta, err := ReadMeta(s.dataDir); err == nil && meta != nil && meta.Verifier != "" {
		if verifyKey(meta, key) != nil {
			return false
		}
	}
	return s.verifyKey(key)
}

func (s *Store) set(mode string, salt, key []byte) {
	s.mu.Lock()
	s.mode, s.salt, s.key = mode, salt, key
	s.decryptFailed = false
	s.mu.Unlock()
}

// ErrWrongMasterPassword reports a master password that does not decrypt the
// data dir's ciphertext. Distinct from a formatting error so the unlock
// dialog can re-prompt instead of treating it as a setup problem.
var ErrWrongMasterPassword = errors.New("master password does not decrypt this data")

// verifyKey reports whether key actually decrypts the data dir's ciphertext:
// the wired secret files are scanned for the first enc:v1 value and one
// decryption is attempted. A dir without ciphertext verifies trivially —
// there is nothing a wrong key could break (fresh install, post-reset). The
// generic JSON walk is deliberate: the field manifest lives in the store
// package, which cannot be imported from here (store → credentials).
func (s *Store) verifyKey(key []byte) bool {
	s.mu.Lock()
	files := s.secretFiles
	s.mu.Unlock()
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(s.dataDir, name))
		if err != nil || !bytes.Contains(data, []byte(Prefix)) {
			continue
		}
		var obj interface{}
		if err := json.Unmarshal(data, &obj); err != nil {
			return false // unreadable shape while carrying ciphertext: assume the worst
		}
		if v, ok := firstEncryptedValue(obj); ok {
			_, err := DecryptField(v, key)
			return err == nil
		}
	}
	return true
}

// firstEncryptedValue returns the first string value carrying the in-place
// ciphertext prefix, walking objects and arrays generically.
func firstEncryptedValue(node interface{}) (string, bool) {
	switch n := node.(type) {
	case map[string]interface{}:
		for _, v := range n {
			if s, ok := v.(string); ok && IsEncrypted(s) {
				return s, true
			}
			if found, ok := firstEncryptedValue(v); ok {
				return found, true
			}
		}
	case []interface{}:
		for _, v := range n {
			if found, ok := firstEncryptedValue(v); ok {
				return found, true
			}
		}
	}
	return "", false
}

// ErrKeychainNotFound marks a keychain entry that is truly absent (as opposed
// to a transient read failure), so callers can distinguish "key lost" from
// "keychain temporarily unreadable" — the latter must not trigger the
// KeychainLost→reset flow that would wipe stored passwords.
var ErrKeychainNotFound = errors.New("keychain entry not found")

// getKey returns the decoded key for a keychain entry.
//   - (nil, nil)         → no entry (ErrKeychainNotFound / empty value)
//   - ([]byte, nil)      → entry found and decoded
//   - (nil, other error) → transient read/parse failure
func (s *Store) getKey(name string) ([]byte, error) {
	v, err := s.keychain.Get(name)
	if err != nil {
		// The real keychain (backend/sync/keyring_desktop.go and the Android
		// shim) reports a missing entry as keyring.ErrNotFound, never as this
		// package's sentinel — only the test fake returns the sentinel. Map
		// both, otherwise "key lost" can never happen in production and the
		// UI dead-ends in NeedsSetup with no reset path.
		if errors.Is(err, ErrKeychainNotFound) || errors.Is(err, keyring.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if v == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// SetSecretFiles wires the config files that may carry in-place enc:v1
// ciphertext. The list is the store manifest's file set, injected by the app:
// the manifest lives in the store package, which cannot be imported from here
// (store → credentials). Unwired stores report no ciphertext, so production
// wiring is mandatory.
func (s *Store) SetSecretFiles(files []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secretFiles = append([]string{}, files...)
}

// storeHasSecrets reports whether any wired config file in the data dir
// already contains in-place enc:v1 ciphertext. A bare Setup on such a dir
// would orphan the existing ciphertext, so it must be guarded.
func (s *Store) storeHasSecrets() bool {
	s.mu.Lock()
	files := s.secretFiles
	s.mu.Unlock()
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(s.dataDir, name))
		if err != nil {
			continue
		}
		if bytes.Contains(data, []byte(Prefix)) {
			return true
		}
	}
	return false
}

func randomKey() ([]byte, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	return k, nil
}
