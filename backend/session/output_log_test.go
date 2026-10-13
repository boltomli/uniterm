package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestANSIStripperRemovesCharacterSetDesignation(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"G0 ASCII", "Type help\x1b(B for instructions", "Type help for instructions"},
		{"G1 ASCII", "root\x1b)B@host", "root@host"},
		{"all SCS introducers", "a\x1b(B\x1b)B\x1b*B\x1b+B\x1b-B\x1b.B\x1b/Bb", "ab"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stripper ansiStripper
			if got := string(stripper.Strip([]byte(tt.in))); got != tt.want {
				t.Fatalf("Strip(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestANSIStripperCharacterSetDesignationAcrossChunks(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
	}{
		{"split after ESC", []string{"root\x1b", "(B@host"}},
		{"split after introducer", []string{"root\x1b(", "B@host"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stripper ansiStripper
			var got []byte
			for _, chunk := range tt.chunks {
				got = append(got, stripper.Strip([]byte(chunk))...)
			}
			if string(got) != "root@host" {
				t.Fatalf("stripped chunks = %q, want %q", got, "root@host")
			}
		})
	}
}

// TestEnableLogUsesOwnerOnlyPerms: session logs contain raw terminal output
// (tokens, `cat .env`), so the log directory must be created 0700 and the
// log file 0600 — never readable by other local users (security.md 文件会话
// Low: "session logs are world-readable"). os.Stat cannot observe POSIX
// mode bits on Windows, so the call sites go through the mkdirAll /
// openLogFile seams: the test wraps them to record the requested modes
// while forwarding to the real functions, then asserts the recorded modes
// AND that the directory/file were really created.
func TestEnableLogUsesOwnerOnlyPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	var gotDirMode, gotFileMode os.FileMode
	mkdirAll = func(path string, perm os.FileMode) error {
		gotDirMode = perm
		return os.MkdirAll(path, perm)
	}
	openLogFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		gotFileMode = perm
		return os.OpenFile(name, flag, perm)
	}
	t.Cleanup(func() {
		mkdirAll = os.MkdirAll
		openLogFile = os.OpenFile
	})

	var l OutputLogger
	l.SetBuffered(false)
	path, err := l.Enable(dir, "perm.log", "session", "host", "ssh")
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	defer l.Disable()

	if gotDirMode != 0o700 {
		t.Errorf("log dir mode = %#o, want 0o700", gotDirMode)
	}
	if gotFileMode != 0o600 {
		t.Errorf("log file mode = %#o, want 0o600", gotFileMode)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("log dir was not created: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("log file was not created: %v", err)
	}
	if runtime.GOOS != "windows" {
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("log dir on-disk mode = %#o, want 0o700", got)
		}
		pfi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := pfi.Mode().Perm(); got != 0o600 {
			t.Errorf("log file on-disk mode = %#o, want 0o600", got)
		}
	}
}
