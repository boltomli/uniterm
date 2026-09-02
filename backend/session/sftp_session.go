package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	osUser "os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type SFTPSession struct {
	baseSession
	sshClient  *ssh.Client
	sftpClient *sftp.Client
	cwd        string
	localCwd   string
	mu         sync.RWMutex
	transfers  map[string]*TransferTask
	taskSeq    int64
	sem        chan struct{} // concurrency limiter, nil = unlimited

	// UID/GID -> name cache read from the remote /etc/passwd and /etc/group
	// (issue #702). Loaded once on Connect; a missing/unreadable entry falls
	// back to the numeric id.
	userMapOnce sync.Once
	userMap     map[int]string
	groupMap    map[int]string
}

func NewSFTPSession(id string) *SFTPSession {
	homeDir, _ := os.UserHomeDir()
	return &SFTPSession{
		baseSession: baseSession{
			id:          id,
			sessionType: "sftp",
			status:      StatusDisconnected,
		},
		cwd:       "/",
		localCwd:  homeDir,
		transfers: make(map[string]*TransferTask),
	}
}

// SetMaxConcurrency limits concurrent file transfers. n <= 0 means unlimited.
func (s *SFTPSession) SetMaxConcurrency(n int) {
	if n > 0 {
		s.sem = make(chan struct{}, n)
	}
}

func (s *SFTPSession) Connect(config ConnectionConfig) error {
	s.setStatus(StatusConnecting)
	s.title = fmt.Sprintf("%s@%s", config.User, config.Host)

	authMethods, err := buildAuthMethods(config)
	if err != nil {
		s.setStatus(StatusError)
		return err
	}

	clientConfig := &ssh.ClientConfig{
		User:            config.User,
		Auth:            authMethods,
		Timeout:         30 * time.Second,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Config: sshAlgorithms(),
	}

	addr := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	client, err := dialSSHTCP(addr, clientConfig, config.Proxy)
	if err != nil {
		s.setStatus(StatusError)
		return fmt.Errorf("ssh dial: %w", err)
	}

	sc, err := sftp.NewClient(client)
	if err != nil {
		// subsystem 启动失败：若是协议流被污染(登录脚本打印)，像 MobaXterm 那样
		// fallback 到 exec sftp-server 并跳过噪声；仍失败再抛可操作提示。
		sc2, ferr := trySFTPExecFallback(client, err)
		if ferr == nil {
			sc = sc2
		} else {
			client.Close()
			s.setStatus(StatusError)
			return fmt.Errorf("sftp client: %w", hintSFTPStartupError(err, ferr))
		}
	}

	go func() {
		_ = client.Wait()
		s.Disconnect()
	}()

	s.sshClient = client
	s.sftpClient = sc
	// Preload remote user/group name maps so list owners show names, not numbers.
	s.loadUserGroupMaps()
	if wd, err := sc.Getwd(); err == nil {
		s.cwd = wd
	}
	s.setStatus(StatusConnected)

	return nil
}

// isSFTPStreamPolluted 判断错误是否为“SFTP 协议流被服务器输出污染”那一类。
func isSFTPStreamPolluted(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "packet too long") ||
		strings.Contains(msg, "message too long") ||
		strings.Contains(msg, "version packet")
}

// sftpServerPaths 是各发行版 sftp-server 二进制的常见位置。
// 逗号分隔的 shell 表达式：找到第一个存在的就 exec 它。
var sftpServerPaths = []string{
	"/usr/libexec/sftp-server",     // RHEL/CentOS/Fedora、macOS
	"/usr/lib/openssh/sftp-server", // Debian/Ubuntu
	"/usr/lib/ssh/sftp-server",     // Arch、SUSE
	"/usr/libexec/openssh/sftp-server",
	"/usr/lib/sftp-server",
}

// sftpFallbackMarker 是插在噪声与 SFTP 协议流之间的唯一分隔标记。
// 登录脚本的打印发生在 marker 之前，客户端读到 marker 后丢弃前面全部垃圾，
// 再把干净的剩余流交给 SFTP 解析——这样无论 profile/bashrc/BASH_ENV/~/.ssh/rc
// 怎么打印都不影响。
const sftpFallbackMarker = "__UNITERM_SFTP_BEGIN__"

// buildSFTPExecCommand 生成一条 shell 命令：先在 PATH 与常见路径里找到 sftp-server，
// 打印 marker，然后 exec 它接管 stdio。找不到则以非零码退出。
func buildSFTPExecCommand() string {
	// 候选：先查 PATH，再逐个常见绝对路径。
	candidates := append([]string{"$(command -v sftp-server 2>/dev/null)"}, sftpServerPaths...)
	var b strings.Builder
	b.WriteString("s=''; for p in ")
	b.WriteString(strings.Join(candidates, " "))
	b.WriteString("; do if [ -x \"$p\" ]; then s=\"$p\"; break; fi; done; ")
	b.WriteString("[ -n \"$s\" ] || exit 127; ")
	// marker 后紧跟换行，printf 到 stdout；随后 exec 覆盖当前进程，之后 stdout 全是 SFTP 协议。
	b.WriteString("printf '" + sftpFallbackMarker + "\\n'; exec \"$s\"")
	return b.String()
}

// trySFTPExecFallback 在 subsystem 方式失败(且属于协议流被污染)时，
// 改为 exec sftp-server，并跳过 stdout 上 marker 之前的全部噪声，
// 再用管道接管，绕过被登录脚本污染的协议流。
func trySFTPExecFallback(client *ssh.Client, subsystemErr error) (*sftp.Client, error) {
	if !isSFTPStreamPolluted(subsystemErr) {
		return nil, subsystemErr
	}
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("fallback new session: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("fallback stdin: %w", err)
	}
	stdoutRaw, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("fallback stdout: %w", err)
	}
	if err := sess.Start(buildSFTPExecCommand()); err != nil {
		sess.Close()
		return nil, fmt.Errorf("fallback exec sftp-server: %w", err)
	}
	// 跳过 marker 之前的全部噪声，把游标停在 SFTP 协议第一个字节。
	clean := bufio.NewReader(stdoutRaw)
	if err := skipToMarker(clean, sftpFallbackMarker); err != nil {
		sess.Close()
		return nil, fmt.Errorf("fallback marker: %w", err)
	}
	sc, err := sftp.NewClientPipe(clean, stdin)
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("fallback sftp handshake: %w", err)
	}
	return sc, nil
}

// skipToMarker 从 r 逐行读，丢弃直到读到含 marker 的那一行为止（含该行）。
// 之后 r 的读游标停在 marker 行的换行符之后，即 SFTP 协议流起点。
func skipToMarker(r *bufio.Reader, marker string) error {
	for {
		line, err := r.ReadString('\n')
		if strings.Contains(line, marker) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("未在服务器输出中找到 SFTP 起始标记（sftp-server 可能不存在或被拒绝执行）")
		}
	}
}

// hintSFTPStartupError 把 sftp 子系统启动失败的底层错误翻成可操作的提示。
// 最常见的是 packet too long：SSH 通了、认证也过了，但服务器登录脚本
// (/etc/profile、~/.bashrc、~/.ssh/rc 等) 在非交互会话里往 stdout 打印了内容，
// 污染了 SFTP 协议流。subErr 是 subsystem 方式的原始错误，fbErr 是 exec fallback
// 的失败原因(便于诊断到底卡在哪一步)。
func hintSFTPStartupError(subErr, fbErr error) error {
	if isSFTPStreamPolluted(subErr) {
		return fmt.Errorf("服务器 SFTP 子系统启动失败：登录脚本在非交互会话中产生了输出，"+
			"污染了 SFTP 协议流；已尝试自动 fallback 到 exec sftp-server 但未成功。"+
			"请清理服务器上 /etc/profile、/etc/profile.d/*、~/.bashrc、~/.ssh/rc、/etc/ssh/sshrc "+
			"以及 $BASH_ENV 指向的脚本里向 stdout 的打印（用 `case $- in *i*)` 判断仅交互 shell 才输出）。"+
			"subsystem 错误：%v；fallback 错误：%w", subErr, fbErr)
	}
	if fbErr != nil {
		return fbErr
	}
	return subErr
}

func (s *SFTPSession) Write(data []byte) error {
	return nil
}

func (s *SFTPSession) Resize(cols, rows int) error {
	return nil
}

func (s *SFTPSession) Disconnect() error {
	if s.sftpClient != nil {
		s.sftpClient.Close()
	}
	if s.sshClient != nil {
		s.sshClient.Close()
	}
	s.setStatus(StatusDisconnected)
	return nil
}

func (s *SFTPSession) IsConnected() bool {
	return s.Status() == StatusConnected
}

// FileItem represents a file entry returned to the frontend.
type FileItem struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
	Mode    string `json:"mode"`
	IsDir   bool   `json:"isDir"`
	IsHidden bool  `json:"isHidden"`
	Owner   string `json:"owner"`
	Group   string `json:"group"`
}

// FileListResult wraps files + current directory for a list response.
type FileListResult struct {
	Files []FileItem `json:"files"`
	Dir   string     `json:"dir"`
}

// TransferTask tracks an ongoing file transfer.
type TransferTask struct {
	ID         string
	Type       string // "upload" | "download"
	LocalPath  string
	RemotePath string
	Progress   int64
	Total      int64
	Status     string // "pending" | "running" | "paused" | "done" | "error" | "cancelled"
	ctx        context.Context
	cancel     context.CancelFunc
	paused     bool
	pauseCh    chan struct{}
}

func (t *TransferTask) start() {
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.pauseCh = make(chan struct{})
}

func (t *TransferTask) done() {
	if t.cancel != nil {
		t.cancel()
	}
}

func (t *TransferTask) waitIfPaused() {
	for {
		if t.paused {
			select {
			case <-t.pauseCh:
				continue
			case <-t.ctx.Done():
				return
			}
		}
		return
	}
}

func (s *SFTPSession) nextTaskID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&s.taskSeq, 1))
}

// --- UID/GID -> name resolution (issue #702) --------------------------------
// A remote listing only yields numeric UID/GID, so owners render as "1000".
// Translate them to names by reading the remote /etc/passwd and /etc/group
// over SFTP once, cached for this connection. Unreadable or unresolved entries
// fall back to the numeric value.

func (s *SFTPSession) loadUserGroupMaps() {
	s.userMapOnce.Do(func() {
		users := map[int]string{}
		groups := map[int]string{}
		if s.sftpClient != nil {
			if data := s.readRemoteText("/etc/passwd"); data != nil {
				parseLinuxNameMap(data, users, 2)
			}
			if data := s.readRemoteText("/etc/group"); data != nil {
				parseLinuxNameMap(data, groups, 2)
			}
		}
		s.userMap = users
		s.groupMap = groups
	})
}

// readRemoteText reads a small remote file over SFTP, returning nil on failure.
func (s *SFTPSession) readRemoteText(p string) []byte {
	f, err := s.sftpClient.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	return data
}

// parseLinuxNameMap parses `/etc/passwd`-style lines (`name:x:<id>:...`) into
// an id->name map. `idIndex` points at the numeric id field.
func parseLinuxNameMap(data []byte, out map[int]string, idIndex int) {
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) < idIndex+1 || parts[0] == "" {
			continue
		}
		id, err := strconv.Atoi(parts[idIndex])
		if err != nil || id < 0 {
			continue
		}
		if _, exists := out[id]; !exists {
			out[id] = parts[0]
		}
	}
}

func (s *SFTPSession) resolveOwnerGroup(fi os.FileInfo) (string, string) {
	stat, ok := fi.Sys().(*sftp.FileStat)
	if !ok {
		return "", ""
	}
	owner := ""
	// UID/GID 0 is root, so resolve unconditionally (a > 0 guard would hide it).
	if name, ok := s.userMap[int(stat.UID)]; ok {
		owner = name
	} else {
		owner = fmt.Sprintf("%d", stat.UID)
	}
	group := ""
	if name, ok := s.groupMap[int(stat.GID)]; ok {
		group = name
	} else {
		group = fmt.Sprintf("%d", stat.GID)
	}
	return owner, group
}

// --- Public API methods (called from app.go Wails bindings) ---

func (s *SFTPSession) requireClient() error {
	if s.sftpClient == nil {
		return fmt.Errorf("SFTP session not connected")
	}
	return nil
}

func (s *SFTPSession) ListRemote(dir string) (FileListResult, error) {
	type outcome struct {
		res FileListResult
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := s.listRemoteUnlocked(dir)
		ch <- outcome{res, err}
	}()
	select {
	case out := <-ch:
		return out.res, out.err
	case <-time.After(25 * time.Second):
		return FileListResult{}, fmt.Errorf("list remote timeout")
	}
}

func (s *SFTPSession) listRemoteUnlocked(dir string) (FileListResult, error) {
	if err := s.requireClient(); err != nil {
		return FileListResult{}, err
	}
	if dir == "" {
		dir = s.cwd
	} else if !path.IsAbs(dir) {
		dir = path.Join(s.cwd, dir)
	}
	infos, err := s.sftpClient.ReadDir(dir)
	if err != nil {
		return FileListResult{}, err
	}
	files := make([]FileItem, 0, len(infos))
	for _, fi := range infos {
		owner, group := s.resolveOwnerGroup(fi)
		isDir := fi.IsDir()
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, err := s.sftpClient.Stat(path.Join(dir, fi.Name())); err == nil {
				isDir = target.IsDir()
			}
		}
		isHidden := fi.Name() != "" && fi.Name()[0] == '.'
		if stat, ok := fi.Sys().(*sftp.FileStat); ok {
			for _, ext := range stat.Extended {
				if ext.ExtType == "win32-file-attributes" {
					if attrs, err := strconv.ParseInt(ext.ExtData, 0, 32); err == nil {
						isHidden = attrs&0x2 != 0 // FILE_ATTRIBUTE_HIDDEN
					}
				}
			}
		}
		files = append(files, FileItem{
			Name:     fi.Name(),
			Size:     fi.Size(),
			ModTime:  fi.ModTime().Format(time.RFC3339),
			Mode:     fi.Mode().String(),
			IsDir:    isDir,
			IsHidden: isHidden,
			Owner:    owner,
			Group:    group,
		})
	}
	return FileListResult{Files: files, Dir: dir}, nil
}

func (s *SFTPSession) ListLocal(dir string) (FileListResult, error) {
	if dir == "" {
		dir = s.localCwd
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.localCwd, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return FileListResult{}, err
	}
	files := make([]FileItem, 0, len(entries))
	for _, e := range entries {
		fi, _ := e.Info()
		var size int64
		var mode os.FileMode
		var modTime time.Time
		if fi != nil {
			size = fi.Size()
			mode = fi.Mode()
			modTime = fi.ModTime()
		}
		owner := ""
		// Windows reports the owner as "COMPUTERNAME\user", which is verbose in
		// the owner column; keep only the user part.
		if currentUser, err := osUser.Current(); err == nil {
			if i := strings.LastIndexByte(currentUser.Username, '\\'); i >= 0 {
				owner = currentUser.Username[i+1:]
			} else {
				owner = currentUser.Username
			}
		}
		isDir := e.IsDir()
		if fi != nil && fi.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				isDir = target.IsDir()
			}
		}
		isHidden := e.Name() != "" && e.Name()[0] == '.'
		if !isHidden {
			isHidden = isPathHidden(filepath.Join(dir, e.Name()))
		}
		files = append(files, FileItem{
			Name:     e.Name(),
			Size:     size,
			ModTime:  modTime.Format(time.RFC3339),
			Mode:     mode.String(),
			IsDir:    isDir,
			IsHidden: isHidden,
			Owner:    owner,
		})
	}
	return FileListResult{Files: files, Dir: dir}, nil
}

func (s *SFTPSession) ListLocalDrives() ([]FileItem, error) {
	var drives []FileItem
	for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		root := string(letter) + ":\\"
		fi, err := os.Stat(root)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			drives = append(drives, FileItem{
				Name:    root,
				Size:    0,
				ModTime: fi.ModTime().Format(time.RFC3339),
				Mode:    fi.Mode().String(),
				IsDir:   true,
			})
		}
	}
	return drives, nil
}

func (s *SFTPSession) ChangeRemoteDir(dir string) (FileListResult, error) {
	if err := s.requireClient(); err != nil {
		return FileListResult{}, err
	}
	target := dir
	if !path.IsAbs(dir) {
		target = path.Join(s.cwd, dir)
	}
	fi, err := s.sftpClient.Stat(target)
	if err != nil {
		return FileListResult{}, fmt.Errorf("no such directory: %s", target)
	}
	if !fi.IsDir() {
		return FileListResult{}, fmt.Errorf("not a directory: %s", target)
	}
	real, _ := s.sftpClient.RealPath(target)
	s.mu.Lock()
	s.cwd = real
	s.mu.Unlock()
	return s.ListRemote(real)
}

func (s *SFTPSession) ChangeLocalDir(dir string) (FileListResult, error) {
	target := dir
	if !filepath.IsAbs(dir) {
		target = filepath.Join(s.localCwd, dir)
	}
	fi, err := os.Stat(target)
	if err != nil {
		return FileListResult{}, fmt.Errorf("no such directory: %s", target)
	}
	if !fi.IsDir() {
		return FileListResult{}, fmt.Errorf("not a directory: %s", target)
	}
	abs, _ := filepath.Abs(target)
	s.mu.Lock()
	s.localCwd = abs
	s.mu.Unlock()
	return s.ListLocal(abs)
}

func (s *SFTPSession) MakeDir(dir string) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	p := dir
	if !path.IsAbs(p) {
		p = path.Join(s.cwd, p)
	}
	return s.sftpClient.Mkdir(p)
}

func (s *SFTPSession) Remove(p string, recursive bool) error {
	// Deletes always go through the shell `rm -rf` fast path (much faster for
	// large trees), falling back to SFTP recursive removal when no shell is
	// available. `recursive` is kept only for the FS interface contract;
	// `rm -rf` is always recursive.
	if err := s.requireClient(); err != nil {
		return err
	}
	if !path.IsAbs(p) {
		p = path.Join(s.cwd, p)
	}
	p = path.Clean(p)
	if p == "/" || p == "." || p == "" {
		return fmt.Errorf("refusing to delete path: %s", p)
	}
	if s.sshClient != nil {
		session, err := s.sshClient.NewSession()
		if err != nil {
			// Fall back to SFTP remove.
			return s.rmRecursive(p)
		}
		defer session.Close()
		cmd := "rm -rf -- " + shellEscape(p)
		if runErr := session.Run(cmd); runErr != nil {
			// Some servers lack a shell rm; fall back to SFTP.
			if fe := s.rmRecursive(p); fe != nil {
				return fmt.Errorf("quick delete failed (%v); sftp fallback: %w", runErr, fe)
			}
		}
		return nil
	}
	return s.rmRecursive(p)
}

func (s *SFTPSession) Rename(oldName, newName string) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	old := oldName
	if !path.IsAbs(old) {
		old = path.Join(s.cwd, old)
	}
	newPath := newName
	if !path.IsAbs(newPath) {
		newPath = path.Join(s.cwd, newPath)
	}
	return s.sftpClient.Rename(old, newPath)
}

func (s *SFTPSession) Chmod(p string, mode os.FileMode) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	if !path.IsAbs(p) {
		p = path.Join(s.cwd, p)
	}
	return s.sftpClient.Chmod(p, mode)
}

func (s *SFTPSession) Get(remotePath, localPath string, recursive bool) (string, error) {
	if err := s.requireClient(); err != nil {
		return "", err
	}
	rp := remotePath
	if !path.IsAbs(rp) {
		rp = path.Join(s.cwd, rp)
	}
	lp := localPath
	if !filepath.IsAbs(lp) {
		lp = filepath.Join(s.localCwd, lp)
	}
	if recursive {
		total, err := s.dirSizeRemote(rp)
		if err != nil {
			return "", err
		}
		task := &TransferTask{
			ID:         s.nextTaskID("dl"),
			Type:       "download",
			LocalPath:  lp,
			RemotePath: rp,
			Total:      total,
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
				s.mu.Lock()
				delete(s.transfers, task.ID)
				s.mu.Unlock()
			}()
			if err := s.downloadDir(rp, lp, task); err != nil {
				task.Status = "error"
				s.emitTransferEvent(task, err)
				return
			}
			task.Status = "done"
			s.emitTransferComplete(task)
		}()
		return task.ID, nil
	}
	task := &TransferTask{
		ID:         s.nextTaskID("dl"),
		Type:       "download",
		LocalPath:  lp,
		RemotePath: rp,
		Status:     "pending",
	}
	s.startTransfer(task)
	return task.ID, nil
}

func (s *SFTPSession) Put(localPath, remotePath string, recursive bool) (string, error) {
	if err := s.requireClient(); err != nil {
		return "", err
	}
	lp := localPath
	if !filepath.IsAbs(lp) {
		lp = filepath.Join(s.localCwd, lp)
	}
	rp := remotePath
	if !path.IsAbs(rp) {
		rp = path.Join(s.cwd, rp)
	}
	if recursive {
		total, err := s.dirSizeLocal(lp)
		if err != nil {
			return "", err
		}
		task := &TransferTask{
			ID:         s.nextTaskID("ul"),
			Type:       "upload",
			LocalPath:  lp,
			RemotePath: rp,
			Total:      total,
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
				s.mu.Lock()
				delete(s.transfers, task.ID)
				s.mu.Unlock()
			}()
			if err := s.uploadDir(lp, rp, task); err != nil {
				task.Status = "error"
				s.emitTransferEvent(task, err)
				return
			}
			task.Status = "done"
			s.emitTransferComplete(task)
		}()
		return task.ID, nil
	}
	task := &TransferTask{
		ID:         s.nextTaskID("ul"),
		Type:       "upload",
		LocalPath:  lp,
		RemotePath: rp,
		Status:     "pending",
	}
	s.startTransfer(task)
	return task.ID, nil
}

// --- Local file operations ---

func (s *SFTPSession) LocalRemove(p string, recursive bool) error {
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.localCwd, p)
	}
	if recursive {
		return os.RemoveAll(p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		entries, err := os.ReadDir(p)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("directory not empty (%d items)", len(entries))
		}
	}
	return os.Remove(p)
}

func (s *SFTPSession) LocalRename(oldName, newName string) error {
	old := oldName
	if !filepath.IsAbs(old) {
		old = filepath.Join(s.localCwd, old)
	}
	newPath := newName
	if !filepath.IsAbs(newPath) {
		newPath = filepath.Join(s.localCwd, newPath)
	}
	return os.Rename(old, newPath)
}

func (s *SFTPSession) LocalMkdir(dir string) error {
	p := dir
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.localCwd, p)
	}
	return os.MkdirAll(p, 0755)
}

// LocalGetContent reads a local file's full content.
func (s *SFTPSession) LocalGetContent(localPath string) ([]byte, error) {
	p := localPath
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.localCwd, p)
	}
	return os.ReadFile(p)
}

// LocalPutContent writes content to a local file, creating parent directories as needed.
func (s *SFTPSession) LocalPutContent(localPath string, content []byte) error {
	p := localPath
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.localCwd, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, content, 0644)
}

// LocalCopy copies a local file or directory.
func (s *SFTPSession) LocalCopy(oldPath, newPath string) error {
	old := oldPath
	if !filepath.IsAbs(old) {
		old = filepath.Join(s.localCwd, old)
	}
	n := newPath
	if !filepath.IsAbs(n) {
		n = filepath.Join(s.localCwd, n)
	}
	return localCopyRecursive(old, n)
}

// LocalMove moves a local file or directory (rename, same filesystem only).
func (s *SFTPSession) LocalMove(oldPath, newPath string) error {
	old := oldPath
	if !filepath.IsAbs(old) {
		old = filepath.Join(s.localCwd, old)
	}
	n := newPath
	if !filepath.IsAbs(n) {
		n = filepath.Join(s.localCwd, n)
	}
	return os.Rename(old, n)
}

// PutContent writes raw content directly to a remote file via SFTP.
func (s *SFTPSession) PutContent(remotePath string, content []byte) error {
	type outcome struct{ err error }
	ch := make(chan outcome, 1)
	go func() {
		ch <- outcome{s.putContentUnlocked(remotePath, content)}
	}()
	select {
	case out := <-ch:
		return out.err
	case <-time.After(25 * time.Second):
		return fmt.Errorf("write remote content timeout")
	}
}

func (s *SFTPSession) putContentUnlocked(remotePath string, content []byte) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	rp := remotePath
	if !path.IsAbs(rp) {
		rp = path.Join(s.cwd, rp)
	}
	// Ensure parent directory exists
	parentDir := path.Dir(rp)
	if err := s.sftpClient.MkdirAll(parentDir); err != nil {
		return err
	}
	f, err := s.sftpClient.Create(rp)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(content)
	return err
}

// GetContent reads the full content of a remote file.
// Like ListRemote, it is bounded by a timeout: a wedged connection (remote
// stall, half-open TCP) must surface an error instead of hanging the caller
// — the external-editor open path blocks the UI on this call.
func (s *SFTPSession) GetContent(remotePath string) ([]byte, error) {
	type outcome struct {
		b   []byte
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		b, err := s.getContentUnlocked(remotePath)
		ch <- outcome{b, err}
	}()
	select {
	case out := <-ch:
		return out.b, out.err
	case <-time.After(25 * time.Second):
		return nil, fmt.Errorf("read remote content timeout")
	}
}

func (s *SFTPSession) getContentUnlocked(remotePath string) ([]byte, error) {
	if err := s.requireClient(); err != nil {
		return nil, err
	}
	rp := remotePath
	if !path.IsAbs(rp) {
		rp = path.Join(s.cwd, rp)
	}
	f, err := s.sftpClient.Open(rp)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// shellEscape returns a safely single-quoted string for shell commands.
func shellEscape(str string) string {
	return "'" + strings.ReplaceAll(str, "'", "'\\''") + "'"
}

// Copy copies a file or directory on the remote server.
// Tries shell cp -r first (zero data transfer on Linux), falls back to
// SFTP-level copy for servers without cp (Windows, etc.).
func (s *SFTPSession) Copy(oldPath, newPath string) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	old := oldPath
	if !path.IsAbs(old) {
		old = path.Join(s.cwd, old)
	}
	n := newPath
	if !path.IsAbs(n) {
		n = path.Join(s.cwd, n)
	}
	// Try shell cp -r first (server-side copy, zero data transfer)
	session, err := s.sshClient.NewSession()
	if err != nil {
		return err
	}
	err = session.Run(fmt.Sprintf("cp -r -- %s %s", shellEscape(old), shellEscape(n)))
	session.Close()
	if err == nil {
		return nil
	}
	// Fallback: SFTP-level copy (compatible with Windows and servers without cp)
	return s.sftpCopy(old, n)
}

// sftpCopy copies a file or directory using SFTP operations only.
// Works on any SFTP server regardless of the remote shell environment.
func (s *SFTPSession) sftpCopy(oldPath, newPath string) error {
	srcInfo, err := s.sftpClient.Stat(oldPath)
	if err != nil {
		return err
	}
	if srcInfo.IsDir() {
		return s.sftpCopyDir(oldPath, newPath)
	}
	return s.sftpCopyFile(oldPath, newPath)
}

func (s *SFTPSession) sftpCopyFile(oldPath, newPath string) error {
	src, err := s.sftpClient.Open(oldPath)
	if err != nil {
		return err
	}
	defer src.Close()

	parentDir := path.Dir(newPath)
	if err := s.sftpClient.MkdirAll(parentDir); err != nil {
		return err
	}

	dst, err := s.sftpClient.Create(newPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

func (s *SFTPSession) sftpCopyDir(oldPath, newPath string) error {
	if err := s.sftpClient.MkdirAll(newPath); err != nil {
		return err
	}
	entries, err := s.sftpClient.ReadDir(oldPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		oldEntry := path.Join(oldPath, entry.Name())
		newEntry := path.Join(newPath, entry.Name())
		if entry.IsDir() {
			if err := s.sftpCopyDir(oldEntry, newEntry); err != nil {
				return err
			}
		} else {
			if err := s.sftpCopyFile(oldEntry, newEntry); err != nil {
				return err
			}
		}
	}
	return nil
}

// Move moves a file or directory on the remote server.
// Tries SFTP Rename first (atomic, server-side), falls back to shell mv.
func (s *SFTPSession) Move(oldPath, newPath string) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	old := oldPath
	if !path.IsAbs(old) {
		old = path.Join(s.cwd, old)
	}
	n := newPath
	if !path.IsAbs(n) {
		n = path.Join(s.cwd, n)
	}
	// Try SFTP native rename first (same filesystem, zero data transfer)
	if err := s.sftpClient.Rename(old, n); err == nil {
		return nil
	}
	// Fallback: shell mv handles cross-filesystem moves
	session, err := s.sshClient.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	return session.Run(fmt.Sprintf("mv -- %s %s", shellEscape(old), shellEscape(n)))
}

// CancelTransfer cancels an ongoing transfer task.
func (s *SFTPSession) CancelTransfer(taskID string) error {
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

// PauseTransfer pauses an ongoing transfer task.
func (s *SFTPSession) PauseTransfer(taskID string) error {
	s.mu.Lock()
	task, ok := s.transfers[taskID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	task.paused = true
	task.Status = "paused"
	s.emitTransferComplete(task)
	return nil
}

// ResumeTransfer resumes a paused transfer task.
func (s *SFTPSession) ResumeTransfer(taskID string) error {
	s.mu.Lock()
	task, ok := s.transfers[taskID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	task.paused = false
	task.Status = "running"
		close(task.pauseCh)
		task.pauseCh = make(chan struct{})
		s.emitTransferStart(task)
		return nil
	}

// --- Recursive helpers ---

// localCopyRecursive copies files and directories on the local filesystem.
func localCopyRecursive(src, dst string) error {
	si, err := os.Stat(src)
	if err != nil {
		return err
	}
	if si.IsDir() {
		if err := os.MkdirAll(dst, si.Mode()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := localCopyRecursive(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, si.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (s *SFTPSession) rmRecursive(p string) error {
	fi, err := s.sftpClient.Stat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		infos, err := s.sftpClient.ReadDir(p)
		if err != nil {
			return err
		}
		for _, info := range infos {
			childPath := path.Join(p, info.Name())
			if err := s.rmRecursive(childPath); err != nil {
				return err
			}
		}
		return s.sftpClient.RemoveDirectory(p)
	}
	return s.sftpClient.Remove(p)
}

func (s *SFTPSession) dirSizeRemote(dir string) (int64, error) {
	infos, err := s.sftpClient.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, fi := range infos {
		if fi.IsDir() {
			sz, err := s.dirSizeRemote(path.Join(dir, fi.Name()))
			if err != nil {
				return 0, err
			}
			total += sz
		} else {
			total += fi.Size()
		}
	}
	return total, nil
}

func (s *SFTPSession) dirSizeLocal(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			sz, err := s.dirSizeLocal(filepath.Join(dir, e.Name()))
			if err != nil {
				return 0, err
			}
			total += sz
		} else {
			fi, err := e.Info()
			if err != nil {
				return 0, err
			}
			total += fi.Size()
		}
	}
	return total, nil
}

// --- Transfer methods ---

func (s *SFTPSession) startTransfer(task *TransferTask) {
	task.start()
	s.mu.Lock()
	s.transfers[task.ID] = task
	s.mu.Unlock()
	go func() {
		defer func() {
			task.done()
			s.mu.Lock()
			delete(s.transfers, task.ID)
			s.mu.Unlock()
		}()
		task.Status = "running"
		s.emitTransferStart(task)

		// Acquire concurrency slot
		if s.sem != nil {
			select {
			case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
			case <-task.ctx.Done():
				task.Status = "cancelled"
				s.emitTransferComplete(task)
				return
			}
		}

		var src io.Reader
		var dst io.Writer

		if task.Type == "download" {
			remoteFile, e := s.sftpClient.Open(task.RemotePath)
			if e != nil {
				task.Status = "error"
				s.emitTransferEvent(task, e)
				return
			}
			defer remoteFile.Close()
			fi, _ := remoteFile.Stat()
			if fi != nil {
				task.Total = fi.Size()
			}
			src = remoteFile
			localFile, e := os.Create(task.LocalPath)
			if e != nil {
				task.Status = "error"
				s.emitTransferEvent(task, e)
				return
			}
			defer localFile.Close()
			dst = localFile
		} else {
			localFile, e := os.Open(task.LocalPath)
			if e != nil {
				task.Status = "error"
				s.emitTransferEvent(task, e)
				return
			}
			defer localFile.Close()
			fi, _ := localFile.Stat()
			if fi != nil {
				task.Total = fi.Size()
			}
			src = localFile
			remoteFile, e := s.sftpClient.Create(task.RemotePath)
			if e != nil {
				task.Status = "error"
				s.emitTransferEvent(task, e)
				return
			}
			defer remoteFile.Close()
			dst = remoteFile
		}

		buf := make([]byte, 64*1024)
		for {
			select {
			case <-task.ctx.Done():
				task.Status = "cancelled"
				s.emitTransferComplete(task)
				return
			default:
			}
			task.waitIfPaused()
			select {
			case <-task.ctx.Done():
				task.Status = "cancelled"
				s.emitTransferComplete(task)
				return
			default:
			}
			n, e := src.Read(buf)
			if n > 0 {
				dst.Write(buf[:n])
				task.Progress += int64(n)
				s.emitTransferProgress(task)
			}
			if e == io.EOF {
				break
			}
			if e != nil {
				task.Status = "error"
				s.emitTransferEvent(task, e)
				return
			}
		}
		task.Status = "done"
		s.emitTransferComplete(task)
	}()
}

func (s *SFTPSession) downloadDir(remoteDir, localDir string, task *TransferTask) error {
	select {
	case <-task.ctx.Done():
		return task.ctx.Err()
	default:
	}
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return err
	}
	infos, err := s.sftpClient.ReadDir(remoteDir)
	if err != nil {
		return err
	}
	for _, fi := range infos {
		rp := path.Join(remoteDir, fi.Name())
		lp := filepath.Join(localDir, fi.Name())
		if fi.IsDir() {
			if err := s.downloadDir(rp, lp, task); err != nil {
				return err
			}
		} else {
			if err := s.transferFile(task, lp, rp, "download"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SFTPSession) uploadDir(localDir, remoteDir string, task *TransferTask) error {
	select {
	case <-task.ctx.Done():
		return task.ctx.Err()
	default:
	}
	if err := s.sftpClient.MkdirAll(remoteDir); err != nil {
		return err
	}
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		rp := path.Join(remoteDir, entry.Name())
		lp := filepath.Join(localDir, entry.Name())
		if entry.IsDir() {
			if err := s.uploadDir(lp, rp, task); err != nil {
				return err
			}
		} else {
			if err := s.transferFile(task, lp, rp, "upload"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SFTPSession) transferFile(task *TransferTask, localPath, remotePath, tfType string) error {
	if tfType == "download" {
		src, err := s.sftpClient.Open(remotePath)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := os.Create(localPath)
		if err != nil {
			return err
		}
		defer dst.Close()
		buf := make([]byte, 64*1024)
		for {
			select {
			case <-task.ctx.Done():
				return task.ctx.Err()
			default:
			}
			task.waitIfPaused()
			n, e := src.Read(buf)
			if n > 0 {
				dst.Write(buf[:n])
				task.Progress += int64(n)
				s.emitTransferProgress(task)
			}
			if e != nil {
				break
			}
		}
	} else {
		src, err := os.Open(localPath)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := s.sftpClient.Create(remotePath)
		if err != nil {
			return err
		}
		defer dst.Close()
		buf := make([]byte, 64*1024)
		for {
			select {
			case <-task.ctx.Done():
				return task.ctx.Err()
			default:
			}
			task.waitIfPaused()
			n, e := src.Read(buf)
			if n > 0 {
				dst.Write(buf[:n])
				task.Progress += int64(n)
				s.emitTransferProgress(task)
			}
			if e != nil {
				break
			}
		}
	}
	return nil
}

// --- Transfer event emitters ---

func (s *SFTPSession) emitTransferStart(task *TransferTask) {
	name := filepath.Base(task.LocalPath)
	if task.Type == "download" {
		name = path.Base(task.RemotePath)
	}
	payload := map[string]interface{}{
		"type":   "sftp:transfer",
		"taskId": task.ID,
		"event":  "start",
		"tfType": task.Type,
		"name":   name,
		"total":  task.Total,
	}
	jsonBytes, _ := json.Marshal(payload)
	s.emitData([]byte("\x1b]633;S" + string(jsonBytes) + "\x07"))
}

func (s *SFTPSession) emitTransferProgress(task *TransferTask) {
	payload := map[string]interface{}{
		"type":     "sftp:transfer",
		"taskId":   task.ID,
		"event":    "progress",
		"progress": task.Progress,
		"total":    task.Total,
	}
	jsonBytes, _ := json.Marshal(payload)
	s.emitData([]byte("\x1b]633;S" + string(jsonBytes) + "\x07"))
}

func (s *SFTPSession) emitTransferComplete(task *TransferTask) {
	payload := map[string]interface{}{
		"type":   "sftp:transfer",
		"taskId": task.ID,
		"event":  "complete",
		"status": task.Status,
	}
	jsonBytes, _ := json.Marshal(payload)
	s.emitData([]byte("\x1b]633;S" + string(jsonBytes) + "\x07"))
}

func (s *SFTPSession) emitTransferEvent(task *TransferTask, err error) {
	payload := map[string]interface{}{
		"type":   "sftp:transfer",
		"taskId": task.ID,
		"event":  "complete",
		"status": "error",
		"error":  err.Error(),
	}
	jsonBytes, _ := json.Marshal(payload)
	s.emitData([]byte("\x1b]633;S" + string(jsonBytes) + "\x07"))
}
