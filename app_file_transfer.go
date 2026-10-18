package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/ys-ll/uniterm/backend/mcp"
	"github.com/ys-ll/uniterm/backend/session"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
	// Symlink creates a symbolic link on the remote side. Only backends with
	// link semantics implement it (SFTP/SCP/WSL); others return an error and
	// the frontend hides the UI entry.
	Symlink(target, linkPath string) error
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

// transferRetry is implemented by backends that support retrying a failed
// transfer from a frontend-held checkpoint (SFTP; SCP from Task 6). Optional
// so the other protocol backends don't have to implement it yet.
type transferRetry interface {
	RetryTransfer(spec session.TransferSpec, skipCompleted []string) (string, error)
	DismissTransfer(taskID string) error
}

// SftpRetryTransfer re-runs a failed transfer, skipping files the frontend
// reports as already completed.
func (a *App) SftpRetryTransfer(sessionID string, spec session.TransferSpec, skipCompleted []string) (string, error) {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return "", err
	}
	r, ok := fs.(transferRetry)
	if !ok {
		return "", fmt.Errorf("retry not supported for this protocol")
	}
	return r.RetryTransfer(spec, skipCompleted)
}

// SftpDismissTransfer removes a retained failed task from the backend.
func (a *App) SftpDismissTransfer(sessionID, taskID string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	if r, ok := fs.(transferRetry); ok {
		return r.DismissTransfer(taskID)
	}
	return nil
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
	// F-04: listing is browsing — it must NOT register local roots. The
	// local-pane trust anchor is the user's explicit directory selection
	// (OpenDirectoryDialog / drops → fileGrants); otherwise a compromised
	// renderer could call SftpListLocal(sid, "/") and turn the session into
	// an arbitrary local read/write primitive.
	return fs.ListLocal(dir)
}

// ── Local-pane path authorization ──
//
// The local pane of a file-transfer session can browse the whole disk, but
// the mutating / content bindings (SftpLocalGetContent/PutContent/Copy/
// Move/Remove/Rename/Mkdir and the open-with/edit-external helpers) only
// accept paths the user explicitly chose: a directory picked through the
// native dialog (OpenDirectoryDialog → fileGrants.addDir), or a file/drop
// grant. (F-04: pane navigation alone used to count as trust, which let a
// compromised renderer widen the roots to "/" with a single listing call.)
// Containment is re-checked after symlink resolution, so a granted directory
// containing a symlink to the outside cannot open a window to its target.

// requireSftpLocalPath refuses local paths the user has not granted (plus any
// ".." traversal and any symlink escape).
func (a *App) requireSftpLocalPath(sessionID, p string) error {
	if err := rejectDotDotPath(p); err != nil {
		return err
	}
	_, err := resolveGrantedLocalPath(p)
	return err
}

// requireLocalPanePath gates the local-pane "open with" / "edit externally"
// bindings, whose signatures carry no session id. Same grant model as
// requireSftpLocalPath (F-04).
func (a *App) requireLocalPanePath(p string) error {
	if err := rejectDotDotPath(p); err != nil {
		return err
	}
	_, err := resolveGrantedLocalPath(p)
	return err
}

// resolveGrantedLocalPath enforces the local grant model including symlink
// resolution (F-04): the lexical check passes for a granted directory, then// every existing path component is resolved (symlinks and Windows junctions)
// and the containment is re-checked against the granted roots.
func resolveGrantedLocalPath(p string) (string, error) {
	clean := filepath.Clean(p)
	if !fileGrants.allowsDir(clean) {
		return "", fmt.Errorf("local path not granted: %s", p)
	}
	dirs := fileGrants.dirList()
	resolved, err := mcp.ResolveMcpLocalPath(clean, dirs)
	if err != nil {
		// An exact file grant (dialog/drop picked this very file) stands on
		// its own: the user chose the file, following its own link is the
		// chosen target.
		if fileGrants.allowsExactFile(clean) {
			return clean, nil
		}
		return "", fmt.Errorf("local path not granted (symlink-resolved): %s", p)
	}
	return resolved, nil
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
	// F-04: navigation is browsing only — no root registration (see
	// SftpListLocal).
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

// SftpSymlink creates a symbolic link on the remote side. Only supported by
// SFTP/SCP/WSL backends; other protocols return a "not supported" error.
func (a *App) SftpSymlink(sessionID, target, linkPath string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.Symlink(target, linkPath)
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
	if err := a.requireSftpLocalPath(sessionID, path); err != nil {
		return err
	}
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.LocalRemove(path, recursive)
}

func (a *App) SftpLocalRename(sessionID, oldPath, newPath string) error {
	if err := a.requireSftpLocalPath(sessionID, oldPath); err != nil {
		return err
	}
	if err := a.requireSftpLocalPath(sessionID, newPath); err != nil {
		return err
	}
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.LocalRename(oldPath, newPath)
}

func (a *App) SftpLocalMkdir(sessionID, dir string) error {
	if err := a.requireSftpLocalPath(sessionID, dir); err != nil {
		return err
	}
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.LocalMkdir(dir)
}

func (a *App) SftpLocalGetContent(sessionID, localPath string) (string, error) {
	if err := a.requireSftpLocalPath(sessionID, localPath); err != nil {
		return "", err
	}
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
	if err := a.requireSftpLocalPath(sessionID, localPath); err != nil {
		return err
	}
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
	if err := a.requireSftpLocalPath(sessionID, oldPath); err != nil {
		return err
	}
	if err := a.requireSftpLocalPath(sessionID, newPath); err != nil {
		return err
	}
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.LocalCopy(oldPath, newPath)
}

func (a *App) SftpLocalMove(sessionID, oldPath, newPath string) error {
	if err := a.requireSftpLocalPath(sessionID, oldPath); err != nil {
		return err
	}
	if err := a.requireSftpLocalPath(sessionID, newPath); err != nil {
		return err
	}
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	return fs.LocalMove(oldPath, newPath)
}

func (a *App) SftpGet(sessionID, remotePath, localPath string, recursive bool) (string, error) {
	// localPath embeds a remote-controlled filename; it must not walk out of
	// the destination directory with ".." segments.
	if err := rejectDotDotPath(localPath); err != nil {
		return "", err
	}
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
	if err := rejectDotDotPath(localPath); err != nil {
		return "", err
	}
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
//   - final upload: when the editing session ends, the last state is pushed
//     once more as a safety net.
//
// The upload is skipped whenever the temp file content hash is unchanged, so
// merely opening the editor won't produce a redundant upload.
//
// The temp file uses a deterministic name per (session, remote path), so
// reopening the same file reuses the same local path — this matters because
// single-instance editors receive files through a short-lived launcher
// process (issue #737): the launched process exiting does NOT mean the editor
// closed. The editor's lifetime is therefore not tracked at all: the watcher
// and the temp file live as long as the session, and cleanup happens on
// session close, app exit (all dirs of this PID), or the stale-age sweep at
// the next app start.
// ---------------------------------------------------------------------------

// extEditSessions tracks active external-edit runs per session (keyed by a
// unique per-run key) so they can be cancelled — killing the editor process —
// when a session closes. Temp files are NOT deleted here: they use stable,
// deterministic names per (session, remote path) and stay on disk until the
// session's scratch dir is wiped (session close), the app exits, or the age
// sweep at app start, so a single-instance editor that was handed the file
// never loses it while it is being edited.
var (
	extEditMu       sync.Mutex
	extEditSessions = map[string]map[string]context.CancelFunc{}
	extEditRunSeq   atomic.Uint64
	// extEditWatchers holds the single live watcher per temp file. Reopening
	// the same remote file must REUSE the watcher (re-baselining its hash) —
	// stacking one watcher per open would make every save upload (and toast)
	// once per watcher.
	extEditWatchers = map[string]*extEditWatcher{}
)

// extEditWatcher is the shared upload baseline for one temp file's watcher.
type extEditWatcher struct {
	lastUploaded string // content hash already pushed to the remote
}

// extEditRootDir is the scratch root for external-edit temp files; each
// session gets a subdirectory keyed by its sanitized ID.
func extEditRootDir() string {
	return filepath.Join(os.TempDir(), "uniterm-extedit")
}

// extEditDirs maps a session id to the scratch dir created for it in this
// process (see ensureExtEditSessionDir). Guarded by extEditMu.
var extEditDirs = map[string]string{}

// ensureExtEditSessionDir returns the session's scratch dir, creating it on
// first use with os.MkdirTemp: the name still carries the PID (so the dir
// stays attributable to this instance and is swept by the exit/age cleanups)
// but ends in a random suffix, so another local user cannot predict,
// pre-create or swap it — the pre-fix path was
// os.TempDir()/uniterm-extedit/<pid>-<sanitized session id> with mode 0755.
// The dir is created 0700: it holds a copy of a remote file being edited.
// The path is memoized so a session's temp file keeps one stable location for
// its whole life (single-instance editors are handed that path once and keep
// writing there, issue #737).
func ensureExtEditSessionDir(sessionID string) (string, error) {
	extEditMu.Lock()
	defer extEditMu.Unlock()
	if d, ok := extEditDirs[sessionID]; ok {
		return d, nil
	}
	root := extEditRootDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	// Tighten a root created by an older build (0755); best-effort because
	// a missing chmod must not block editing.
	_ = os.Chmod(root, 0700)
	d, err := os.MkdirTemp(root, fmt.Sprintf("%d-%s-*", os.Getpid(), sanitizePart(sessionID)))
	if err != nil {
		return "", err
	}
	extEditDirs[sessionID] = d
	return d, nil
}

// releaseExtEditSessionDir wipes a session's scratch dir and forgets it, so a
// later session with the same id gets a fresh (equally unpredictable) dir.
// Callers stop the session's watchers first, so nothing references its files.
func releaseExtEditSessionDir(sessionID string) {
	extEditMu.Lock()
	d := extEditDirs[sessionID]
	delete(extEditDirs, sessionID)
	extEditMu.Unlock()
	if d != "" {
		_ = os.RemoveAll(d)
	}
}

const (
	// Scratch dirs older than this are leftovers from a run that exited
	// without cleanup (crash / force-kill) and are swept at app startup.
	extEditStaleAge = 7 * 24 * time.Hour
)

// writeExtEditTemp writes the local temp copy for a remote file and returns
// its path. The preferred name is deterministic per (session, remote path)
// — reopening the same remote file reuses the same local path, so an editor
// that receives it through a single-instance handoff never finds it deleted
// underneath. The hash suffix sits BEFORE the extension so editors keep the
// extension for syntax highlighting. A stale copy may still be held open by
// an editor instance (its tab still open): overwrite-in-place then fails on
// Windows, so fall back to a unique suffixed name — this open then behaves
// like the old unique-name scheme instead of failing outright.
func writeExtEditTemp(dir, stem string, sum []byte, ext string, content []byte) (string, error) {
	tmp := filepath.Join(dir, fmt.Sprintf("%s-%x%s", stem, sum, ext))
	// 0600: the file is a copy of a remote file being edited and must not be
	// readable by other local users while it sits in the scratch dir.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		// Locked by a live editor copy: fall back to a unique name.
		tmp = filepath.Join(dir, stem+"-"+hex.EncodeToString(sum)+"-"+strconv.FormatUint(extEditRunSeq.Add(1), 10)+ext)
		f, err = os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return "", err
		}
	}
	_, werr := f.Write(content)
	cerr := f.Close()
	if werr != nil {
		return "", werr
	}
	if cerr != nil {
		return "", cerr
	}
	return tmp, nil
}

// externalEditorCommand resolves the external editor command Go-side from
// the persisted local state. The webview-supplied editorCmd argument of the
// editor bindings is kept in their signatures for compatibility but is
// deliberately IGNORED: a compromised renderer must not be able to exec an
// arbitrary program through them. The program part must additionally be a
// bare name (no path separators), so the setting can only launch something
// findable on PATH — never a dropped binary by absolute or relative path.
func (a *App) externalEditorCommand() (string, error) {
	if a.localStateStore == nil {
		return "", errors.New("no external editor configured")
	}
	ls, err := a.localStateStore.Load()
	if err != nil {
		return "", errors.New("no external editor configured")
	}
	cmd := strings.TrimSpace(ls.ExternalEditor)
	if cmd == "" {
		return "", errors.New("no external editor configured")
	}
	prog, _, err := splitCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("invalid external editor command: %w", err)
	}
	if prog != filepath.Base(prog) || strings.ContainsAny(prog, `/\`) {
		return "", fmt.Errorf("external editor must be a bare program name, got: %s", prog)
	}
	return cmd, nil
}

// SftpOpenExternalEditor opens a remote file in a configurable external editor
// and starts background auto-upload of changes back to the remote path.
// The third argument is kept for binding compatibility but ignored — the
// editor command is resolved Go-side from the persisted local state (see
// externalEditorCommand); the temp file path is appended as the final
// argument. Returns immediately once the editor has launched; progress is
// reported via "sftp:extedit" Wails events.
func (a *App) SftpOpenExternalEditor(sessionID, remotePath, _ string) error {
	editorCmd, err := a.externalEditorCommand()
	if err != nil {
		return err
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

	// Validate the command before any state is armed: a bad editor command
	// must leave no temp file, watcher or registered run behind.
	prog, args, err := splitCommand(editorCmd)
	if err != nil {
		return err
	}

	// Temp copy + auto-upload watcher, shared with the system "open with"
	// launch below — both flavours edit the same kind of temp file.
	tmp, runCtx, runKey, err := a.startExtEditWatcher(sessionID, remotePath, fs, content)
	if err != nil {
		return err
	}

	args = append(args, tmp)

	cmd := exec.CommandContext(runCtx, prog, args...)
	hideProcWindow(cmd)
	if err := cmd.Start(); err != nil {
		unregisterExternalEdit(sessionID, runKey)
		return fmt.Errorf("failed to start external editor %s: %w", prog, err)
	}

	a.emitExtEditStarted(sessionID, remotePath, tmp)
	return nil
}

// SftpOpenWithSystem opens a remote file through the OS "open with" flow —
// on Windows the system file-association picker pops up ("How do you want to
// open this file?"), on macOS/Linux the default handler is launched (no
// scriptable chooser exists there). Unlike SftpOpenExternalEditor there is no
// editor command and no binary-file restriction, so doc/xlsx-style files can
// be edited with the application the user picks. The same temp copy +
// auto-upload watcher as the external editor is armed, so whatever the picked
// application saves is pushed back to the remote path.
func (a *App) SftpOpenWithSystem(sessionID, remotePath string) error {
	fs, err := a.getSftp(sessionID)
	if err != nil {
		return err
	}
	content, err := fs.GetContent(remotePath)
	if err != nil {
		return err
	}
	tmp, _, runKey, err := a.startExtEditWatcher(sessionID, remotePath, fs, content)
	if err != nil {
		return err
	}
	if err := a.openWithSystem(tmp); err != nil {
		unregisterExternalEdit(sessionID, runKey)
		return err
	}
	a.emitExtEditStarted(sessionID, remotePath, tmp)
	return nil
}

// OpenWithSystemLocal opens a local file through the OS "open with" flow
// (system picker on Windows, default handler elsewhere). Used by the SFTP
// "local" pane: no temp copy and no auto-upload — the file already lives on
// disk and is edited in place.
func (a *App) OpenWithSystemLocal(localPath string) error {
	if strings.TrimSpace(localPath) == "" {
		return errors.New("empty local path")
	}
	// Runs the OS handler on the path (which executes .app/.desktop/.command
	// targets): only a granted path or one inside a session's browsed local
	// roots (the pane's own navigation) is accepted.
	if err := a.requireLocalPanePath(localPath); err != nil {
		return err
	}
	return a.openWithSystem(localPath)
}

// emitExtEditStarted pushes the "sftp:extedit" started event shared by both
// launch flavours; the frontend toasts from it.
func (a *App) emitExtEditStarted(sessionID, remotePath, tmp string) {
	a.emit("sftp:extedit", map[string]interface{}{
		"sessionId": sessionID,
		"path":      remotePath,
		"status":    "started",
		"tmp":       tmp,
	})
}

// startExtEditWatcher materializes the temp local copy of a remote file and
// arms the auto-upload watcher for it. Shared by SftpOpenExternalEditor and
// SftpOpenWithSystem, which differ only in how the temp file is opened. The
// returned runCtx cancels the run (killing an editor launched with it, see
// registerExternalEdit) and runKey unregisters it on launch failure.
func (a *App) startExtEditWatcher(sessionID, remotePath string, fs fileTransferSession, content []byte) (tmp string, runCtx context.Context, runKey string, err error) {
	dir, err := ensureExtEditSessionDir(sessionID)
	if err != nil {
		return "", nil, "", err
	}
	base := sanitizePart(path.Base(remotePath))
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	sum := sha256.Sum256([]byte(remotePath))
	tmp, err = writeExtEditTemp(dir, stem, sum[:4], ext, content)
	if err != nil {
		return "", nil, "", err
	}

	runKey = fmt.Sprintf("%s#%d", tmp, extEditRunSeq.Add(1))

	runCtx, cancel := context.WithCancel(context.Background())
	registerExternalEdit(sessionID, runKey, cancel)

	// One watcher per temp file: reopening the same remote file re-baselines
	// the existing watcher (the fresh download is already the remote content)
	// instead of stacking another one beside it.
	h := hashBytes(content)
	extEditMu.Lock()
	w := extEditWatchers[tmp]
	if w != nil {
		w.lastUploaded = h
	}
	extEditMu.Unlock()
	if w == nil {
		w = &extEditWatcher{lastUploaded: h}
		extEditMu.Lock()
		extEditWatchers[tmp] = w
		extEditMu.Unlock()
		go a.pollExternalEditor(runCtx, sessionID, fs, remotePath, tmp, w, content)
	}
	return tmp, runCtx, runKey, nil
}

// OpenExternalEditorLocal launches the configured external editor directly on
// a local file (used by the SFTP "local" pane). Unlike the remote flavour, no
// temp copy or auto-upload is involved: the file already lives on disk, so the
// editor edits it in place and the launch returns immediately. The second
// argument is kept for binding compatibility but ignored — the editor command
// comes from the persisted local state (see externalEditorCommand).
func (a *App) OpenExternalEditorLocal(localPath, _ string) error {
	cmd, err := a.externalEditorCommand()
	if err != nil {
		return err
	}
	// The path is a pane-local file: require it to be granted or inside a
	// session's browsed local roots before handing it to a program.
	if err := a.requireLocalPanePath(localPath); err != nil {
		return err
	}
	prog, args, err := splitCommand(cmd)
	if err != nil {
		return err
	}
	args = append(args, localPath)
	cmd2 := exec.Command(prog, args...)
	hideProcWindow(cmd2)
	if err := cmd2.Start(); err != nil {
		return fmt.Errorf("failed to start external editor %s: %w", prog, err)
	}
	go cmd2.Wait()
	return nil
}

// pollExternalEditor watches the temp file until the session ends, pushing
// changes back to the remote path (debounced live uploads). The editor's
// lifetime is deliberately NOT tracked: the watcher runs for the whole
// session, so changes land on the remote no matter how often the editor is
// opened and closed — including through single-instance editor hand-offs
// (issue #737), which is why the temp file also stays put while editing.
func (a *App) pollExternalEditor(runCtx context.Context, sessionID string,
	fs fileTransferSession, remotePath, tmp string, w *extEditWatcher, initial []byte) {

	defer func() {
		extEditMu.Lock()
		if extEditWatchers[tmp] == w {
			delete(extEditWatchers, tmp)
		}
		extEditMu.Unlock()
	}()

	lastUploaded := w.lastUploaded
	var dirty bool
	var lastSeenMtime time.Time
	poll := time.NewTicker(400 * time.Millisecond)
	defer poll.Stop()

	for {
		select {
		case <-runCtx.Done():
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
			dirty = false
			if h := hashBytes(b); h != lastUploaded {
				// Update the shared baseline under the registry lock so a
				// concurrent watcher of the same temp file can never push a
				// duplicate upload of identical content.
				extEditMu.Lock()
				if w.lastUploaded == h {
					extEditMu.Unlock()
					lastUploaded = h
					continue
				}
				perr := fs.PutContent(remotePath, b)
				if perr == nil {
					w.lastUploaded = h
					extEditMu.Unlock()
					lastUploaded = h
					a.emitExtEdit(sessionID, remotePath, "uploaded")
				} else {
					extEditMu.Unlock()
				}
			}
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

// registerExternalEdit records one edit run (keyed by a unique per-run key,
// since several runs may watch the same temp file).
func registerExternalEdit(sessionID, runKey string, cancel context.CancelFunc) {
	extEditMu.Lock()
	defer extEditMu.Unlock()
	if extEditSessions[sessionID] == nil {
		extEditSessions[sessionID] = map[string]context.CancelFunc{}
	}
	extEditSessions[sessionID][runKey] = cancel
}

func unregisterExternalEdit(sessionID, runKey string) {
	extEditMu.Lock()
	defer extEditMu.Unlock()
	if m, ok := extEditSessions[sessionID]; ok {
		delete(m, runKey)
		if len(m) == 0 {
			delete(extEditSessions, sessionID)
		}
	}
}

// cancelExternalEdits stops every active external edit for a session (called
// when the session is unregistered / closed), killing editor processes and
// wiping the session's temp scratch dir. The dir name is unique per session,
// so concurrent app instances never clash.
func cancelExternalEdits(sessionID string) {
	extEditMu.Lock()
	for _, cancel := range extEditSessions[sessionID] {
		cancel()
	}
	delete(extEditSessions, sessionID)
	extEditMu.Unlock()
	// Best-effort wipe of the session's scratch dir — no watcher references
	// its files anymore.
	releaseExtEditSessionDir(sessionID)
}

// cleanupExtEditsOnExit deletes every external-edit scratch dir belonging to
// this process, identified by the PID prefix of the dir name. Best-effort:
// a file still held open by an editor fails the removal and is left for the
// startup age sweep.
func cleanupExtEditsOnExit() {
	pidPrefix := fmt.Sprintf("%d-", os.Getpid())
	entries, err := os.ReadDir(extEditRootDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), pidPrefix) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(extEditRootDir(), e.Name()))
	}
}

// sweepStaleExtEditDirs removes external-edit scratch dirs left behind by
// previous runs that exited without cleanup (crash / force-kill). Dirs newer
// than extEditStaleAge are kept — another concurrently running instance may
// still be using them. Runs once at app startup.
func sweepStaleExtEditDirs() {
	entries, err := os.ReadDir(extEditRootDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < extEditStaleAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(extEditRootDir(), e.Name()))
	}
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
