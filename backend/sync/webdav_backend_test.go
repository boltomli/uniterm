package sync

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// testFilesUnder collects every file path beneath root (test helper for the
// escape checks below).
func testFilesUnder(t *testing.T, root string) map[string]bool {
	t.Helper()
	files := map[string]bool{}
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			files[path] = true
		}
		return nil
	})
	return files
}

// TestWebDAVBackendFetchAllRejectsTraversalNames is the security.md P0
// regression guard: the sync server is untrusted (it never sees the
// encryption key), and manifest.json is fully attacker-controlled. Any
// manifest file name outside the syncable whitelist plus the reserved
// salt/manifest resources must be rejected before any disk write, with an
// error — never silently skipped, never escaping dir.
func TestWebDAVBackendFetchAllRejectsTraversalNames(t *testing.T) {
	badNames := []string{
		"../../evil.txt",
		`..\evil`,
		"/abs.txt",
		"a/b.txt",
		"..",
	}
	for _, name := range badNames {
		t.Run(name, func(t *testing.T) {
			f := newFakeDAV(t)
			b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
			if err := b.Probe(); err != nil {
				t.Fatal(err)
			}
			// Plant the hostile manifest and the resource it references
			// directly, bypassing StoreAll: a malicious server can send
			// anything.
			c := NewWebDAVClient(f.server.URL, "sync", "u", "p")
			manifest := SnapshotInfo{
				Version:    "v1",
				UpdatedAt:  time.Now().UTC(),
				FileHashes: map[string]string{name: "aaa"},
			}
			if err := c.Put(manifestName, mustJSON(manifest)); err != nil {
				t.Fatal(err)
			}
			if err := c.Put(name, []byte("pwned")); err != nil {
				t.Fatal(err)
			}

			// Nest the mirror two levels deep inside this test function's
			// private temp tree so a ".."/"../.." escape lands inside the
			// scanned area, which is itself private to this test. Scanning
			// the shared %TEMP% root (the previous approach) raced with
			// other packages' tests under `go test ./...`.
			base := t.TempDir() // <temp>/<TestName...>/001 — private per test
			mirror := filepath.Join(base, "lvl0", "lvl1")
			if err := os.MkdirAll(mirror, 0o755); err != nil {
				t.Fatal(err)
			}
			scanRoot := filepath.Dir(base) // <temp>/<TestName...> — still private
			outsideBefore := testFilesUnder(t, scanRoot)

			err := b.FetchAll(mirror)
			if err == nil {
				t.Fatalf("FetchAll accepted hostile manifest name %q", name)
			}
			if !strings.Contains(err.Error(), "disallowed") {
				t.Fatalf("FetchAll error must call out the rejected manifest entry, got: %v", err)
			}
			for path := range testFilesUnder(t, scanRoot) {
				if !outsideBefore[path] {
					t.Fatalf("FetchAll leaked a file outside %s: %s", mirror, path)
				}
			}
		})
	}
}

// One hostile entry poisons the whole manifest: validation happens before
// any write, so not even whitelisted siblings get mirrored.
func TestWebDAVBackendFetchAllRejectsBeforeWritingAnything(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}
	c := NewWebDAVClient(f.server.URL, "sync", "u", "p")
	manifest := SnapshotInfo{
		Version:   "v1",
		UpdatedAt: time.Now().UTC(),
		FileHashes: map[string]string{
			"connections.json": "aaa",
			"../../evil.txt":   "bbb",
		},
	}
	if err := c.Put(manifestName, mustJSON(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("connections.json", []byte("cipher")); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("../../evil.txt", []byte("pwned")); err != nil {
		t.Fatal(err)
	}

	mirror := t.TempDir()
	outside := filepath.Dir(filepath.Dir(mirror))
	outsideBefore := testFilesUnder(t, outside)

	if err := b.FetchAll(mirror); err == nil {
		t.Fatal("FetchAll must reject a manifest containing a hostile name")
	}
	if _, err := os.Stat(filepath.Join(mirror, "connections.json")); !os.IsNotExist(err) {
		t.Fatalf("connections.json mirrored despite hostile manifest, stat err=%v", err)
	}
	for path := range testFilesUnder(t, outside) {
		if !outsideBefore[path] {
			t.Fatalf("FetchAll leaked a file outside %s: %s", mirror, path)
		}
	}
}

// Whitelisted manifest names — including the opt-in settings.json — still
// mirror normally.
func TestWebDAVBackendFetchAllLandsWhitelistedFiles(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	for _, name := range []string{"connections.json", settingsJSON} {
		if err := os.WriteFile(filepath.Join(staging, name), []byte("cipher-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.StoreAll(staging, []byte("0123456789abcdef"), SnapshotInfo{
		Version:   "v1",
		UpdatedAt: time.Now().UTC(),
		FileHashes: map[string]string{
			"connections.json": "aaa",
			settingsJSON:       "bbb",
		},
	}); err != nil {
		t.Fatal(err)
	}

	mirror := t.TempDir()
	if err := b.FetchAll(mirror); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"connections.json", settingsJSON} {
		data, err := os.ReadFile(filepath.Join(mirror, name))
		if err != nil || string(data) != "cipher-"+name {
			t.Fatalf("mirror %s = %q err=%v", name, data, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(mirror, manifestName)); err != nil || len(data) == 0 {
		t.Fatalf("manifest.json must be mirrored, data=%q err=%v", data, err)
	}
}
