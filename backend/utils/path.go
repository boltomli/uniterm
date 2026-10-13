package utils

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsafePath reports a path that could escape its intended base directory.
var ErrUnsafePath = errors.New("unsafe path")

// ExpandHomePath resolves a leading "~" to the user's home directory so paths
// like "~/.ssh/id_ed25519" (as written in OpenSSH config IdentityFile lines
// and other third-party sources) can be handed to os.ReadFile as-is. Paths
// without a "~" prefix are returned unchanged.
func ExpandHomePath(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// SafeLocalJoin joins a caller-controlled relative path rel under root and
// verifies the result stays inside root. It rejects empty rel, NUL bytes,
// absolute paths, drive-letter/UNC prefixes, and any ".." element — checked
// against both separator styles so a Windows-style name can't slip through
// on Unix or vice versa. The final path is double-checked with
// filepath.Rel/IsLocal, which also rejects Windows reserved device names.
//
// Every download/unpack/sync writer that joins a remote-controlled file name
// onto a local directory must go through this helper (security.md P0 path
// traversal findings). Lexical containment only: callers that need to defeat
// symlink swaps should additionally open through os.Root.
func SafeLocalJoin(root, rel string) (string, error) {
	if root == "" {
		return "", errors.New("SafeLocalJoin: empty root")
	}
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", ErrUnsafePath
	}
	if !filepath.IsLocal(rel) {
		return "", ErrUnsafePath
	}
	// filepath.IsLocal already rejects ".." elements, absolute paths and
	// reserved names for the host OS. Cross-check the other separator style
	// and volume prefixes so behaviour is identical on Windows and Unix.
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", ErrUnsafePath
	}
	if vol := filepath.VolumeName(rel); vol != "" {
		return "", ErrUnsafePath
	}
	if len(rel) >= 2 && rel[1] == ':' && isASCIILetter(rel[0]) {
		return "", ErrUnsafePath // drive-relative ("c:foo") or drive-absolute
	}
	for _, elem := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if elem == ".." {
			return "", ErrUnsafePath
		}
	}
	joined := filepath.Join(root, rel)
	relOut, err := filepath.Rel(root, joined)
	if err != nil || relOut == ".." || strings.HasPrefix(relOut, ".."+string(filepath.Separator)) {
		return "", ErrUnsafePath
	}
	return joined, nil
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// SafeRemoteName reports whether name is usable as a single path component
// supplied by a remote server or sync peer: non-empty, not "." or "..",
// containing no path separator in either style, no NUL and no drive prefix.
func SafeRemoteName(name string) error {
	if name == "" || name == "." || name == ".." {
		return ErrUnsafePath
	}
	if strings.ContainsRune(name, 0) {
		return ErrUnsafePath
	}
	if strings.ContainsAny(name, `/\`) {
		return ErrUnsafePath
	}
	if len(name) >= 2 && name[1] == ':' {
		return ErrUnsafePath
	}
	return nil
}
