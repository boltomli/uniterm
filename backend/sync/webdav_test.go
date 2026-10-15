package sync

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	// The server starts rejecting uploads part-way through the delta.
	f.rejectUpload("favorites.json")
	staging := t.TempDir()
	next := SnapshotInfo{
		Version:   "v2",
		UpdatedAt: time.Now().UTC(),
		FileHashes: map[string]string{
			"connections.json": "ccc",
			"favorites.json":   "ddd",
		},
	}
	for name := range next.FileHashes {
		if err := os.WriteFile(filepath.Join(staging, name), []byte("cipher-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.StoreAll(staging, []byte("0123456789abcdef"), next); err == nil {
		t.Fatal("StoreAll must fail when an upload is rejected")
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

// fakeDAV 是最小 WebDAV 服务器：内存 KV + PROPFIND/MKCOL 处理。
type fakeDAV struct {
	mu      sync.Mutex
	files   map[string][]byte
	puts    map[string]int
	failPut map[string]bool
	dirs    map[string]bool
	server  *httptest.Server
}

func newFakeDAV(t *testing.T) *fakeDAV {
	f := &fakeDAV{files: map[string][]byte{}, puts: map[string]int{}, failPut: map[string]bool{}, dirs: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case "PUT":
			if f.rejectsPUT(path) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			body, _ := io.ReadAll(r.Body)
			f.files[path] = body
			f.puts[path]++
			w.WriteHeader(http.StatusCreated)
		case "GET":
			data, ok := f.files[path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(data)
		case "PROPFIND":
			if _, ok := f.files[path]; ok {
				w.WriteHeader(http.StatusMultiStatus)
				return
			}
			if f.dirs[path] {
				w.WriteHeader(http.StatusMultiStatus)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case "MKCOL":
			if f.dirs[path] {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			f.dirs[path] = true
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// rejectUpload makes the server reject uploads of a resource (test helper).
// The name is matched against the request path's tail, so callers pass the
// bare blob name.
func (f *fakeDAV) rejectUpload(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failPut[name] = true
}

func (f *fakeDAV) rejectsPUT(path string) bool {
	for name := range f.failPut {
		if path == name || strings.HasSuffix(path, "/"+name) {
			return true
		}
	}
	return false
}

// putCount reports how many times a resource was uploaded (test helper; same
// mutex as the handlers).
func (f *fakeDAV) putCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for path, count := range f.puts {
		if path == name || strings.HasSuffix(path, "/"+name) {
			n += count
		}
	}
	return n
}

// delete removes a resource directly (test helper; same mutex as the handlers).
func (f *fakeDAV) delete(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, name)
}

// get returns a stored resource's bytes, nil if absent (test helper; same
// mutex as the handlers).
func (f *fakeDAV) get(name string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[name]
}

func TestWebDAVClientPutGetExists(t *testing.T) {
	f := newFakeDAV(t)
	c := NewWebDAVClient(f.server.URL, "sub/dir", "user", "pass")
	if err := c.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	// Second call must succeed: an existing collection answers 405 (MKCOL).
	if err := c.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	if !f.dirs["sub"] || !f.dirs["sub/dir"] {
		t.Fatalf("dirs=%v", f.dirs)
	}
	if err := c.Put("connections.json", []byte("cipher")); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get("connections.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "cipher" {
		t.Fatalf("got %q", got)
	}
	ok, err := c.Exists("connections.json")
	if err != nil || !ok {
		t.Fatalf("exists=%v err=%v", ok, err)
	}
	if _, err := c.Get("missing.json"); err != ErrWebDAVNotFound {
		t.Fatalf("want ErrWebDAVNotFound, got %v", err)
	}
	ok, err = c.Exists("missing.json")
	if err != nil || ok {
		t.Fatalf("exists=%v err=%v", ok, err)
	}
}

func TestWebDAVClientPathEscaping(t *testing.T) {
	f := newFakeDAV(t)
	c := NewWebDAVClient(f.server.URL, "", "", "")
	for _, name := range []string{"my file.json", "a#b.json", "a?b.json", "a%zz.json"} {
		if err := c.Put(name, []byte(name)); err != nil {
			t.Fatalf("put %q: %v", name, err)
		}
		got, err := c.Get(name)
		if err != nil {
			t.Fatalf("get %q: %v", name, err)
		}
		if string(got) != name {
			t.Fatalf("get %q: got %q", name, got)
		}
	}
}

// TestWebDAVBackendStoreAllUploadsStagedOnly pins the delta contract: a
// snapshot upload carries the blobs present in the staging dir, and leaves
// every other remote blob untouched — the random per-blob nonce would
// otherwise make a one-file change re-upload the whole config.
func TestWebDAVBackendStoreAllUploadsStagedOnly(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}

	salt := []byte("0123456789abcdef")
	staging := t.TempDir()
	manifest := SnapshotInfo{Version: "v1", FileHashes: map[string]string{
		"connections.json": "aaa",
		"favorites.json":   "bbb",
	}}
	for name := range manifest.FileHashes {
		if err := os.WriteFile(filepath.Join(staging, name), []byte("cipher-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.StoreAll(staging, salt, manifest); err != nil {
		t.Fatal(err)
	}

	// Second snapshot: connections.json changed, favorites.json unchanged
	// and therefore not staged — the remote must keep its copy.
	staging2 := t.TempDir()
	manifest2 := SnapshotInfo{Version: "v2", FileHashes: manifest.FileHashes}
	if err := os.WriteFile(filepath.Join(staging2, "connections.json"), []byte("cipher-2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.StoreAll(staging2, salt, manifest2); err != nil {
		t.Fatalf("delta store: %v", err)
	}
	if got := f.putCount("connections.json"); got != 2 {
		t.Fatalf("changed blob PUT count = %d, want 2", got)
	}
	if got := f.putCount("favorites.json"); got != 1 {
		t.Fatalf("unchanged blob was re-uploaded: %d PUTs", got)
	}

	mirror := t.TempDir()
	if err := b.FetchAll(mirror); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(mirror, "favorites.json"))
	if err != nil || string(data) != "cipher-favorites.json" {
		t.Fatalf("unchanged blob lost from the remote: %q err=%v", data, err)
	}
}

// TestWebDAVBackendFetchAllSkipsNullEntries: a manifest records a
// never-persisted store with the null hash and the remote carries no blob for
// it — fetching must skip the entry, not fail on a 404.
func TestWebDAVBackendFetchAllSkipsNullEntries(t *testing.T) {
	f := newFakeDAV(t)
	b := NewWebDAVBackend(f.server.URL, "sync", "u", "p", nil)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}

	staging := t.TempDir()
	manifest := SnapshotInfo{Version: "v1", FileHashes: map[string]string{
		"connections.json": "aaa",
		"proxies.json":     nullStoreHash(),
	}}
	if err := os.WriteFile(filepath.Join(staging, "connections.json"), []byte("cipher-conn"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.StoreAll(staging, []byte("0123456789abcdef"), manifest); err != nil {
		t.Fatalf("store: %v", err)
	}

	mirror := t.TempDir()
	if err := b.FetchAll(mirror); err != nil {
		t.Fatalf("fetch with a null-hash entry must skip it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mirror, "proxies.json")); !os.IsNotExist(err) {
		t.Fatal("a null entry must not materialise a blob")
	}
}
