package store

import (
	"os"
	"path/filepath"
	"testing"
)

// --- Bootstrap resolution: exe/data (portable) pointer first, user-config
// pointer second, legacy nested pointer (double-joined pre-fix builds) last
// and migrated on read. The pre-fix builds wrote the user-config pointer to
// <UserConfigDir>/uniTerm/bootstrap.json/bootstrap.json (joining the file
// name onto an already-complete file path), which both shadowed portable
// copies via the machine-shared user-config location and left
// bootstrap.json as a directory on disk. ---

func writeRaw(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDataDirPortableWinsOverUserConfigPointer(t *testing.T) {
	exe := t.TempDir()
	defDir := t.TempDir()
	writeRaw(t, filepath.Join(exe, "data", bootstrapFileName), `{"type":"portable"}`)
	writeRaw(t, filepath.Join(defDir, bootstrapFileName), `{"type":"default"}`)

	dd, err := resolveDataDirAt(exe, defDir)
	if err != nil {
		t.Fatal(err)
	}
	if dd.Path != filepath.Join(exe, "data") || dd.Type != "portable" {
		t.Fatalf("portable pointer must win over the machine-shared user-config pointer: %+v", dd)
	}
}

func TestResolveDataDirUserConfigPointerForInstalls(t *testing.T) {
	exe := t.TempDir()
	defDir := t.TempDir()
	// No exe/data pointer — a regular install's exe dir is read-only anyway.
	writeRaw(t, filepath.Join(defDir, bootstrapFileName), `{"type":"default"}`)

	dd, err := resolveDataDirAt(exe, defDir)
	if err != nil {
		t.Fatal(err)
	}
	if dd.Path != defDir || dd.Type != "default" {
		t.Fatalf("without an exe/data pointer the user-config pointer must resolve: %+v", dd)
	}
}

func TestResolveDataDirMigratesLegacyNestedPointer(t *testing.T) {
	exe := t.TempDir()
	defDir := t.TempDir()
	// The double-joined pre-fix layout: bootstrap.json is a directory and the
	// pointer lives one level inside it.
	writeRaw(t, filepath.Join(defDir, bootstrapFileName, bootstrapFileName), `{"type":"default"}`)

	dd, err := resolveDataDirAt(exe, defDir)
	if err != nil {
		t.Fatal(err)
	}
	if dd.Path != defDir || dd.Type != "default" {
		t.Fatalf("legacy nested pointer must still resolve: %+v", dd)
	}
	// Migrated: correct-path file exists, nested layout is gone.
	if _, err := os.Stat(filepath.Join(defDir, bootstrapFileName)); err != nil {
		t.Fatalf("pointer must be rewritten to the correct path: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(defDir, bootstrapFileName, bootstrapFileName)); err == nil {
		t.Fatalf("nested leftover must be removed: %v", fi)
	}
}

func TestResolveDataDirUpgradeWithoutBootstrap(t *testing.T) {
	exe := t.TempDir()
	defDir := t.TempDir()
	writeRaw(t, filepath.Join(defDir, "connections.json"), `{}`)

	dd, err := resolveDataDirAt(exe, defDir)
	if err != nil {
		t.Fatal(err)
	}
	if !dd.Upgrade || dd.Path != defDir || dd.Type != "default" {
		t.Fatalf("existing config without bootstrap must upgrade: %+v", dd)
	}
}

func TestResolveDataDirFirstRun(t *testing.T) {
	dd, err := resolveDataDirAt(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !dd.FirstRun {
		t.Fatalf("empty machine must be first run: %+v", dd)
	}
}

func TestWriteBootstrapAtPlacementAndStaleRemoval(t *testing.T) {
	exe := t.TempDir()
	defDir := t.TempDir()

	// default → user-config pointer, exe/data pointer removed.
	if err := writeBootstrapAt(exe, defDir, "default", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(defDir, bootstrapFileName)); err != nil {
		t.Fatalf("default pointer must live in the user config dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exe, "data", bootstrapFileName)); !os.IsNotExist(err) {
		t.Fatalf("stale exe/data pointer must be removed: %v", err)
	}

	// portable → exe/data pointer, user-config pointer removed.
	if err := writeBootstrapAt(exe, defDir, "portable", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(exe, "data", bootstrapFileName)); err != nil {
		t.Fatalf("portable pointer must live with the exe: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defDir, bootstrapFileName)); !os.IsNotExist(err) {
		t.Fatalf("stale user-config pointer must be removed: %v", err)
	}
}

// TestWriteBootstrapAtClearsLegacyNestedDir: migrating away from a pre-fix
// build leaves bootstrap.json as a DIRECTORY with the pointer inside. The
// write must clear that layout (rename-over-directory fails) and place the
// pointer at the correct path.
func TestWriteBootstrapAtClearsLegacyNestedDir(t *testing.T) {
	exe := t.TempDir()
	defDir := t.TempDir()
	writeRaw(t, filepath.Join(defDir, bootstrapFileName, bootstrapFileName), `{"type":"default"}`)

	if err := writeBootstrapAt(exe, defDir, "portable", ""); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(exe, "data", bootstrapFileName))
	if err != nil || fi.IsDir() {
		t.Fatalf("portable pointer must be a regular file: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(defDir, bootstrapFileName)); !os.IsNotExist(err) {
		t.Fatalf("legacy nested layout must be cleared: %v", err)
	}
}
