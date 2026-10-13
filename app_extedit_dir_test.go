package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scratchTempDir points os.TempDir() at a private directory so the
// external-edit scratch root (os.TempDir()/uniterm-extedit) is created and
// inspected inside the test's own temp dir instead of the shared one.
func scratchTempDir(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, tmp)
	}
}

// Regression (security.md medium): scratch dirs were
// os.TempDir()/uniterm-extedit/<pid>-<sid> with mode 0755, so another local
// user could pre-create or swap the directory (redirecting which content
// gets uploaded back to the server) and read the remote files being edited.
// The dir name must be unpredictable, created 0700, and stable for the
// session (single-instance editors are handed the same path, issue #737).
func TestExtEditSessionDirIsUnpredictableAndPrivate(t *testing.T) {
	scratchTempDir(t)

	dir, err := ensureExtEditSessionDir("sess-one")
	if err != nil {
		t.Fatalf("ensureExtEditSessionDir: %v", err)
	}
	defer releaseExtEditSessionDir("sess-one")

	again, err := ensureExtEditSessionDir("sess-one")
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if again != dir {
		t.Fatalf("scratch dir changed within a session: %q then %q", dir, again)
	}

	other, err := ensureExtEditSessionDir("sess-two")
	if err != nil {
		t.Fatalf("ensure other session: %v", err)
	}
	defer releaseExtEditSessionDir("sess-two")
	if other == dir {
		t.Fatal("two sessions share one scratch dir")
	}

	// The pre-fix path: predictable per (pid, session id).
	predictable := filepath.Join(extEditRootDir(), fmt.Sprintf("%d-%s", os.Getpid(), sanitizePart("sess-one")))
	if dir == predictable {
		t.Fatalf("scratch dir name is predictable: %s", dir)
	}
	if !strings.Contains(filepath.Base(dir), sanitizePart("sess-one")) {
		t.Fatalf("scratch dir %q should still be attributable to its session", dir)
	}

	if runtime.GOOS != "windows" {
		if perm := modePerm(t, extEditRootDir()); perm != 0o700 {
			t.Errorf("scratch root mode = %04o, want 0700", perm)
		}
		if perm := modePerm(t, dir); perm != 0o700 {
			t.Errorf("session scratch dir mode = %04o, want 0700", perm)
		}
	}
}

// Releasing a session removes its scratch dir and forgets the mapping, so
// a later session (same id) starts from a fresh, equally unpredictable dir.
func TestReleaseExtEditSessionDirRemovesScratch(t *testing.T) {
	scratchTempDir(t)

	dir, err := ensureExtEditSessionDir("sess-rel")
	if err != nil {
		t.Fatalf("ensureExtEditSessionDir: %v", err)
	}
	releaseExtEditSessionDir("sess-rel")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch dir still present after release, stat error = %v", err)
	}
	fresh, err := ensureExtEditSessionDir("sess-rel")
	if err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	defer releaseExtEditSessionDir("sess-rel")
	if fresh == dir {
		t.Fatal("released scratch dir was reused instead of recreated")
	}
}
