package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAndroidDataDirCreatesDir(t *testing.T) {
	base := t.TempDir()
	dd, err := resolveAndroidDataDir(base)
	if err != nil {
		t.Fatalf("resolveAndroidDataDir: %v", err)
	}
	want := filepath.Join(base, "uniTerm")
	if dd.Path != want {
		t.Errorf("Path = %q, want %q", dd.Path, want)
	}
	if dd.Type != "default" || dd.FirstRun || dd.Upgrade {
		t.Errorf("DataDir flags wrong: %+v", dd)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Errorf("dir %q not created: %v", want, err)
	}
}

func TestResolveAndroidDataDirEmptyBaseFallsBackToTemp(t *testing.T) {
	dd, err := resolveAndroidDataDir("")
	if err != nil {
		t.Fatalf("resolveAndroidDataDir: %v", err)
	}
	want := filepath.Join(os.TempDir(), "uniTerm")
	if dd.Path != want {
		t.Errorf("Path = %q, want %q (TempDir fallback)", dd.Path, want)
	}
	os.RemoveAll(dd.Path) // don't leave stray dirs in the host temp
}
