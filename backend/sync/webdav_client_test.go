package sync

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDAV 是最小 WebDAV 服务器：内存 KV + PROPFIND/MKCOL 处理。
type fakeDAV struct {
	mu     sync.Mutex
	files  map[string][]byte
	dirs   map[string]bool
	server *httptest.Server
}

func newFakeDAV(t *testing.T) *fakeDAV {
	f := &fakeDAV{files: map[string][]byte{}, dirs: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case "PUT":
			body, _ := io.ReadAll(r.Body)
			f.files[path] = body
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
