package main

// The recorder bindings hand a Go-derived path to the webview (RecorderStart)
// and take one back (RecorderGetContent / RecorderDelete). Before the grant
// gate the latter two were raw os.ReadFile / os.Remove on whatever the
// renderer supplied — a compromised renderer could read or delete any file
// the user can. They now go through requireFileGrant like every raw file
// binding; the only grants are the paths the Go side itself produced.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// isolateRecorderHome points os.UserHomeDir (and therefore the records dir)
// at a temp dir so the test never touches the real ~/Documents/uniTerm/records.
func isolateRecorderHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	return home
}

func TestRecorderGetContentAndDeleteRequireGrant(t *testing.T) {
	isolateRecorderHome(t)
	dir := t.TempDir()
	victim := filepath.Join(dir, "id_rsa")
	if err := os.WriteFile(victim, []byte("SECRET"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := &App{}

	// Ungranted path: refused before any disk access — the file must survive
	// both the read and the delete attempt untouched.
	if got, err := a.RecorderGetContent(victim); err == nil {
		t.Fatalf("RecorderGetContent accepted an ungranted path and returned %q", got)
	}
	if err := a.RecorderDelete(victim); err == nil {
		t.Fatal("RecorderDelete accepted an ungranted path")
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "SECRET" {
		t.Fatalf("ungranted path was disturbed by the recorder bindings: %q, %v", data, err)
	}

	// Once granted (the only producer of such grants is the Go recorder
	// itself: RecorderStart / RecorderList), the gate lets the call through.
	fileGrants.addFile(victim)
	got, err := a.RecorderGetContent(victim)
	if err != nil || got != "SECRET" {
		t.Fatalf("RecorderGetContent(granted) = %q, %v", got, err)
	}
	if err := a.RecorderDelete(victim); err != nil {
		t.Fatalf("RecorderDelete(granted): %v", err)
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Fatalf("granted delete did not remove the file: %v", err)
	}
}

func TestRecorderStartGrantsItsOwnFileAndListGrantsDir(t *testing.T) {
	isolateRecorderHome(t)
	a := &App{}

	path, err := a.RecorderStart("sess-grant-test", 80, 24, "grant test")
	if err != nil {
		t.Fatalf("RecorderStart: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("RecorderStart returned a relative path: %s", path)
	}
	// The path the Go side produced must be usable by the gated bindings
	// without any further user action — that is the whole point of granting
	// it at creation.
	if err := a.RecorderWriteData(path, "hello"); err != nil {
		t.Fatalf("RecorderWriteData(started path): %v", err)
	}
	if err := a.RecorderStop(path); err != nil {
		t.Fatalf("RecorderStop(started path): %v", err)
	}
	if got, err := a.RecorderGetContent(path); err != nil || len(got) == 0 {
		t.Fatalf("RecorderGetContent(started path) = %d bytes, %v", len(got), err)
	}

	// A sibling recording that predates this process (no RecorderStart grant)
	// is only reachable through RecorderList, which grants what it returns.
	sibling := filepath.Join(filepath.Dir(path), "20990102T150405-grant-test-sess.cast")
	if err := os.WriteFile(sibling, []byte("{\"version\":2}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile sibling: %v", err)
	}
	// Not yet listed → still ungranted.
	if _, err := a.RecorderGetContent(sibling); err == nil {
		t.Fatal("unlisted sibling was readable before RecorderList granted it")
	}
	list, err := a.RecorderList()
	if err != nil {
		t.Fatalf("RecorderList: %v", err)
	}
	found := false
	for _, info := range list {
		if info.FilePath == sibling {
			found = true
		}
	}
	if !found {
		t.Fatalf("RecorderList did not return the sibling recording (%d entries)", len(list))
	}
	if got, err := a.RecorderGetContent(sibling); err != nil || got != "{\"version\":2}\n" {
		t.Fatalf("RecorderGetContent(listed sibling) = %q, %v", got, err)
	}
	if err := a.RecorderDelete(sibling); err != nil {
		t.Fatalf("RecorderDelete(listed sibling): %v", err)
	}
	if err := a.RecorderDelete(path); err != nil {
		t.Fatalf("RecorderDelete(started path): %v", err)
	}
}
