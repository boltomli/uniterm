package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

type fakeLegacy struct {
	pw map[string]string
	ak map[string]string
}

func (f fakeLegacy) GetPassword(id string) (string, error)    { return f.pw[id], nil }
func (f fakeLegacy) GetModelAPIKey(id string) (string, error) { return f.ak[id], nil }
func (f fakeLegacy) DeletePassword(id string) error {
	delete(f.pw, id)
	return nil
}

func TestMigrateLegacyKeychainToInPlace(t *testing.T) {
	dir := t.TempDir()
	// Seed connections.json with an empty password (legacy: password in keychain).
	cs := &ConnectionStore{configDir: dir}
	_ = writeJSONFile(cs.filePath(), session.ConnectionStoreData{
		Groups:      []session.ConnectionGroup{},
		Connections: []session.ConnectionConfig{{ID: "c1", Type: "ssh", AuthType: "password", Password: ""}},
	})
	// Seed settings.json with an empty apiKey.
	ss := &SettingsStore{configDir: dir}
	_ = ss.Save(AppSettings{AI: AISettings{Models: []AIModelConfig{{ID: "m1", APIKey: ""}}}})

	legacy := fakeLegacy{pw: map[string]string{"c1": "pw1"}, ak: map[string]string{"m1": "ak1"}}
	n, err := MigrateLegacyKeychainToInPlace(dir, legacy, fakeCipherStore{})
	if err != nil {
		t.Fatalf("MigrateLegacyKeychainToInPlace: %v", err)
	}
	if n != 2 {
		t.Fatalf("migrated %d secrets, want 2", n)
	}
	// connections.json now holds ciphertext.
	raw, _ := os.ReadFile(filepath.Join(dir, storeFileName))
	wantPW, _ := fakeCipherStore{}.Encrypt("pw1")
	if !contains(string(raw), wantPW) {
		t.Fatalf("connection password not migrated: %s", raw)
	}
	rawS, _ := os.ReadFile(filepath.Join(dir, settingsFileName))
	wantAK, _ := fakeCipherStore{}.Encrypt("ak1")
	if !contains(string(rawS), wantAK) {
		t.Fatalf("apiKey not migrated: %s", rawS)
	}
	// Move semantics: the migrated conn/<id> entry is deleted (its encrypted
	// copy is on disk), while the ai-model/<id> entry is kept.
	if _, ok := legacy.pw["c1"]; ok {
		t.Fatal("migrated conn entry not deleted from legacy keychain")
	}
	if _, ok := legacy.ak["m1"]; !ok {
		t.Fatal("ai-model entry should be kept (no delete path)")
	}
}
