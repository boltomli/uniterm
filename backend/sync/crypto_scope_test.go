package sync

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if err := EncryptConfigFilesScoped(files, src, dest, key, nil, nil); err != nil {
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
	if err := DecryptConfigFilesScoped(files, dest, roundTripDest, key, nil); err != nil {
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
	if err := EncryptConfigFiles(src, dest, key, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := DecryptConfigFiles(dest, t.TempDir(), key, nil); err != nil {
		t.Fatal(err)
	}
}
