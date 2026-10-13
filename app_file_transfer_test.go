package main

import (
	"os"
	"runtime"
	"testing"
)

// Regression (security.md medium): the temp copy of a remote file was
// created 0644 — readable by other local users while it is being edited.
func TestWriteExtEditTempUsesPrivateFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced on Windows")
	}
	dir := t.TempDir()

	tmp, err := writeExtEditTemp(dir, "remote", []byte{0xde, 0xad, 0xbe, 0xef}, ".txt", []byte("content"))
	if err != nil {
		t.Fatalf("writeExtEditTemp: %v", err)
	}
	if perm := modePerm(t, tmp); perm != 0o600 {
		t.Fatalf("temp file mode = %04o, want 0600", perm)
	}
}

func modePerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}
