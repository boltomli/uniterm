package sync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWebDAVBackendHeadEmpty(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}
	info, err := b.Head()
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Fatal("empty remote must return nil snapshot")
	}
}

func TestWebDAVBackendStoreAllFetchAllRoundTrip(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}

	staging := t.TempDir()
	salt := []byte("0123456789abcdef")
	manifest := SnapshotInfo{
		Version:   "v1",
		UpdatedAt: time.Now().UTC(),
		FileHashes: map[string]string{
			"connections.json": "aaa",
			"favorites.json":   "bbb",
		},
	}
	for name := range manifest.FileHashes {
		if err := os.WriteFile(filepath.Join(staging, name), []byte("cipher-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.StoreAll(staging, salt, manifest); err != nil {
		t.Fatal(err)
	}

	info, err := b.Head()
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.Version != "v1" || len(info.FileHashes) != 2 {
		t.Fatalf("head = %+v", info)
	}

	mirror := t.TempDir()
	if err := b.FetchAll(mirror); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(mirror, "connections.json"))
	if err != nil || string(data) != "cipher-connections.json" {
		t.Fatalf("mirror file wrong: %q err=%v", data, err)
	}
	gotSalt, err := ReadSaltFile(mirror)
	if err != nil || string(gotSalt) != string(salt) {
		t.Fatalf("salt wrong: %x err=%v", gotSalt, err)
	}
}

func TestWebDAVBackendFetchAllOnEmptyRemote(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}
	if err := b.FetchAll(t.TempDir()); err != nil {
		t.Fatalf("fetch on empty remote must be a no-op, got %v", err)
	}
}

// storeSnapshot uploads one valid snapshot ("v1") with two files to b.
func storeSnapshot(t *testing.T, b *WebDAVBackend) {
	t.Helper()
	staging := t.TempDir()
	for _, name := range []string{"connections.json", "favorites.json"} {
		if err := os.WriteFile(filepath.Join(staging, name), []byte("cipher-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := SnapshotInfo{
		Version:   "v1",
		UpdatedAt: time.Now().UTC(),
		FileHashes: map[string]string{
			"connections.json": "aaa",
			"favorites.json":   "bbb",
		},
	}
	if err := b.StoreAll(staging, []byte("0123456789abcdef"), manifest); err != nil {
		t.Fatal(err)
	}
}

// The manifest is the commit point: a failed StoreAll must never clobber
// the previous snapshot.
func TestWebDAVBackendStoreAllFailureKeepsOldManifest(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}
	storeSnapshot(t, b)

	// Manifest references a file that is absent from stagingDir.
	staging := t.TempDir()
	bad := SnapshotInfo{
		Version:   "v2",
		UpdatedAt: time.Now().UTC(),
		FileHashes: map[string]string{
			"connections.json": "ccc",
			"favorites.json":   "ddd",
		},
	}
	if err := b.StoreAll(staging, []byte("0123456789abcdef"), bad); err == nil {
		t.Fatal("StoreAll with missing staging file must fail")
	}
	info, err := b.Head()
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.Version != "v1" {
		t.Fatalf("old manifest must survive failed StoreAll, head = %+v", info)
	}
}

func TestWebDAVBackendFetchAllToleratesMissingSalt(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}
	storeSnapshot(t, b)
	f.delete(saltName)
	if err := b.FetchAll(t.TempDir()); err != nil {
		t.Fatalf("FetchAll must tolerate a missing salt, got %v", err)
	}
}

func TestWebDAVBackendHeadCorruptManifest(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}
	c := NewWebDAVClient(f.server.URL, "sync", "u", "p")
	if err := c.Put(manifestName, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	info, err := b.Head()
	if err == nil {
		t.Fatalf("corrupt manifest must error, head = %+v", info)
	}
}
