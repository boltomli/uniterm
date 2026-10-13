package session

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudsoda/go-smb2"
)

// --- Fake SMB backend --------------------------------------------------------
//
// *smb2.Share cannot be fabricated without a live server (Open/Create return
// the concrete *smb2.File with an unexported session behind it), so
// smb_session.go narrows the conn/share fields to small interfaces; these
// fakes implement exactly those interfaces.

var fakeSMBReadErr = errors.New("fake smb read failure")

type fakeSMBFile struct {
	data      []byte
	failAfter int64 // when >= 0, ReadAt serves up to this offset then errors
}

func (f *fakeSMBFile) Read(p []byte) (int, error) {
	n, err := f.ReadAt(p, 0)
	_ = err // Read is unused by the production paths under test
	return n, err
}

func (f *fakeSMBFile) ReadAt(p []byte, off int64) (int, error) {
	if f.failAfter >= 0 {
		end := f.failAfter
		if end > int64(len(f.data)) {
			end = int64(len(f.data))
		}
		if off >= end {
			return 0, fakeSMBReadErr
		}
		// Serve the bytes before the failure point together with the error,
		// so the caller observes a mid-transfer failure after real progress.
		return copy(p, f.data[off:end]), fakeSMBReadErr
	}
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeSMBFile) Write([]byte) (int, error) {
	return 0, errors.New("fake smb file: write not supported")
}

func (f *fakeSMBFile) WriteAt([]byte, int64) (int, error) {
	return 0, errors.New("fake smb file: writeAt not supported")
}

func (f *fakeSMBFile) Close() error { return nil }

type fakeSMBShare struct {
	mu        sync.Mutex
	listing   map[string][]os.FileInfo // dir path -> entries
	data      map[string][]byte        // file path -> contents
	failAfter map[string]int64         // file path -> ReadAt failure offset
	statErr   map[string]error         // file path -> Stat error
	blockPath string                   // Open of this path waits on blockCh
	blockCh   chan struct{}
	entered   chan struct{} // Open entry signals, non-blocking
	umounted  bool
}

func newFakeSMBShare() *fakeSMBShare {
	return &fakeSMBShare{
		listing:   map[string][]os.FileInfo{},
		data:      map[string][]byte{},
		failAfter: map[string]int64{},
		statErr:   map[string]error{},
		entered:   make(chan struct{}, 64),
	}
}

func (f *fakeSMBShare) ReadDir(dirname string) ([]os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, ok := f.listing[dirname]
	if !ok {
		return nil, errors.New("fake share: no such directory")
	}
	return entries, nil
}

func (f *fakeSMBShare) Stat(name string) (os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.statErr[name]; ok {
		return nil, err
	}
	if _, ok := f.listing[name]; ok {
		return testFileInfo{name: path.Base(name), mode: os.ModeDir | 0o755}, nil
	}
	if d, ok := f.data[name]; ok {
		return testFileInfo{name: path.Base(name), size: int64(len(d)), mode: 0o644}, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeSMBShare) Open(name string) (smbRemoteFile, error) {
	if name == f.blockPath {
		select {
		case f.entered <- struct{}{}:
		default:
		}
		<-f.blockCh
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.data[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	fail := int64(-1)
	if v, ok := f.failAfter[name]; ok {
		fail = v
	}
	return &fakeSMBFile{data: d, failAfter: fail}, nil
}

func (f *fakeSMBShare) Create(string) (smbRemoteFile, error) {
	return nil, errors.New("fake share: create not supported")
}

func (f *fakeSMBShare) Mkdir(string, os.FileMode) error {
	return errors.New("fake share: mkdir not supported")
}

func (f *fakeSMBShare) Remove(string) error {
	return errors.New("fake share: remove not supported")
}

func (f *fakeSMBShare) RemoveAll(string) error {
	return errors.New("fake share: removeAll not supported")
}

func (f *fakeSMBShare) Rename(string, string) error {
	return errors.New("fake share: rename not supported")
}

func (f *fakeSMBShare) Chmod(string, os.FileMode) error {
	return errors.New("fake share: chmod not supported")
}

func (f *fakeSMBShare) Umount() error {
	f.mu.Lock()
	f.umounted = true
	f.mu.Unlock()
	return nil
}

func (f *fakeSMBShare) wasUmounted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.umounted
}

type fakeSMBConn struct {
	mu       sync.Mutex
	shares   []string
	logoffed bool
}

func (c *fakeSMBConn) Echo() error { return nil }

func (c *fakeSMBConn) Logoff() error {
	c.mu.Lock()
	c.logoffed = true
	c.mu.Unlock()
	return nil
}

func (c *fakeSMBConn) Mount(string, ...smb2.MountOption) (*smb2.Share, error) {
	return nil, errors.New("fake conn: mount not supported")
}

func (c *fakeSMBConn) ListSharenames() ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.shares...), nil
}

func (c *fakeSMBConn) wasLogoffed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logoffed
}

// newTestSMBSession wires an SMBSession to the fake conn/share with a mounted
// share at /data. The conn is returned so tests can assert on it after
// Disconnect has niled the session field.
func newTestSMBSession(t *testing.T, share *fakeSMBShare) (*SMBSession, *fakeSMBConn) {
	t.Helper()
	s := NewSMBSession("smb-test")
	conn := &fakeSMBConn{shares: []string{"data"}}
	s.conn = conn
	s.share = share
	s.cwd = "/data"
	s.setStatus(StatusConnected)
	return s, conn
}

// transferByID returns the live task registered for taskID.
func transferByID(t *testing.T, mu *sync.RWMutex, transfers map[string]*TransferTask, taskID string) *TransferTask {
	t.Helper()
	mu.RLock()
	defer mu.RUnlock()
	task, ok := transfers[taskID]
	if !ok {
		t.Fatalf("transfer %s not registered", taskID)
	}
	return task
}

// waitTransferGone waits (bounded) for the transfer goroutine to drop the
// task from the map; a task stuck in "error" is retained for retry and never
// disappears, which surfaces as a timeout failure.
func waitTransferGone(t *testing.T, mu *sync.RWMutex, transfers map[string]*TransferTask, taskID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.RLock()
		_, ok := transfers[taskID]
		mu.RUnlock()
		if !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("transfer %s never finished", taskID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSMBDisconnectDuringTransferCancelsTask: a directory download blocked
// inside a share call must finish as cancelled — not keep running against a
// dead share and not crash the app — when the session disconnects while it is
// in flight, and ChangeRemoteDir("..") back to the share list must return a
// clean error instead of nil-dereferencing the unmounted share
// (security.md 文件会话 Medium: SMB Disconnect race).
func TestSMBDisconnectDuringTransferCancelsTask(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "dl")
	share := newFakeSMBShare()
	share.listing["dir"] = []os.FileInfo{
		testFileInfo{name: "a.bin", size: 1024, mode: 0o644},
		testFileInfo{name: "b.bin", size: 1024, mode: 0o644},
	}
	share.data["dir/a.bin"] = bytes.Repeat([]byte("a"), 1024)
	share.data["dir/b.bin"] = bytes.Repeat([]byte("b"), 1024)
	share.blockPath = "dir/a.bin"
	share.blockCh = make(chan struct{})
	s, conn := newTestSMBSession(t, share)

	taskID, err := s.Get("/data/dir", localDir, true)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	task := transferByID(t, &s.mu, s.transfers, taskID)

	// Wait until the walker is blocked inside Open("dir/a.bin").
	select {
	case <-share.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("download walker never reached the share Open call")
	}

	if err := s.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	// Navigating back to the share list during the in-flight transfer used to
	// call Umount on the already-nil share and panic the whole process.
	if _, err := s.ChangeRemoteDir(".."); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("ChangeRemoteDir(..) after Disconnect = %v, want not-connected error", err)
	}

	// Unblock the blocked Open; the walker must wind down via the cancel.
	close(share.blockCh)
	waitTransferGone(t, &s.mu, s.transfers, taskID)

	if task.Status != "cancelled" {
		t.Errorf("task status = %q, want cancelled", task.Status)
	}
	if s.IsConnected() {
		t.Error("session still reports connected after Disconnect")
	}
	if !share.wasUmounted() {
		t.Error("Disconnect never unmounted the share")
	}
	if !conn.wasLogoffed() {
		t.Error("Disconnect never logged off the connection")
	}
}

// TestSMBConcurrentDisconnectAndNavigation: list/navigate hammering while
// Disconnect runs must be free of data races and nil derefs. Meaningful with
// -race: the pre-fix code read s.share/s.conn without any lock
// (security.md 文件会话 Medium).
func TestSMBConcurrentDisconnectAndNavigation(t *testing.T) {
	share := newFakeSMBShare()
	share.listing["dir"] = []os.FileInfo{
		testFileInfo{name: "a.bin", size: 1, mode: 0o644},
	}
	share.data["dir/a.bin"] = []byte("a")
	s, _ := newTestSMBSession(t, share)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s.ListRemote("/data/dir")
				s.ChangeRemoteDir("..")
				s.IsConnected()
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	if err := s.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	if s.IsConnected() {
		t.Error("session still reports connected after Disconnect")
	}
	if _, err := s.ListRemote(""); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("ListRemote after Disconnect = %v, want not-connected error", err)
	}
}

// TestSMBDownloadFileFailureLeavesNoPartialFile: a download that fails after
// real bytes were written must leave neither a truncated file under the final
// name nor a .part behind, and must keep an existing target byte-identical
// (security.md 文件会话 Low: partial files left under the final name).
func TestSMBDownloadFileFailureLeavesNoPartialFile(t *testing.T) {
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
			share := newFakeSMBShare()
			share.data["f.bin"] = bytes.Repeat([]byte("x"), 1024)
			// Stat fails so the copy takes the sequential path: it writes the
			// 400 bytes served before failAfter, then errors out.
			share.statErr["f.bin"] = errors.New("fake stat failure")
			share.failAfter["f.bin"] = 400
			s := NewSMBSession("smb-test")

			task := &TransferTask{
				ID:         "dl-fail",
				Type:       "download",
				LocalPath:  localPath,
				RemotePath: "f.bin",
				Status:     "running",
			}
			task.start()
			if err := s.downloadFile(share, task, "f.bin", localPath); err == nil {
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
