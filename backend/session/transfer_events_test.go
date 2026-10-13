package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEmitTransferEventsUseSink(t *testing.T) {
	var got []map[string]any
	TransferEventSink = func(sid string, payload map[string]any) {
		if sid != "s1" {
			t.Fatalf("sessionId = %q, want s1", sid)
		}
		got = append(got, payload)
	}
	defer func() { TransferEventSink = nil }()

	s := &baseSession{id: "s1", sessionType: "sftp", status: StatusConnected}
	task := &TransferTask{ID: "dl-1", Type: "download", RemotePath: "/a/b.txt", LocalPath: `C:\x\b.txt`, Status: "running", Total: 10}
	s.emitTransferStart(task)
	s.emitTransferProgress(task)
	task.Status = "done"
	s.emitTransferComplete(task)

	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	start := got[0]
	if start["event"] != "start" || start["taskId"] != "dl-1" || start["sessionId"] != "s1" || start["name"] != "b.txt" {
		t.Fatalf("bad start payload: %v", start)
	}
	if _, ok := start["sessionId"]; !ok {
		t.Fatal("payload must carry sessionId")
	}
	if !strings.Contains(start["type"].(string), "sftp:transfer") {
		t.Fatalf("payload must carry type sftp:transfer, got %v", start["type"])
	}
	complete := got[2]
	if complete["status"] != "done" {
		t.Fatalf("bad complete payload: %v", complete)
	}
}

func TestProgressThrottle(t *testing.T) {
	var n int
	TransferEventSink = func(sid string, payload map[string]any) {
		if payload["event"] == "progress" {
			n++
		}
	}
	defer func() { TransferEventSink = nil }()
	progressLast = map[string]time.Time{} // reset

	s := &baseSession{id: "s1", sessionType: "sftp", status: StatusConnected}
	task := &TransferTask{ID: "dl-2", Type: "download", RemotePath: "/a", Total: 100}
	s.emitTransferProgress(task)
	s.emitTransferProgress(task) // within 100ms — dropped
	s.emitTransferProgressForced(task)
	if n != 2 {
		t.Fatalf("progress events = %d, want 2 (first + forced)", n)
	}
}

// --- Shared download helpers -------------------------------------------------

// TestSafeDownloadChild pins the join every download walker must use for
// server-supplied names (security.md P0 "all remote downloads traverse",
// CVE-2019-6111 class): a name must be a plain single component and the
// joined path must stay under dir. Anything else must be rejected.
func TestSafeDownloadChild(t *testing.T) {
	dir := t.TempDir()

	bad := []string{
		"", ".", "..",
		"../x", `..\x`, `..\AppData\Roaming\x`,
		"/abs", `\abs`,
		"a/b", `a\b`,
		"a\x00b",
		"C:x", "C:/x", `C:\x`,
	}
	if runtime.GOOS == "windows" {
		bad = append(bad, "NUL") // reserved device name
	}
	for _, name := range bad {
		if got, err := safeDownloadChild(dir, name); err == nil {
			t.Errorf("safeDownloadChild(%q) = %q, want error", name, got)
		}
	}

	ok := map[string]string{
		"file.txt":   filepath.Join(dir, "file.txt"),
		"a b.tar.gz": filepath.Join(dir, "a b.tar.gz"),
		"..hidden":   filepath.Join(dir, "..hidden"),
		"中文名.txt":    filepath.Join(dir, "中文名.txt"),
	}
	for name, want := range ok {
		got, err := safeDownloadChild(dir, name)
		if err != nil {
			t.Errorf("safeDownloadChild(%q) error: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("safeDownloadChild(%q) = %q, want %q", name, got, want)
		}
	}
}

// errWriter fails after accepting limit bytes (or immediately when limit < 0).
type errWriter struct {
	limit int
	err   error
	buf   bytes.Buffer
}

func (w *errWriter) Write(p []byte) (int, error) {
	if w.limit == 0 {
		return 0, w.err
	}
	if w.limit > 0 && len(p) > w.limit {
		n, _ := w.buf.Write(p[:w.limit])
		w.limit = 0
		return n, w.err
	}
	w.limit -= len(p)
	return w.buf.Write(p)
}

// partReader yields data once and then fails with err (simulates a dropped
// link mid-transfer; io.EOF is not produced).
type partReader struct {
	data []byte
	err  error
	done bool
}

func (r *partReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.data), r.err
}

// TestCopyDownloadStream pins the shared download loop: write errors and
// non-EOF read errors must be returned (security.md Medium: dropped write
// errors reported truncated downloads as "done"), cancellation stops the
// copy, and a nil task (WSL Copy/Move) must not panic.
func TestCopyDownloadStream(t *testing.T) {
	newTask := func() *TransferTask {
		task := &TransferTask{ID: "t1", Type: "download", Status: "running"}
		task.start()
		return task
	}

	t.Run("copies data and reports progress", func(t *testing.T) {
		task := newTask()
		var dst bytes.Buffer
		calls := 0
		if err := copyDownloadStream(&dst, bytes.NewReader([]byte("hello")), task, func() { calls++ }); err != nil {
			t.Fatalf("copyDownloadStream: %v", err)
		}
		if dst.String() != "hello" {
			t.Fatalf("dst = %q, want hello", dst.String())
		}
		if task.loadProgress() != 5 {
			t.Fatalf("progress = %d, want 5", task.loadProgress())
		}
		if calls == 0 {
			t.Fatal("progress callback never invoked")
		}
	})

	t.Run("write error is returned", func(t *testing.T) {
		task := newTask()
		boom := errors.New("disk full")
		// Accepts the first 64 KiB chunk, then fails like a full disk.
		w := &errWriter{limit: 64 * 1024, err: boom}
		err := copyDownloadStream(w, bytes.NewReader(bytes.Repeat([]byte("x"), 128*1024)), task, nil)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want disk full", err)
		}
	})

	t.Run("non-EOF read error is returned", func(t *testing.T) {
		task := newTask()
		boom := errors.New("connection reset")
		var dst bytes.Buffer
		err := copyDownloadStream(&dst, &partReader{data: []byte("partial"), err: boom}, task, nil)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want connection reset", err)
		}
		if dst.String() != "partial" {
			t.Fatalf("dst = %q, want partial", dst.String())
		}
	})

	t.Run("eof is success", func(t *testing.T) {
		task := newTask()
		var dst bytes.Buffer
		if err := copyDownloadStream(&dst, bytes.NewReader([]byte("x")), task, nil); err != nil {
			t.Fatalf("copyDownloadStream: %v", err)
		}
	})

	t.Run("cancelled task stops the copy", func(t *testing.T) {
		task := newTask()
		task.cancel()
		var dst bytes.Buffer
		err := copyDownloadStream(&dst, bytes.NewReader([]byte("hello")), task, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if dst.Len() != 0 {
			t.Fatalf("wrote %d bytes after cancel", dst.Len())
		}
	})

	t.Run("nil task copies without panicking", func(t *testing.T) {
		var dst bytes.Buffer
		if err := copyDownloadStream(&dst, bytes.NewReader([]byte("hi")), nil, nil); err != nil {
			t.Fatalf("copyDownloadStream: %v", err)
		}
		if dst.String() != "hi" {
			t.Fatalf("dst = %q, want hi", dst.String())
		}
	})
}

// TestWritePartFile pins the partial-file protocol every downloader must use
// (security.md 文件会话 Low "Partial files are left under the final name
// after a failure or cancel"): the body lands in "<name>.part" next to the
// target and only a fully written, successfully closed body is renamed onto
// it. A failed write must leave an existing target byte-for-byte intact and
// must not leave the .part file behind.
func TestWritePartFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("success replaces content and leaves no part file", func(t *testing.T) {
		final := filepath.Join(dir, "ok.bin")
		if err := os.WriteFile(final, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := writePartFile(final, func(f *os.File) error {
			if _, werr := f.Write([]byte("new-content")); werr != nil {
				return werr
			}
			return nil
		})
		if err != nil {
			t.Fatalf("writePartFile: %v", err)
		}
		got, err := os.ReadFile(final)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "new-content" {
			t.Fatalf("final content = %q, want new-content", got)
		}
		if _, err := os.Stat(final + ".part"); !os.IsNotExist(err) {
			t.Fatalf(".part file left behind (stat err = %v)", err)
		}
	})

	t.Run("failed write keeps old file and leaves no part file", func(t *testing.T) {
		final := filepath.Join(dir, "fail.bin")
		if err := os.WriteFile(final, []byte("keep-me"), 0o644); err != nil {
			t.Fatal(err)
		}
		boom := errors.New("connection reset")
		err := writePartFile(final, func(f *os.File) error {
			if _, werr := f.Write([]byte("half of the body")); werr != nil {
				return werr
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want connection reset", err)
		}
		got, rerr := os.ReadFile(final)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if string(got) != "keep-me" {
			t.Fatalf("final content = %q, want keep-me (partial download must not truncate it)", got)
		}
		if _, serr := os.Stat(final + ".part"); !os.IsNotExist(serr) {
			t.Fatalf(".part file left behind (stat err = %v)", serr)
		}
	})

	t.Run("failed write on a fresh target creates no final file", func(t *testing.T) {
		final := filepath.Join(dir, "fresh.bin")
		boom := errors.New("disk full")
		err := writePartFile(final, func(f *os.File) error { return boom })
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want disk full", err)
		}
		if _, serr := os.Stat(final); !os.IsNotExist(serr) {
			t.Fatalf("final file exists after failure (stat err = %v)", serr)
		}
		if _, serr := os.Stat(final + ".part"); !os.IsNotExist(serr) {
			t.Fatalf(".part file left behind (stat err = %v)", serr)
		}
	})
}

// TestProgressThrottleIsPerSession pins the throttle key: every session
// numbers its tasks from dl-1/ul-1, so two sessions running "dl-1" at the
// same time must not throttle each other, and one session's complete event
// must not delete the other's throttle entry (security.md 文件会话 Low:
// "The progress throttle map is global and keyed only by per-session task
// IDs").
func TestProgressThrottleIsPerSession(t *testing.T) {
	var got []map[string]any
	prev := TransferEventSink
	TransferEventSink = func(sid string, payload map[string]any) { got = append(got, payload) }
	t.Cleanup(func() { TransferEventSink = prev; progressLast = map[string]time.Time{} })
	progressLast = map[string]time.Time{}

	s1 := &baseSession{id: "s1", sessionType: "sftp", status: StatusConnected}
	s2 := &baseSession{id: "s2", sessionType: "sftp", status: StatusConnected}
	task1 := &TransferTask{ID: "dl-1", Type: "download", RemotePath: "/a", Total: 100}
	task2 := &TransferTask{ID: "dl-1", Type: "download", RemotePath: "/b", Total: 100}

	s1.emitTransferProgress(task1)
	s2.emitTransferProgress(task2) // same task ID, other session
	if len(got) != 2 {
		t.Fatalf("got %d progress events, want 2 — two sessions sharing a task ID must not throttle each other", len(got))
	}

	// s1's complete must delete only s1's entry: s2 is still inside its
	// 100 ms window and must stay throttled.
	s1.emitTransferComplete(task1)
	got = nil
	s2.emitTransferProgress(task2)
	if len(got) != 0 {
		t.Fatalf("got %d events after s1 completed, want 0 — s1's complete dropped s2's throttle entry", len(got))
	}
	// ...while s1, whose entry WAS deleted, emits again immediately.
	got = nil
	s1.emitTransferProgress(task1)
	if len(got) != 1 {
		t.Fatalf("got %d events for s1 after its complete, want 1 — its throttle entry must be gone", len(got))
	}
	s2.emitTransferComplete(task2)
}

// TestEmitTransferEventClearsThrottleEntries pins the error path: a task
// that ends in an error must drop its throttle entry like the done path
// does, otherwise every failed transfer leaks one map entry for the life of
// the app (security.md 文件会话 Low: "emitTransferEvent (error path) never
// deletes its entry, so it leaks").
func TestEmitTransferEventClearsThrottleEntries(t *testing.T) {
	prev := TransferEventSink
	TransferEventSink = func(string, map[string]any) {}
	t.Cleanup(func() { TransferEventSink = prev; progressLast = map[string]time.Time{} })
	progressLast = map[string]time.Time{}

	s := &baseSession{id: "s1", sessionType: "sftp", status: StatusConnected}
	task := &TransferTask{ID: "dl-1", Type: "download", RemotePath: "/a", Total: 100}
	s.emitTransferProgress(task)
	if len(progressLast) == 0 {
		t.Fatal("no throttle entry after a progress event — the test cannot prove anything")
	}
	s.emitTransferEvent(task, errors.New("boom"))
	if len(progressLast) != 0 {
		t.Fatalf("throttle entries left behind after an error: %v", progressLast)
	}
}

// TestFileEventsCoalescedOnTick pins the per-file event merge: a burst of
// file-start/file-done pairs must be coalesced onto the 100 ms tick instead
// of firing one Wails IPC event per file (a 100k-file tree used to send
// 200k+ events), and no file whose start was emitted may lose its done —
// the frontend would keep that entry "running" forever.
func TestFileEventsCoalescedOnTick(t *testing.T) {
	var events []map[string]any
	prev := TransferEventSink
	TransferEventSink = func(sid string, payload map[string]any) { events = append(events, payload) }
	t.Cleanup(func() { TransferEventSink = prev; progressLast = map[string]time.Time{} })
	progressLast = map[string]time.Time{}

	s := &baseSession{id: "s-burst", sessionType: "sftp", status: StatusConnected}
	task := &TransferTask{ID: "dl-7", Type: "download", RemotePath: "/big", Status: "running"}
	const files = 200
	task.setFileCount(files)
	for i := range files {
		rel := fmt.Sprintf("f%03d.txt", i)
		task.beginFile(rel)
		s.emitFileStart(task, rel, rel)
		task.finishFile(rel)
		s.emitFileDone(task, rel)
	}

	starts, dones := map[string]int{}, map[string]int{}
	for _, ev := range events {
		switch ev["event"] {
		case "file-start":
			starts[ev["file"].(string)]++
		case "file-done":
			dones[ev["file"].(string)]++
		}
	}
	if len(starts) > 3 {
		t.Errorf("%d file-start events for a %d-file burst, want them coalesced on the 100 ms tick", len(starts), files)
	}
	if len(dones) > 5 {
		t.Errorf("%d file-done events for a %d-file burst, want them coalesced on the 100 ms tick", len(dones), files)
	}
	for rel := range starts {
		if dones[rel] == 0 {
			t.Errorf("file-start for %q was emitted but its file-done was dropped — the UI keeps it running forever", rel)
		}
	}
	// Finishing the task must clean up the per-task state behind it.
	s.emitTransferComplete(task)
}
