package recorder

import (
	"os"
	"runtime"
	"testing"
)

// TestRecordingFileIsPrivate pins the 0600 mode: a cast file replays whatever
// the terminal displayed, which regularly includes credentials.
func TestRecordingFileIsPrivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	r := &Recorder{records: make(map[string]*openRecording)}
	path, err := r.StartRecording("sess-1", 80, 24, "test")
	if err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	defer func() { _ = r.StopRecording(path) }()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat recording: %v", err)
	}
	// Windows has no POSIX permission bits (Go reports 0666 for any writable
	// file); the 0600/0700 modes are enforced on Unix.
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("recording mode = %o, want 600", perm)
		}
	}

	dir, err := os.Stat(r.RecordsDir())
	if err != nil {
		t.Fatalf("stat records dir: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := dir.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("records dir mode = %o, want no group/other access", perm)
		}
	}
}
