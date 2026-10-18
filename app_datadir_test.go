package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeDataDirFixture seeds a data dir with one file that must survive any
// refused migration.
func writeDataDirFixture(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "connections.json")
	seed := `{"connections":[{"id":"c1","name":"prod","password":"enc:v1:abc"}]}`
	if err := os.WriteFile(path, []byte(seed), 0600); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	return path
}

func assertDataIntact(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("user data missing after refused migration: %v", err)
	}
	if !strings.Contains(string(raw), `"c1"`) {
		t.Fatalf("user data was modified: %s", raw)
	}
}

// TestSetDataDir_MigrateToSelfRefuses locks in the P0 fix: migrating the data
// directory onto itself must fail BEFORE copyDir/removeDataDir run. The old
// code copied each file onto itself and then deleted the source, wiping
// connections, settings, identities and credentials.meta.
func TestSetDataDir_MigrateToSelfRefuses(t *testing.T) {
	dir := t.TempDir()
	path := writeDataDirFixture(t, dir)

	a := &App{dataDir: dir}
	err := a.SetDataDir("custom", dir, true)
	if err == nil {
		t.Fatal("SetDataDir to itself must return an error, got nil")
	}
	assertDataIntact(t, path)
}

// TestSetDataDir_MigrateIntoOwnTreeRefuses covers the second half of the
// fix: a target nested inside the source makes filepath.Walk recurse into
// the destination (and removeDataDir would then delete it).
func TestSetDataDir_MigrateIntoOwnTreeRefuses(t *testing.T) {
	dir := t.TempDir()
	path := writeDataDirFixture(t, dir)

	a := &App{dataDir: dir}
	target := filepath.Join(dir, "nested")
	err := a.SetDataDir("custom", target, true)
	if err == nil {
		t.Fatal("SetDataDir into a subdirectory of the current dir must return an error, got nil")
	}
	assertDataIntact(t, path)
	if fi, statErr := os.Stat(target); statErr == nil && fi.IsDir() {
		entries, _ := os.ReadDir(target)
		if len(entries) != 0 {
			t.Fatalf("migration ran into the source tree: target holds %d entries", len(entries))
		}
	}
}

// TestSetDataDir_MigrateFromParentRefuses: migrating into a parent of the
// current dir copies the source into a directory that already contains it.
func TestSetDataDir_MigrateFromParentRefuses(t *testing.T) {
	parent := t.TempDir()
	src := filepath.Join(parent, "data")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := writeDataDirFixture(t, src)

	a := &App{dataDir: src}
	if err := a.SetDataDir("custom", parent, true); err == nil {
		t.Fatal("SetDataDir into a parent of the current dir must return an error, got nil")
	}
	assertDataIntact(t, path)
}

// TestSetDataDir_MigrateViaSymlinkRefuses: a symlink pointing at the current
// data dir must be detected through EvalSymlinks, not compared literally.
func TestSetDataDir_MigrateViaSymlinkRefuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on windows")
	}
	dir := t.TempDir()
	path := writeDataDirFixture(t, dir)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	a := &App{dataDir: dir}
	if err := a.SetDataDir("custom", link, true); err == nil {
		t.Fatal("SetDataDir through a symlink to the current dir must return an error, got nil")
	}
	assertDataIntact(t, path)
}

// TestSetDataDir_MigrateCaseInsensitiveOnWindows: Windows paths are
// case-insensitive, so "C:\Data" and "c:\data" are the same directory.
func TestSetDataDir_MigrateCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive comparison only applies to windows")
	}
	dir := t.TempDir()
	path := writeDataDirFixture(t, dir)

	a := &App{dataDir: dir}
	if err := a.SetDataDir("custom", strings.ToUpper(dir), true); err == nil {
		t.Fatal("SetDataDir to the same dir in a different case must return an error, got nil")
	}
	assertDataIntact(t, path)
}

// TestCheckMigratePaths exercises the overlap helper directly, including the
// disjoint case that must keep working.
func TestCheckMigratePaths(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	sub := filepath.Join(src, "sub")
	for _, d := range []string{src, sub} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	sibling := filepath.Join(root, "sibling")
	if err := os.MkdirAll(sibling, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cases := []struct {
		name    string
		src     string
		dst     string
		wantErr bool
	}{
		{"identical", src, src, true},
		{"identical-trailing-separator", src + string(filepath.Separator), src, true},
		{"nested", src, sub, true},
		{"parent", sub, src, true},
		{"disjoint", src, sibling, false},
	}
	for _, c := range cases {
		err := checkMigratePaths(c.src, c.dst)
		if c.wantErr && err == nil {
			t.Errorf("%s: expected an error, got nil", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}
	}

	// Error identity: callers (and tests) can match the specific failures.
	if !errors.Is(checkMigratePaths(src, src), errSameDataDir) {
		t.Errorf("identical dirs should report errSameDataDir, got %v", checkMigratePaths(src, src))
	}
	if !errors.Is(checkMigratePaths(src, sub), errNestedDataDir) {
		t.Errorf("nested target should report errNestedDataDir, got %v", checkMigratePaths(src, sub))
	}
	if !errors.Is(checkMigratePaths(sub, src), errParentDataDir) {
		t.Errorf("parent target should report errParentDataDir, got %v", checkMigratePaths(sub, src))
	}
}

// F-12.2: the data directory must be private (0700) — the pre-fix builds
// created it 0755, letting other local users list file names (the files
// themselves are 0600). Windows does not enforce POSIX permission bits, so
// the assertions are skipped there.
func TestDataDirCreatedPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	base := t.TempDir()
	target := filepath.Join(base, "uniTerm-data")
	if !isWritable(target) {
		t.Fatal("isWritable must create the data dir")
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0700 {
		t.Fatalf("data dir mode = %04o, want 0700", fi.Mode().Perm())
	}
}

// F-12.2: pre-existing 0755 data dirs are tightened on startup.
func TestHardenDataDirPermTightensLegacyDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	hardenDataDirPerm(dir)
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0700 {
		t.Fatalf("mode after hardening = %04o, want 0700", fi.Mode().Perm())
	}
	// Empty input is a no-op (startup may call it before the dir exists).
	hardenDataDirPerm("")
}
