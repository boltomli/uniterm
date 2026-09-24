package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/store"
)

func TestAppendFileBase64RejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows developer mode")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "download")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fileGrants.addFile(link)

	err := (&App{}).AppendFileBase64(link, base64.StdEncoding.EncodeToString([]byte("overwrite")), 0)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("AppendFileBase64 symlink error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "safe" {
		t.Fatalf("symlink target changed to %q", got)
	}
}

// TestRawFileBindingsRequireGrant guards the confinement of the raw file
// bindings: without a dialog/drop grant they must refuse both reads and
// writes, and a grant on a directory must not allow escaping it with "..".
func TestRawFileBindingsRequireGrant(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "picked.txt")
	if err := os.WriteFile(inside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("top"), 0600); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	// Ungranted absolute paths — the pre-fix behavior — must fail.
	if _, err := app.ReadFileBase64(outside); err == nil {
		t.Fatal("ReadFileBase64 accepted an ungranted path")
	}
	if _, err := app.ReadFileChunkBase64(outside, 0, 1); err == nil {
		t.Fatal("ReadFileChunkBase64 accepted an ungranted path")
	}
	if _, err := app.FileSize(outside); err == nil {
		t.Fatal("FileSize accepted an ungranted path")
	}
	if err := app.WriteFileBase64(outside, "bmV3"); err == nil {
		t.Fatal("WriteFileBase64 accepted an ungranted path")
	}
	if err := app.AppendFileBase64(outside, "bmV3", 0); err == nil {
		t.Fatal("AppendFileBase64 accepted an ungranted path")
	}

	// A granted directory allows its children but blocks ".." escapes.
	fileGrants.addDir(dir)
	if _, err := app.ReadFileBase64(inside); err != nil {
		t.Fatalf("ReadFileBase64 inside granted dir: %v", err)
	}
	escaped := filepath.Join(dir, "..", "esc")
	if err := app.WriteFileBase64(escaped, "bmV3"); err == nil {
		t.Fatal("WriteFileBase64 escaped a granted directory with ..")
	}
	if _, err := app.ReadFileBase64(filepath.Join(outside, "..", filepath.Base(outside))); err == nil {
		t.Fatal("ReadFileBase64 read an ungranted path via ..")
	}

	// The other webview-facing file bindings refuse ungranted paths too.
	if err := app.ExportConnections(outside, ""); err == nil {
		t.Fatal("ExportConnections accepted an ungranted path")
	}
	if _, err := app.SetBackgroundImage(outside); err == nil {
		t.Fatal("SetBackgroundImage accepted an ungranted path")
	}
	if _, err := app.ParseImportFile("uniterm", outside, ""); err == nil ||
		!strings.Contains(err.Error(), "not selected by a file dialog") {
		t.Fatalf("ParseImportFile ungranted error = %v", err)
	}
}

// TestSaveSettingsDoesNotGrantWebviewDir pins the SaveSettings grant bypass:
// the zmodem download directory arrives through a raw webview binding, so
// saving settings must not add it to the grant set — otherwise a compromised
// webview obtains any directory in one call. Only the dialog picker and
// LoadSettings (value read back from disk) grant it.
func TestSaveSettingsDoesNotGrantWebviewDir(t *testing.T) {
	ss, err := store.NewSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &App{settingsStore: ss}

	dir := filepath.Join(t.TempDir(), "zmodem")
	settings := store.AppSettings{}
	settings.Terminal.ZmodemDownloadDir = dir
	if err := app.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if err := requireFileGrant(filepath.Join(dir, "file")); err == nil {
		t.Fatal("SaveSettings granted a webview-supplied directory")
	}

	if _, err := app.LoadSettings(); err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if err := requireFileGrant(filepath.Join(dir, "file")); err != nil {
		t.Fatalf("LoadSettings did not grant the persisted dir: %v", err)
	}
}

// TestSftpTransfersRejectDotDot guards the remote-controlled-filename hole:
// a hostile server name like "../../x" joined onto the chosen local directory
// must not escape it (grants cannot catch this — the pane paths are browsed,
// not dialog-granted).
func TestSftpTransfersRejectDotDot(t *testing.T) {
	app := &App{}
	// Concatenated, not filepath.Join — Join would clean the ".." away.
	evil := t.TempDir() + string(filepath.Separator) + ".." + string(filepath.Separator) + "escape.bin"
	if _, err := app.SftpGet("missing-session", "/remote/f", evil, false); err == nil ||
		!strings.Contains(err.Error(), ".. segment") {
		t.Fatalf("SftpGet dotdot error = %v", err)
	}
	if _, err := app.SftpPut("missing-session", evil, "/remote/f", false); err == nil ||
		!strings.Contains(err.Error(), ".. segment") {
		t.Fatalf("SftpPut dotdot error = %v", err)
	}
}
