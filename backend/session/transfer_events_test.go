package session

import (
	"bytes"
	"context"
	"errors"
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
