package session

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// setFTPCodec configures s with the encoding a Connect(config) would pick.
func setFTPCodec(s *FTPSession, name string) {
	if name == "" || name == "utf-8" {
		name = ""
	}
	s.encName = name
	s.enc = encodingByName(name)
}

// --- Name decoding (garbled Chinese directory names) ---

func TestFTPDecodeNameUTF8Fallback(t *testing.T) {
	s := NewFTPSession("ftp-test")
	setFTPCodec(s, "utf-8")

	// Server sends GBK bytes for "中文" — invalid UTF-8, must be decoded.
	if got := s.ftpDecodeName("\xd6\xd0\xce\xc4"); got != "中文" {
		t.Fatalf("GBK fallback decode: got %q, want 中文", got)
	}
	// Valid UTF-8 names must pass through untouched.
	if got := s.ftpDecodeName("日本語.txt"); got != "日本語.txt" {
		t.Fatalf("valid UTF-8 passthrough: got %q", got)
	}
}

func TestFTPDecodeNameExplicitGBK(t *testing.T) {
	s := NewFTPSession("ftp-test")
	setFTPCodec(s, "gbk")

	if got := s.ftpDecodeName("\xd6\xd0\xce\xc4"); got != "中文" {
		t.Fatalf("GBK decode: got %q, want 中文", got)
	}
}

func TestFTPDecodeNameShiftJIS(t *testing.T) {
	s := NewFTPSession("ftp-test")
	setFTPCodec(s, "shift-jis")

	if got := s.ftpDecodeName("\x93\xfa\x96{"); got != "日本" {
		t.Fatalf("Shift-JIS decode: got %q, want 日本", got)
	}
}

func TestFTPDecodeNameLatin1(t *testing.T) {
	s := NewFTPSession("ftp-test")
	setFTPCodec(s, "latin-1")

	if got := s.ftpDecodeName("\xe9"); got != "é" {
		t.Fatalf("latin-1 decode: got %q, want é", got)
	}
}

// --- Path encoding (outgoing Retr/Stor/etc. must speak the server charset) ---

func TestFTPEncodePathGBK(t *testing.T) {
	s := NewFTPSession("ftp-test")
	setFTPCodec(s, "gbk")

	want := "/a/\xd6\xd0\xce\xc4/x.txt"
	if got := s.ftpEncodePath("/a/中文/x.txt"); got != want {
		t.Fatalf("GBK encode: got %q, want %q", got, want)
	}
}

func TestFTPEncodePathPassthroughUTF8(t *testing.T) {
	s := NewFTPSession("ftp-test")
	setFTPCodec(s, "utf-8")

	in := "/a/中文/x.txt"
	if got := s.ftpEncodePath(in); got != in {
		t.Fatalf("UTF-8 passthrough: got %q, want %q", got, in)
	}
}

// A name recovered via the GBK fallback must round-trip: operations on it
// go out with the exact raw bytes the server originally sent.
func TestFTPEncodePathRestoresRawBytes(t *testing.T) {
	s := NewFTPSession("ftp-test")
	setFTPCodec(s, "utf-8")

	raw := "\xd6\xd0\xce\xc4"
	dec := s.ftpDecodeName(raw)
	if dec != "中文" {
		t.Fatalf("decode prerequisite failed: got %q", dec)
	}
	want := "/d/" + raw
	if got := s.ftpEncodePath("/d/" + dec); got != want {
		t.Fatalf("raw-byte restore: got %q, want %q", got, want)
	}
}

// --- Transfer cancellation (X button has no effect) ---

type blockingReader struct{ ch chan struct{} }

func (b *blockingReader) Read([]byte) (int, error) {
	<-b.ch
	return 0, errors.New("unreachable")
}

func TestFTPProgressReaderCancel(t *testing.T) {
	s := NewFTPSession("ftp-test")
	task := &TransferTask{ID: "t1"}
	task.start()
	defer task.done()
	task.cancel() // cancel before the first read

	pr := &progressReader{r: &blockingReader{ch: make(chan struct{})}, task: task, s: s}
	errCh := make(chan error, 1)
	go func() {
		_, err := pr.Read(make([]byte, 128))
		errCh <- err
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Read after cancel: got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read blocked after cancellation")
	}
}

// --- Fake FTP server harness -------------------------------------------------
//
// Scripted plaintext FTP control server for Connect/transfer tests: one
// goroutine per accepted connection, every command line recorded into a
// mutex-protected transcript, handlers chosen by accept order (Connect may
// open several connections during TLS negotiation and probing).

type fakeFTP struct {
	ln       net.Listener
	mu       sync.Mutex
	cmds     []string
	conns    []*fakeFTPConn
	handlers []func(*fakeFTPConn)
	wg       sync.WaitGroup
}

type fakeFTPConn struct {
	srv  *fakeFTP
	conn net.Conn
	r    *bufio.Reader
	mu   sync.Mutex
	data net.Listener // pending PASV/EPSV data listener
}

func newFakeFTP(t *testing.T, handlers ...func(*fakeFTPConn)) *fakeFTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake FTP listen: %v", err)
	}
	fs := &fakeFTP{ln: ln, handlers: handlers}
	fs.wg.Add(1)
	go func() {
		defer fs.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			fc := &fakeFTPConn{srv: fs, conn: c, r: bufio.NewReader(c)}
			fs.mu.Lock()
			fs.conns = append(fs.conns, fc)
			idx := len(fs.conns) - 1
			fs.mu.Unlock()
			fs.wg.Add(1)
			go func() {
				defer fs.wg.Done()
				defer c.Close()
				if idx < len(fs.handlers) {
					fs.handlers[idx](fc)
				}
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		fs.mu.Lock()
		conns := append([]*fakeFTPConn(nil), fs.conns...)
		fs.mu.Unlock()
		for _, fc := range conns {
			fc.conn.Close()
			fc.closeData()
		}
		fs.wg.Wait()
	})
	return fs
}

func (fs *fakeFTP) port() int { return fs.ln.Addr().(*net.TCPAddr).Port }

// transcript returns a copy of every command received so far (all conns).
func (fs *fakeFTP) transcript() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.cmds...)
}

// read reads one command line and records it in the transcript.
func (f *fakeFTPConn) read() (string, error) {
	line, err := f.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	f.srv.mu.Lock()
	f.srv.cmds = append(f.srv.cmds, line)
	f.srv.mu.Unlock()
	return line, nil
}

func (f *fakeFTPConn) send(line string) error {
	_, err := io.WriteString(f.conn, line+"\r\n")
	return err
}

// listenData opens a fresh passive data listener for the next EPSV/PASV.
func (f *fakeFTPConn) listenData() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	old := f.data
	f.data = ln
	f.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func (f *fakeFTPConn) closeData() {
	f.mu.Lock()
	ln := f.data
	f.data = nil
	f.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

// serveData accepts the pending data connection, announces 150, runs body
// over it, closes the data connection (classic order) and completes the
// transfer with 226 on the control connection.
func (f *fakeFTPConn) serveData(body func(net.Conn)) {
	f.mu.Lock()
	ln := f.data
	f.data = nil
	f.mu.Unlock()
	if ln == nil {
		f.send("425 Use EPSV or PASV first")
		return
	}
	dc, err := ln.Accept()
	ln.Close()
	if err != nil {
		f.send("425 Cannot accept data connection")
		return
	}
	defer func() {
		dc.Close()
		f.send("226 Transfer complete")
	}()
	f.send("150 Opening data connection")
	if body != nil {
		body(dc)
	}
}

type ftpFakeFile struct {
	data  string
	abort bool // after sending data, reset the data conn instead of closing it
}

type ftpPlainOpts struct {
	feat    []string          // FEAT feature lines (sent with a leading space)
	listing map[string]string // MLSD <path> -> listing payload
	files   map[string]*ftpFakeFile
}

// ftpPlainServer serves a full plaintext session: login, FEAT, TYPE, passive
// mode, MLSD and RETR. Unknown commands get 500.
func ftpPlainServer(opts ftpPlainOpts) func(*fakeFTPConn) {
	return func(f *fakeFTPConn) {
		if err := f.send("220 fake FTP server ready"); err != nil {
			return
		}
		for {
			line, err := f.read()
			if err != nil {
				return
			}
			cmd, arg, _ := strings.Cut(line, " ")
			switch strings.ToUpper(cmd) {
			case "USER":
				f.send("331 Password required")
			case "PASS":
				f.send("230 User logged in")
			case "FEAT":
				f.send("211-Features:")
				for _, feature := range opts.feat {
					f.send(" " + feature)
				}
				f.send("211 End")
			case "TYPE":
				f.send("200 " + arg + " okay")
			case "OPTS":
				f.send("200 okay")
			case "EPSV":
				port, err := f.listenData()
				if err != nil {
					f.send("425 Cannot open data connection")
					continue
				}
				f.send(fmt.Sprintf("229 Entering Extended Passive Mode (|||%d|)", port))
			case "PASV":
				port, err := f.listenData()
				if err != nil {
					f.send("425 Cannot open data connection")
					continue
				}
				f.send(fmt.Sprintf("227 Entering Passive Mode (127,0,0,1,%d,%d)", port/256, port%256))
			case "MLSD":
				payload, ok := opts.listing[arg]
				if !ok {
					f.send("550 No such directory")
					continue
				}
				f.serveData(func(dc net.Conn) { io.WriteString(dc, payload) })
			case "RETR":
				file, ok := opts.files[arg]
				if !ok {
					f.send("550 No such file")
					continue
				}
				f.serveData(func(dc net.Conn) {
					io.WriteString(dc, file.data)
					if file.abort {
						if tc, ok := dc.(*net.TCPConn); ok {
							tc.SetLinger(0)
						}
					}
				})
			case "DELE", "RMD":
				f.send("250 Okay")
			case "QUIT":
				f.send("221 Bye")
				return
			default:
				f.send("500 Unknown command")
			}
		}
	}
}

// ftpAuthTLSReject greets and refuses AUTH TLS with the given reply code.
func ftpAuthTLSReject(code int) func(*fakeFTPConn) {
	return func(f *fakeFTPConn) {
		if err := f.send("220 fake FTP server ready"); err != nil {
			return
		}
		line, err := f.read()
		if err != nil {
			return
		}
		if !strings.HasPrefix(strings.ToUpper(line), "AUTH TLS") {
			return
		}
		f.send(fmt.Sprintf("%d AUTH TLS not supported here", code))
	}
}

// ftpCloseOnAuthTLS greets, reads AUTH TLS and hangs up without replying —
// a transport-level failure, not a protocol rejection.
func ftpCloseOnAuthTLS() func(*fakeFTPConn) {
	return func(f *fakeFTPConn) {
		if err := f.send("220 fake FTP server ready"); err != nil {
			return
		}
		_, _ = f.read() // AUTH TLS — vanish without a reply
	}
}

// connectPlain connects a fresh session to fs with default (plaintext) settings.
func connectPlain(t *testing.T, fs *fakeFTP) *FTPSession {
	t.Helper()
	s := NewFTPSession("ftp-fake")
	if err := s.Connect(ConnectionConfig{Host: "127.0.0.1", Port: fs.port(), User: "u", Password: "p"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { s.Disconnect() })
	return s
}

// --- Recursive download: server-supplied names (security.md P0) ---

// MLSD servers list cdir/pdir (".", "..") as entries; they are listing noise,
// not downloadable children. A recursive walker must skip them — following
// "." recurses forever — and must route every other name through
// safeDownloadChild so a malicious "..\x" name cannot escape the target dir.
func TestFTPDownloadDirLegitNames(t *testing.T) {
	fs := newFakeFTP(t, ftpPlainServer(ftpPlainOpts{
		feat: []string{"MLST size;type;modify;"},
		listing: map[string]string{
			"/dl": "type=file;size=5; .\r\ntype=file;size=9; ..\r\ntype=file;size=11; hello.txt\r\n",
		},
		files: map[string]*ftpFakeFile{
			"/dl/hello.txt": {data: "hello world"},
		},
	}))
	s := connectPlain(t, fs)

	localDir := filepath.Join(t.TempDir(), "out")
	task := &TransferTask{ID: "dl-dot", Type: "download", RemotePath: "/dl", LocalPath: localDir}
	task.start()
	defer task.done()

	if err := s.downloadDir("/dl", localDir, task); err != nil {
		t.Fatalf("downloadDir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(localDir, "hello.txt"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("content = %q, want %q", got, "hello world")
	}
	for _, cmd := range fs.transcript() {
		if rest, ok := strings.CutPrefix(cmd, "RETR "); ok && rest != "/dl/hello.txt" {
			t.Errorf("dot entries must never be fetched, got %q", cmd)
		}
	}
}

// A "..\evil.txt" listing name must be rejected before any local join: on
// Windows the backslash is a separator and filepath.Join would escape
// localDir (CVE-2019-6111 class).
func TestFTPDownloadDirRejectsTraversalName(t *testing.T) {
	fs := newFakeFTP(t, ftpPlainServer(ftpPlainOpts{
		feat: []string{"MLST size;type;"},
		listing: map[string]string{
			"/dl": "type=file;size=6; ..\\evil.txt\r\n",
		},
		files: map[string]*ftpFakeFile{
			"/dl/..\\evil.txt": {data: "escape"},
		},
	}))
	s := connectPlain(t, fs)

	out := t.TempDir()
	localDir := filepath.Join(out, "d")
	escaped := filepath.Join(out, "evil.txt")
	defer os.Remove(escaped)

	task := &TransferTask{ID: "dl-trav", Type: "download", RemotePath: "/dl", LocalPath: localDir}
	task.start()
	defer task.done()

	err := s.downloadDir("/dl", localDir, task)
	if err == nil {
		t.Fatal("downloadDir must reject a server name containing a path separator")
	}
	if !strings.Contains(err.Error(), "unsafe remote file name") {
		t.Errorf("error = %v, want unsafe remote file name", err)
	}
	if _, statErr := os.Stat(escaped); statErr == nil {
		t.Errorf("file escaped localDir: %s exists", escaped)
	}
	for _, cmd := range fs.transcript() {
		if strings.HasPrefix(cmd, "RETR ") {
			t.Errorf("traversal name must be rejected before RETR, got %q", cmd)
		}
	}
}

// rmRecursive/dirSizeRemote must ignore cdir/pdir entries: following ".."
// walks the parent directory (deleting/scanning outside the requested tree),
// following "." recurses forever.
func TestFTPRemoteRecursionSkipsDotEntries(t *testing.T) {
	listings := map[string]string{
		"/dl": "type=dir;size=0; ..\r\ntype=file;size=4; a.txt\r\n",
		"/":   "type=file;size=1; x\r\n",
	}
	newSession := func(t *testing.T) (*FTPSession, *fakeFTP) {
		t.Helper()
		fs := newFakeFTP(t, ftpPlainServer(ftpPlainOpts{
			feat:    []string{"MLST size;type;"},
			listing: listings,
		}))
		return connectPlain(t, fs), fs
	}
	assertNoParent := func(t *testing.T, fs *fakeFTP) {
		t.Helper()
		for _, cmd := range fs.transcript() {
			if cmd == "MLSD /" || cmd == "RMD /" || cmd == "DELE /x" {
				t.Errorf("recursed into parent directory: %q", cmd)
			}
		}
	}

	t.Run("rmRecursive", func(t *testing.T) {
		s, fs := newSession(t)
		if err := s.rmRecursive("/dl"); err != nil {
			t.Fatalf("rmRecursive: %v", err)
		}
		assertNoParent(t, fs)
	})

	t.Run("dirSizeRemote", func(t *testing.T) {
		s, fs := newSession(t)
		size, err := s.dirSizeRemote("/dl")
		if err != nil {
			t.Fatalf("dirSizeRemote: %v", err)
		}
		if size != 4 {
			t.Errorf("size = %d, want 4 (parent listing must not be counted)", size)
		}
		assertNoParent(t, fs)
	})
}

// --- FTPS TLS configuration ---

// Without ServerName a verified TLS handshake always fails with "either
// ServerName or InsecureSkipVerify must be specified", which pushed users to
// disable verification entirely. The name is the host with any :port stripped
// (certificates never carry the port), including the bracketed IPv6 form.
func TestFTPAuthTLSServerName(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		want       string
		skipVerify bool
	}{
		{name: "bare hostname", host: "ftp.example.com", want: "ftp.example.com"},
		{name: "hostname with port", host: "ftp.example.com:2121", want: "ftp.example.com"},
		{name: "ipv4 with port", host: "192.168.10.7:990", want: "192.168.10.7", skipVerify: true},
		{name: "bracketed ipv6 with port", host: "[::1]:2121", want: "::1"},
		{name: "bracketed ipv6", host: "[::1]", want: "::1"},
		{name: "bare ipv6", host: "::1", want: "::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ConnectionConfig{Host: tt.host, FtpSkipVerify: tt.skipVerify}
			got := ftpTLSConfig(cfg)
			if got.ServerName != tt.want {
				t.Errorf("ServerName = %q, want %q", got.ServerName, tt.want)
			}
			if got.InsecureSkipVerify != tt.skipVerify {
				t.Errorf("InsecureSkipVerify = %v, want %v", got.InsecureSkipVerify, tt.skipVerify)
			}
		})
	}
}

// --- auto-mode TLS downgrade policy ---

// FtpEncryption "auto" may fall back to plaintext only when the server
// genuinely has no FTPS: a real 500 to AUTH TLS plus a credential-free FEAT
// probe that does NOT advertise AUTH TLS. The downgrade itself must be
// visible in the session feed — sending a password in the clear must never
// look like a normal connection.
func TestFTPConnectAutoDowngradesWithVisibleWarning(t *testing.T) {
	fs := newFakeFTP(t,
		ftpAuthTLSReject(500), // conn 1: explicit-TLS dial, AUTH TLS refused with 500
		// conn 2: FEAT probe — no AUTH TLS advertised. Also serves as the
		// plaintext login target of the pre-fix fallback (RED run).
		ftpPlainServer(ftpPlainOpts{feat: []string{"MLST size;type;"}}),
		ftpPlainServer(ftpPlainOpts{feat: []string{"MLST size;type;"}}), // conn 3: login
	)
	s := NewFTPSession("ftp-fake")
	var out strings.Builder
	s.SetOnDataCallback(func(data []byte) { out.Write(data) })
	err := s.Connect(ConnectionConfig{
		Host: "127.0.0.1", Port: fs.port(), User: "u", Password: "p",
		FtpEncryption: "auto",
	})
	if err != nil {
		t.Fatalf("Connect with auto encryption: %v", err)
	}
	defer s.Disconnect()
	if !strings.Contains(out.String(), "NOT encrypted") {
		t.Errorf("plaintext downgrade must warn in the session feed; captured output:\n%s", out.String())
	}
}

// A MITM answering 500 to AUTH TLS while the server's FEAT still advertises
// it is inconsistent (STARTTLS stripping shape): auto mode must refuse the
// downgrade and must never send PASS.
func TestFTPConnectAutoRefusesWhenFeatAdvertisesTLS(t *testing.T) {
	fs := newFakeFTP(t,
		ftpAuthTLSReject(500),
		ftpPlainServer(ftpPlainOpts{feat: []string{"AUTH TLS", "MLST size;type;"}}),
	)
	s := NewFTPSession("ftp-fake")
	err := s.Connect(ConnectionConfig{
		Host: "127.0.0.1", Port: fs.port(), User: "u", Password: "p",
		FtpEncryption: "auto",
	})
	if err == nil {
		s.Disconnect()
		t.Fatal("Connect must fail: AUTH TLS rejected yet FEAT advertises it (likely tampering)")
	}
	for _, cmd := range fs.transcript() {
		if strings.HasPrefix(cmd, "PASS ") {
			t.Errorf("credentials sent in clear despite tampering shape: %q", cmd)
		}
	}
}

// AUTH TLS failing at transport level (connection dropped, no reply) is not
// a server rejection — auto mode must fail instead of downgrading and must
// never send PASS.
func TestFTPConnectAutoRefusesOnConnectionError(t *testing.T) {
	fs := newFakeFTP(t,
		ftpCloseOnAuthTLS(), // conn 1: drops the connection when AUTH TLS arrives
		ftpPlainServer(ftpPlainOpts{feat: []string{"MLST size;type;"}}), // conn 2: working plaintext server, must not be used
	)
	s := NewFTPSession("ftp-fake")
	err := s.Connect(ConnectionConfig{
		Host: "127.0.0.1", Port: fs.port(), User: "u", Password: "p",
		FtpEncryption: "auto",
	})
	if err == nil {
		s.Disconnect()
		t.Fatal("Connect must fail: AUTH TLS failed at transport level, downgrade forbidden")
	}
	for _, cmd := range fs.transcript() {
		if strings.HasPrefix(cmd, "PASS ") {
			t.Errorf("password sent after transport-level AUTH TLS failure: %q", cmd)
		}
	}
}

// --- transferFile error handling ---

// A data connection reset mid-transfer leaves a short local file: transferFile
// must surface the read error instead of reporting a completed download
// (security.md "Write and read errors are ignored").
func TestFTPTransferFileReturnsReadError(t *testing.T) {
	fs := newFakeFTP(t, ftpPlainServer(ftpPlainOpts{
		feat: []string{"MLST size;type;"},
		files: map[string]*ftpFakeFile{
			"/dl/part.bin": {data: "0123456789", abort: true},
		},
	}))
	s := connectPlain(t, fs)

	task := &TransferTask{ID: "rf-1", Type: "download", RemotePath: "/dl/part.bin"}
	task.start()
	defer task.done()

	local := filepath.Join(t.TempDir(), "part.bin")
	err := s.transferFile(task, local, "/dl/part.bin", "download")
	if err == nil {
		t.Fatal("transferFile must report the truncated data connection, got nil")
	}
	if errors.Is(err, io.EOF) {
		t.Errorf("truncation must not look like a clean end of stream: %v", err)
	}
}
