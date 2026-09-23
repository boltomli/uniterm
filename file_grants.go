package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// pathGrantSet records filesystem paths the user explicitly chose: through a
// native file dialog, an OS file drop, or the saved zmodem download
// directory. The raw file bindings (ReadFileBase64 and friends) refuse
// anything else, so a compromised webview cannot read or write arbitrary
// files — the dialogs and drops are the trust boundary. Containment under a
// granted directory is lexical, so a remote-controlled transfer filename
// containing "../" cannot escape the chosen folder.
type pathGrantSet struct {
	mu    sync.Mutex
	files map[string]struct{}
	dirs  map[string]struct{}
}

var fileGrants = pathGrantSet{
	files: map[string]struct{}{},
	dirs:  map[string]struct{}{},
}

// addFile grants exact file paths (dialog results, dropped files).
func (g *pathGrantSet) addFile(paths ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range paths {
		if p == "" {
			continue
		}
		g.files[filepath.Clean(p)] = struct{}{}
	}
}

// addDir grants every path lexically contained in dir (directory dialogs,
// the configured zmodem download directory).
func (g *pathGrantSet) addDir(dir string) {
	if dir == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.dirs[filepath.Clean(dir)] = struct{}{}
}

// allows reports whether path is granted: an exact file grant, or a path
// strictly inside a granted directory.
func (g *pathGrantSet) allows(path string) bool {
	clean := filepath.Clean(path)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.files[clean]; ok {
		return true
	}
	for dir := range g.dirs {
		rel, err := filepath.Rel(dir, clean)
		if err != nil {
			continue
		}
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

// requireFileGrant gates a raw file binding before it touches the disk.
func requireFileGrant(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if !fileGrants.allows(path) {
		return fmt.Errorf("path not selected by a file dialog: %s", path)
	}
	return nil
}

// rejectDotDotPath refuses any path containing a ".." segment. Transfer
// bindings that join a remote-controlled filename onto a chosen local
// directory (SftpGet) or a browsed pane directory cannot rely on grants
// alone — the crafted name itself must not walk out of its base directory.
func rejectDotDotPath(path string) error {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == ".." {
			return fmt.Errorf("path contains .. segment: %s", path)
		}
	}
	return nil
}
