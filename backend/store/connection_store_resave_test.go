package store

// Regression tests for security.md storage finding #7: the legacy re-save
// path (populatePasswords → encryptForSaveLocked) could write plaintext and
// dropped identity usernames.
//   1. encryptForSaveLocked ignored Encrypt errors and kept the plaintext,
//      so when the credential store is LOCKED a password recovered from the
//      old keychain (conn/<id>) was written to connections.json in the clear.
//      Save fails closed in the same situation; the re-save must too.
//   2. The same function cleared conn.User for identity connections, while
//      Save deliberately keeps it (issue #959: the connection's own username
//      override wins over the identity's). Any lazy migration silently
//      dropped the override.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

// lockedCipherStore simulates a locked credential store: Decrypt works (the
// caller already holds decrypted data), but Encrypt fails with the same
// "credentials locked" error credentials.Store returns when no key is set.
type lockedCipherStore struct{}

func (lockedCipherStore) Encrypt(string) (string, error) {
	return "", errors.New("credentials locked")
}
func (lockedCipherStore) Decrypt(encoded string) (string, error) {
	if strings.HasPrefix(encoded, "enc:v1:") {
		return "", errors.New("credentials locked")
	}
	return encoded, nil
}

// TestLockedStoreResaveNeverWritesPlaintext: a connection whose password only
// exists in the legacy keychain is recovered in memory on Load, which then
// lazy-migrates it. With the cipher unable to encrypt, that re-save must be
// skipped — the plaintext must never reach connections.json.
func TestLockedStoreResaveNeverWritesPlaintext(t *testing.T) {
	dir := t.TempDir()
	s := &ConnectionStore{configDir: dir, passwordStore: lockedCipherStore{}}
	s.SetLegacyKeychain(fakeLegacyKC{pw: map[string]string{"c1": "legacy-secret"}})

	mustWriteConnections(t, dir, session.ConnectionStoreData{
		Groups: []session.ConnectionGroup{},
		Connections: []session.ConnectionConfig{
			{ID: "c1", Name: "n", Type: "ssh", Host: "h", Port: 22, User: "u", AuthType: "password"},
		},
	})

	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The caller still gets the recovered password in memory.
	if got := data.Connections[0].Password; got != "legacy-secret" {
		t.Fatalf("in-memory password = %q, want %q", got, "legacy-secret")
	}

	raw, err := os.ReadFile(filepath.Join(dir, storeFileName))
	if err != nil {
		t.Fatalf("read connections.json: %v", err)
	}
	if strings.Contains(string(raw), "legacy-secret") {
		t.Fatalf("locked re-save wrote the plaintext password to disk: %s", raw)
	}
}

// TestResaveKeepsIdentityUsername: the legacy re-save path must preserve the
// username override on identity connections, exactly like Save does.
func TestResaveKeepsIdentityUsername(t *testing.T) {
	dir := t.TempDir()
	s := &ConnectionStore{configDir: dir, passwordStore: fakeCipherStore{}}

	// One connection triggers the re-save (plaintext on disk, pre-enc:v1),
	// one identity connection carries a username override that must survive.
	mustWriteConnections(t, dir, session.ConnectionStoreData{
		Groups: []session.ConnectionGroup{},
		Connections: []session.ConnectionConfig{
			{ID: "c-plain", Name: "p", Type: "ssh", Host: "h", Port: 22, User: "u", AuthType: "password", Password: "plain-on-disk"},
			{ID: "c-ident", Name: "i", Type: "ssh", Host: "h2", Port: 22, User: "override-user", AuthType: "identity", IdentityId: "id1"},
		},
	})

	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The plaintext connection was encrypted in place by the re-save…
	if pw := data.Connections[0].Password; pw != "plain-on-disk" {
		t.Fatalf("caller password = %q", pw)
	}
	raw, err := os.ReadFile(filepath.Join(dir, storeFileName))
	if err != nil {
		t.Fatalf("read connections.json: %v", err)
	}
	if strings.Contains(string(raw), "plain-on-disk") {
		t.Fatalf("re-save left the plaintext password on disk: %s", raw)
	}

	// …and the identity connection kept its username override.
	byID := map[string]session.ConnectionConfig{}
	for _, c := range data.Connections {
		byID[c.ID] = c
	}
	if got := byID["c-ident"].User; got != "override-user" {
		t.Fatalf("identity username override lost in Load data: %q", got)
	}

	// Re-read the file directly: the override must also survive on disk.
	raw2, err := os.ReadFile(filepath.Join(dir, storeFileName))
	if err != nil {
		t.Fatalf("re-read connections.json: %v", err)
	}
	var onDisk session.ConnectionStoreData
	if err := json.Unmarshal(raw2, &onDisk); err != nil {
		t.Fatalf("unmarshal connections.json: %v", err)
	}
	for _, c := range onDisk.Connections {
		if c.ID == "c-ident" && c.User != "override-user" {
			t.Fatalf("identity username override lost on disk: %+v", c)
		}
	}
}

// TestEncryptForSaveLockedKeepsIdentityUser pins the function itself: an
// identity connection's User survives while its (unused) credentials stay
// cleared, matching Save's semantics.
func TestEncryptForSaveLockedKeepsIdentityUser(t *testing.T) {
	s := &ConnectionStore{configDir: t.TempDir(), passwordStore: fakeCipherStore{}}
	in := session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{
			{ID: "c-ident", AuthType: "identity", IdentityId: "id1", User: "override-user", Password: "stale-pw"},
		},
	}
	out, err := s.encryptForSaveLocked(in)
	if err != nil {
		t.Fatalf("encryptForSaveLocked: %v", err)
	}
	c := out.Connections[0]
	if c.User != "override-user" {
		t.Fatalf("User cleared: %q", c.User)
	}
	if c.Password != "" {
		t.Fatalf("stale identity password kept: %q", c.Password)
	}
	// The caller's copy is untouched (deep copy semantics).
	if in.Connections[0].Password != "stale-pw" {
		t.Fatalf("input mutated: %+v", in.Connections[0])
	}
}
