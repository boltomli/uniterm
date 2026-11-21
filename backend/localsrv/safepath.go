package localsrv

import (
	"os"
	"path/filepath"
	"strings"
)

// lexicallyWithinRoot reports whether full is lexically contained in root.
// The root itself counts as contained. This is the first half of every
// containment check in this package; resolveWithinRoot adds the symlink-aware
// half.
func lexicallyWithinRoot(root, full string) bool {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveWithinRoot resolves symlinks (and Windows junctions) on the root and
// on the deepest existing ancestor of full, then re-checks containment
// against the resolved root. Lexical containment alone is not enough: a
// symlink inside a served directory points at arbitrary files outside it,
// and every os.Open/os.Create in the local services follows it.
//
// full need not exist (upload targets, new objects): symlinks are resolved for
// the deepest existing ancestor and the remaining segments are appended
// verbatim, so a dangling link or a new file is still evaluated against the
// directory it would land in.
//
// The returned path is the resolved one; callers should use it for the actual
// file operation so the check and the open refer to the same target.
func resolveWithinRoot(root, full string) (string, bool) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = filepath.Clean(root)
	}

	p := filepath.Clean(full)
	var suffix []string
	for {
		if _, lerr := os.Lstat(p); lerr == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		suffix = append([]string{filepath.Base(p)}, suffix...)
		p = parent
	}

	resolved := p
	if rp, rerr := filepath.EvalSymlinks(p); rerr == nil {
		resolved = rp
	}
	for _, seg := range suffix {
		resolved = filepath.Join(resolved, seg)
	}

	if !lexicallyWithinRoot(resolvedRoot, resolved) {
		return "", false
	}
	return resolved, true
}
