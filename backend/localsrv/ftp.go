package localsrv

import (
	"bufio"
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ftpMaxLoginAttempts bounds the failed USER/PASS attempts one control
// connection may make before the server drops it. Together with the small
// delay on every failure this keeps an unauthenticated peer from replaying a
// password dictionary over one connection.
const ftpMaxLoginAttempts = 5

// ftpLoginFailureDelay slows down each rejected credential. It blocks only
// this connection's goroutine, so it never affects other clients.
const ftpLoginFailureDelay = 300 * time.Millisecond

// StartFTPServer starts an FTP server serving rootDir on the given port.
// Authentication is by username/password; when anonymous is true, any login
// is accepted. When anonymous access is off, a username and password are
// required — an unauthenticated FTP server on a LAN-reachable port would let
// anyone read, overwrite and delete the whole root directory. Returns the
// server id.
func (m *Manager) StartFTPServer(cfg FTPConfig) (string, error) {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return "", fmt.Errorf("invalid port: %d", cfg.Port)
	}
	if !cfg.Anonymous && (strings.TrimSpace(cfg.Username) == "" || cfg.Password == "") {
		return "", fmt.Errorf("username and password are required unless anonymous access is enabled")
	}
	absRoot, err := filepath.Abs(cfg.RootDir)
	if err != nil {
		return "", fmt.Errorf("invalid root directory: %w", err)
	}

	id, ctx := m.newServer()
	m.log(ServiceFTP, id, fmt.Sprintf("Starting FTP server on port %d ...", cfg.Port))

	ln, err := listen(ctx, "tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		m.finish(id)
		return "", fmt.Errorf("failed to listen on port %d: %w", cfg.Port, err)
	}

	m.setStop(id, func() {})

	go func() {
		defer m.finish(id)
		m.state(ServiceFTP, id, true)
		m.log(ServiceFTP, id, "FTP server started")

		var wg sync.WaitGroup
		for {
			conn, err := ln.Accept()
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				m.handleFTPConn(ctx, id, conn, absRoot, cfg)
			}()
		}
		wg.Wait()
		m.state(ServiceFTP, id, false)
		m.log(ServiceFTP, id, "FTP server stopped")
	}()

	m.startTimeout(id, ServiceFTP, cfg.TimeoutSeconds)
	return id, nil
}

// ftpSession holds the per-connection state.
type ftpSession struct {
	rootDir      string
	cwd          string
	authed       bool
	username     string
	peerIP       string // control-connection peer; PORT/EPRT must match it
	failedAuth   int    // consecutive rejected USER/PASS attempts
	dataConn     net.Conn
	pasvLn       net.Listener
	pasvAddr     string
	transferType string // "A" (ASCII) or "I" (binary)
	renameFrom   string
}

// handleFTPConn serves one FTP control connection.
func (m *Manager) handleFTPConn(ctx context.Context, id string, conn net.Conn, rootDir string, cfg FTPConfig) {
	remote := conn.RemoteAddr().String()
	m.log(ServiceFTP, id, fmt.Sprintf("FTP connection from %s", remote))

	peerIP := ""
	if host, _, err := net.SplitHostPort(remote); err == nil {
		peerIP = host
	}
	sess := &ftpSession{
		rootDir:      rootDir,
		cwd:          "/",
		peerIP:       peerIP,
		transferType: "I",
	}
	defer sess.closeData()

	// When the server is stopped (ctx cancelled), force-close this control
	// connection so a blocking ReadString returns immediately instead of
	// waiting for its (long) read deadline. This keeps Stop responsive even
	// with idle client connections open.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		conn.Close()
	}()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	writeFTP := func(format string, args ...any) {
		fmt.Fprintf(writer, format+"\r\n", args...)
		writer.Flush()
	}

	// Banner.
	writeFTP("220 uniTerm FTP server ready")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				m.log(ServiceFTP, id, fmt.Sprintf("FTP read error from %s: %v", remote, err))
			}
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, " ", 2)
		cmd := strings.ToUpper(parts[0])
		arg := ""
		if len(parts) > 1 {
			arg = parts[1]
		}

		// Never write the password into the event feed / UI log.
		loggedArg := arg
		if cmd == "PASS" {
			loggedArg = "***"
		}
		m.log(ServiceFTP, id, fmt.Sprintf("%s -> %s %s", remote, cmd, loggedArg))

		// Authentication gate: until USER/PASS succeeded, only the login and
		// capability-negotiation commands may run. Without this, CWD/RETR/
		// STOR/DELE/… served the root directory to any unauthenticated peer.
		if !sess.authed {
			switch cmd {
			case "USER", "PASS", "QUIT", "SYST", "FEAT", "OPTS", "NOOP", "TYPE":
			default:
				writeFTP("530 Please login with USER and PASS")
				continue
			}
		}

		switch cmd {
		case "USER":
			sess.username = arg
			if cfg.Anonymous {
				sess.authed = true
				writeFTP("230 Login successful")
			} else {
				writeFTP("331 Password required for %s", arg)
			}

		case "PASS":
			if sess.authed {
				writeFTP("230 Already logged in")
			} else if cfg.Anonymous {
				sess.authed = true
				writeFTP("230 Login successful")
			} else if ftpSecretEqual(sess.username, cfg.Username) && ftpSecretEqual(arg, cfg.Password) {
				sess.authed = true
				sess.failedAuth = 0
				writeFTP("230 Login successful")
			} else {
				sess.failedAuth++
				if sess.failedAuth >= ftpMaxLoginAttempts {
					writeFTP("421 Too many failed login attempts")
					m.log(ServiceFTP, id, fmt.Sprintf("too many failed logins from %s, closing", remote))
					return
				}
				time.Sleep(ftpLoginFailureDelay)
				writeFTP("530 Login incorrect")
			}

		case "QUIT":
			writeFTP("221 Goodbye")
			return

		case "SYST":
			writeFTP("215 UNIX Type: L8")

		case "FEAT":
			writeFTP("211-Features:")
			writeFTP(" UTF8")
			writeFTP(" PASV")
			writeFTP(" EPSV")
			writeFTP(" SIZE")
			writeFTP(" MDTM")
			writeFTP("211 End")

		case "OPTS":
			if strings.EqualFold(arg, "UTF8 ON") {
				writeFTP("200 OK")
			} else {
				writeFTP("200 OK")
			}

		case "NOOP":
			writeFTP("200 OK")

		case "TYPE":
			sess.transferType = strings.ToUpper(arg)
			writeFTP("200 Type set to %s", sess.transferType)

		case "PWD", "XPWD":
			writeFTP("257 \"%s\" is the current directory", sess.cwd)

		case "CWD":
			m.ftpCwd(sess, arg, writeFTP)

		case "CDUP":
			parent := filepath.Dir(sess.cwd)
			if parent == "." || parent == "" {
				parent = "/"
			}
			sess.cwd = parent
			writeFTP("200 Directory changed to %s", sess.cwd)

		case "MKD", "XMKD":
			m.ftpMkd(sess, arg, writeFTP)

		case "RMD", "XRMD":
			m.ftpRmd(sess, arg, writeFTP)

		case "DELE":
			m.ftpDele(sess, arg, writeFTP)

		case "RNFR":
			// The source must be validated here as well: RNTO only checks the
			// destination, so an unchecked RNFR would let a client rename (and
			// therefore exfiltrate) any file on the disk into the shared root.
			from := m.ftpPath(sess, arg)
			if !ftpSafe(sess.rootDir, from) {
				writeFTP("550 Access denied")
				break
			}
			sess.renameFrom = from
			writeFTP("350 Ready for RNTO")

		case "RNTO":
			m.ftpRnto(sess, arg, writeFTP)

		case "SIZE":
			m.ftpSize(sess, arg, writeFTP)

		case "MDTM":
			m.ftpMdtm(sess, arg, writeFTP)

		case "PASV":
			m.ftpPasv(ctx, sess, writeFTP)

		case "EPSV":
			m.ftpEpsv(ctx, sess, writeFTP)

		case "PORT":
			m.ftpPort(sess, arg, false, writeFTP)

		case "EPRT":
			m.ftpPort(sess, arg, true, writeFTP)

		case "LIST", "NLST":
			m.ftpList(ctx, id, sess, arg, cmd == "NLST", writeFTP)

		case "RETR":
			m.ftpRetr(ctx, id, sess, arg, writeFTP)

		case "STOR":
			m.ftpStor(ctx, id, sess, arg, writeFTP)

		default:
			writeFTP("502 Command not implemented: %s", cmd)
		}
	}
}

// ftpPath resolves a client-supplied path to an absolute path within rootDir.
func (m *Manager) ftpPath(sess *ftpSession, p string) string {
	if !strings.HasPrefix(p, "/") {
		p = sess.cwd + "/" + p
	}
	clean := filepath.Clean("/" + p)
	full := filepath.Join(sess.rootDir, clean)
	return full
}

// ftpSafe checks that a resolved path stays within rootDir. Accessing the
// root itself (rel == ".") is allowed; anything escaping above root ("..")
// is denied. The check is repeated after resolving symlinks so a link inside
// the shared directory cannot point at the rest of the disk.
func ftpSafe(rootDir, full string) bool {
	if !lexicallyWithinRoot(rootDir, full) {
		return false
	}
	_, ok := resolveWithinRoot(rootDir, full)
	return ok
}

func (m *Manager) ftpCwd(sess *ftpSession, arg string, writeFTP func(string, ...any)) {
	full := m.ftpPath(sess, arg)
	if !ftpSafe(sess.rootDir, full) {
		writeFTP("550 Access denied")
		return
	}
	fi, err := os.Stat(full)
	if err != nil || !fi.IsDir() {
		writeFTP("550 Failed to change directory")
		return
	}
	rel, _ := filepath.Rel(sess.rootDir, full)
	if rel == "." {
		sess.cwd = "/"
	} else {
		sess.cwd = "/" + filepath.ToSlash(rel)
	}
	writeFTP("250 Directory successfully changed")
}

func (m *Manager) ftpMkd(sess *ftpSession, arg string, writeFTP func(string, ...any)) {
	full := m.ftpPath(sess, arg)
	if !ftpSafe(sess.rootDir, full) {
		writeFTP("550 Access denied")
		return
	}
	if err := os.MkdirAll(full, 0o755); err != nil {
		writeFTP("550 Create directory operation failed")
		return
	}
	writeFTP("257 \"%s\" created", arg)
}

func (m *Manager) ftpRmd(sess *ftpSession, arg string, writeFTP func(string, ...any)) {
	full := m.ftpPath(sess, arg)
	if !ftpSafe(sess.rootDir, full) {
		writeFTP("550 Access denied")
		return
	}
	if err := os.Remove(full); err != nil {
		writeFTP("550 Remove directory operation failed")
		return
	}
	writeFTP("250 Remove directory operation successful")
}

func (m *Manager) ftpDele(sess *ftpSession, arg string, writeFTP func(string, ...any)) {
	full := m.ftpPath(sess, arg)
	if !ftpSafe(sess.rootDir, full) {
		writeFTP("550 Access denied")
		return
	}
	if err := os.Remove(full); err != nil {
		writeFTP("550 Delete operation failed")
		return
	}
	writeFTP("250 Delete operation successful")
}

func (m *Manager) ftpRnto(sess *ftpSession, arg string, writeFTP func(string, ...any)) {
	if sess.renameFrom == "" {
		writeFTP("503 Bad sequence of commands")
		return
	}
	target := m.ftpPath(sess, arg)
	if !ftpSafe(sess.rootDir, target) {
		writeFTP("550 Access denied")
		return
	}
	if err := os.Rename(sess.renameFrom, target); err != nil {
		writeFTP("550 Rename failed")
		return
	}
	sess.renameFrom = ""
	writeFTP("250 Rename successful")
}

func (m *Manager) ftpSize(sess *ftpSession, arg string, writeFTP func(string, ...any)) {
	full := m.ftpPath(sess, arg)
	if !ftpSafe(sess.rootDir, full) {
		writeFTP("550 Access denied")
		return
	}
	fi, err := os.Stat(full)
	if err != nil {
		writeFTP("550 File not found")
		return
	}
	writeFTP("213 %d", fi.Size())
}

func (m *Manager) ftpMdtm(sess *ftpSession, arg string, writeFTP func(string, ...any)) {
	full := m.ftpPath(sess, arg)
	if !ftpSafe(sess.rootDir, full) {
		writeFTP("550 Access denied")
		return
	}
	fi, err := os.Stat(full)
	if err != nil {
		writeFTP("550 File not found")
		return
	}
	writeFTP("213 %s", fi.ModTime().Format("20060102150405"))
}

// ftpPasv opens a passive-mode listener and reports its address.
func (m *Manager) ftpPasv(ctx context.Context, sess *ftpSession, writeFTP func(string, ...any)) {
	sess.closeData()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		writeFTP("425 Can't open data connection")
		return
	}
	addr := ln.Addr().(*net.TCPAddr)
	sess.pasvLn = ln
	p1 := addr.Port / 256
	p2 := addr.Port % 256
	// Report the host as 127.0.0.1; clients on the same machine work.
	writeFTP("227 Entering Passive Mode (127,0,0,1,%d,%d)", p1, p2)
}

// ftpEpsv opens an extended passive-mode listener.
func (m *Manager) ftpEpsv(ctx context.Context, sess *ftpSession, writeFTP func(string, ...any)) {
	sess.closeData()
	ln, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		writeFTP("425 Can't open data connection")
		return
	}
	addr := ln.Addr().(*net.TCPAddr)
	sess.pasvLn = ln
	writeFTP("229 Entering Extended Passive Mode (|||%d|)", addr.Port)
}

// ftpPort handles active-mode PORT/EPRT (the client asks the server to dial
// back to an address of its choosing). The address MUST be the control
// connection's peer: without that check the server becomes a bounce/proxy
// primitive that can scan and reach any host on the LAN as well as ship local
// files to an arbitrary destination.
func (m *Manager) ftpPort(sess *ftpSession, arg string, eprt bool, writeFTP func(string, ...any)) {
	sess.closeData()

	var ip, portStr string
	if eprt {
		// RFC 2428: |<af>|<addr>|<port>|
		fields := strings.Split(arg, "|")
		if len(fields) < 4 {
			writeFTP("501 Invalid EPRT argument")
			return
		}
		switch fields[1] {
		case "1", "2":
		default:
			writeFTP("522 Network protocol not supported, use (1,2)")
			return
		}
		ip, portStr = fields[2], fields[3]
	} else {
		// RFC 959: h1,h2,h3,h4,p1,p2
		parts := strings.Split(arg, ",")
		if len(parts) != 6 {
			writeFTP("501 Invalid PORT argument")
			return
		}
		ip = strings.Join(parts[:4], ".")
		p1, err1 := strconv.Atoi(parts[4])
		p2, err2 := strconv.Atoi(parts[5])
		if err1 != nil || err2 != nil || p1 < 0 || p1 > 255 || p2 < 0 || p2 > 255 {
			writeFTP("501 Invalid PORT argument")
			return
		}
		portStr = strconv.Itoa(p1*256 + p2)
	}

	port, perr := strconv.Atoi(portStr)
	if perr != nil || port <= 0 || port > 65535 {
		writeFTP("501 Invalid data port")
		return
	}
	if !ftpSameHost(ip, sess.peerIP) {
		m.log(ServiceFTP, "", fmt.Sprintf("rejected PORT/EPRT to %s (control peer %s)", ip, sess.peerIP))
		writeFTP("501 PORT address does not match the control connection")
		return
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, portStr), 5*time.Second)
	if err != nil {
		writeFTP("425 Can't open data connection")
		return
	}
	sess.dataConn = conn
	writeFTP("200 PORT command successful")
}

// ftpSecretEqual compares two credentials in constant time (the length is
// still observable, which is inherent to FTP's wire format).
func ftpSecretEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ftpSameHost reports whether the data-connection address equals the control
// connection's peer address.
func ftpSameHost(ip, peerIP string) bool {
	if ip == "" || peerIP == "" {
		return false
	}
	a, b := net.ParseIP(ip), net.ParseIP(peerIP)
	if a != nil && b != nil {
		return a.Equal(b)
	}
	return strings.EqualFold(ip, peerIP)
}

// openData establishes the data connection (passive accept or active).
func (sess *ftpSession) openData() (net.Conn, error) {
	if sess.dataConn != nil {
		conn := sess.dataConn
		sess.dataConn = nil
		return conn, nil
	}
	if sess.pasvLn != nil {
		ln := sess.pasvLn
		sess.pasvLn = nil
		// Bound the accept so an idle passive client can't block this
		// connection's goroutine (and thus server shutdown) forever.
		if tln, ok := ln.(*net.TCPListener); ok {
			_ = tln.SetDeadline(time.Now().Add(30 * time.Second))
		}
		conn, err := ln.Accept()
		ln.Close()
		return conn, err
	}
	return nil, fmt.Errorf("no data connection")
}

func (sess *ftpSession) closeData() {
	if sess.dataConn != nil {
		sess.dataConn.Close()
		sess.dataConn = nil
	}
	if sess.pasvLn != nil {
		sess.pasvLn.Close()
		sess.pasvLn = nil
	}
}

func (m *Manager) ftpList(ctx context.Context, id string, sess *ftpSession, arg string, namesOnly bool, writeFTP func(string, ...any)) {
	target := sess.cwd
	if arg != "" && arg != "-a" && !strings.HasPrefix(arg, "-") {
		target = arg
	}
	full := m.ftpPath(sess, target)
	if !ftpSafe(sess.rootDir, full) {
		writeFTP("550 Access denied")
		return
	}
	fi, err := os.Stat(full)
	if err != nil {
		writeFTP("550 File not found")
		return
	}

	writeFTP("150 Here comes the directory listing")
	dataConn, err := sess.openData()
	if err != nil {
		writeFTP("425 Can't open data connection")
		return
	}
	defer dataConn.Close()

	if !fi.IsDir() {
		if namesOnly {
			fmt.Fprintf(dataConn, "%s\r\n", filepath.Base(full))
		} else {
			fmt.Fprintf(dataConn, "%s\r\n", ftpListLine(fi, filepath.Base(full)))
		}
	} else {
		entries, _ := os.ReadDir(full)
		for _, e := range entries {
			info, _ := e.Info()
			if namesOnly {
				fmt.Fprintf(dataConn, "%s\r\n", e.Name())
			} else {
				fmt.Fprintf(dataConn, "%s\r\n", ftpListLine(info, e.Name()))
			}
		}
	}
	writeFTP("226 Directory send OK")
}

// ftpListLine produces a Unix `ls -l` style line for LIST output.
func ftpListLine(fi os.FileInfo, name string) string {
	perm := fi.Mode().Perm().String()
	if fi.IsDir() {
		perm = "d" + perm[1:]
	}
	return fmt.Sprintf("%s 1 ftp ftp %d %s %s",
		perm,
		fi.Size(),
		fi.ModTime().Format("Jan 2 15:04"),
		name,
	)
}

func (m *Manager) ftpRetr(ctx context.Context, id string, sess *ftpSession, arg string, writeFTP func(string, ...any)) {
	full := m.ftpPath(sess, arg)
	if !ftpSafe(sess.rootDir, full) {
		writeFTP("550 Access denied")
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeFTP("550 File not found")
		return
	}
	defer f.Close()

	writeFTP("150 Opening data connection")
	dataConn, err := sess.openData()
	if err != nil {
		writeFTP("425 Can't open data connection")
		return
	}
	defer dataConn.Close()

	n, err := io.Copy(dataConn, f)
	if err != nil {
		m.log(ServiceFTP, id, fmt.Sprintf("RETR error: %v", err))
		writeFTP("426 Transfer aborted")
		return
	}
	m.log(ServiceFTP, id, fmt.Sprintf("sent %d bytes", n))
	writeFTP("226 Transfer complete")
}

func (m *Manager) ftpStor(ctx context.Context, id string, sess *ftpSession, arg string, writeFTP func(string, ...any)) {
	full := m.ftpPath(sess, arg)
	if !ftpSafe(sess.rootDir, full) {
		writeFTP("550 Access denied")
		return
	}
	f, err := os.Create(full)
	if err != nil {
		writeFTP("550 File creation failed")
		return
	}
	defer f.Close()

	writeFTP("150 Opening data connection")
	dataConn, err := sess.openData()
	if err != nil {
		writeFTP("425 Can't open data connection")
		return
	}
	defer dataConn.Close()

	n, err := io.Copy(f, dataConn)
	if err != nil {
		m.log(ServiceFTP, id, fmt.Sprintf("STOR error: %v", err))
		writeFTP("426 Transfer aborted")
		return
	}
	m.log(ServiceFTP, id, fmt.Sprintf("received %d bytes", n))
	writeFTP("226 Transfer complete")
}
