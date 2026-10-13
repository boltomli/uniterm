package log

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// useTempLog points the package at a private temp file (under a not-yet
// existing subdirectory, so directory permissions are observable too) and
// resets any state left by a previous test. The original path is restored
// on cleanup.
func useTempLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs", "uniterm.log")
	old := logPathFn
	logPathFn = func() string { return path }
	Close()
	t.Cleanup(func() {
		Close()
		logPathFn = old
	})
	return path
}

// Regression (security.md medium, perf): Writef wrote every line straight to
// the file and fsynced it while holding the package mutex, so one slow disk
// stalled every goroutine that logged (CreateSession, FrontendLog, autotest).
// Writes must stay in a buffer until flush time.
func TestWritefDoesNotHitDiskPerLine(t *testing.T) {
	path := useTempLog(t)
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	Writef("buffer-marker-%s", "alpha")

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if bytes.Contains(got, []byte("buffer-marker-alpha")) {
		t.Fatalf("Writef pushed the line to disk immediately; log already holds %q", got)
	}
}

// Regression (security.md medium): the log directory was created 0755 and
// the file 0644, so other local users could read it (the log carries hosts,
// commands and errors).
func TestLogFileAndDirArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced on Windows")
	}
	path := useTempLog(t)
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	Writef("private-check")

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat log dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("log dir mode = %04o, want 0700", perm)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("log file mode = %04o, want 0600", perm)
	}
}
