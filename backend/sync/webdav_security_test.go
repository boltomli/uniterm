package sync

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tests in this file are the WebDAV hardening regressions: an untrusted
// sync server must never be able to (a) make the client speak a cleartext
// transport that leaks the sync password, or (b) escape the mirror
// directory through a hostile manifest.json file name.

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

// TestWebDAVClientRejectsCleartextServerURL: the sync password rides on
// every WebDAV request, so a non-loopback cleartext (or non-HTTP) server
// URL must be rejected before any request is sent.
func TestWebDAVClientRejectsCleartextServerURL(t *testing.T) {
	cases := []struct{ name, serverURL string }{
		{"plain http", "http://dav.example.com/dav"},
		{"http by ip", "http://203.0.113.10/dav"},
		{"ftp scheme", "ftp://dav.example.com/dav"},
		{"file scheme", "file:///etc/dav"},
		{"no scheme", "dav.example.com/dav"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewWebDAVClient(tc.serverURL, "sync", "u", "p")
			// rootURL resolves the base for every request; the scheme
			// rejection must surface here, before any request is sent.
			if _, err := c.rootURL(); err == nil || !strings.Contains(err.Error(), "https://") {
				t.Fatalf("rootURL err = %v, want https:// rejection", err)
			}
			if err := c.MkdirAll(); err == nil || !strings.Contains(err.Error(), "https://") {
				t.Fatalf("MkdirAll err = %v, want https:// rejection", err)
			}
		})
	}
}

// TestWebDAVClientAllowsLoopbackHTTP: plain http:// stays tolerated for
// loopback hosts (local servers, tests) — guard against over-blocking.
func TestWebDAVClientAllowsLoopbackHTTP(t *testing.T) {
	f := newFakeDAV(t)
	for _, host := range []string{"127.0.0.1", "localhost"} {
		t.Run(host, func(t *testing.T) {
			serverURL := strings.Replace(f.server.URL, "127.0.0.1", host, 1)
			c := NewWebDAVClient(serverURL, "sync", "u", "p")
			if err := c.MkdirAll(); err != nil {
				t.Fatal(err)
			}
			if err := c.Put("connections.json", []byte("cipher")); err != nil {
				t.Fatal(err)
			}
			got, err := c.Get("connections.json")
			if err != nil || string(got) != "cipher" {
				t.Fatalf("get = %q err=%v", got, err)
			}
		})
	}
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

// TestWebDAVBackendFetchAllLandsWhitelistedFiles: whitelisted manifest
// names — including the opt-in settings.json — still mirror normally
// (guard against over-blocking).
func TestWebDAVBackendFetchAllLandsWhitelistedFiles(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	for _, name := range []string{"connections.json", "settings.json"} {
		if err := os.WriteFile(filepath.Join(staging, name), []byte("cipher-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.StoreAll(staging, []byte("0123456789abcdef"), SnapshotInfo{
		Version:   "v1",
		UpdatedAt: time.Now().UTC(),
		FileHashes: map[string]string{
			"connections.json": "aaa",
			"settings.json":    "bbb",
		},
	}); err != nil {
		t.Fatal(err)
	}

	mirror := t.TempDir()
	if err := b.FetchAll(mirror); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"connections.json", "settings.json"} {
		data, err := os.ReadFile(filepath.Join(mirror, name))
		if err != nil || string(data) != "cipher-"+name {
			t.Fatalf("mirror %s = %q err=%v", name, data, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(mirror, manifestName)); err != nil || len(data) == 0 {
		t.Fatalf("manifest.json must be mirrored, data=%q err=%v", data, err)
	}
}
