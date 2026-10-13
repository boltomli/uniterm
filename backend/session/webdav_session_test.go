package session

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

	total, err := s.calcWebdavRemoteDirSize(s.client, "/dir")
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

	err := s.downloadDir(s.client, "/dir", localDir, task)
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

	if err := s.downloadDir(s.client, "/dir", localDir, task); err != nil {
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

	if err := s.downloadDir(s.client, "/dir", localDir, task); err != nil {
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

// --- Fake WebDAV client ------------------------------------------------------
//
// The disconnect-during-transfer and partial-file scenarios need control the
// httptest server cannot give (blocking inside ReadStream, erroring mid-body),
// and the session's client field is the injected seam
// (security.md 文件会话 Medium/Low).

type fakeWebdavClient struct {
	entered chan struct{} // ReadStream entry signals, non-blocking
	blockCh chan struct{} // when non-nil, ReadStream waits on it
	stream  io.ReadCloser // returned by ReadStream when err is nil
	err     error         // returned by ReadStream when non-nil
}

func (c *fakeWebdavClient) ReadStream(p string) (io.ReadCloser, error) {
	if c.entered != nil {
		select {
		case c.entered <- struct{}{}:
		default:
		}
	}
	if c.blockCh != nil {
		<-c.blockCh
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.stream, nil
}

func (c *fakeWebdavClient) Stat(string) (os.FileInfo, error) {
	return testFileInfo{name: "f.bin", mode: 0o644}, nil
}

func (c *fakeWebdavClient) ReadDir(string) ([]os.FileInfo, error) {
	return nil, errors.New("fake webdav client: readDir not supported")
}

func (c *fakeWebdavClient) Mkdir(string, os.FileMode) error {
	return errors.New("fake webdav client: mkdir not supported")
}

func (c *fakeWebdavClient) MkdirAll(string, os.FileMode) error {
	return errors.New("fake webdav client: mkdirAll not supported")
}

func (c *fakeWebdavClient) Remove(string) error {
	return errors.New("fake webdav client: remove not supported")
}

func (c *fakeWebdavClient) RemoveAll(string) error {
	return errors.New("fake webdav client: removeAll not supported")
}

func (c *fakeWebdavClient) Rename(string, string, bool) error {
	return errors.New("fake webdav client: rename not supported")
}

func (c *fakeWebdavClient) Copy(string, string, bool) error {
	return errors.New("fake webdav client: copy not supported")
}

func (c *fakeWebdavClient) Read(string) ([]byte, error) {
	return nil, errors.New("fake webdav client: read not supported")
}

func (c *fakeWebdavClient) Write(string, []byte, os.FileMode) error {
	return errors.New("fake webdav client: write not supported")
}

func (c *fakeWebdavClient) WriteStream(string, io.Reader, os.FileMode) error {
	return errors.New("fake webdav client: writeStream not supported")
}

// errAfterReader serves data once, then fails with err — a mid-body stream
// failure like a dropped link.
type errAfterReader struct {
	data []byte
	err  error
	off  int
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

func (r *errAfterReader) Close() error { return nil }

// TestWebdavDisconnectDuringTransferCancelsTask: a download blocked inside
// ReadStream must finish as cancelled — not keep going and not report success
// — when the session disconnects mid-transfer, and the local download folder
// stays clean (security.md 文件会话 Medium: WebDAV Disconnect race).
func TestWebdavDisconnectDuringTransferCancelsTask(t *testing.T) {
	tmp := t.TempDir()
	localPath := filepath.Join(tmp, "f.bin")
	client := &fakeWebdavClient{
		entered: make(chan struct{}, 1),
		blockCh: make(chan struct{}),
		stream:  io.NopCloser(strings.NewReader("OK")),
	}
	s := NewWebDAVSession("webdav-test")
	s.client = client

	taskID, err := s.Get("/dir/f.bin", localPath, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	task := transferByID(t, &s.mu, s.transfers, taskID)

	// Wait until the download is blocked inside the server call.
	select {
	case <-client.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("download never reached the ReadStream call")
	}

	if err := s.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	close(client.blockCh)
	waitTransferGone(t, &s.mu, s.transfers, taskID)

	if task.Status != "cancelled" {
		t.Errorf("task status = %q, want cancelled", task.Status)
	}
	if s.IsConnected() {
		t.Error("session still reports connected after Disconnect")
	}
	if _, err := s.requireClient(); err == nil {
		t.Error("requireClient succeeded after Disconnect")
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Errorf("cancelled download left a file under the final name (stat err: %v)", err)
	}
	if _, err := os.Stat(localPath + ".part"); !os.IsNotExist(err) {
		t.Errorf("cancelled download left a .part file behind (stat err: %v)", err)
	}
}

// TestWebdavDownloadFileFailureLeavesNoPartialFile: a download that fails
// after real bytes were streamed must leave neither a truncated file under
// the final name nor a .part behind, and must keep an existing target
// byte-identical (security.md 文件会话 Low: partial files left under the
// final name).
func TestWebdavDownloadFileFailureLeavesNoPartialFile(t *testing.T) {
	cases := []struct {
		name        string
		preexisting bool
	}{
		{name: "no target"},
		{name: "preexisting target", preexisting: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			localPath := filepath.Join(tmp, "f.bin")
			if tc.preexisting {
				if err := os.WriteFile(localPath, []byte("OLD"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			client := &fakeWebdavClient{
				stream: &errAfterReader{
					data: []byte("ABCD"),
					err:  errors.New("fake webdav stream failure"),
				},
			}
			s := NewWebDAVSession("webdav-test")
			s.client = client

			task := &TransferTask{
				ID:         "dl-fail",
				Type:       "download",
				LocalPath:  localPath,
				RemotePath: "/dir/f.bin",
				Status:     "running",
			}
			task.start()
			if err := s.downloadFile(client, task, "/dir/f.bin", localPath); err == nil {
				t.Fatal("downloadFile succeeded, want error")
			}

			if _, err := os.Stat(localPath + ".part"); !os.IsNotExist(err) {
				t.Errorf(".part file left behind (stat err: %v)", err)
			}
			if tc.preexisting {
				b, err := os.ReadFile(localPath)
				if err != nil || string(b) != "OLD" {
					t.Errorf("preexisting target = %q, err %v; want byte-identical OLD", b, err)
				}
			} else if _, err := os.Stat(localPath); !os.IsNotExist(err) {
				t.Errorf("truncated download left under the final name (stat err: %v)", err)
			}
		})
	}
}
