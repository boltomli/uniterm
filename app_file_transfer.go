package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ys-ll/uniterm/backend/session"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// SFTP direct API — called from frontend without terminal layer

// fileTransferSession is the common interface for all file-transfer sessions
// (SFTP, FTP, SMB, WebDAV, S3). Every direct Sftp* app method dispatches through
// this contract, so protocol-agnostic features (like the external editor below)
// work across every file-transfer backend without per-protocol code.
type fileTransferSession interface {
	ListRemote(dir string) (session.FileListResult, error)
	ListLocal(dir string) (session.FileListResult, error)
	ChangeRemoteDir(dir string) (session.FileListResult, error)
	ChangeLocalDir(dir string) (session.FileListResult, error)
	ListLocalDrives() ([]session.FileItem, error)
	MakeDir(dir string) error
	Remove(path string, recursive bool) error
	Rename(oldPath, newPath string) error
	Chmod(path string, mode os.FileMode) error
	LocalRemove(path string, recursive bool) error
	LocalRename(oldPath, newPath string) error
	LocalMkdir(dir string) error
	LocalGetContent(path string) ([]byte, error)
	LocalPutContent(path string, content []byte) error
	LocalCopy(oldPath, newPath string) error
	LocalMove(oldPath, newPath string) error
	Get(remotePath, localPath string, recursive bool) (string, error)
	Put(localPath, remotePath string, recursive bool) (string, error)
	PutContent(remotePath string, content []byte) error
	GetContent(remotePath string) ([]byte, error)
	Copy(oldPath, newPath string) error
	Move(oldPath, newPath string) error
	CancelTransfer(taskID string) error
	PauseTransfer(taskID string) error
	ResumeTransfer(taskID string) error
}

func (a *App) getSftp(sid string) (fileTransferSession, error) {
	if a.sessionManager == nil {
		return nil, fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sid)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sid)
	}
	if fs, ok := s.(fileTransferSession); ok {
		return fs, nil
	}
	return nil, fmt.Errorf("not a file transfer session: %s", sid)
}

func (a *App) SftpListRemote(sessionID, dir string) (session.FileListResult, error) {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return session.FileListResult{}, err
	}
	return fs.ListRemote(dir)
}

func (a *App) SftpListLocal(sessionID, dir string) (session.FileListResult, error) {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return session.FileListResult{}, err
	}
	return fs.ListLocal(dir)
}

func (a *App) SftpChangeRemoteDir(sessionID, dir string) (session.FileListResult, error) {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return session.FileListResult{}, err
	}
	return fs.ChangeRemoteDir(dir)
}

func (a *App) SftpChangeLocalDir(sessionID, dir string) (session.FileListResult, error) {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return session.FileListResult{}, err
	}
	return fs.ChangeLocalDir(dir)
}

func (a *App) SftpListLocalDrives(sessionID string) ([]session.FileItem, error) {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return nil, err
	}
	return fs.ListLocalDrives()
}

func (a *App) SftpMakeDir(sessionID, dir string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.MakeDir(dir)
}

func (a *App) SftpRemove(sessionID, path string, recursive bool) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.Remove(path, recursive)
}

// SftpRename renames a remote file or directory.
func (a *App) SftpRename(sessionID, oldPath, newPath string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.Rename(oldPath, newPath)
}

func (a *App) SftpChmod(sessionID, path, mode string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	modeUint, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return fmt.Errorf("invalid mode: %s", mode)
	}
	return fs.Chmod(path, os.FileMode(modeUint))
}

func (a *App) SftpLocalRemove(sessionID, path string, recursive bool) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.LocalRemove(path, recursive)
}

func (a *App) SftpLocalRename(sessionID, oldPath, newPath string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.LocalRename(oldPath, newPath)
}

func (a *App) SftpLocalMkdir(sessionID, dir string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.LocalMkdir(dir)
}

func (a *App) SftpLocalGetContent(sessionID, localPath string) (string, error) {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return "", err
	}
	content, err := fs.LocalGetContent(localPath)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(content), nil
}

func (a *App) SftpLocalPutContent(sessionID, localPath, contentBase64, encoding string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	content, err := base64.StdEncoding.DecodeString(contentBase64)
	if err != nil {
		return err
	}
	// Re-encode if target encoding is not UTF-8 (frontend always sends UTF-8)
	content, err = convertEncoding(content, encoding)
	if err != nil {
		return err
	}
	return fs.LocalPutContent(localPath, content)
}

func (a *App) SftpLocalCopy(sessionID, oldPath, newPath string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.LocalCopy(oldPath, newPath)
}

func (a *App) SftpLocalMove(sessionID, oldPath, newPath string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.LocalMove(oldPath, newPath)
}

func (a *App) SftpGet(sessionID, remotePath, localPath string, recursive bool) (string, error) {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return "", err
	}
	return fs.Get(remotePath, localPath, recursive)
}

func (a *App) SftpCancelTransfer(sessionID, taskID string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.CancelTransfer(taskID)
}

func (a *App) SftpPauseTransfer(sessionID, taskID string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.PauseTransfer(taskID)
}

func (a *App) SftpResumeTransfer(sessionID, taskID string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.ResumeTransfer(taskID)
}

func (a *App) SftpPut(sessionID, localPath, remotePath string, recursive bool) (string, error) {
	// Auto-detect directories so drag-dropping a folder works even when
	// the caller passes recursive=false (single-file upload API).
	if !recursive {
		if info, err := os.Stat(localPath); err == nil && info.IsDir() {
			recursive = true
		}
	}
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return "", err
	}
	return fs.Put(localPath, remotePath, recursive)
}

func (a *App) SftpPutContent(sessionID, remotePath, contentBase64, encoding string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	content, err := base64.StdEncoding.DecodeString(contentBase64)
	if err != nil {
		return err
	}
	// Re-encode if target encoding is not UTF-8 (frontend always sends UTF-8)
	content, err = convertEncoding(content, encoding)
	if err != nil {
		return err
	}
	return fs.PutContent(remotePath, content)
}

// convertEncoding converts UTF-8 bytes to the target encoding.
// Returns the original bytes unchanged if encoding is UTF-8 or empty.
func convertEncoding(utf8Bytes []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(encoding) {
	case "", "utf-8", "utf8":
		return utf8Bytes, nil
	case "gbk", "gb2312", "gb18030":
		reader := transform.NewReader(bytes.NewReader(utf8Bytes), simplifiedchinese.GBK.NewEncoder())
		return io.ReadAll(reader)
	default:
		return nil, fmt.Errorf("unsupported encoding: %s", encoding)
	}
}

func (a *App) SftpGetContent(sessionID, remotePath string) (string, error) {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return "", err
	}
	content, err := fs.GetContent(remotePath)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(content), nil
}

func (a *App) SftpCopy(sessionID, oldPath, newPath string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.Copy(oldPath, newPath)
}

func (a *App) SftpMove(sessionID, oldPath, newPath string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.Move(oldPath, newPath)
}

// ---------------------------------------------------------------------------
// External editor ("open remote file in an external editor, auto-upload")
//
// A single protocol-agnostic engine: download the remote file to a temp local
// file, open it in a configurable external editor, then watch the temp file
// and push changes back to the remote path. Because it only relies on
// GetContent/PutContent from the fileTransferSession contract, it works for
// SFTP, FTP, SMB, WebDAV and S3 alike.
//
// Upload strategy is a hybrid:
//   - live auto-upload: while the editor is open, a stable change (file not
//     modified for ~1.2s) is pushed back automatically;
//   - final upload: when the editor process exits, the last state is pushed
//     once more as a safety net.
//
// The upload is skipped whenever the temp file content hash is unchanged, so
// merely opening the editor won't produce a redundant upload.
// ---------------------------------------------------------------------------

// extEditSessions tracks active external-edit runs per session (keyed by temp
// file path) so they can be cancelled — killing the editor process and letting
// each goroutine clean its temp file — when a session closes.
var (
	extEditMu       sync.Mutex
	extEditSessions = map[string]map[string]context.CancelFunc{}
)

// SftpOpenExternalEditor opens a remote file in a configurable external editor
// and starts background auto-upload of changes back to the remote path.
// editorCmd is the program + any fixed flags (quoted paths allowed); the temp
// file path is appended as the final argument. Returns immediately once the
// editor has launched; progress is reported via "sftp:extedit" Wails events.
func (a *App) SftpOpenExternalEditor(sessionID, remotePath, editorCmd string) error {
	if strings.TrimSpace(editorCmd) == "" {
		return errors.New("no external editor configured")
	}
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}

	content, err := fs.GetContent(remotePath)
	if err != nil {
		return err
	}
	if isBinaryExt(content) {
		return errors.New("refusing to open binary file in external editor")
	}

	// Temp file in a per-session scratch dir (unique per invocation).
	dir := filepath.Join(os.TempDir(), "uniterm-extedit", sanitizePart(sessionID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, sanitizePart(path.Base(remotePath))+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err = f.Write(content); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err = f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	prog, args, err := splitCommand(editorCmd)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	args = append(args, tmp)

	runCtx, cancel := context.WithCancel(context.Background())
	registerExternalEdit(sessionID, tmp, cancel)

	cmd := exec.CommandContext(runCtx, prog, args...)
	hideProcWindow(cmd)
	if err := cmd.Start(); err != nil {
		unregisterExternalEdit(sessionID, tmp, cancel)
		os.Remove(tmp)
		return fmt.Errorf("failed to start external editor %s: %w", prog, err)
	}

	a.emit("sftp:extedit", map[string]interface{}{
		"sessionId": sessionID,
		"path":      remotePath,
		"status":    "started",
		"tmp":       tmp,
	})

	go a.pollExternalEditor(runCtx, sessionID, fs, remotePath, tmp, cmd, cancel,
		content)
	return nil
}

// OpenExternalEditorLocal launches the configured external editor directly on
// a local file (used by the SFTP "local" pane). Unlike the remote flavour, no
// temp copy or auto-upload is involved: the file already lives on disk, so the
// editor edits it in place and the launch returns immediately.
func (a *App) OpenExternalEditorLocal(localPath, editorCmd string) error {
	if strings.TrimSpace(editorCmd) == "" {
		return errors.New("no external editor configured")
	}
	prog, args, err := splitCommand(editorCmd)
	if err != nil {
		return err
	}
	args = append(args, localPath)
	cmd := exec.Command(prog, args...)
	hideProcWindow(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start external editor %s: %w", prog, err)
	}
	go cmd.Wait()
	return nil
}

// pollExternalEditor watches the temp file until the editor exits, pushing
// changes back to the remote path (debounced live uploads + a final upload).
func (a *App) pollExternalEditor(runCtx context.Context, sessionID string,
	fs fileTransferSession, remotePath, tmp string, cmd *exec.Cmd, cancel context.CancelFunc,
	initial []byte) {

	defer unregisterExternalEdit(sessionID, tmp, cancel)
	defer os.Remove(tmp)
	defer cancel()

	lastUploaded := hashBytes(initial)
	var dirty bool
	var lastSeenMtime time.Time

	poll := time.NewTicker(400 * time.Millisecond)
	defer poll.Stop()
	editorDone := make(chan error, 1)
	go func() { editorDone <- cmd.Wait() }()

	for {
		select {
		case <-runCtx.Done():
			return
		case <-editorDone:
			if b, err := os.ReadFile(tmp); err == nil {
				if h := hashBytes(b); h != lastUploaded {
					if perr := fs.PutContent(remotePath, b); perr == nil {
						lastUploaded = h
						a.emitExtEdit(sessionID, remotePath, "uploaded")
					}
				}
			}
			a.emitExtEdit(sessionID, remotePath, "closed")
			return
		case <-poll.C:
			st, err := os.Stat(tmp)
			if err != nil {
				// Temp file temporarily missing (editor mid save-via-rename).
				continue
			}
			if !st.ModTime().Equal(lastSeenMtime) {
				lastSeenMtime = st.ModTime()
				dirty = true
			}
			if !dirty {
				continue
			}
			if time.Since(lastSeenMtime) < 1200*time.Millisecond {
				// Editor may still be writing; wait for the file to settle.
				continue
			}
			b, rerr := os.ReadFile(tmp)
			if rerr != nil {
				dirty = false
				continue
			}
			if h := hashBytes(b); h != lastUploaded {
				if perr := fs.PutContent(remotePath, b); perr == nil {
					lastUploaded = h
					a.emitExtEdit(sessionID, remotePath, "uploaded")
				}
			}
			dirty = false
		}
	}
}

func (a *App) emitExtEdit(sessionID, remotePath, status string) {
	a.emit("sftp:extedit", map[string]interface{}{
		"sessionId": sessionID,
		"path":      remotePath,
		"status":    status,
	})
}

// registerExternalEdit records the cancel func for a session + temp file.
func registerExternalEdit(sessionID, tmp string, cancel context.CancelFunc) {
	extEditMu.Lock()
	defer extEditMu.Unlock()
	if extEditSessions[sessionID] == nil {
		extEditSessions[sessionID] = map[string]context.CancelFunc{}
	}
	extEditSessions[sessionID][tmp] = cancel
}

func unregisterExternalEdit(sessionID, tmp string, cancel context.CancelFunc) {
	extEditMu.Lock()
	defer extEditMu.Unlock()
	if m, ok := extEditSessions[sessionID]; ok {
		delete(m, tmp)
		if len(m) == 0 {
			delete(extEditSessions, sessionID)
		}
	}
}

// cancelExternalEdits stops every active external edit for a session (called
// when the session is unregistered / closed), killing editor processes and
// letting each goroutine clean its temp file.
func cancelExternalEdits(sessionID string) {
	extEditMu.Lock()
	defer extEditMu.Unlock()
	for _, cancel := range extEditSessions[sessionID] {
		cancel()
	}
	delete(extEditSessions, sessionID)
}

// isBinaryExt heuristically rejects binary content (mirrors the frontend check).
func isBinaryExt(b []byte) bool {
	sample := b
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if len(sample) == 0 {
		return false
	}
	nonPrintable := 0
	for _, c := range sample {
		if c < 0x09 || (c > 0x0D && c < 0x20) {
			nonPrintable++
		}
	}
	return float64(nonPrintable)/float64(len(sample)) > 0.3
}

// splitCommand splits an editor command into a program + fixed args, honoring
// double quotes so paths with spaces work (e.g. "C:\Program Files\app.exe" -w).
func splitCommand(s string) (string, []string, error) {
	var parts []string
	var cur strings.Builder
	inQuote := false
	readAny := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			readAny = true
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
			readAny = true
		}
	}
	if inQuote {
		return "", nil, errors.New("unbalanced quotes in editor command")
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	if len(parts) == 0 {
		return "", nil, errors.New("empty editor command")
	}
	_ = readAny
	return parts[0], parts[1:], nil
}

func sanitizePart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|', ' ', '\n', '\r', '\t':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 || b.String() == "." || b.String() == ".." {
		return "file"
	}
	return b.String()
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// --- External editor detection --------------------------------------------

// ExternalEditorOption describes an external editor that was detected on the
// host. Label is the display text (editor name plus the command), Value is the
// command passed to the editor engine (which appends the temp file path).
type ExternalEditorOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ListExternalEditors scans the host for installed text editors and returns
// only the ones actually available, so the settings dropdown doesn't list
// editors that aren't installed here. detectExternalEditors is implemented per
// platform (app_windows.go / app_darwin.go / app_linux.go), since each OS
// offers a different set of editors.
func (a *App) ListExternalEditors() ([]ExternalEditorOption, error) {
	return detectExternalEditors(), nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
