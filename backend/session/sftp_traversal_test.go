package session

import (
	"bytes"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// --- Fake in-process SFTP backend (walkers) ---------------------------------
//
// pkg/sftp can run its request server over any io.ReadWriteCloser, so the
// download walkers can be driven with a scripted remote: directory listings
// and file contents are plain maps, which is the only way to serve names a
// real filesystem on Windows refuses to hold ("..\win.txt").

// testFileInfo is a hand-rolled os.FileInfo for scripted listings.
type testFileInfo struct {
	name string
	size int64
	mode os.FileMode
}

func (f testFileInfo) Name() string       { return f.name }
func (f testFileInfo) Size() int64        { return f.size }
func (f testFileInfo) Mode() os.FileMode  { return f.mode }
func (f testFileInfo) ModTime() time.Time { return time.Time{} }
func (f testFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f testFileInfo) Sys() any           { return nil }

// testLister implements sftp.ListerAt over a fixed slice (same shape as the
// listerat example in pkg/sftp's request-example.go).
type testLister []os.FileInfo

func (l testLister) ListAt(buf []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(buf, l[offset:])
	if n < len(buf) {
		return n, io.EOF
	}
	return n, nil
}

// fakeSFTPBackend serves scripted directories (List) and files (Get).
type fakeSFTPBackend struct {
	dirs  map[string][]os.FileInfo
	files map[string][]byte
}

func (b *fakeSFTPBackend) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	data, ok := b.files[r.Filepath]
	if !ok {
		return nil, os.ErrNotExist
	}
	return bytes.NewReader(data), nil
}

func (b *fakeSFTPBackend) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		entries, ok := b.dirs[r.Filepath]
		if !ok {
			return nil, os.ErrNotExist
		}
		return testLister(entries), nil
	case "Stat", "Lstat":
		if data, ok := b.files[r.Filepath]; ok {
			return testLister{testFileInfo{name: path.Base(r.Filepath), size: int64(len(data)), mode: 0o644}}, nil
		}
		if _, ok := b.dirs[r.Filepath]; ok {
			return testLister{testFileInfo{name: path.Base(r.Filepath), mode: os.ModeDir | 0o755}}, nil
		}
		return nil, os.ErrNotExist
	}
	return nil, os.ErrNotExist
}

// newFakeSFTPSession wires a session to the scripted backend above.
func newFakeSFTPSession(t *testing.T, backend *fakeSFTPBackend) *SFTPSession {
	t.Helper()
	c1, c2 := net.Pipe()
	srv := sftp.NewRequestServer(c2, sftp.Handlers{FileGet: backend, FileList: backend})
	go srv.Serve()
	client, err := sftp.NewClientPipe(c1, c1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		srv.Close()
		c1.Close()
		c2.Close()
	})
	s := NewSFTPSession("fake-sftp")
	s.sftpClient = client
	s.sshClient = nil
	return s
}

// --- Walker traversal guard --------------------------------------------------

// TestWalkRemoteForDownloadRejectsTraversalNames: a compromised server that
// lists "..\win.txt" (legal on the wire; pkg/sftp's client only strips
// "/"-separated names) must abort the walk instead of planning a local path
// outside the download folder (security.md High: SFTP directory downloads).
func TestWalkRemoteForDownloadRejectsTraversalNames(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "dl")
	backend := &fakeSFTPBackend{
		dirs: map[string][]os.FileInfo{
			"/remote": {
				testFileInfo{name: `..\win.txt`, size: 3, mode: 0o644},
				testFileInfo{name: "good.txt", size: 4, mode: 0o644},
			},
		},
		files: map[string][]byte{
			"/remote/good.txt":   []byte("GOOD"),
			`/remote/..\win.txt`: []byte("BAD"),
		},
	}
	s := newFakeSFTPSession(t, backend)
	task := &TransferTask{ID: "dl-1", Type: "download", RemotePath: "/remote", LocalPath: localDir, Status: "running"}
	task.start()

	var out []dirEntry2
	err := s.walkRemoteForDownload("/remote", localDir, task, &out)
	if err == nil {
		t.Errorf("walkRemoteForDownload accepted the traversal name %q", `..\win.txt`)
	}
	if len(out) != 0 {
		for _, e := range out {
			if rel, rerr := filepath.Rel(localDir, e.lp); rerr != nil || rel == ".." || len(rel) >= 2 && rel[:2] == ".." {
				t.Errorf("walk planned a local path outside the download folder: %q", e.lp)
			}
		}
		t.Errorf("walk planned %d file(s) despite the malicious entry", len(out))
	}
	// The entry must not be re-joined as a nested path either.
	if _, serr := os.Stat(filepath.Join(tmp, "win.txt")); serr == nil {
		t.Errorf("traversal name resolved outside the download folder")
	}
}

// TestDownloadDirRejectsTraversalWithoutWriting: end to end through
// downloadDir — the malicious listing fails the transfer and nothing is
// written, while a name without the guard would have been downloaded.
func TestDownloadDirRejectsTraversalWithoutWriting(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "dl")
	backend := &fakeSFTPBackend{
		dirs: map[string][]os.FileInfo{
			"/remote": {
				testFileInfo{name: `..\win.txt`, size: 3, mode: 0o644},
				testFileInfo{name: "good.txt", size: 4, mode: 0o644},
			},
		},
		files: map[string][]byte{
			"/remote/good.txt":   []byte("GOOD"),
			`/remote/..\win.txt`: []byte("BAD"),
		},
	}
	s := newFakeSFTPSession(t, backend)
	task := &TransferTask{ID: "dl-2", Type: "download", RemotePath: "/remote", LocalPath: localDir, Status: "running"}
	task.start()

	if err := s.downloadDir("/remote", localDir, task); err != nil {
		t.Fatalf("downloadDir: %v", err)
	}
	entries, rerr := os.ReadDir(localDir)
	if rerr != nil && !os.IsNotExist(rerr) {
		t.Fatalf("ReadDir: %v", rerr)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("downloadDir wrote files despite the malicious listing: %v", names)
	}
	if len(task.FailedFiles) == 0 {
		t.Errorf("malicious listing must be recorded in FailedFiles")
	}
	if _, serr := os.Stat(filepath.Join(tmp, "win.txt")); serr == nil {
		t.Errorf("file written outside the download folder")
	}
}

// TestDownloadDirLegitNamesSucceed: ordinary names (spaces, unicode,
// nesting) download normally once the traversal guard is in place.
func TestDownloadDirLegitNamesSucceed(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "dl")
	backend := &fakeSFTPBackend{
		dirs: map[string][]os.FileInfo{
			"/remote": {
				testFileInfo{name: "ok.txt", size: 2, mode: 0o644},
				testFileInfo{name: "sub dir", mode: os.ModeDir | 0o755},
				testFileInfo{name: "..hidden", size: 6, mode: 0o644},
			},
			"/remote/sub dir": {
				testFileInfo{name: "in.txt", size: 2, mode: 0o644},
			},
		},
		files: map[string][]byte{
			"/remote/ok.txt":         []byte("OK"),
			"/remote/..hidden":       []byte("HIDDEN"),
			"/remote/sub dir/in.txt": []byte("IN"),
		},
	}
	s := newFakeSFTPSession(t, backend)
	task := &TransferTask{ID: "dl-3", Type: "download", RemotePath: "/remote", LocalPath: localDir, Status: "running"}
	task.start()

	if err := s.downloadDir("/remote", localDir, task); err != nil {
		t.Fatalf("downloadDir: %v", err)
	}
	want := map[string]string{
		"ok.txt":                           "OK",
		"..hidden":                         "HIDDEN",
		filepath.Join("sub dir", "in.txt"): "IN",
	}
	for rel, content := range want {
		b, err := os.ReadFile(filepath.Join(localDir, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if string(b) != content {
			t.Errorf("%s = %q, want %q", rel, b, content)
		}
	}
	if len(task.FailedFiles) != 0 {
		t.Errorf("FailedFiles = %v, want none", task.FailedFiles)
	}
}

// TestRmRecursiveDoesNotFollowSymlinks: the delete fallback must Lstat: a
// symlink entry is removed as a link, never recursed into (security.md:
// deleting a folder with "shared -> /srv/data" wiped /srv/data because
// Stat() followed the link).
func TestRmRecursiveDoesNotFollowSymlinks(t *testing.T) {
	s, root := newTestSFTPSession(t)

	sub := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(sub, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "nested", "f.txt"), []byte("F"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "shared")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "data.txt"), []byte("DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(sub, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := s.rmRecursive(sub); err != nil {
		t.Fatalf("rmRecursive: %v", err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Errorf("proj/ still exists (err=%v)", err)
	}
	// The link target's contents must survive.
	if b, err := os.ReadFile(filepath.Join(target, "data.txt")); err != nil || string(b) != "DATA" {
		t.Errorf("link target damaged: %q, err %v", b, err)
	}
}

// TestRmRecursiveRemovesPlainTree: ordinary directories still recurse and
// delete bottom-up.
func TestRmRecursiveRemovesPlainTree(t *testing.T) {
	s, root := newTestSFTPSession(t)
	tree := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "a", "b", "f.txt"), []byte("F"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "top.txt"), []byte("T"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.rmRecursive(tree); err != nil {
		t.Fatalf("rmRecursive: %v", err)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Errorf("tree still exists (err=%v)", err)
	}
}
