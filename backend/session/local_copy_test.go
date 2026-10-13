package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// localCopyWalkers are the two local recursive copy paths security.md
// flags ("Local recursive copy can loop forever"): the file-browser local
// copy (localFSOps.LocalCopy → localCopyRecursive) and the WSL Copy/Move and
// Get/Put walker (copyPath). Both must reject a destination inside the
// source and must never follow symlinks while recursing.
var localCopyWalkers = []struct {
	name string
	fn   func(src, dst string) error
}{
	{"localCopyRecursive", func(src, dst string) error { return localCopyRecursive(src, dst) }},
	{"copyPath", func(src, dst string) error { return copyPath(src, dst, nil) }},
}

// copyWithTimeout runs fn and fails the test when it does not return in
// time. The pre-fix walkers recurse into their own output until the path
// limit is hit; a regression must surface as a test failure, not a hang.
func copyWithTimeout(t *testing.T, d time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("local copy did not finish within %s — infinite recursion?", d)
		return nil
	}
}

// TestLocalCopyRejectsDestinationInsideSource pins the up-front guard: a
// destination that is inside the source (or identical to it) must be refused
// before anything is created. Otherwise the walker MkdirAll's the
// destination, lists the source — which now contains it — and recurses into
// its own output forever (security.md 文件会话 Low).
func TestLocalCopyRejectsDestinationInsideSource(t *testing.T) {
	for _, w := range localCopyWalkers {
		w := w
		t.Run(w.name+" dst inside src", func(t *testing.T) {
			src := t.TempDir()
			// A FILE at the destination keeps the pre-fix code from starting
			// the runaway walk: it dies in MkdirAll with an unrelated error,
			// which is exactly the red this test must show first.
			dst := filepath.Join(src, "occupied")
			if err := os.WriteFile(dst, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := w.fn(src, dst)
			if !errors.Is(err, errLocalCopyDstInSrc) {
				t.Fatalf("err = %v, want errLocalCopyDstInSrc", err)
			}
		})

		t.Run(w.name+" dst == src", func(t *testing.T) {
			src := t.TempDir()
			seed := filepath.Join(src, "keep.txt")
			if err := os.WriteFile(seed, []byte("keep"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := w.fn(src, src)
			if !errors.Is(err, errLocalCopyDstInSrc) {
				t.Fatalf("err = %v, want errLocalCopyDstInSrc", err)
			}
			// Self-copy must not damage the source (the pre-fix file branch
			// opens the file for reading and truncates it to zero bytes).
			got, rerr := os.ReadFile(seed)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(got) != "keep" {
				t.Fatalf("source content = %q, want keep", got)
			}
		})
	}
}

// TestLocalCopyCopiesSymlinksAsLinks pins the no-follow rule: link entries
// are recreated as links instead of being dereferenced. Following them is
// what lets a copy walk into a symlink loop (or into a tree that contains
// one) until the path limit is reached (security.md 文件会话 Low).
func TestLocalCopyCopiesSymlinksAsLinks(t *testing.T) {
	requireSymlinks(t)
	for _, w := range localCopyWalkers {
		w := w
		t.Run(w.name, func(t *testing.T) {
			src := t.TempDir()
			dst := filepath.Join(t.TempDir(), "dst")
			if err := os.WriteFile(filepath.Join(src, "target.txt"), []byte("payload"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(src, "target.txt"), filepath.Join(src, "l")); err != nil {
				t.Fatal(err)
			}
			// Mutual symlink loop: a → b → a. Following it fails with ELOOP,
			// so the pre-fix Stat-based walker aborts the whole copy.
			if err := os.Symlink(filepath.Join(src, "b"), filepath.Join(src, "a")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(src, "a"), filepath.Join(src, "b")); err != nil {
				t.Fatal(err)
			}

			if err := copyWithTimeout(t, 30*time.Second, func() error { return w.fn(src, dst) }); err != nil {
				t.Fatalf("copy: %v", err)
			}

			for _, name := range []string{"l", "a", "b"} {
				fi, lerr := os.Lstat(filepath.Join(dst, name))
				if lerr != nil {
					t.Fatalf("Lstat(%s): %v", name, lerr)
				}
				if fi.Mode()&os.ModeSymlink == 0 {
					t.Errorf("%s was dereferenced (mode %v, want symlink)", name, fi.Mode())
				}
			}
			got, rerr := os.ReadFile(filepath.Join(dst, "target.txt"))
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(got) != "payload" {
				t.Fatalf("target.txt = %q, want payload", got)
			}
		})
	}
}

// TestLocalCopySelfSymlinkTerminates covers the shape that recurses forever
// in the pre-fix walkers: a symlink inside the source pointing at the source
// itself. The copy must finish (the link is replicated, never walked into)
// instead of growing dst/loop/loop/loop… until ENAMETOOLONG.
func TestLocalCopySelfSymlinkTerminates(t *testing.T) {
	requireSymlinks(t)
	for _, w := range localCopyWalkers {
		w := w
		t.Run(w.name, func(t *testing.T) {
			src := t.TempDir()
			dst := filepath.Join(t.TempDir(), "dst")
			if err := os.Symlink(src, filepath.Join(src, "loop")); err != nil {
				t.Fatal(err)
			}
			if err := copyWithTimeout(t, 30*time.Second, func() error { return w.fn(src, dst) }); err != nil {
				t.Fatalf("copy: %v", err)
			}
			fi, lerr := os.Lstat(filepath.Join(dst, "loop"))
			if lerr != nil {
				t.Fatalf("Lstat(loop): %v", lerr)
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("loop was dereferenced (mode %v, want symlink)", fi.Mode())
			}
		})
	}
}
