package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ys-ll/uniterm/backend/credentials"
	"github.com/ys-ll/uniterm/backend/session"
)

// fakeLegacyKC is a LegacyPasswordSource keyed by connection ID, mimicking the
// pre-enc:v1 keychain (conn/<id>) holding plaintext passwords. DeletePassword
// mutates the shared map, so tests can assert move semantics through the
// original handle.
type fakeLegacyKC struct{ pw map[string]string }

func (f fakeLegacyKC) GetPassword(id string) (string, error) { return f.pw[id], nil }
func (fakeLegacyKC) GetModelAPIKey(string) (string, error)   { return "", nil }
func (f fakeLegacyKC) DeletePassword(id string) error {
	delete(f.pw, id)
	return nil
}

// TestLoadFallsBackToLegacyKeychain reproduces the upgrade regression: a
// connection saved under the old scheme has no enc:v1 field (the password
// lived in the keychain), and the one-shot migration never ran. Load() must
// recover the password from the legacy keychain and lazy-migrate it to enc:v1
// on the resulting save so it is not invisible.
func TestLoadFallsBackToLegacyKeychain(t *testing.T) {
	dir := t.TempDir()
	s := &ConnectionStore{configDir: dir, passwordStore: fakeCipherStore{}}
	s.SetLegacyKeychain(fakeLegacyKC{pw: map[string]string{"c1": "secret-pw"}})

	// JSON with no password field — exactly the old keychain-mode shape.
	mustWriteConnections(t, dir, session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{
			{ID: "c1", Type: "ssh", AuthType: "password"},
		},
	})

	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := data.Connections[0].Password; got != "secret-pw" {
		t.Fatalf("password = %q, want %q (legacy keychain fallback)", got, "secret-pw")
	}
}

// TestLoadLegacyFallBackLazyMigrates verifies the recovered plaintext is
// written back as enc:v1 so subsequent loads don't depend on the keychain,
// and that the legacy entry is then deleted (move semantics).
func TestLoadLegacyFallBackLazyMigrates(t *testing.T) {
	dir := t.TempDir()
	kc := fakeLegacyKC{pw: map[string]string{"c1": "secret-pw"}}
	s := &ConnectionStore{configDir: dir, passwordStore: fakeCipherStore{}}
	s.SetLegacyKeychain(kc)

	mustWriteConnections(t, dir, session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{
			{ID: "c1", Type: "ssh", AuthType: "password"},
		},
	})
	if _, err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, storeFileName))
	var d session.ConnectionStoreData
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if !credentials.IsEncrypted(d.Connections[0].Password) {
		t.Fatalf("password not lazy-migrated to enc:v1: %q", d.Connections[0].Password)
	}
	// Move semantics: the encrypted copy is on disk, so the legacy entry must
	// be gone — a kept-forever copy is what let a user-cleared password
	// resurrect on the next load.
	if _, ok := kc.pw["c1"]; ok {
		t.Fatal("legacy keychain entry not deleted after lazy migration")
	}
}

// TestClearedPasswordDoesNotResurrectFromLegacy locks in the resurrection bug
// fix: backfill used to key on an empty JSON password field while the legacy
// keychain entry was kept forever, so a user-cleared password came back on the
// next start. With move semantics the entry is deleted once the encrypted copy
// is on disk, so clearing stays cleared across a restart.
func TestClearedPasswordDoesNotResurrectFromLegacy(t *testing.T) {
	dir := t.TempDir()
	kc := fakeLegacyKC{pw: map[string]string{"c1": "secret-pw"}}
	s := &ConnectionStore{configDir: dir, passwordStore: fakeCipherStore{}}
	s.SetLegacyKeychain(kc)

	// First load backfills from the legacy keychain and lazy-migrates.
	mustWriteConnections(t, dir, session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{
			{ID: "c1", Type: "ssh", AuthType: "password"},
		},
	})
	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if data.Connections[0].Password != "secret-pw" {
		t.Fatalf("backfill = %q, want secret-pw", data.Connections[0].Password)
	}

	// The user clears the password.
	data.Connections[0].Password = ""
	if err := s.Save(data); err != nil {
		t.Fatalf("Save cleared: %v", err)
	}

	// "Restart": a fresh store over the same directory must not resurrect it.
	s2 := &ConnectionStore{configDir: dir, passwordStore: fakeCipherStore{}}
	s2.SetLegacyKeychain(kc)
	fresh, err := s2.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := fresh.Connections[0].Password; got != "" {
		t.Fatalf("cleared password resurrected on reload: %q", got)
	}
}

// TestLoadEncV1PreferredOverLegacy ensures an existing enc:v1 field is used
// and the legacy keychain is never consulted for that connection.
func TestLoadEncV1PreferredOverLegacy(t *testing.T) {
	dir := t.TempDir()
	s := &ConnectionStore{configDir: dir, passwordStore: fakeCipherStore{}}
	enc, _ := fakeCipherStore{}.Encrypt("enc-pw")
	// Legacy holds a DIFFERENT value — it must be ignored.
	s.SetLegacyKeychain(fakeLegacyKC{pw: map[string]string{"c1": "stale-legacy"}})

	mustWriteConnections(t, dir, session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{
			{ID: "c1", Type: "ssh", AuthType: "password", Password: enc},
		},
	})
	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := data.Connections[0].Password; got != "enc-pw" {
		t.Fatalf("password = %q, want enc:v1 value %q", got, "enc-pw")
	}
}

func mustWriteConnections(t *testing.T, dir string, data session.ConnectionStoreData) {
	t.Helper()
	s := &ConnectionStore{configDir: dir}
	if err := writeJSONFile(s.filePath(), data); err != nil {
		t.Fatalf("writeJSONFile: %v", err)
	}
}
