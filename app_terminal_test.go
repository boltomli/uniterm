package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ys-ll/uniterm/backend/session"
	"github.com/ys-ll/uniterm/backend/store"
	"golang.org/x/crypto/ssh"
)

func TestCreateSPICESessionThroughSSHTunnel(t *testing.T) {
	testDesktopSessionThroughSSHTunnel(t, "spice", 3, 5903, []string{"main", "display", "inputs", "cursor"})
}

func TestCreateVNCSessionThroughSSHTunnel(t *testing.T) {
	testDesktopSessionThroughSSHTunnel(t, "vnc", 5900, 5900, []string{"rfb"})
}

func testDesktopSessionThroughSSHTunnel(t *testing.T, protocol string, port, targetPort int, channels []string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() != "jump" || string(password) != "jump-pw" {
				return nil, fmt.Errorf("unexpected jump-host credentials")
			}
			return nil, nil
		},
	}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	targets := make(chan string, 8)
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		conn, channels, requests, err := ssh.NewServerConn(raw, serverConfig)
		if err != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		for request := range channels {
			var target struct {
				Host       string
				Port       uint32
				OriginHost string
				OriginPort uint32
			}
			if request.ChannelType() != "direct-tcpip" || ssh.Unmarshal(request.ExtraData(), &target) != nil {
				request.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			channel, requests, err := request.Accept()
			if err != nil {
				continue
			}
			targets <- net.JoinHostPort(target.Host, fmt.Sprint(target.Port))
			go ssh.DiscardRequests(requests)
			go func() {
				defer channel.Close()
				_, _ = io.Copy(channel, channel)
			}()
		}
	}()
	connections, err := store.NewConnectionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connections.SetPasswordStore(stubPasswordStore{})
	if err := connections.Save(session.ConnectionStoreData{Connections: []session.ConnectionConfig{{
		ID: "jump", Type: "ssh", Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port,
		User: "jump", AuthType: "password",
	}}}); err != nil {
		t.Fatal(err)
	}
	a := &App{sessionManager: session.NewSessionManager(), tunnelService: session.NewTunnelService(), connectionStore: connections}
	t.Cleanup(a.tunnelService.Shutdown)
	info, err := a.CreateSession(protocol, session.ConnectionConfig{
		Type: protocol, Host: protocol + ".internal.invalid", Port: port,
		TunnelSSHConnID: "jump", TunnelSSHPassword: "jump-pw",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.CloseSession(info.ID) })
	endpoint, err := url.Parse(info.ProxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Hostname() != "127.0.0.1" || info.Status != session.StatusConnected || info.Type != protocol {
		t.Fatalf("%s did not return a connected local proxy: %+v", protocol, info)
	}
	dialer := websocket.Dialer{Subprotocols: []string{"binary"}, HandshakeTimeout: 3 * time.Second}
	var sockets []*websocket.Conn
	for _, channel := range channels {
		ws, _, err := dialer.Dial(info.ProxyAddr, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.Close() })
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		payload := "REDQ:" + channel
		if err := ws.WriteMessage(websocket.BinaryMessage, []byte(payload)); err != nil {
			t.Fatal(err)
		}
		kind, echoed, err := ws.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage || string(echoed) != payload {
			t.Fatalf("%s %s channel did not travel through SSH: %q, %v", protocol, channel, echoed, err)
		}
		select {
		case target := <-targets:
			if target != net.JoinHostPort(protocol+".internal.invalid", fmt.Sprint(targetPort)) {
				t.Fatalf("SSH channel reached %q", target)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s channel bypassed the SSH tunnel", protocol)
		}
		sockets = append(sockets, ws)
	}
	if err := a.CloseSession(info.ID); err != nil {
		t.Fatal(err)
	}
	for _, ws := range sockets {
		if _, _, err := ws.ReadMessage(); err == nil {
			t.Fatalf("%s channel survived session/tunnel shutdown", protocol)
		}
	}
}

func TestAppendFileBase64RejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows developer mode")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "download")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fileGrants.addFile(link)

	err := (&App{}).AppendFileBase64(link, base64.StdEncoding.EncodeToString([]byte("overwrite")), 0)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("AppendFileBase64 symlink error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "safe" {
		t.Fatalf("symlink target changed to %q", got)
	}
}

// TestRawFileBindingsRequireGrant guards the confinement of the raw file
// bindings: without a dialog/drop grant they must refuse both reads and
// writes, and a grant on a directory must not allow escaping it with "..".
func TestRawFileBindingsRequireGrant(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "picked.txt")
	if err := os.WriteFile(inside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("top"), 0600); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	// Ungranted absolute paths — the pre-fix behavior — must fail.
	if _, err := app.ReadFileBase64(outside); err == nil {
		t.Fatal("ReadFileBase64 accepted an ungranted path")
	}
	if _, err := app.ReadFileChunkBase64(outside, 0, 1); err == nil {
		t.Fatal("ReadFileChunkBase64 accepted an ungranted path")
	}
	if _, err := app.FileSize(outside); err == nil {
		t.Fatal("FileSize accepted an ungranted path")
	}
	if err := app.WriteFileBase64(outside, "bmV3"); err == nil {
		t.Fatal("WriteFileBase64 accepted an ungranted path")
	}
	if err := app.AppendFileBase64(outside, "bmV3", 0); err == nil {
		t.Fatal("AppendFileBase64 accepted an ungranted path")
	}

	// A granted directory allows its children but blocks ".." escapes.
	fileGrants.addDir(dir)
	if _, err := app.ReadFileBase64(inside); err != nil {
		t.Fatalf("ReadFileBase64 inside granted dir: %v", err)
	}
	escaped := filepath.Join(dir, "..", "esc")
	if err := app.WriteFileBase64(escaped, "bmV3"); err == nil {
		t.Fatal("WriteFileBase64 escaped a granted directory with ..")
	}
	if _, err := app.ReadFileBase64(filepath.Join(outside, "..", filepath.Base(outside))); err == nil {
		t.Fatal("ReadFileBase64 read an ungranted path via ..")
	}

	// The other webview-facing file bindings refuse ungranted paths too.
	if err := app.ExportConnections(outside, ""); err == nil {
		t.Fatal("ExportConnections accepted an ungranted path")
	}
	if _, err := app.SetBackgroundImage(outside); err == nil {
		t.Fatal("SetBackgroundImage accepted an ungranted path")
	}
	if _, err := app.ParseImportFile("uniterm", outside, ""); err == nil ||
		!strings.Contains(err.Error(), "not selected by a file dialog") {
		t.Fatalf("ParseImportFile ungranted error = %v", err)
	}
}

// TestSaveSettingsDoesNotGrantWebviewDir pins the SaveSettings grant bypass:
// the zmodem download directory arrives through a raw webview binding, so
// saving settings must not add it to the grant set — otherwise a compromised
// webview obtains any directory in one call. Only the dialog picker and
// LoadSettings (value read back from disk) grant it.
func TestSaveSettingsDoesNotGrantWebviewDir(t *testing.T) {
	ss, err := store.NewSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &App{settingsStore: ss}

	dir := filepath.Join(t.TempDir(), "zmodem")
	settings := store.AppSettings{}
	settings.Terminal.ZmodemDownloadDir = dir
	if err := app.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if err := requireFileGrant(filepath.Join(dir, "file")); err == nil {
		t.Fatal("SaveSettings granted a webview-supplied directory")
	}

	if _, err := app.LoadSettings(); err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if err := requireFileGrant(filepath.Join(dir, "file")); err != nil {
		t.Fatalf("LoadSettings did not grant the persisted dir: %v", err)
	}
}

// TestSftpTransfersRejectDotDot guards the remote-controlled-filename hole:
// a hostile server name like "../../x" joined onto the chosen local directory
// must not escape it (grants cannot catch this — the pane paths are browsed,
// not dialog-granted).
func TestSftpTransfersRejectDotDot(t *testing.T) {
	app := &App{}
	// Concatenated, not filepath.Join — Join would clean the ".." away.
	evil := t.TempDir() + string(filepath.Separator) + ".." + string(filepath.Separator) + "escape.bin"
	if _, err := app.SftpGet("missing-session", "/remote/f", evil, false); err == nil ||
		!strings.Contains(err.Error(), ".. segment") {
		t.Fatalf("SftpGet dotdot error = %v", err)
	}
	if _, err := app.SftpPut("missing-session", evil, "/remote/f", false); err == nil ||
		!strings.Contains(err.Error(), ".. segment") {
		t.Fatalf("SftpPut dotdot error = %v", err)
	}
}

// Only the entry hop can resolve the exit alias. Only the exit hop can
// resolve the destination alias. Direct or single-hop dialing cannot pass.
func sessionChainTestHop(t *testing.T, name, expectedHost, forwardAddr string) int {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if meta.User() != name || string(password) != name+"-password" {
			return nil, fmt.Errorf("wrong credentials")
		}
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer raw.Close()
				conn, channels, reqs, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(reqs)
				for request := range channels {
					var dest struct {
						Host       string
						Port       uint32
						OriginHost string
						OriginPort uint32
					}
					if request.ChannelType() != "direct-tcpip" || ssh.Unmarshal(request.ExtraData(), &dest) != nil || dest.Host != expectedHost {
						request.Reject(ssh.ConnectionFailed, "unexpected destination")
						continue
					}
					ch, reqs, err := request.Accept()
					if err != nil {
						continue
					}
					go ssh.DiscardRequests(reqs)
					go func() {
						defer ch.Close()
						if forwardAddr == "" {
							io.Copy(ch, ch)
							return
						}
						target, err := net.DialTimeout("tcp", forwardAddr, time.Second)
						if err != nil {
							return
						}
						defer target.Close()
						go func() { io.Copy(ch, target); ch.Close() }()
						io.Copy(target, ch)
					}()
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestSessionJumpHostChainTraffic(t *testing.T) {
	exitPort := sessionChainTestHop(t, "exit", "target.internal.invalid", "")
	entryPort := sessionChainTestHop(t, "entry", "exit.internal.invalid", net.JoinHostPort("127.0.0.1", strconv.Itoa(exitPort)))
	connections, err := store.NewConnectionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connections.SetPasswordStore(stubPasswordStore{})
	if err := connections.Save(session.ConnectionStoreData{Connections: []session.ConnectionConfig{
		{ID: "entry", Type: "ssh", Host: "127.0.0.1", Port: entryPort, User: "entry", AuthType: "password", Password: "entry-password"},
		{ID: "exit", Type: "ssh", Host: "exit.internal.invalid", Port: exitPort, User: "exit", AuthType: "password", Password: "exit-password", TunnelSSHConnID: "entry"},
	}}); err != nil {
		t.Fatal(err)
	}
	a := &App{connectionStore: connections, tunnelService: session.NewTunnelService()}
	t.Cleanup(a.tunnelService.Shutdown)
	config := session.ConnectionConfig{Type: "tcp", Host: "target.internal.invalid", Port: 2222, TunnelSSHConnID: "exit"}
	if err := a.setupJumpHostTunnel("test-chain", "tcp", &config); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(config.Host, strconv.Itoa(config.Port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, "through-both-hops"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("through-both-hops"))
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "through-both-hops" {
		t.Fatalf("forwarded data = %q, %v", buf, err)
	}
	if config.TunnelTargetHost != "target.internal.invalid" || config.TunnelTargetPort != 2222 {
		t.Fatal("lost original target")
	}
	if _, err := a.TestConnection(session.ConnectionConfig{Type: "tcp", Host: "target.internal.invalid", Port: 2222, TunnelSSHConnID: "exit"}); err != nil {
		t.Fatalf("test connection uses a different route: %v", err)
	}
}

func TestSessionJumpHostChainCredentials(t *testing.T) {
	connections, err := store.NewConnectionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connections.SetPasswordStore(stubPasswordStore{})
	identities, err := store.NewIdentityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identities.SetPasswordStore(stubPasswordStore{})
	if err := identities.Save(session.IdentityStoreData{Identities: []session.Identity{{ID: "identity", Username: "identity-user", AuthType: "password", Password: "identity-password"}}}); err != nil {
		t.Fatal(err)
	}
	a := &App{connectionStore: connections, identityStore: identities}
	for _, identity := range []bool{false, true} {
		t.Run(fmt.Sprintf("identity=%v", identity), func(t *testing.T) {
			authType := "password"
			if identity {
				authType = "identity"
			}
			if err := connections.Save(session.ConnectionStoreData{Connections: []session.ConnectionConfig{
				{ID: "entry", Type: "ssh", AuthType: authType, IdentityId: "identity"},
				{ID: "exit", Type: "ssh", User: "saved-user", Password: "saved-password", AuthType: "password", TunnelSSHConnID: "entry", TunnelSSHUser: "entry-user", TunnelSSHPassword: "entry-password"},
			}}); err != nil {
				t.Fatal(err)
			}
			chain, err := a.sessionJumpHostChain(session.ConnectionConfig{TunnelSSHConnID: "exit", TunnelSSHUser: "temporary", TunnelSSHPassword: "temporary"})
			if err != nil {
				t.Fatal(err)
			}
			wantUser, wantPassword := "entry-user", "entry-password"
			if identity {
				wantUser, wantPassword = "identity-user", "identity-password"
			}
			if chain[0].User != wantUser || chain[0].Password != wantPassword {
				t.Fatal("parent credentials resolved incorrectly")
			}
			if chain[1].User != "saved-user" || chain[1].Password != "saved-password" {
				t.Fatal("temporary credentials replaced saved credentials")
			}
		})
	}
}

func TestParseShellHistoryPlainZsh(t *testing.T) {
	// The macOS default: EXTENDED_HISTORY off, one command per line.
	raw := "ls\ncd ~/dev\ngit status\n"
	got := parseShellHistory(raw, 10)
	want := []string{"git status", "cd ~/dev", "ls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistoryExtendedZsh(t *testing.T) {
	// oh-my-zsh and friends turn EXTENDED_HISTORY on.
	raw := ": 1730000000:0;claude\n: 1730000005:12;git push\n"
	got := parseShellHistory(raw, 10)
	want := []string{"git push", "claude"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistoryTrailingNewlineContinuation(t *testing.T) {
	// Seen in the wild: a pasted command keeps its trailing newline, which zsh
	// stores as a backslash plus an empty line. The command is still usable.
	raw := "ssh app@10.0.0.1\\\n\nls\n"
	got := parseShellHistory(raw, 10)
	want := []string{"ls", "ssh app@10.0.0.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistorySkipsMultiLineCommands(t *testing.T) {
	// A loop cannot be offered as a single-line completion.
	raw := "for i in 1 2 3; do\\\necho $i\\\ndone\nls\n"
	got := parseShellHistory(raw, 10)
	want := []string{"ls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistorySkipsBashTimestamps(t *testing.T) {
	raw := "#1730000000\nls\n#1730000005\ngit status\n"
	got := parseShellHistory(raw, 10)
	want := []string{"git status", "ls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistoryDedupesKeepingNewest(t *testing.T) {
	raw := "ls\ngit status\nls\n"
	got := parseShellHistory(raw, 10)
	want := []string{"ls", "git status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistoryRespectsLimit(t *testing.T) {
	raw := "a\nb\nc\nd\n"
	got := parseShellHistory(raw, 2)
	want := []string{"d", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistoryDropsInvalidUTF8(t *testing.T) {
	raw := "ls\n\xff\xfe bad\ngit status\n"
	got := parseShellHistory(raw, 10)
	want := []string{"git status", "bad", "ls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestShellHistoryPathPrefersHistfile(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom_history")
	t.Setenv("HISTFILE", want)
	if got := shellHistoryPath(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestShellHistoryPathFollowsShell(t *testing.T) {
	t.Setenv("HISTFILE", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir reads USERPROFILE on Windows; set both so the override
	// works on every platform.
	t.Setenv("USERPROFILE", home)

	t.Setenv("SHELL", "/bin/zsh")
	if got, want := shellHistoryPath(), filepath.Join(home, ".zsh_history"); got != want {
		t.Fatalf("zsh: got %q, want %q", got, want)
	}
	t.Setenv("SHELL", "/bin/bash")
	if got, want := shellHistoryPath(), filepath.Join(home, ".bash_history"); got != want {
		t.Fatalf("bash: got %q, want %q", got, want)
	}
}

func TestReadShellHistoryTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")

	// Small file: returned whole, no truncation.
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, truncated, err := readShellHistoryTail(path, 1024)
	if err != nil || truncated || string(raw) != "first\nsecond\n" {
		t.Fatalf("small file: %q truncated=%v err=%v", raw, truncated, err)
	}

	// File larger than the cap: only the tail survives and the partial first
	// line ("ld" — the cut middle of "old-command") is dropped.
	big := "old-command\n" + strings.Repeat("x", 100) + "\nnewer\nnewest\n"
	if err := os.WriteFile(path, []byte(big), 0600); err != nil {
		t.Fatal(err)
	}
	raw, truncated, err = readShellHistoryTail(path, 20)
	if err != nil || !truncated {
		t.Fatalf("big file: truncated=%v err=%v", truncated, err)
	}
	commands := parseShellHistory(string(raw), 10)
	if len(commands) == 0 || commands[0] != "newest" {
		t.Fatalf("tail lost newest entries: %q", commands)
	}
	for _, command := range commands {
		if command == "old-command" || strings.HasPrefix(command, "x") {
			t.Fatalf("head entry survived the tail read: %q", commands)
		}
	}
}
