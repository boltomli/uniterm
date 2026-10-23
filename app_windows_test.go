//go:build windows
// +build windows

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// detectThirdPartyShells must offer well-known third-party shell installs
// (Cygwin, MSYS2, Nushell) alongside the built-in shells, using only paths
// that actually exist on the host.
func TestDetectThirdPartyShells(t *testing.T) {
	existing := func(paths ...string) func(string) bool {
		set := make(map[string]bool)
		for _, p := range paths {
			set[strings.ToLower(p)] = true
		}
		return func(p string) bool { return set[strings.ToLower(p)] }
	}

	t.Run("cygwin fixed path", func(t *testing.T) {
		got := detectThirdPartyShells("",
			existing(`C:\cygwin64\bin\bash.exe`),
			func(string) (string, error) { return "", nil })
		want := []string{`C:\cygwin64\bin\bash.exe`}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("cygwin registry rootdir", func(t *testing.T) {
		got := detectThirdPartyShells(`D:\cygwin`,
			existing(`D:\cygwin\bin\bash.exe`),
			func(string) (string, error) { return "", nil })
		if len(got) != 1 || got[0] != `D:\cygwin\bin\bash.exe` {
			t.Errorf("got %v, want [D:\\cygwin\\bin\\bash.exe]", got)
		}
	})

	t.Run("registry rootdir that does not exist is skipped", func(t *testing.T) {
		got := detectThirdPartyShells(`D:\gone`,
			existing(`C:\cygwin64\bin\bash.exe`),
			func(string) (string, error) { return "", nil })
		for _, p := range got {
			if strings.Contains(strings.ToLower(p), "d:\\gone") {
				t.Errorf("nonexistent registry rootdir leaked into %v", got)
			}
		}
		if len(got) != 1 || got[0] != `C:\cygwin64\bin\bash.exe` {
			t.Errorf("got %v, want [C:\\cygwin64\\bin\\bash.exe]", got)
		}
	})

	t.Run("msys2 usr bin bash", func(t *testing.T) {
		got := detectThirdPartyShells("",
			existing(`C:\msys64\usr\bin\bash.exe`),
			func(string) (string, error) { return "", nil })
		if len(got) != 1 || got[0] != `C:\msys64\usr\bin\bash.exe` {
			t.Errorf("got %v, want [C:\\msys64\\usr\\bin\\bash.exe]", got)
		}
	})

	t.Run("nushell via PATH", func(t *testing.T) {
		got := detectThirdPartyShells("", func(string) bool { return false },
			func(name string) (string, error) {
				if name == "nu.exe" {
					return `C:\Users\me\scoop\shims\nu.exe`, nil
				}
				return "", nil
			})
		if len(got) != 1 || got[0] != `C:\Users\me\scoop\shims\nu.exe` {
			t.Errorf("got %v, want [nu.exe resolved path]", got)
		}
	})

	t.Run("nothing installed yields empty", func(t *testing.T) {
		got := detectThirdPartyShells("", func(string) bool { return false },
			func(string) (string, error) { return "", nil })
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("duplicates are removed", func(t *testing.T) {
		// Registry rootdir pointing at the same cygwin64 install as the fixed
		// path candidate must not yield the shell twice.
		got := detectThirdPartyShells(`C:\cygwin64`,
			existing(`C:\cygwin64\bin\bash.exe`),
			func(string) (string, error) { return "", nil })
		if len(got) != 1 {
			t.Errorf("got %v, want single entry", got)
		}
	})
}

func TestClipboardReadFileChunkRanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte{0, 1, 2, 3, 255}, 0600); err != nil {
		t.Fatal(err)
	}
	encoded, err := readClipboardFileChunk(path, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := base64.StdEncoding.DecodeString(encoded)
	if string(data) != string([]byte{2, 3, 255}) {
		t.Fatalf("unexpected bytes: %v", data)
	}
	for _, bounds := range [][2]int64{{-1, 1}, {0, -1}, {0, 8*1024*1024 + 1}} {
		if _, err := readClipboardFileChunk(path, bounds[0], int(bounds[1])); err == nil {
			t.Fatalf("accepted invalid range %v", bounds)
		}
	}
}

func TestClipboardGetFilesLive(t *testing.T) {
	if os.Getenv("UNITERM_CLIPBOARD_LIVE_TEST") != "1" {
		t.Skip("UNITERM_CLIPBOARD_LIVE_TEST not enabled")
	}
	a := &App{}
	files := a.ClipboardGetFiles()
	if len(files) == 0 {
		t.Skip("no files on the clipboard; copy a file first to run this test")
	}
	t.Logf("got %d files", len(files))
	for _, f := range files {
		t.Logf("file: %+v", f)
		if f.Size > 0 {
			b64, err := a.ClipboardReadFileChunk(0, 0, 16)
			t.Logf("chunk: %d bytes err=%v", len(b64), err)
		}
	}
}
