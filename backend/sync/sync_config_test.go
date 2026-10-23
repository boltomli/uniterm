package sync

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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

func TestSyncableFilesIncludesOptIns(t *testing.T) {
	found := map[string]bool{}
	for _, n := range syncableFiles {
		found[n] = true
	}
	for _, want := range []string{"settings.json", "skills.json", "commands.json", "mcp_clients.json"} {
		if !found[want] {
			t.Fatalf("syncableFiles must contain opt-in entry %s", want)
		}
	}
	if len(syncableFiles) != len(defaultSyncedFiles)+4 {
		t.Fatalf("syncableFiles len %d, want defaultSyncedFiles len %d + 4", len(syncableFiles), len(defaultSyncedFiles))
	}
}

func TestSyncConfigLoadMigratesLegacyWebDAVFields(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{
		"backend": "webdav",
		"webdavServer": "https://dav.example.com",
		"webdavPath": "uniTerm-sync",
		"webdavUser": "alice"
	}`)
	if err := os.WriteFile(filepath.Join(dir, syncConfigFileName), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := NewSyncConfigStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backend != "webdav" {
		t.Fatalf("backend = %q, want webdav", cfg.Backend)
	}
	want := RemoteConfig{Server: "https://dav.example.com", Path: "uniTerm-sync", User: "alice"}
	if cfg.Remote != want {
		t.Fatalf("remote = %+v, want %+v", cfg.Remote, want)
	}
}

func TestSyncConfigLoadRemoteWinsOverLegacy(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{
		"backend": "webdav",
		"remote": {"server": "https://new.example.com", "path": "p", "user": "bob"},
		"webdavServer": "https://old.example.com"
	}`)
	if err := os.WriteFile(filepath.Join(dir, syncConfigFileName), data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := NewSyncConfigStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote.Server != "https://new.example.com" {
		t.Fatalf("remote.server = %q, want the new-style value to win", cfg.Remote.Server)
	}
}

func TestSyncConfigSaveLoadRoundTripRemote(t *testing.T) {
	dir := t.TempDir()
	store := NewSyncConfigStore(dir)
	in := SyncConfig{
		Backend: "s3",
		Remote:  RemoteConfig{Server: "https://s3.example.com", Path: "sync", User: "AK", Bucket: "bkt", Region: "us-east-1", URLStyle: "path"},
		Branch:  "main",
	}
	if err := store.Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if out.Remote != in.Remote {
		t.Fatalf("remote = %+v, want %+v", out.Remote, in.Remote)
	}
}

// skills.json / commands.json / mcp_clients.json are opt-in entries: absent
// from the default sync scope but resolvable once the user ticks them in the
// scope picker.
func TestEffectiveSyncFilesOptIns(t *testing.T) {
	for _, name := range []string{"skills.json", "commands.json", "mcp_clients.json"} {
		if got := EffectiveSyncFiles(nil); containsStr(got, name) {
			t.Fatalf("%s must not be in the default scope, got %v", name, got)
		}
		got := EffectiveSyncFiles([]string{name})
		if len(got) != 1 || got[0] != name {
			t.Fatalf("%s must resolve when explicitly ticked, got %v", name, got)
		}
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
