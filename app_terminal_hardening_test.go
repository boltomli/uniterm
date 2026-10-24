package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

// Regression (security.md low): WriteFileBase64 used os.WriteFile, which
// follows a symlink swapped in after the grant check — AppendFileBase64
// already refuses symlinks through os.Root. Both writes must refuse.
func TestWriteFileBase64RejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows developer mode")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "edit")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fileGrants.addFile(link)

	err := (&App{}).WriteFileBase64(link, base64.StdEncoding.EncodeToString([]byte("overwrite")))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("WriteFileBase64 symlink error = %v, want refusal", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "safe" {
		t.Fatalf("symlink target changed to %q", got)
	}
}

// Regression (security.md low): the write had no size bound — the whole
// payload crosses the IPC bridge as one string and is decoded into memory.
// Payloads over the cap must be rejected before anything is decoded or
// created; payloads under it still write normally.
func TestWriteFileBase64RejectsOversizePayload(t *testing.T) {
	prev := maxWriteFileBase64Bytes
	maxWriteFileBase64Bytes = 16
	t.Cleanup(func() { maxWriteFileBase64Bytes = prev })

	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	fileGrants.addFile(path)

	oversize := base64.StdEncoding.EncodeToString(make([]byte, 17))
	if err := (&App{}).WriteFileBase64(path, oversize); err == nil {
		t.Fatal("WriteFileBase64 accepted a payload over the size cap")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversize payload must not create the file, stat error = %v", err)
	}

	if err := (&App{}).WriteFileBase64(path, base64.StdEncoding.EncodeToString([]byte("ok"))); err != nil {
		t.Fatalf("in-cap write failed: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "ok" {
		t.Fatalf("in-cap write produced %q (err %v)", got, err)
	}
}

// S-01: ReadFileBase64 read the whole file with no bound — a compromised
// renderer could ask for a multi-GB allocation in one call. Files over the
// cap must be refused before os.ReadFile allocates; in-cap files read
// normally.
func TestReadFileBase64RejectsOversizeFile(t *testing.T) {
	prev := maxReadFileBase64Bytes
	maxReadFileBase64Bytes = 16
	t.Cleanup(func() { maxReadFileBase64Bytes = prev })

	dir := t.TempDir()
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, make([]byte, 17), 0600); err != nil {
		t.Fatal(err)
	}
	fileGrants.addFile(big)
	if _, err := (&App{}).ReadFileBase64(big); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ReadFileBase64 read an oversize file: %v", err)
	}

	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	fileGrants.addFile(small)
	got, err := (&App{}).ReadFileBase64(small)
	if err != nil || got != base64.StdEncoding.EncodeToString([]byte("ok")) {
		t.Fatalf("in-cap read = %q (err %v)", got, err)
	}
}

// S-01: ReadFileChunkBase64's length came straight from the renderer and
// was only clamped by the file's remaining size — over-limit requests
// allocate make([]byte, length). Clamp to the chunk cap instead: chunked
// callers resume by offset, so a partial chunk cannot lose data.
func TestReadFileChunkBase64ClampsLength(t *testing.T) {
	prev := maxReadFileChunkBytes
	maxReadFileChunkBytes = 8
	t.Cleanup(func() { maxReadFileChunkBytes = prev })

	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(path, make([]byte, 100), 0600); err != nil {
		t.Fatal(err)
	}
	fileGrants.addFile(path)

	got, err := (&App{}).ReadFileChunkBase64(path, 0, 1<<30)
	if err != nil {
		t.Fatalf("clamped chunk read failed: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 8 {
		t.Fatalf("chunk length = %d, want the clamped 8", len(raw))
	}

	// Resuming from the clamp boundary keeps working.
	got, err = (&App{}).ReadFileChunkBase64(path, 8, 1<<30)
	if err != nil {
		t.Fatalf("resumed chunk read failed: %v", err)
	}
	if raw, _ = base64.StdEncoding.DecodeString(got); len(raw) != 8 {
		t.Fatalf("resumed chunk length = %d, want 8", len(raw))
	}
}

// S-02: AppendFileBase64 decoded an unbounded payload before anything
// checked its size — inconsistent with WriteFileBase64's cap. The pre-check
// must reject before the decode allocates, and before the file is created.
func TestAppendFileBase64RejectsOversizePayload(t *testing.T) {
	prev := maxWriteFileBase64Bytes
	maxWriteFileBase64Bytes = 16
	t.Cleanup(func() { maxWriteFileBase64Bytes = prev })

	dir := t.TempDir()
	path := filepath.Join(dir, "append.txt")
	fileGrants.addFile(path)

	oversize := base64.StdEncoding.EncodeToString(make([]byte, 17))
	if err := (&App{}).AppendFileBase64(path, oversize, 0); err == nil {
		t.Fatal("AppendFileBase64 accepted a payload over the size cap")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversize payload must not create the file, stat error = %v", err)
	}

	if err := (&App{}).AppendFileBase64(path, base64.StdEncoding.EncodeToString([]byte("ok")), 0); err != nil {
		t.Fatalf("in-cap append failed: %v", err)
	}
}

// Regression (security.md low): panelAutoTriggered and panelLogs kept an
// entry for every panel ever opened. ReleasePanel drops both when a tab
// closes (and closes the panel's log so buffered output + footer land).
func TestReleasePanelDropsPanelState(t *testing.T) {
	a := &App{
		panelLogs:          make(map[string]*session.OutputLogger),
		sessionToPanel:     make(map[string]string),
		panelAutoTriggered: make(map[string]bool),
	}
	logger := &session.OutputLogger{}
	if _, err := logger.Enable(t.TempDir(), "", "panel", "", "session"); err != nil {
		t.Fatalf("enable panel log: %v", err)
	}
	other := &session.OutputLogger{}
	if _, err := other.Enable(t.TempDir(), "", "panel", "", "session"); err != nil {
		t.Fatalf("enable other panel log: %v", err)
	}
	// The assertions below need both logs open; close them afterwards so the
	// temp dirs can be removed.
	t.Cleanup(func() {
		logger.Disable()
		other.Disable()
	})
	a.panelLogs["p1"] = logger
	a.panelLogs["p2"] = other
	a.panelAutoTriggered["p1"] = true
	a.panelAutoTriggered["p2"] = true

	a.ReleasePanel("p1")

	if _, ok := a.panelLogs["p1"]; ok {
		t.Fatal("panelLogs entry survived ReleasePanel")
	}
	if a.panelAutoTriggered["p1"] {
		t.Fatal("panelAutoTriggered entry survived ReleasePanel")
	}
	if logger.Enabled() {
		t.Fatal("panel log still enabled after ReleasePanel")
	}
	if _, ok := a.panelLogs["p2"]; !ok || !a.panelAutoTriggered["p2"] {
		t.Fatal("ReleasePanel must only drop the released panel")
	}
	if !other.Enabled() {
		t.Fatal("another panel's log was closed by ReleasePanel")
	}

	// Empty ids are ignored, not fatal.
	a.ReleasePanel("")
}
