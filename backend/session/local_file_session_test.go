package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestLocalFileSession(t *testing.T) (*LocalFileSession, string) {
	t.Helper()
	root := t.TempDir()
	s := NewLocalFileSession("t1")
	if err := s.Connect(ConnectionConfig{Cwd: root}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return s, filepath.ToSlash(root)
}

func TestLocalFileSessionConnectStartPath(t *testing.T) {
	root := t.TempDir()
	s := NewLocalFileSession("t1")
	if err := s.Connect(ConnectionConfig{Cwd: root}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	want := filepath.ToSlash(filepath.Clean(root))
	if filepath.ToSlash(s.localCwd) != want {
		t.Errorf("local cwd = %s, want %s", s.localCwd, want)
	}
	if s.remote.localCwd != want {
		t.Errorf("remote cwd = %s, want %s", s.remote.localCwd, want)
	}
	if !s.IsConnected() {
		t.Error("session should be connected")
	}
	// A nonexistent start path falls back to home.
	s2 := NewLocalFileSession("t2")
	if err := s2.Connect(ConnectionConfig{Cwd: filepath.Join(root, "missing")}); err != nil {
		t.Fatalf("connect fallback: %v", err)
	}
	home, _ := os.UserHomeDir()
	if filepath.ToSlash(s2.remote.localCwd) != filepath.ToSlash(home) {
		t.Errorf("fallback cwd = %s, want home %s", s2.remote.localCwd, home)
	}
}

func TestLocalFileSessionResolveRemote(t *testing.T) {
	s, root := newTestLocalFileSession(t)
	bslash := string(os.PathSeparator)
	cases := []struct{ in, want string }{
		{"", root},
		{"sub", root + "/sub"},
		{"./a/../b", root + "/b"},
		{"/", "/"},
		{"/C:", "C:/"},
		{"/C:/Users", "C:/Users"},
		{"C:" + bslash + "Users", "C:/Users"},
		{"C:/Users/x", "C:/Users/x"},
	}
	for _, c := range cases {
		if got := s.resolveRemote(c.in); got != c.want {
			t.Errorf("resolveRemote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLocalFileSessionListRemoteAndNavigate(t *testing.T) {
	s, root := newTestLocalFileSession(t)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.ListRemote("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Dir != root {
		t.Errorf("dir = %s, want %s", res.Dir, root)
	}
	var sawSub, sawFile bool
	for _, f := range res.Files {
		if f.Name == "sub" && f.IsDir {
			sawSub = true
		}
		if f.Name == "hello.txt" && !f.IsDir {
			sawFile = true
		}
	}
	if !sawSub || !sawFile {
		t.Errorf("listing incomplete: sawSub=%v sawFile=%v", sawSub, sawFile)
	}
	res, err = s.ChangeRemoteDir("sub")
	if err != nil {
		t.Fatalf("cd: %v", err)
	}
	if res.Dir != root+"/sub" {
		t.Errorf("cd dir = %s, want %s", res.Dir, root+"/sub")
	}
	if _, err := s.ChangeRemoteDir("hello.txt-elsewhere"); err == nil {
		t.Error("cd to missing dir should fail")
	}
}

func TestLocalFileSessionRemoteRoot(t *testing.T) {
	s, _ := newTestLocalFileSession(t)
	res, err := s.ListRemote("/")
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	if res.Dir != "/" {
		t.Errorf("root dir = %s, want /", res.Dir)
	}
	if len(res.Files) == 0 {
		t.Fatal("root listing is empty")
	}
	for _, f := range res.Files {
		if !f.IsDir {
			t.Errorf("root entry %q should be a dir", f.Name)
		}
	}
}

// waitTaskDone blocks until the task id leaves the session's transfers map
// (successful/failed/cancelled tasks are all removed or dismissible) or the
// deadline expires.
func waitTaskDone(t *testing.T, s *LocalFileSession, taskID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		_, running := s.transfers[taskID]
		s.mu.RUnlock()
		if !running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s did not finish in time", taskID)
}

func TestLocalFileSessionTransferRoundTrip(t *testing.T) {
	s, root := newTestLocalFileSession(t)
	src := filepath.Join(root, "src.txt")
	payload := strings.Repeat("A", 200*1024) // > copy buffer, exercises progress
	if err := os.WriteFile(src, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	// Put: local pane -> remote pane. The copy runs async; wait for the
	// task to leave the transfers map before reading the result.
	putTask, err := s.Put(src, "copied.txt", false)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	waitTaskDone(t, s, putTask)
	got, err := s.GetContent("copied.txt")
	if err != nil {
		t.Fatalf("get content: %v", err)
	}
	if string(got) != payload {
		t.Errorf("remote copy content mismatch (%d bytes)", len(got))
	}
	// Get: remote pane -> a fresh local dir.
	dstDir := t.TempDir()
	taskID, err := s.Get("copied.txt", filepath.Join(dstDir, "out.txt"), false)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	waitTaskDone(t, s, taskID)
	out, err := os.ReadFile(filepath.Join(dstDir, "out.txt"))
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if string(out) != payload {
		t.Errorf("local copy content mismatch (%d bytes)", len(out))
	}
}

func TestLocalFileSessionTransferControls(t *testing.T) {
	s, _ := newTestLocalFileSession(t)
	if err := s.CancelTransfer("nope"); err == nil {
		t.Error("cancel of unknown task should fail")
	}
	if err := s.PauseTransfer("nope"); err == nil {
		t.Error("pause of unknown task should fail")
	}
	if err := s.ResumeTransfer("nope"); err == nil {
		t.Error("resume of unknown task should fail")
	}
	if err := s.DismissTransfer("nope"); err != nil {
		t.Errorf("dismiss of unknown task should be a no-op: %v", err)
	}
}

// A recursive Remove must never reach a filesystem root. The previous guard
// compared against "/" and VolumeName(target)==target, which both miss the
// Windows forms ("\\" and "C:\\"), so deleting the drive-picker row could run
// os.RemoveAll on an entire volume.
func TestLocalFileSessionFilesystemRootGuard(t *testing.T) {
	roots := []string{string(filepath.Separator), "/", `\`, "//"}
	if runtime.GOOS == "windows" {
		// resolveRemote normalizes drive-relative inputs to "C:/", and the
		// drive-picker row joins to "/C:"; all three forms end up as C:\.
		roots = append(roots, `C:`, `C:\`, "C:/")
	}
	for _, p := range roots {
		target := filepath.FromSlash(p)
		if !isFilesystemRoot(target) {
			t.Errorf("isFilesystemRoot(%q) = false, want true", target)
		}
	}

	s, root := newTestLocalFileSession(t)
	normal := []string{root, root + "/sub", filepath.Join(root, "sub", "file.txt")}
	for _, p := range normal {
		if isFilesystemRoot(p) {
			t.Errorf("isFilesystemRoot(%q) = true, want false", p)
		}
	}
	// Non-recursive removal of a real directory still works.
	dir := filepath.Join(root, "gone")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("gone", false); err != nil {
		t.Fatalf("Remove(normal dir): %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir still exists: %v", err)
	}
}

// readFileCapped keeps the editor from materializing an arbitrarily large
// local file (and its base64 IPC copy) in memory.
func TestLocalFileReadFileCapped(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := readFileCapped(small, 16); err != nil || string(got) != "hello" {
		t.Fatalf("readFileCapped(small) = %q, %v", got, err)
	}
	if _, err := readFileCapped(small, 4); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("readFileCapped over limit = %v, want too-large error", err)
	}
	if _, err := readFileCapped(dir, 16); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("readFileCapped(dir) = %v, want directory error", err)
	}
	if _, err := readFileCapped(filepath.Join(dir, "missing"), 16); err == nil {
		t.Fatal("readFileCapped(missing) succeeded")
	}
}

// dirSize must count a symlinked directory's content (copyTree materialises
// it) and must terminate on a link loop.
func TestLocalFileDirSizeSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("1234567"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sub, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err := dirSize(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != 5+7+7 {
		t.Fatalf("dirSize = %d, want 19 (file + subtree + symlinked subtree)", got)
	}

	// Loop: sub/loop -> root. The walk must stop at the resolved root instead
	// of growing the path until ENAMETOOLONG.
	if err := os.Symlink(root, filepath.Join(sub, "loop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err = dirSize(root)
	if err != nil {
		t.Fatalf("dirSize with loop: %v", err)
	}
	if got <= 0 {
		t.Fatalf("dirSize with loop = %d, want a bounded positive size", got)
	}
}

// Directory transfers compute their total inside the worker (the binding
// returns before the tree walk), so the completion path must still work.
func TestLocalFileSessionDirectoryTransfer(t *testing.T) {
	s, root := newTestLocalFileSession(t)
	src := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("bbbbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := s.Put(src, "tree-copy", true)
	if err != nil {
		t.Fatalf("put dir: %v", err)
	}
	waitTaskDone(t, s, id)
	got, err := os.ReadFile(filepath.Join(root, "tree-copy", "sub", "b.txt"))
	if err != nil || string(got) != "bbbbb" {
		t.Fatalf("copied file = %q, %v", got, err)
	}
}

func TestLocalFileHiddenAttributeProbeScope(t *testing.T) {
	for _, dir := range []string{`C:\Users\x`, "/home/x"} {
		if !hiddenAttributeProbeable(dir) {
			t.Errorf("hiddenAttributeProbeable(%q) = false, want true", dir)
		}
	}
	// UNC shares (WSL's 9P mount) must not pay one network probe per entry.
	for _, dir := range []string{`\\wsl.localhost\Ubuntu\home`, "//wsl.localhost/Ubuntu/home", `\\server\share`} {
		if hiddenAttributeProbeable(dir) {
			t.Errorf("hiddenAttributeProbeable(%q) = true, want false", dir)
		}
	}
}
