package localsrv

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// freeTCPPort reserves and releases a port for a test server.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// startTestFTP launches an FTP server for root and stops it on cleanup.
func startTestFTP(t *testing.T, root string, cfg FTPConfig) int {
	t.Helper()
	port := freeTCPPort(t)
	cfg.RootDir = root
	cfg.Port = port
	m := NewManager()
	id, err := m.StartFTPServer(cfg)
	if err != nil {
		t.Fatalf("StartFTPServer: %v", err)
	}
	t.Cleanup(func() { m.Stop(id) })
	time.Sleep(100 * time.Millisecond)
	return port
}

type testFTPClient struct {
	conn net.Conn
	r    *bufio.Reader
}

func dialTestFTP(t *testing.T, port int) *testFTPClient {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial ftp: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	c := &testFTPClient{conn: conn, r: bufio.NewReader(conn)}
	if line := c.read(); !strings.HasPrefix(line, "220") {
		t.Fatalf("unexpected banner: %q", line)
	}
	return c
}

func (c *testFTPClient) read() string {
	line, _ := c.r.ReadString('\n')
	return strings.TrimSpace(line)
}

func (c *testFTPClient) cmd(format string, args ...any) string {
	fmt.Fprintf(c.conn, format+"\r\n", args...)
	return c.read()
}

// TestFTPRequiresLogin is the regression test for the unauthenticated-access
// finding: every data command must be refused until USER/PASS succeeded.
func TestFTPRequiresLogin(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	port := startTestFTP(t, root, FTPConfig{Username: "admin", Password: "s3cret"})

	c := dialTestFTP(t, port)
	for _, cmd := range []string{"PWD", "SIZE secret.txt", "RETR secret.txt", "STOR evil.txt", "DELE secret.txt", "LIST"} {
		reply := c.cmd("%s", cmd)
		if !strings.HasPrefix(reply, "530") {
			t.Fatalf("%s before login: got %q, want 530", cmd, reply)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "secret.txt")); err != nil {
		t.Fatalf("unauthenticated DELE removed the file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "evil.txt")); err == nil {
		t.Fatal("unauthenticated STOR created a file")
	}

	if reply := c.cmd("USER admin"); !strings.HasPrefix(reply, "331") {
		t.Fatalf("USER: got %q", reply)
	}
	if reply := c.cmd("PASS wrong"); !strings.HasPrefix(reply, "530") {
		t.Fatalf("wrong password: got %q, want 530", reply)
	}
	if reply := c.cmd("PASS s3cret"); !strings.HasPrefix(reply, "230") {
		t.Fatalf("correct password: got %q, want 230", reply)
	}
	if reply := c.cmd("SIZE secret.txt"); !strings.HasPrefix(reply, "213") {
		t.Fatalf("SIZE after login: got %q, want 213", reply)
	}
}

// TestFTPRequiresCredentialsUnlessAnonymous pins the start-up validation: an
// FTP server on a LAN port must never run without an auth method.
func TestFTPRequiresCredentialsUnlessAnonymous(t *testing.T) {
	m := NewManager()
	if _, err := m.StartFTPServer(FTPConfig{RootDir: t.TempDir(), Port: freeTCPPort(t)}); err == nil {
		t.Fatal("StartFTPServer accepted an FTP server with no credentials and anonymous disabled")
	}
	if _, err := m.StartFTPServer(FTPConfig{RootDir: t.TempDir(), Port: freeTCPPort(t), Username: "u"}); err == nil {
		t.Fatal("StartFTPServer accepted a half-configured credential pair")
	}
	// Anonymous mode must keep working with no credentials.
	port := freeTCPPort(t)
	id, err := m.StartFTPServer(FTPConfig{RootDir: t.TempDir(), Port: port, Anonymous: true})
	if err != nil {
		t.Fatalf("anonymous StartFTPServer: %v", err)
	}
	defer m.Stop(id)
}

// TestFTPRejectsForeignActiveModeAddress is the regression test for the FTP
// bounce finding: PORT/EPRT must point at the control connection's peer.
func TestFTPRejectsForeignActiveModeAddress(t *testing.T) {
	root := t.TempDir()
	port := startTestFTP(t, root, FTPConfig{Username: "u", Password: "p"})

	c := dialTestFTP(t, port)
	if reply := c.cmd("USER u"); !strings.HasPrefix(reply, "331") {
		t.Fatalf("USER: %q", reply)
	}
	if reply := c.cmd("PASS p"); !strings.HasPrefix(reply, "230") {
		t.Fatalf("PASS: %q", reply)
	}
	// 10.1.2.3 is not this test's peer (127.0.0.1).
	if reply := c.cmd("PORT 10,1,2,3,4,5"); !strings.HasPrefix(reply, "501") {
		t.Fatalf("foreign PORT: got %q, want 501", reply)
	}
	if reply := c.cmd("EPRT |1|10.1.2.3|1025|"); !strings.HasPrefix(reply, "501") {
		t.Fatalf("foreign EPRT: got %q, want 501", reply)
	}
	if reply := c.cmd("EPRT |1|127.0.0.1|1025|"); strings.HasPrefix(reply, "501") {
		t.Fatalf("own-address EPRT was rejected: %q", reply)
	}
}

// TestFTPPasswordNotLogged is the regression test for the cleartext PASS in
// the server event feed (which the UI renders in the "Server output" panel).
func TestFTPPasswordNotLogged(t *testing.T) {
	const password = "Sup3rS3cret!"
	root := t.TempDir()
	port := freeTCPPort(t)

	m := NewManager()
	var mu sync.Mutex
	var messages []string
	m.SetEventEmitter(func(name string, payload any) {
		if name != EventLog {
			return
		}
		if mp, ok := payload.(map[string]any); ok {
			mu.Lock()
			messages = append(messages, fmt.Sprint(mp["message"]))
			mu.Unlock()
		}
	})
	id, err := m.StartFTPServer(FTPConfig{RootDir: root, Port: port, Username: "admin", Password: password})
	if err != nil {
		t.Fatalf("StartFTPServer: %v", err)
	}
	defer m.Stop(id)
	time.Sleep(100 * time.Millisecond)

	c := dialTestFTP(t, port)
	c.cmd("USER admin")
	c.cmd("PASS %s", password)

	mu.Lock()
	defer mu.Unlock()
	if len(messages) == 0 {
		t.Fatal("no log events captured")
	}
	for _, msg := range messages {
		if strings.Contains(msg, password) {
			t.Fatalf("password leaked into the server log: %q", msg)
		}
	}
}

// TestFTPSymlinkEscapeRefused checks that a symlink inside the served root
// cannot expose a file outside it.
func TestFTPSymlinkEscapeRefused(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}

	port := startTestFTP(t, root, FTPConfig{Username: "u", Password: "p"})
	c := dialTestFTP(t, port)
	c.cmd("USER u")
	c.cmd("PASS p")
	if reply := c.cmd("SIZE escape.txt"); !strings.HasPrefix(reply, "550") {
		t.Fatalf("SIZE through symlink: got %q, want 550", reply)
	}
	if reply := c.cmd("RETR escape.txt"); !strings.HasPrefix(reply, "550") {
		t.Fatalf("RETR through symlink: got %q, want 550", reply)
	}
}

// TestFTPRenameSourceIsConfined covers RNFR: only the RNTO destination used to
// be validated, so the source is checked too (defense in depth — the path
// resolver already collapses traversal into the root).
func TestFTPRenameSourceIsConfined(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	port := startTestFTP(t, root, FTPConfig{Username: "u", Password: "p"})
	c := dialTestFTP(t, port)
	c.cmd("USER u")
	c.cmd("PASS p")

	// Any traversal in the source has to stay inside the root: the outside
	// file must survive and must not be pulled into the shared directory.
	c.cmd("RNFR ../../outside.txt")
	c.cmd("RNTO stolen.txt")
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file disappeared: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "stolen.txt")); err == nil && strings.Contains(string(data), "OUTSIDE") {
		t.Fatalf("file outside the root was moved into it: %q", data)
	}
}
