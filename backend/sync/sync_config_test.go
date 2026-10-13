package sync

import (
	"os"
	"path/filepath"
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
