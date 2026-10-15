package sync

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// STORE-09 equivalent for sync-config.json: a corrupt file must be renamed
// aside (quarantined) and surfaced as an error, so the next Save cannot
// silently overwrite the user's repo/WebDAV configuration and the corrupt
// bytes stay recoverable.
func TestSyncConfigLoadQuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, syncConfigFileName)
	garbage := []byte("{not json {{{")
	if err := os.WriteFile(path, garbage, 0600); err != nil {
		t.Fatal(err)
	}

	store := NewSyncConfigStore(dir)
	if _, err := store.Load(); err == nil {
		t.Fatal("corrupt sync-config.json must return an error, got nil (silent default)")
	}

	// The corrupt bytes must be preserved under a quarantine name and the
	// original path freed so the next Save starts from a clean slate.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("corrupt file must be renamed aside, stat err=%v", err)
	}
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 quarantine file, got %d", len(matches))
	}
	kept, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != string(garbage) {
		t.Fatalf("quarantined bytes differ:\n got %q\nwant %q", kept, garbage)
	}

	// A subsequent Save creates a fresh config without clobbering the
	// quarantined original.
	if err := store.Save(SyncConfig{Branch: "main", RepoURL: "https://example.com/repo.git"}); err != nil {
		t.Fatal(err)
	}
	kept2, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(kept2) != string(garbage) {
		t.Fatalf("Save clobbered the quarantined original:\n got %q\nwant %q", kept2, garbage)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("fresh config must load after Save: %v", err)
	}
	if loaded.RepoURL != "https://example.com/repo.git" {
		t.Fatalf("config = %+v", loaded)
	}
}

func TestEffectiveSyncFilesNilScopeKeepsLegacy(t *testing.T) {
	got := EffectiveSyncFiles(nil)
	if !reflect.DeepEqual(got, defaultSyncedFiles) {
		t.Fatalf("nil scope = %v, want legacy %v", got, defaultSyncedFiles)
	}
	// DeepEqual cannot detect aliasing; mutating the result must not
	// touch the shared defaultSyncedFiles slice.
	got[0] = "x"
	if defaultSyncedFiles[0] == "x" {
		t.Fatal("nil scope result aliases defaultSyncedFiles; want a copy")
	}
}

func TestEffectiveSyncFilesFiltersToSyncable(t *testing.T) {
	got := EffectiveSyncFiles([]string{"settings.json", "connections.json", "not-a-file.json"})
	want := []string{"settings.json", "connections.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEffectiveSyncFilesDedupesScope(t *testing.T) {
	got := EffectiveSyncFiles([]string{"settings.json", "settings.json"})
	want := []string{"settings.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSyncableFilesIncludesSettings(t *testing.T) {
	found := false
	for _, n := range syncableFiles {
		if n == "settings.json" {
			found = true
		}
	}
	if !found {
		t.Fatal("syncableFiles must contain settings.json")
	}
	if len(syncableFiles) != len(defaultSyncedFiles)+1 {
		t.Fatalf("syncableFiles len %d, want defaultSyncedFiles len %d + 1", len(syncableFiles), len(defaultSyncedFiles))
	}
}
