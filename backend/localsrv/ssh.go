package localsrv

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// StartSSHServer starts an SSH/SFTP server serving rootDir on the given port.
// Authentication is by username/password; if both are empty, any login is
// accepted. The SFTP subsystem is restricted to rootDir. Returns the server id.
func (m *Manager) StartSSHServer(cfg SSHConfig) (string, error) {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return "", fmt.Errorf("invalid port: %d", cfg.Port)
	}
	absRoot, err := filepath.Abs(cfg.RootDir)
	if err != nil {
		return "", fmt.Errorf("invalid root directory: %w", err)
	}

	id, ctx := m.newServer()
	m.log(ServiceSSH, id, fmt.Sprintf("Starting SSH/SFTP server on port %d ...", cfg.Port))

	// Generate a host key (RSA 2048) for this server instance.
	hostKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		m.finish(id)
		return "", fmt.Errorf("failed to generate host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		m.finish(id)
		return "", fmt.Errorf("failed to create signer: %w", err)
	}

	sshCfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			// Accept any credentials when none are configured, otherwise
			// require an exact match.
			if cfg.Username == "" && cfg.Password == "" {
				return nil, nil
			}
			if c.User() == cfg.Username && string(pass) == cfg.Password {
				return nil, nil
			}
			return nil, fmt.Errorf("authentication failed")
		},
	}
	sshCfg.AddHostKey(signer)

	ln, err := listen(ctx, "tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		m.finish(id)
		return "", fmt.Errorf("failed to listen on port %d: %w", cfg.Port, err)
	}

	// Track every accepted connection so shutdown (Stop/StopAll) can close
	// them. This unblocks the SSH handshake in handleSSHConn and any open
	// session, which lets the accept goroutine's WaitGroup actually drain and
	// the server goroutine terminate — instead of leaking connection
	// goroutines forever when the service or app is stopped.
	var connMu sync.Mutex
	active := make(map[net.Conn]struct{})
	closeConns := func() {
		connMu.Lock()
		for c := range active {
			_ = c.Close()
		}
		connMu.Unlock()
	}

	// Close already-accepted connections as soon as the context is cancelled
	// (belt-and-braces in case Stop is never called directly).
	go func() {
		<-ctx.Done()
		closeConns()
	}()

	m.setStop(id, closeConns)

	go func() {
		defer m.finish(id)
		m.state(ServiceSSH, id, true)
		m.log(ServiceSSH, id, "SSH/SFTP server started")

		var wg sync.WaitGroup
		for {
			conn, err := ln.Accept()
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				continue
			}
			connMu.Lock()
			active[conn] = struct{}{}
			connMu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					connMu.Lock()
					delete(active, conn)
					connMu.Unlock()
				}()
				defer conn.Close()
				m.handleSSHConn(ctx, id, conn, sshCfg, absRoot)
			}()
		}
		wg.Wait()
		m.state(ServiceSSH, id, false)
		m.log(ServiceSSH, id, "SSH/SFTP server stopped")
	}()

	m.startTimeout(id, ServiceSSH, cfg.TimeoutSeconds)
	return id, nil
}

// handleSSHConn performs the SSH handshake and serves channels (SFTP subsystem).
func (m *Manager) handleSSHConn(ctx context.Context, id string, conn net.Conn, sshCfg *ssh.ServerConfig, rootDir string) {
	sConn, chans, reqs, err := ssh.NewServerConn(conn, sshCfg)
	if err != nil {
		m.log(ServiceSSH, id, fmt.Sprintf("SSH handshake failed from %s: %v", conn.RemoteAddr(), err))
		return
	}
	defer sConn.Close()
	m.log(ServiceSSH, id, fmt.Sprintf("SSH login from %s (user: %s)", conn.RemoteAddr(), sConn.User()))

	// Discard global requests (required by the ssh package).
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "only session channels supported")
			continue
		}
		ch, reqs, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go m.handleSSHSession(ctx, id, ch, reqs, rootDir)
	}
}

// handleSSHSession handles a session channel: SFTP subsystem and simple commands.
func (m *Manager) handleSSHSession(ctx context.Context, id string, ch ssh.Channel, reqs <-chan *ssh.Request, rootDir string) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "subsystem":
			if len(req.Payload) >= 4 && string(req.Payload[4:]) == "sftp" {
				req.Reply(true, nil)
				m.serveSFTP(id, ch, rootDir)
				return
			}
			req.Reply(false, nil)
		case "exec":
			// Minimal exec support: echo back the command (not a real shell).
			req.Reply(true, nil)
			fmt.Fprintf(ch, "uniTerm local SSH server — SFTP only\r\n")
			ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
			return
		case "shell", "pty-req":
			req.Reply(true, nil)
			fmt.Fprintf(ch, "uniTerm local SSH server — SFTP only\r\n")
			ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// serveSFTP runs an SFTP server rooted at rootDir over the given channel.
func (m *Manager) serveSFTP(id string, ch ssh.Channel, rootDir string) {
	handlers := sftp.Handlers{
		FileGet:  sftpFileGet{root: rootDir},
		FilePut:  sftpFilePut{root: rootDir},
		FileCmd:  sftpFileCmd{root: rootDir},
		FileList: sftpFileList{root: rootDir},
	}

	srv := sftp.NewRequestServer(ch, handlers)
	if err := srv.Serve(); err != nil && err != io.EOF {
		m.log(ServiceSSH, id, fmt.Sprintf("SFTP session error: %v", err))
	}
}

// sftpFileGet implements sftp.FileReader for the GET method.
type sftpFileGet struct{ root string }

func (h sftpFileGet) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	p, err := sftpResolve(h.root, r.Filepath)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// sftpFilePut implements sftp.FileWriter for the PUT/OPEN methods.
type sftpFilePut struct{ root string }

func (h sftpFilePut) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	p, err := sftpResolve(h.root, r.Filepath)
	if err != nil {
		return nil, err
	}
	return os.Create(p)
}

// sftpFileCmd implements sftp.FileCmder for Setstat/Rename/Rmdir/Mkdir/Remove.
type sftpFileCmd struct{ root string }

func (h sftpFileCmd) Filecmd(r *sftp.Request) error {
	p, err := sftpResolve(h.root, r.Filepath)
	if err != nil {
		return err
	}
	switch r.Method {
	case "Mkdir":
		return os.MkdirAll(p, 0o755)
	case "Rmdir", "Remove":
		return os.RemoveAll(p)
	case "Rename":
		tp, err := sftpResolve(h.root, r.Target)
		if err != nil {
			return err
		}
		return os.Rename(p, tp)
	default:
		// Setstat, Link, Symlink: no-op for a read-only-ish local file server.
		return nil
	}
}

// sftpFileList implements sftp.FileLister for List/Stat/Readlink methods.
type sftpFileList struct{ root string }

func (h sftpFileList) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	p, err := sftpResolve(h.root, r.Filepath)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return &fileListerAt{infos: []os.FileInfo{fi}}, nil
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	infos := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name() < infos[j].Name() })
	return &fileListerAt{infos: infos}, nil
}

// sftpResolve resolves a client-supplied path against rootDir and rejects any
// path that escapes the root directory. Accessing the root itself (rel == ".")
// is allowed; anything escaping above root ("..") is denied.
func sftpResolve(rootDir, clientPath string) (string, error) {
	cleanPath := filepath.Clean("/" + strings.TrimPrefix(clientPath, "/"))
	full := filepath.Join(rootDir, cleanPath)
	rel, err := filepath.Rel(rootDir, full)
	if err != nil {
		return "", fmt.Errorf("access denied: path outside root")
	}
	if rel == "." {
		return full, nil
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("access denied: path outside root")
	}
	return full, nil
}

// fileListerAt adapts a single FileInfo or a slice of FileInfos to the
// sftp.ListerAt interface.
type fileListerAt struct {
	infos []os.FileInfo
}

func (l *fileListerAt) ListAt(into []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l.infos)) {
		return 0, io.EOF
	}
	n := copy(into, l.infos[offset:])
	if n < len(into) {
		return n, io.EOF
	}
	return n, nil
}

// ensure fileListerAt satisfies sftp.ListerAt at compile time.
var _ sftp.ListerAt = (*fileListerAt)(nil)
