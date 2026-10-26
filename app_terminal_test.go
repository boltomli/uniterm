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
	"runtime"
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
