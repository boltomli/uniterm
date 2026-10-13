package session

import (
	"fmt"
	"io"
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
	progressMu   sync.Mutex
	progressLast = map[string]time.Time{}
)

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
	progressMu.Lock()
	last, ok := progressLast[task.ID]
	now := time.Now()
	if ok && now.Sub(last) < progressMinInterval {
		progressMu.Unlock()
		return
	}
	progressLast[task.ID] = now
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
	progressMu.Lock()
	delete(progressLast, task.ID)
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
func (s *baseSession) emitTransferEvent(task *TransferTask, err error) {
	task.Status = "error"
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "complete", "status": "error", "error": err.Error(),
	})
}

// --- Per-file events (directory transfers) ---

func (s *baseSession) emitFileStart(task *TransferTask, rel, display string) {
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "file-start", "file": rel, "name": display,
		"completedFiles": task.completedCount(), "fileCount": task.fileCount(),
	})
}

func (s *baseSession) emitFileDone(task *TransferTask, rel string) {
	s.emitTransferPayload(map[string]any{
		"taskId": task.ID, "event": "file-done", "file": rel,
		"completedFiles": task.completedCount(), "fileCount": task.fileCount(),
	})
}

func (s *baseSession) emitFileFailed(task *TransferTask, rel string, err error) {
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
