package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// atomicWriteFile writes data to path via a temp file in the same directory,
// fsyncs the file, then renames over the destination. On POSIX this is
// atomic and survives process kill or power loss between calls without
// leaving a half-written file at the target path.
//
// Fixes: STORE-03, STORE-09, STORE-10, STORE-12, STORE-19, STORE-21 (refs in
// .planning/audit/phase-2/TRIAGE.md).
func atomicWriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return renameWithRetry(tmpName, path)
}

// Win32 codes for "the destination is momentarily busy". On Windows
// os.Rename is MoveFileEx(REPLACE_EXISTING), which fails while any other
// handle has the destination open without FILE_SHARE_DELETE — e.g. Load's
// deliberately lock-free os.ReadFile (F-109). POSIX rename is atomic and
// never reports these.
const (
	errorSharingViolation syscall.Errno = 32 // ERROR_SHARING_VIOLATION
	errorLockViolation    syscall.Errno = 33 // ERROR_LOCK_VIOLATION
	errorAccessDenied     syscall.Errno = 5  // ERROR_ACCESS_DENIED
)

// renameWithRetry replaces src with dst, retrying briefly while the
// destination is busy. The blocking reader holds its handle only for the
// duration of a read, so a short bounded retry (100ms worst case) covers the
// whole contention window without delaying a real failure meaningfully.
func renameWithRetry(src, dst string) error {
	var err error
	for range 50 {
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
		if !isTransientRenameErr(err) {
			return err
		}
		time.Sleep(2 * time.Millisecond)
	}
	return err
}

func isTransientRenameErr(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case errorSharingViolation, errorLockViolation, errorAccessDenied:
		return true
	}
	return false
}

// quarantineCorrupt renames a corrupt JSON file aside so the next Save
// can proceed without losing the user's prior data to a silent overwrite.
// Returns the new path, or empty string if the rename was unnecessary.
//
// Fixes: STORE-09 (silent unmarshal-failure data wipe in 4 stores).
func quarantineCorrupt(path string) string {
	ts := time.Now().UTC().Format("20060102T150405")
	target := path + ".corrupt-" + ts
	if err := renameWithRetry(path, target); err != nil {
		return ""
	}
	return target
}

// copyFileWithoutSymlinks copies src to dst without following symlinks.
// If src is itself a symlink, the copy is skipped (returns nil). Used by
// SkillsStore to prevent symlink-following deletions.
//
// Fixes: STORE-02.
func copyFileWithoutSymlinks(src, dst string) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 {
		// Refuse to copy symlinks to avoid traversing arbitrary paths.
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
