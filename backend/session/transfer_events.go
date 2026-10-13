package session

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/ys-ll/uniterm/backend/utils"
)

// TransferEventSink is installed by the App layer at startup. File-transfer
// backends publish progress through it as Wails events ("sftp:transfer")
// instead of embedding OSC-633 sequences into the terminal data stream.
var TransferEventSink func(sessionID string, payload map[string]any)

// FileFailure records one file that failed inside a directory transfer.
type FileFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

const progressMinInterval = 100 * time.Millisecond

var (
	progressMu sync.Mutex
	// progressLast holds the per-task progress throttle timestamps. Keys are
	// session-qualified (transferThrottleKey): every session numbers its
	// tasks from dl-1/ul-1, so a bare task ID would let two sessions
	// throttle each other and let one session's complete event delete the
	// other's entry (security.md 文件会话 Low).
	progressLast = map[string]time.Time{}

	// fileEventState carries the per-task per-file event coalescing: lastEmit
	// is the 100 ms tick timestamp shared by file-start/file-done, and
	// started holds the files whose file-start was actually emitted — their
	// file-done must be emitted too or the frontend keeps those entries
	// "running" forever. Entries are dropped with progressLast when the task
	// completes or errors.
	fileEventState = map[string]*fileEventEntry{}
)

type fileEventEntry struct {
	lastEmit time.Time
	started  map[string]bool
}

// transferThrottleKey namespaces throttle state by session and task.
func (s *baseSession) transferThrottleKey(task *TransferTask) string {
	return s.id + "/" + task.ID
}

// fileStateFor returns (creating if needed) the coalescing state for key.
// Callers hold progressMu.
func fileStateFor(key string) *fileEventEntry {
	st := fileEventState[key]
	if st == nil {
		st = &fileEventEntry{started: map[string]bool{}}
		fileEventState[key] = st
	}
	return st
}

func transferDisplayName(task *TransferTask) string {
	if task.Type == "download" {
		return path.Base(task.RemotePath)
	}
	return filepath.Base(task.LocalPath)
}

func (s *baseSession) emitTransferPayload(payload map[string]any) {
	if TransferEventSink == nil {
		return
	}
	payload["type"] = "sftp:transfer"
	payload["sessionId"] = s.id
	TransferEventSink(s.id, payload)
}

func (s *baseSession) emitTransferStart(task *TransferTask) {
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "start", "tfType": task.Type,
		"name": transferDisplayName(task), "total": task.loadTotal(),
		"localPath": task.LocalPath, "remotePath": task.RemotePath,
		"fileCount": task.fileCount(),
	})
}

func (s *baseSession) emitTransferProgress(task *TransferTask) {
	key := s.transferThrottleKey(task)
	progressMu.Lock()
	last, ok := progressLast[key]
	now := time.Now()
	if ok && now.Sub(last) < progressMinInterval {
		progressMu.Unlock()
		return
	}
	progressLast[key] = now
	progressMu.Unlock()
	s.emitTransferProgressForced(task)
}

// emitTransferProgressForced bypasses the throttle (final updates).
func (s *baseSession) emitTransferProgressForced(task *TransferTask) {
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "progress",
		"progress": task.loadProgress(), "total": task.loadTotal(),
	})
}

func (s *baseSession) emitTransferComplete(task *TransferTask) {
	key := s.transferThrottleKey(task)
	progressMu.Lock()
	delete(progressLast, key)
	delete(fileEventState, key)
	progressMu.Unlock()
	payload := map[string]any{
		"taskId": task.ID, "event": "complete", "status": task.Status,
	}
	task.fileMu.RLock()
	if task.FileCount > 0 || task.FailedFiles != nil {
		payload["failedFiles"] = task.FailedFiles
		payload["completedFiles"] = task.CompletedFiles
		payload["fileCount"] = task.FileCount
	}
	task.fileMu.RUnlock()
	s.emitTransferPayload(payload)
}

// emitTransferPaused reports that a transfer is paused. Unlike the old
// "complete" + status "paused" event this never looks finished to the UI.
func (s *baseSession) emitTransferPaused(task *TransferTask) {
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "paused",
	})
}

func (s *baseSession) emitTransferResumed(task *TransferTask) {
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "resumed",
	})
}

// emitTransferEvent reports a task-level error (event "complete", status "error").
// The throttle and file-coalescing state go with it: a task that ends in an
// error leaks its map entries just like one that ends in done otherwise
// (security.md 文件会话 Low: "emitTransferEvent (error path) never deletes
// its entry").
func (s *baseSession) emitTransferEvent(task *TransferTask, err error) {
	task.Status = "error"
	key := s.transferThrottleKey(task)
	progressMu.Lock()
	delete(progressLast, key)
	delete(fileEventState, key)
	progressMu.Unlock()
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "complete", "status": "error", "error": err.Error(),
	})
}

// --- Per-file events (directory transfers) ---

// emitFileStart reports the file the transfer is working on. Events are
// coalesced onto one 100 ms tick per task: with SFTP's worker pool a tree of
// 100k small files used to fire 200k+ Wails IPC events in bursts (security.md
// 文件会话 Low "Per-file events are not throttled"). A start dropped inside
// the tick never enters the `started` set, so its file-done may be dropped
// too — the frontend then simply never shows that file, while the counters
// stay correct through the next emitted event and the final complete.
func (s *baseSession) emitFileStart(task *TransferTask, rel, display string) {
	key := s.transferThrottleKey(task)
	progressMu.Lock()
	st := fileStateFor(key)
	now := time.Now()
	if !st.lastEmit.IsZero() && now.Sub(st.lastEmit) < progressMinInterval {
		progressMu.Unlock()
		return
	}
	st.lastEmit = now
	st.started[rel] = true
	progressMu.Unlock()
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "file-start", "file": rel, "name": display,
		"completedFiles": task.completedCount(), "fileCount": task.fileCount(),
	})
}

// emitFileDone reports a finished file. It rides the same 100 ms tick as
// emitFileStart, with one exception: a file whose file-start was emitted
// MUST get its done event even inside the tick, or the frontend keeps that
// entry "running" forever. The number of such forced events is bounded by
// how many starts the tick emitted.
func (s *baseSession) emitFileDone(task *TransferTask, rel string) {
	key := s.transferThrottleKey(task)
	progressMu.Lock()
	st := fileStateFor(key)
	now := time.Now()
	forced := false
	if st.started[rel] {
		delete(st.started, rel)
		forced = true
	}
	if !forced && !st.lastEmit.IsZero() && now.Sub(st.lastEmit) < progressMinInterval {
		progressMu.Unlock()
		return
	}
	st.lastEmit = now
	progressMu.Unlock()
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "file-done", "file": rel,
		"completedFiles": task.completedCount(), "fileCount": task.fileCount(),
	})
}

// emitFileFailed is never coalesced: failures are rare and the frontend
// needs every one of them. The file leaves the started set with its final
// event so nothing dangles until the task ends.
func (s *baseSession) emitFileFailed(task *TransferTask, rel string, err error) {
	key := s.transferThrottleKey(task)
	progressMu.Lock()
	if st := fileEventState[key]; st != nil {
		delete(st.started, rel)
	}
	progressMu.Unlock()
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "file-failed", "file": rel, "error": err.Error(),
		"completedFiles": task.completedCount(), "fileCount": task.fileCount(),
	})
}

// --- Shared download helpers -------------------------------------------------

// safeDownloadChild joins one server-supplied listing name under dir and
// verifies the result stays inside dir. Every recursive download walker
// (SCP/SFTP/FTP/SMB/WebDAV) must resolve child paths through this helper:
// the name comes from the remote server, which may be compromised
// (security.md P0 path traversal, CVE-2019-6111 class). A name must be a
// plain single component — no empty/"/\."/"..", no separators in either
// style, no NUL, no drive prefix — and the joined path is double-checked
// with filepath.Rel containment (utils.SafeLocalJoin).
func safeDownloadChild(dir, name string) (string, error) {
	if err := utils.SafeRemoteName(name); err != nil {
		return "", fmt.Errorf("unsafe remote file name %q", name)
	}
	p, err := utils.SafeLocalJoin(dir, name)
	if err != nil {
		return "", fmt.Errorf("unsafe remote file name %q", name)
	}
	return p, nil
}

// transferCtxErr reports the task's cancellation error, or nil when there is
// no task, the task was never started, or it is still running. Every copy or
// download loop must consult it so cancel actually stops the transfer
// (security.md: "Cancelling a WSL transfer does nothing").
func transferCtxErr(task *TransferTask) error {
	if task == nil {
		return nil
	}
	ctx := task.ctx
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// copyDownloadStream streams src into dst while honouring the task's cancel
// and pause state and reporting progress through progress (optional). Unlike
// the ad-hoc loops it replaces (s3/webdav/ftp downloads), a failed write and
// a non-EOF read error are returned instead of dropped — otherwise a full
// disk or a dropped link leaves a short local file reported as completed
// (security.md "Write and read errors are ignored"). task may be nil for
// copies without transfer tracking (e.g. WSL Copy/Move).
func copyDownloadStream(dst io.Writer, src io.Reader, task *TransferTask, progress func()) error {
	buf := make([]byte, 64*1024)
	for {
		if err := transferCtxErr(task); err != nil {
			return err
		}
		if task != nil {
			task.waitIfPaused()
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			if task != nil {
				task.addProgress(int64(n))
			}
			if progress != nil {
				progress()
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
	}
}

// writePartFile writes localPath through a same-directory "<name>.part"
// temporary file and only renames it onto localPath once the whole body has
// been written and closed successfully. A failed or cancelled download
// therefore never leaves a truncated file under the final name and never
// destroys the file that was already there (security.md 文件会话 Low:
// "Partial files are left under the final name after a failure or cancel").
// write must fill f and must not close it; every failure path removes the
// .part file before returning.
func writePartFile(localPath string, write func(f *os.File) error) error {
	part := localPath + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		// Close is idempotent here: the success path already closed f, and a
		// second Close only reports the same error we already returned.
		_ = f.Close()
		if !committed {
			_ = os.Remove(part)
		}
	}()
	if err := write(f); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(part, localPath); err != nil {
		return err
	}
	committed = true
	return nil
}
