package utils

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSafeLocalJoin pins the shared local-path join helper used by every
// download/unpack/sync writer (security.md P0). Traversal input must be
// rejected; accepted input must land inside root.
func TestSafeLocalJoin(t *testing.T) {
	root := t.TempDir()

	reject := []string{
		"",
		"..",
		"../x",
		"a/../../x",
		"a/../b", // conservative: any ".." element is refused
		"/abs",
		`\abs`,
		"//srv/share/x",
		`\\srv\share\x`,
		"C:/x",
		`C:\x`,
		"c:foo",
		"a\x00b",
	}
	for _, rel := range reject {
		if got, err := SafeLocalJoin(root, rel); err == nil {
			t.Errorf("SafeLocalJoin(%q) = %q, want error", rel, got)
		}
	}

	accept := map[string]string{
		"a/b.txt":   filepath.Join(root, "a", "b.txt"),
		"b.txt":     filepath.Join(root, "b.txt"),
		"./a/b.txt": filepath.Join(root, "a", "b.txt"),
	}
	for rel, want := range accept {
		got, err := SafeLocalJoin(root, rel)
		if err != nil {
			t.Errorf("SafeLocalJoin(%q) error: %v", rel, err)
			continue
		}
		if got != want {
			t.Errorf("SafeLocalJoin(%q) = %q, want %q", rel, got, want)
		}
	}

	// Result must be lexically contained in root.
	got, err := SafeLocalJoin(root, filepath.Join("sub", "file.txt"))
	if err != nil {
		t.Fatalf("SafeLocalJoin(valid): %v", err)
	}
	relOut, err := filepath.Rel(root, got)
	if err != nil || relOut == ".." || strings.HasPrefix(relOut, ".."+string(filepath.Separator)) {
		t.Errorf("SafeLocalJoin escaped root: %q", got)
	}

	if runtime.GOOS == "windows" {
		// Reserved device names are not local paths on Windows.
		if _, err := SafeLocalJoin(root, "NUL"); err == nil {
			t.Errorf("SafeLocalJoin(\"NUL\") accepted on windows")
		}
	}

	if _, err := SafeLocalJoin("", "a"); err == nil {
		t.Errorf("SafeLocalJoin with empty root accepted")
	}
}

// TestSafeRemoteName pins the single-component check for server-supplied
// file names (SCP directives, FTP/SFTP entries, sync manifest names).
func TestSafeRemoteName(t *testing.T) {
	ok := []string{"file.txt", "a b.tar.gz", "中文名.txt"}
	for _, name := range ok {
		if err := SafeRemoteName(name); err != nil {
			t.Errorf("SafeRemoteName(%q) = %v, want nil", name, err)
		}
	}

	bad := []string{
		"", ".", "..",
		"a/b", `a\b`,
		"/x", `\x`,
		"C:x", "a\x00b",
	}
	for _, name := range bad {
		if err := SafeRemoteName(name); err == nil {
			t.Errorf("SafeRemoteName(%q) accepted, want error", name)
		}
	}
}
