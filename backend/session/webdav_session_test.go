package session

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/studio-b12/gowebdav"
)

// --- Fake WebDAV server ------------------------------------------------------
//
// gowebdav speaks PROPFIND (207 multistatus) for listings and GET for file
// bodies, so an httptest handler with a scripted listing is enough to drive
// the download walker with names a real filesystem cannot hold ("..\win.txt").

// webdavSelf builds the first <response>: gowebdav's ReadDir treats it as the
// collection itself (skipSelf) and requires a 200 collection propstat.
func webdavSelf(href string) string {
	return `<D:response><D:href>` + href + `</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`
}

// webdavFile builds a file <response>; the entry name is derived from the
// href (path.Base after percent-decoding).
func webdavFile(href, size string) string {
	return `<D:response><D:href>` + href + `</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>` + size + `</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`
}

type fakeWebDAV struct {
	mu       sync.Mutex
	gets     []string
	listings map[string]string // PROPFIND path -> inner responses (self first)
}

func (f *fakeWebDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "PROPFIND":
		inner, ok := f.listings[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(207)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">`+inner+`</D:multistatus>`)
	case "GET":
		p := r.URL.Path
		f.mu.Lock()
		f.gets = append(f.gets, p)
		f.mu.Unlock()
		switch {
		case strings.Contains(p, "win"):
			io.WriteString(w, "EVIL")
		case strings.Contains(p, "ok.txt"):
			io.WriteString(w, "OK")
		case strings.Contains(p, "in.txt"):
			io.WriteString(w, "IN")
		default:
			http.NotFound(w, r)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeWebDAV) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gets...)
}

// newTestWebDAVSession wires a WebDAVSession to the fake server.
func newTestWebDAVSession(t *testing.T, listings map[string]string) (*WebDAVSession, *fakeWebDAV) {
	t.Helper()
	f := &fakeWebDAV{listings: listings}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s := NewWebDAVSession("webdav-test")
	s.client = gowebdav.NewClient(srv.URL, "", "")
	return s, f
}

func newWebdavDownloadTask(localDir string) *TransferTask {
	task := &TransferTask{
		ID:         "dl-1",
		Type:       "download",
		LocalPath:  localDir,
		RemotePath: "/dir",
		Status:     "running",
	}
	task.start()
	return task
}

// TestWebdavCalcDirSizeSkipsDotEntries: servers may list "." in a PROPFIND;
// counting it inflates the total, and as a folder it would recurse forever
// (security.md: FTP walkers — same guard applied to the WebDAV walkers).
func TestWebdavCalcDirSizeSkipsDotEntries(t *testing.T) {
	listings := map[string]string{
		"/dir/": webdavSelf("/dir/") +
			webdavFile("/dir/./", "5") +
			webdavFile("/dir/ok.txt", "2"),
	}
	s, _ := newTestWebDAVSession(t, listings)

	total, err := s.calcWebdavRemoteDirSize("/dir")
	if err != nil {
		t.Fatalf("calcWebdavRemoteDirSize: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (the \".\" entry must be skipped)", total)
	}
}

// TestWebdavDownloadDirRejectsTraversalNames: a listing entry "..\win.txt"
// must abort the walk before any request or local write outside the download
// folder (security.md High: WebDAV directory downloads).
func TestWebdavDownloadDirRejectsTraversalNames(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "dl")
	listings := map[string]string{
		"/dir/": webdavSelf("/dir/") +
			webdavFile("/dir/ok.txt", "2") +
			webdavFile("/dir/..%5Cwin.txt", "4"),
	}
	s, fake := newTestWebDAVSession(t, listings)
	task := newWebdavDownloadTask(localDir)

	err := s.downloadDir("/dir", localDir, task)
	if err == nil {
		t.Errorf("downloadDir accepted the traversal name %q", `..\win.txt`)
	}
	for _, p := range fake.requested() {
		if strings.Contains(p, "win") {
			t.Errorf("walker requested the traversal entry: %s", p)
		}
	}
	if _, serr := os.Stat(filepath.Join(tmp, "win.txt")); serr == nil {
		t.Errorf("file written outside the download folder")
	}
	if b, rerr := os.ReadFile(filepath.Join(localDir, "ok.txt")); rerr != nil || string(b) != "OK" {
		t.Errorf("legit entry before the bad one must still download: %q, err %v", b, rerr)
	}
}

// TestWebdavDownloadDirSkipsDotEntries: "." / ".." listing entries are
// skipped — never requested, never written — and the rest of the listing
// still downloads.
func TestWebdavDownloadDirSkipsDotEntries(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "dl")
	listings := map[string]string{
		"/dir/": webdavSelf("/dir/") +
			webdavFile("/dir/./", "5") +
			webdavFile("/dir/../", "6") +
			webdavFile("/dir/ok.txt", "2"),
	}
	s, fake := newTestWebDAVSession(t, listings)
	task := newWebdavDownloadTask(localDir)

	if err := s.downloadDir("/dir", localDir, task); err != nil {
		t.Fatalf("downloadDir: %v", err)
	}
	for _, p := range fake.requested() {
		if p == "/dir" || p == "/" {
			t.Errorf("walker issued a GET for a dot entry: %s", p)
		}
	}
	b, rerr := os.ReadFile(filepath.Join(localDir, "ok.txt"))
	if rerr != nil || string(b) != "OK" {
		t.Errorf("ok.txt = %q, err %v", b, rerr)
	}
	entries, _ := os.ReadDir(localDir)
	for _, e := range entries {
		if e.Name() == "." || e.Name() == ".." {
			t.Errorf("dot entry materialised locally: %s", e.Name())
		}
	}
}

// TestWebdavDownloadDirNested: ordinary names — a subdirectory and a file
// inside it — keep downloading after the guards.
func TestWebdavDownloadDirNested(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "dl")
	listings := map[string]string{
		"/dir/": webdavSelf("/dir/") +
			`<D:response><D:href>/dir/sub/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>` +
			webdavFile("/dir/ok.txt", "2"),
		"/dir/sub/": webdavSelf("/dir/sub/") +
			webdavFile("/dir/sub/in.txt", "2"),
	}
	s, _ := newTestWebDAVSession(t, listings)
	task := newWebdavDownloadTask(localDir)

	if err := s.downloadDir("/dir", localDir, task); err != nil {
		t.Fatalf("downloadDir: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(localDir, "ok.txt")); err != nil || string(b) != "OK" {
		t.Errorf("ok.txt = %q, err %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(localDir, "sub", "in.txt")); err != nil || string(b) != "IN" {
		t.Errorf("sub/in.txt = %q, err %v", b, err)
	}
	if len(task.FailedFiles) != 0 {
		t.Errorf("FailedFiles = %v, want none", task.FailedFiles)
	}
}
