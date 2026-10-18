package session

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/studio-b12/gowebdav"
)

// webdavClient is the subset of *gowebdav.Client the WebDAV session calls.
// The field is an interface so tests can inject a fake client (security.md
// 文件会话 Medium: Disconnect used to nil the client out from under running
// transfers).
type webdavClient interface {
	ReadDir(path string) ([]os.FileInfo, error)
	Stat(path string) (os.FileInfo, error)
	Mkdir(path string, perm os.FileMode) error
	MkdirAll(path string, perm os.FileMode) error
	Remove(path string) error
	RemoveAll(path string) error
	Rename(oldpath, newpath string, overwrite bool) error
	Copy(oldpath, newpath string, overwrite bool) error
	Read(path string) ([]byte, error)
	Write(path string, data []byte, perm os.FileMode) error
	ReadStream(path string) (io.ReadCloser, error)
	WriteStream(path string, stream io.Reader, perm os.FileMode) error
}

type WebDAVSession struct {
	baseSession
	localFSOps
	client    webdavClient
	cwd       string
	mu        sync.RWMutex
	transfers map[string]*TransferTask
	taskSeq   int64
}

func NewWebDAVSession(id string) *WebDAVSession {
	return &WebDAVSession{
		baseSession: baseSession{
			id:          id,
			sessionType: "webdav",
			status:      StatusDisconnected,
		},
		localFSOps: newLocalFSOps(),
		cwd:        "/",
		transfers:  make(map[string]*TransferTask),
	}
}

func (s *WebDAVSession) Connect(config ConnectionConfig) error {
	// The sync password (and any credentials) rides on every request; a
	// cleartext non-loopback endpoint would send it unencrypted. Reject
	// before dialing or creating any client state.
	if err := validateWebDAVServerURL(config.Host); err != nil {
		s.setStatus(StatusError)
		return err
	}
	s.setStatus(StatusConnecting)

	url := strings.TrimSuffix(config.Host, "/")
	s.title = fmt.Sprintf("%s@%s", config.User, url)

	client := gowebdav.NewClient(url, config.User, config.Password)
	if err := client.Connect(); err != nil {
		s.setStatus(StatusError)
		return fmt.Errorf("webdav connect: %w", err)
	}

	s.mu.Lock()
	s.client = client
	s.cwd = "/"
	s.mu.Unlock()
	s.setStatus(StatusConnected)
	return nil
}

// validateWebDAVServerURL enforces an encrypted transport: https:// is
// always accepted, plain http:// only for loopback hosts, any other scheme
// is rejected outright.
func validateWebDAVServerURL(serverURL string) error {
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil {
		return fmt.Errorf("webdav: invalid server url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHostname(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("webdav server must use https:// (password would be sent unencrypted), got %q", serverURL)
	default:
		return fmt.Errorf("webdav server must use https://, got %s:// scheme (%q)", u.Scheme, serverURL)
	}
}

// isLoopbackHostname reports whether host is "localhost" or a loopback IP.
func isLoopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *WebDAVSession) Write(data []byte) error     { return nil }
func (s *WebDAVSession) Resize(cols, rows int) error { return nil }

// cancelAllTransfers cancels every tracked transfer so its worker unwinds
// through the existing task.ctx.Err() branches (finishing as "cancelled")
// instead of touching a client that Disconnect has already dropped.
// Callers must not hold s.mu.
func (s *WebDAVSession) cancelAllTransfers() {
	s.mu.RLock()
	tasks := make([]*TransferTask, 0, len(s.transfers))
	for _, t := range s.transfers {
		tasks = append(tasks, t)
	}
	s.mu.RUnlock()
	for _, t := range tasks {
		if t.cancel != nil {
			t.cancel()
		}
	}
}

func (s *WebDAVSession) Disconnect() error {
	// Cancel running transfers BEFORE dropping the client (security.md
	// 文件会话 Medium: Disconnect racing a download used to leave it reporting
	// success/error instead of cancelled, and could nil-deref the client).
	s.cancelAllTransfers()
	s.mu.Lock()
	s.client = nil
	s.mu.Unlock()
	s.setStatus(StatusDisconnected)
	return nil
}

func (s *WebDAVSession) IsConnected() bool {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	return s.Status() == StatusConnected && client != nil
}

// requireClient returns the WebDAV client, or an error when the session is
// not connected. Callers must use only the returned reference for the whole
// operation: Disconnect may clear s.client at any time (security.md 文件会话
// Medium).
func (s *WebDAVSession) requireClient() (webdavClient, error) {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		return nil, fmt.Errorf("WebDAV session not connected")
	}
	return client, nil
}

func (s *WebDAVSession) resolveRemote(p string) (string, error) {
	if p == "" {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.cwd, nil
	}
	if path.IsAbs(p) {
		return p, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return path.Join(s.cwd, p), nil
}

func (s *WebDAVSession) nextTaskID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&s.taskSeq, 1))
}

func (s *WebDAVSession) ListRemote(dir string) (FileListResult, error) {
	client, err := s.requireClient()
	if err != nil {
		return FileListResult{}, err
	}
	target, err := s.resolveRemote(dir)
	if err != nil {
		return FileListResult{}, err
	}
	entries, err := client.ReadDir(target)
	if err != nil {
		return FileListResult{}, err
	}
	files := make([]FileItem, 0, len(entries))
	for _, e := range entries {
		modTime := ""
		if !e.ModTime().IsZero() {
			modTime = e.ModTime().Format(time.RFC3339)
		}
		files = append(files, FileItem{
			Name:    e.Name(),
			Size:    e.Size(),
			ModTime: modTime,
			Mode:    e.Mode().String(),
			IsDir:   e.IsDir(),
		})
	}
	return FileListResult{Files: files, Dir: target}, nil
}

func (s *WebDAVSession) ChangeRemoteDir(dir string) (FileListResult, error) {
	client, err := s.requireClient()
	if err != nil {
		return FileListResult{}, err
	}
	target, err := s.resolveRemote(dir)
	if err != nil {
		return FileListResult{}, err
	}
	// ReadDir doubles as an existence + is-dir check, and gowebdav's
	// internal PROPFIND handles trailing-slash normalization that
	// Stat() does not (Apache mod_dav returns 301 for /dir without a
	// slash, which gowebdav treats as an error).
	entries, err := client.ReadDir(target)
	if err != nil {
		return FileListResult{}, err
	}
	s.mu.Lock()
	s.cwd = target
	s.mu.Unlock()
	files := make([]FileItem, 0, len(entries))
	for _, e := range entries {
		modTime := ""
		if !e.ModTime().IsZero() {
			modTime = e.ModTime().Format(time.RFC3339)
		}
		files = append(files, FileItem{
			Name:    e.Name(),
			Size:    e.Size(),
			ModTime: modTime,
			Mode:    e.Mode().String(),
			IsDir:   e.IsDir(),
		})
	}
	return FileListResult{Files: files, Dir: target}, nil
}

// Symlink is not supported: WebDAV has no link resource type.
func (s *WebDAVSession) Symlink(_, _ string) error {
	return fmt.Errorf("symlink is not supported by WebDAV")
}

func (s *WebDAVSession) MakeDir(dir string) error {
	client, err := s.requireClient()
	if err != nil {
		return err
	}
	p, err := s.resolveRemote(dir)
	if err != nil {
		return err
	}
	return client.Mkdir(p, 0755)
}

func (s *WebDAVSession) Remove(p string, recursive bool) error {
	client, err := s.requireClient()
	if err != nil {
		return err
	}
	target, err := s.resolveRemote(p)
	if err != nil {
		return err
	}
	if recursive {
		return client.RemoveAll(target)
	}
	fi, err := client.Stat(target)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		entries, err := client.ReadDir(target)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("directory not empty (%d items)", len(entries))
		}
	}
	return client.Remove(target)
}

func (s *WebDAVSession) Rename(oldName, newName string) error {
	client, err := s.requireClient()
	if err != nil {
		return err
	}
	old, err := s.resolveRemote(oldName)
	if err != nil {
		return err
	}
	n, err := s.resolveRemote(newName)
	if err != nil {
		return err
	}
	return client.Rename(old, n, false)
}

func (s *WebDAVSession) Chmod(p string, mode os.FileMode) error {
	return fmt.Errorf("WebDAV does not support chmod")
}

func (s *WebDAVSession) Copy(oldPath, newPath string) error {
	client, err := s.requireClient()
	if err != nil {
		return err
	}
	old, err := s.resolveRemote(oldPath)
	if err != nil {
		return err
	}
	n, err := s.resolveRemote(newPath)
	if err != nil {
		return err
	}
	return client.Copy(old, n, false)
}

func (s *WebDAVSession) Move(oldPath, newPath string) error {
	return s.Rename(oldPath, newPath)
}

func (s *WebDAVSession) GetContent(remotePath string) ([]byte, error) {
	client, err := s.requireClient()
	if err != nil {
		return nil, err
	}
	p, err := s.resolveRemote(remotePath)
	if err != nil {
		return nil, err
	}
	return client.Read(p)
}

func (s *WebDAVSession) PutContent(remotePath string, content []byte) error {
	client, err := s.requireClient()
	if err != nil {
		return err
	}
	p, err := s.resolveRemote(remotePath)
	if err != nil {
		return err
	}
	parentDir := path.Dir(p)
	if err := client.MkdirAll(parentDir, 0755); err != nil {
		return err
	}
	return client.Write(p, content, 0644)
}

func (s *WebDAVSession) Get(remotePath, localPath string, recursive bool) (string, error) {
	client, err := s.requireClient()
	if err != nil {
		return "", err
	}
	rp, err := s.resolveRemote(remotePath)
	if err != nil {
		return "", err
	}
	lp := localPath
	if !filepath.IsAbs(lp) {
		lp = filepath.Join(s.localCwd, lp)
	}
	task := &TransferTask{
		ID:         s.nextTaskID("dl"),
		Type:       "download",
		LocalPath:  lp,
		RemotePath: rp,
		Status:     "running",
	}
	task.start()
	s.mu.Lock()
	s.transfers[task.ID] = task
	s.mu.Unlock()
	s.emitTransferStart(task)
	go func() {
		defer func() {
			task.done()
			if task.Status == "error" {
				// Retained in s.transfers for retry; the frontend drops it
				// via DismissTransfer.
				return
			}
			s.mu.Lock()
			delete(s.transfers, task.ID)
			s.mu.Unlock()
		}()
		var err error
		if recursive {
			err = s.downloadDir(client, rp, lp, task)
		} else {
			err = s.downloadFile(client, task, rp, lp)
		}
		if err != nil {
			if task.ctx.Err() != nil {
				// cancelled mid-transfer: not a transfer error
				task.Status = "cancelled"
				s.emitTransferComplete(task)
				return
			}
			s.emitTransferEvent(task, err)
			return
		}
		task.Status = "done"
		s.emitTransferComplete(task)
	}()
	return task.ID, nil
}

func (s *WebDAVSession) Put(localPath, remotePath string, recursive bool) (string, error) {
	client, err := s.requireClient()
	if err != nil {
		return "", err
	}
	lp := localPath
	if !filepath.IsAbs(lp) {
		lp = filepath.Join(s.localCwd, lp)
	}
	rp, err := s.resolveRemote(remotePath)
	if err != nil {
		return "", err
	}
	task := &TransferTask{
		ID:         s.nextTaskID("ul"),
		Type:       "upload",
		LocalPath:  lp,
		RemotePath: rp,
		Status:     "running",
	}
	task.start()
	s.mu.Lock()
	s.transfers[task.ID] = task
	s.mu.Unlock()
	s.emitTransferStart(task)
	go func() {
		defer func() {
			task.done()
			if task.Status == "error" {
				// Retained in s.transfers for retry; the frontend drops it
				// via DismissTransfer.
				return
			}
			s.mu.Lock()
			delete(s.transfers, task.ID)
			s.mu.Unlock()
		}()
		var err error
		if recursive {
			err = s.uploadDir(client, lp, rp, task)
		} else {
			err = s.uploadFile(client, task, lp, rp)
		}
		if err != nil {
			if task.ctx.Err() != nil {
				// cancelled mid-transfer: not a transfer error
				task.Status = "cancelled"
				s.emitTransferComplete(task)
				return
			}
			s.emitTransferEvent(task, err)
			return
		}
		task.Status = "done"
		s.emitTransferComplete(task)
	}()
	return task.ID, nil
}

// RetryTransfer (re)starts a transfer from a frontend-held checkpoint.
// resolveRemote is idempotent on the resolved paths the spec carries, so the
// retry goes through Get/Put like the original request. skipCompleted is
// ignored — the transfer restarts from scratch.
func (s *WebDAVSession) RetryTransfer(spec TransferSpec, skipCompleted []string) (string, error) {
	if _, err := s.requireClient(); err != nil {
		return "", err
	}
	if spec.Type == "download" {
		return s.Get(spec.RemotePath, spec.LocalPath, spec.Recursive)
	}
	return s.Put(spec.LocalPath, spec.RemotePath, spec.Recursive)
}

// DismissTransfer drops a retained (failed) task from the transfers map.
func (s *WebDAVSession) DismissTransfer(taskID string) error {
	s.mu.Lock()
	delete(s.transfers, taskID)
	s.mu.Unlock()
	return nil
}

func (s *WebDAVSession) CancelTransfer(taskID string) error {
	s.mu.Lock()
	task, ok := s.transfers[taskID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	if task.cancel != nil {
		task.cancel()
	}
	return nil
}

func (s *WebDAVSession) PauseTransfer(taskID string) error {
	s.mu.Lock()
	task, ok := s.transfers[taskID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	task.setPaused(true)
	task.Status = "paused"
	s.emitTransferComplete(task)
	return nil
}

func (s *WebDAVSession) ResumeTransfer(taskID string) error {
	s.mu.Lock()
	task, ok := s.transfers[taskID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	task.setPaused(false)
	task.Status = "running"
	close(task.pauseCh)
	task.pauseCh = make(chan struct{})
	s.emitTransferStart(task)
	return nil
}

// calcWebdavRemoteDirSize recursively sums up all file sizes under a remote WebDAV directory.
func (s *WebDAVSession) calcWebdavRemoteDirSize(client webdavClient, remoteDir string) (int64, error) {
	var total int64
	entries, err := client.ReadDir(remoteDir)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		// Servers may list "." and ".."; recursing on them never terminates.
		if name := e.Name(); name == "." || name == ".." {
			continue
		}
		if e.IsDir() {
			sub, err := s.calcWebdavRemoteDirSize(client, path.Join(remoteDir, e.Name()))
			if err != nil {
				return 0, err
			}
			total += sub
		} else {
			total += e.Size()
		}
	}
	return total, nil
}

func (s *WebDAVSession) downloadDir(client webdavClient, remoteDir, localDir string, task *TransferTask) error {
	if client == nil {
		return fmt.Errorf("WebDAV session not connected")
	}
	// Calculate total size for progress tracking
	if task.loadTotal() <= 0 {
		if total, err := s.calcWebdavRemoteDirSize(client, remoteDir); err == nil {
			task.setTotal(total)
		}
	}
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return err
	}
	entries, err := client.ReadDir(remoteDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		select {
		case <-task.ctx.Done():
			return task.ctx.Err()
		default:
		}
		// Skip listing noise first (a "." entry would join onto localDir
		// itself), then require the server-supplied name to be a plain
		// component that stays under localDir (security.md P0: WebDAV
		// directory downloads join names unchecked — "..\x" escapes on
		// Windows).
		name := e.Name()
		if name == "." || name == ".." {
			continue
		}
		rp := path.Join(remoteDir, name)
		lp, jerr := safeDownloadChild(localDir, name)
		if jerr != nil {
			return jerr
		}
		if e.IsDir() {
			if err := s.downloadDir(client, rp, lp, task); err != nil {
				return err
			}
		} else {
			if err := s.downloadFile(client, task, rp, lp); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *WebDAVSession) downloadFile(client webdavClient, task *TransferTask, remotePath, localPath string) error {
	if client == nil {
		return fmt.Errorf("WebDAV session not connected")
	}
	// Get file size first for progress tracking
	if task.loadTotal() <= 0 {
		if fi, err := client.Stat(remotePath); err == nil {
			if fi.Size() > 0 {
				task.setTotal(fi.Size())
			}
		}
	}

	rc, err := client.ReadStream(remotePath)
	if err != nil {
		return err
	}
	defer rc.Close()
	// Write through a same-directory .part file so a failed or cancelled
	// transfer never leaves a truncated file under the final name and never
	// destroys the file that was already there (security.md 文件会话 Low).
	return writePartFile(localPath, func(dst *os.File) error {
		// The shared stream copy returns write errors and non-EOF read errors
		// (the old loop dropped dst.Write's error, so a full disk left a short
		// file reported as completed) and surfaces the Close error too.
		return copyDownloadStream(dst, rc, task, func() { s.emitTransferProgress(task) })
	})
}

func (s *WebDAVSession) uploadDir(client webdavClient, localDir, remoteDir string, task *TransferTask) error {
	if client == nil {
		return fmt.Errorf("WebDAV session not connected")
	}
	// Calculate total size for progress tracking
	if task.loadTotal() <= 0 {
		if total, err := calcLocalDirSize(localDir); err == nil {
			task.setTotal(total)
		}
	}

	if err := client.MkdirAll(remoteDir, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		select {
		case <-task.ctx.Done():
			return task.ctx.Err()
		default:
		}
		lp := filepath.Join(localDir, entry.Name())
		rp := path.Join(remoteDir, entry.Name())
		if entry.IsDir() {
			if err := s.uploadDir(client, lp, rp, task); err != nil {
				return err
			}
		} else {
			if err := s.uploadFile(client, task, lp, rp); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *WebDAVSession) uploadFile(client webdavClient, task *TransferTask, localPath, remotePath string) error {
	if client == nil {
		return fmt.Errorf("WebDAV session not connected")
	}
	src, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return err
	}
	// Set total size from file stat for progress tracking
	if task.loadTotal() <= 0 && fi.Size() > 0 {
		task.setTotal(fi.Size())
	}
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.WriteStream(remotePath, pr, 0644)
	}()
	buf := make([]byte, 64*1024)
	totalWritten := int64(0)
	for {
		select {
		case <-task.ctx.Done():
			pw.CloseWithError(task.ctx.Err())
			return task.ctx.Err()
		default:
		}
		task.waitIfPaused()
		n, e := src.Read(buf)
		if n > 0 {
			if _, we := pw.Write(buf[:n]); we != nil {
				pw.CloseWithError(we)
				return we
			}
			totalWritten += int64(n)
			task.addProgress(int64(n))
			s.emitTransferProgress(task)
		}
		if e != nil {
			if e == io.EOF {
				pw.Close()
				break
			}
			pw.CloseWithError(e)
			return e
		}
	}
	return <-errCh
}
