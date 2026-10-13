package sync

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// renameFile is the rename primitive used by atomicWriteFile. It is a
// variable so tests can inject a failure before the rename lands and prove
// the destination file is left untouched.
var renameFile = os.Rename

// atomicWriteFile writes data to path via a temp file in the same directory
// followed by a rename. A crash or failure mid-write can then never truncate
// the destination (STORE-03 semantics, package-local copy: backend/store's
// helper is unexported and sync must not import store).
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
// handle has the destination open without FILE_SHARE_DELETE — e.g. a
// store Load's deliberately lock-free os.ReadFile. POSIX rename is atomic
// and never reports these.
const (
	errorSharingViolation syscall.Errno = 32 // ERROR_SHARING_VIOLATION
	errorLockViolation    syscall.Errno = 33 // ERROR_LOCK_VIOLATION
	errorAccessDenied     syscall.Errno = 5  // ERROR_ACCESS_DENIED
)

// renameWithRetry replaces src with dst, retrying briefly while the
// destination is busy. The blocking reader holds its handle only for the
// duration of a read, so a short bounded retry (100ms worst case) covers
// the whole contention window without delaying a real failure meaningfully.
func renameWithRetry(src, dst string) error {
	var err error
	for range 50 {
		if err = renameFile(src, dst); err == nil {
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
	if errors.As(err, &errno) {
		switch errno {
		case errorSharingViolation, errorLockViolation, errorAccessDenied:
			return true
		}
	}
	return false
}
