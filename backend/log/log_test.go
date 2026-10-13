package log

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Buffered lines must reach the file on Flush, not just on Close.
func TestFlushPersistsBufferedLines(t *testing.T) {
	path := useTempLog(t)
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	Writef("pending-line-%s", "alpha")
	Flush()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !bytes.Contains(got, []byte("pending-line-alpha")) {
		t.Fatalf("Flush did not persist the buffered line; log holds %q", got)
	}
}

// Close is the process-exit path (main defers it): nothing written before it
// may be lost.
func TestCloseFlushesBufferedLines(t *testing.T) {
	path := useTempLog(t)
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	Writef("closing-line-%s", "alpha")
	Close()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !bytes.Contains(got, []byte("closing-line-alpha")) {
		t.Fatalf("Close dropped the buffered line; log holds %q", got)
	}
}

// Regression (security.md medium): the app log never rotated, so a long-lived
// session grew it without bound (and FrontendLog fed it from the renderer).
// Crossing the size threshold must roll the file over and keep a bounded
// number of archives.
func TestLogRotatesBySizeAndKeepsArchives(t *testing.T) {
	path := useTempLog(t)
	prev := maxLogFileSize
	maxLogFileSize = 512
	t.Cleanup(func() { maxLogFileSize = prev })

	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	payload := strings.Repeat("x", 80)
	for i := range 20 {
		Writef("line-%02d %s", i, payload)
	}
	// A line written after the last roll-over must land in the active file.
	Writef("final-marker %s", payload)
	Flush()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("current log missing after rotation: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated archive missing after rotation: %v", err)
	}
	if _, err := os.Stat(path + ".6"); err == nil {
		t.Fatalf("archive .%d exists: more than keepLogFiles archives retained", keepLogFiles+1)
	}
	matches, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) > keepLogFiles+1 {
		t.Fatalf("%d log files retained, want at most %d (current + %d archives)", len(matches), keepLogFiles+1, keepLogFiles)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current log: %v", err)
	}
	if !bytes.Contains(got, []byte("final-marker")) {
		t.Fatalf("newest line missing from active log; holds %q", got)
	}
}

// Writers and the periodic flusher race by design; -race must stay clean.
func TestConcurrentWritefAndFlushAreRaceSafe(t *testing.T) {
	path := useTempLog(t)
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 50 {
				Writef("worker %d line %d", i, j)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			Flush()
		}
	}()
	wg.Wait()
	Flush()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !bytes.Contains(got, []byte("worker 7 line 49")) {
		t.Fatalf("concurrent writes lost; log holds %d bytes", len(got))
	}
}
