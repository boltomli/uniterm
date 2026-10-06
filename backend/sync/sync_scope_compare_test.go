package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompareConfigDirsScopedIgnoresOutOfScope(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "connections.json"), []byte(`{"v":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "connections.json"), []byte(`{"v":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	// settings.json differs but is out of scope.
	if err := os.WriteFile(filepath.Join(a, "settings.json"), []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "settings.json"), []byte(`{"theme":"light"}`), 0600); err != nil {
		t.Fatal(err)
	}
	same, err := compareConfigDirs([]string{"connections.json"}, a, b, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("out-of-scope difference must be ignored")
	}
	same, err = compareConfigDirs([]string{"connections.json", "settings.json"}, a, b, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Fatal("in-scope difference must be detected")
	}
}

func TestIsConfigDirEmptyScoped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// settings.json not in scope → dir counts as empty.
	if !isConfigDirEmpty([]string{"connections.json"}, dir) {
		t.Fatal("expected empty when settings.json is out of scope")
	}
	if isConfigDirEmpty(syncableFiles, dir) {
		t.Fatal("expected non-empty when settings.json is in scope")
	}
}
