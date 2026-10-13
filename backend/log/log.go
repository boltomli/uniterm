// Package log writes the application log (backend + frontend messages).
//
// Writes are buffered and pushed to the file on a periodic flush instead of
// fsyncing every line under the package mutex: hot paths (CreateSession,
// FrontendLog, autotest) log from many goroutines, and one slow disk used to
// stall all of them. The file rotates by size with a bounded number of
// archives, and the directory/file are private to the user.
package log

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// flushInterval is how often buffered lines are pushed to the file.
	// Close (and Shutdown) flush synchronously, so at most this much output
	// can be in memory at any time.
	flushInterval = 1 * time.Second
	// keepLogFiles is the number of rotated archives retained
	// (uniterm.log.1 … uniterm.log.N) beside the active log.
	keepLogFiles = 5
	// logBufferBytes is the size of the write buffer.
	logBufferBytes = 64 * 1024
)

// maxLogFileSize is the size at which the active log is rotated. A var so
// tests can roll the file over without writing megabytes.
var maxLogFileSize int64 = 4 << 20 // 4 MiB

var (
	mu   sync.Mutex
	file *os.File
	buf  *bufio.Writer
	// written counts bytes appended since the file was opened, so rotation
	// also accounts for a log left large by a previous run.
	written int64
	dirty   bool
	// stopCh/doneCh drive the periodic flusher goroutine (nil when idle).
	stopCh chan struct{}
	doneCh chan struct{}
)

// logPathFn is where the app log lives. It is a variable so tests can point
// the package at a private temp file; production always resolves the
// build-tagged logPath.
var logPathFn = logPath

func Init() error {
	mu.Lock()
	defer mu.Unlock()

	if file != nil {
		return nil
	}
	if err := initLocked(); err != nil {
		return err
	}
	startFlusherLocked()
	return nil
}

// Flush pushes buffered lines to the file (and therefore to the OS). Safe to
// call from any goroutine; Shutdown calls it so no line is lost on quit.
func Flush() {
	mu.Lock()
	defer mu.Unlock()
	flushLocked()
}

// Close flushes, syncs and closes the log. Subsequent writes reopen the file
// lazily, so calling Close early (panic path, shutdown) is safe.
func Close() {
	mu.Lock()
	stop, done := stopCh, doneCh
	stopCh, doneCh = nil, nil
	if stop != nil {
		close(stop)
	}
	mu.Unlock()
	if done != nil {
		<-done
	}

	mu.Lock()
	defer mu.Unlock()
	flushLocked()
	if file != nil {
		_ = file.Sync()
		_ = file.Close()
		file = nil
	}
	buf = nil
	dirty = false
	written = 0
}

func Writef(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05.000"), msg)

	mu.Lock()
	defer mu.Unlock()

	// Init ran before the wails Android bridge attached, so its logPath
	// (/data/local/tmp) was unwritable and file stayed nil. Retry lazily:
	// once TMPDIR is pinned to the app sandbox (see utils.EnsureTempDir)
	// the next write opens the file and the log comes alive.
	if file == nil {
		if initLocked() != nil {
			return
		}
		startFlusherLocked()
	}

	if _, err := buf.WriteString(line); err != nil {
		return
	}
	written += int64(len(line))
	dirty = true
	if written >= maxLogFileSize {
		rotateLocked()
	}
}

// flushLocked writes buffered bytes to the file; callers must hold mu.
func flushLocked() {
	if !dirty || buf == nil {
		return
	}
	if err := buf.Flush(); err != nil {
		return
	}
	dirty = false
}

// startFlusherLocked starts the periodic flusher; callers must hold mu.
func startFlusherLocked() {
	if doneCh != nil {
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	stopCh, doneCh = stop, done
	go func() {
		defer close(done)
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				Flush()
			}
		}
	}()
}

// rotateLocked rolls the active log over to <path>.1, shifting the older
// archives up and dropping the one beyond keepLogFiles, then reopens a fresh
// log. Callers must hold mu.
func rotateLocked() {
	flushLocked()
	base := logPathFn()
	if file != nil {
		_ = file.Sync()
		_ = file.Close()
		file = nil
	}
	buf = nil
	dirty = false
	written = 0

	for i := keepLogFiles - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", base, i)
		dst := fmt.Sprintf("%s.%d", base, i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	if err := os.Rename(base, base+".1"); err != nil && !os.IsNotExist(err) {
		// Another process (viewer/editor) holds the file: keep appending to
		// it instead of failing — and retrying — on every following write.
		fmt.Fprintf(os.Stderr, "log: rotate %s: %v\n", base, err)
		_ = initLocked()
		written = 0
		startFlusherLocked()
		return
	}
	// A failure here leaves file == nil; the next Writef retries.
	_ = initLocked()
	startFlusherLocked()
}

// initLocked opens the log file; callers must hold mu.
func initLocked() error {
	path := logPathFn()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Tighten a directory created by older builds (0755). Never the system
	// temp dir itself: on Android the log lives directly under it.
	if dir != os.TempDir() {
		_ = os.Chmod(dir, 0700)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	// Pre-existing files were created 0644 by older builds.
	_ = f.Chmod(0600)
	if st, err := f.Stat(); err == nil {
		written = st.Size()
	} else {
		written = 0
	}
	file = f
	buf = bufio.NewWriterSize(f, logBufferBytes)
	dirty = false
	return nil
}
