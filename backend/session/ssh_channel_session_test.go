package session

// Regression tests for issue #983 (Xshell-style "duplicate channel"):
// NewSSHChannelSession must open a new session channel on the source
// session's authenticated client — no second TCP connection, no re-auth —
// and must keep the shared client alive after the source disconnects.

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testHostKeyPEM is re-exported from ssh_dial_test.go in this package.
// startChannelTestServer accepts session channels and runs a trivial echo
// shell: whatever the client types is written back. Returns the listener's
// address plus counters for accepted TCP connections and opened channels.
func startChannelTestServer(t *testing.T) (addr string, tcpConns *int32, channels *int32) {
	t.Helper()
	signer, err := ssh.ParsePrivateKey([]byte(testHostKeyPEM))
	if err != nil {
		t.Fatalf("parse test host key: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, got []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil // accept any password
		},
	}
	config.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	var connCount, chCount int32
	tcpConns, channels = &connCount, &chCount

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connCount++
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					if newCh.ChannelType() != "session" {
						newCh.Reject(ssh.UnknownChannelType, "only session channels")
						continue
					}
					ch, chReqs, err := newCh.Accept()
					if err != nil {
						continue
					}
					chCount++
					go func(ch ssh.Channel, chReqs <-chan *ssh.Request) {
						defer ch.Close()
						for req := range chReqs {
							switch req.Type {
							case "shell", "pty-req":
								if req.WantReply {
									req.Reply(true, nil)
								}
							case "exec":
								if req.WantReply {
									req.Reply(true, nil)
								}
							default:
								if req.WantReply {
									req.Reply(false, nil)
								}
							}
						}
					}(ch, chReqs)
					// Echo loop: send back whatever arrives.
					go func(ch ssh.Channel) {
						buf := make([]byte, 4096)
						for {
							n, err := ch.Read(buf)
							if n > 0 {
								if _, werr := ch.Write(buf[:n]); werr != nil {
									return
								}
							}
							if err != nil {
								return
							}
						}
					}(ch)
				}
			}()
		}
	}()
	return ln.Addr().String(), tcpConns, channels
}

func dialTestSSHClient(t *testing.T, addr string) *ssh.Client {
	t.Helper()
	config := &ssh.ClientConfig{
		User:            "tester",
		Auth:            []ssh.AuthMethod{ssh.Password("pw")},
		Timeout:         5 * time.Second,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// noIntegration returns a config with shell integration explicitly off, so
// attach opens exactly one session channel (integration's exec-channel shell
// detection would otherwise inflate the channel count).
func noIntegration() ConnectionConfig {
	off := false
	return ConnectionConfig{User: "tester", Host: "testhost", AuthType: "password", Password: "pw", ShellIntegration: &off}
}

// connectTestSSHSession builds an SSHSession wired to a fresh client on the
// test server, completing Connect's channel attach (the dial step is
// bypassed by assigning the clientRef directly).
func connectTestSSHSession(t *testing.T, addr string) *SSHSession {
	t.Helper()
	client := dialTestSSHClient(t, addr)
	s := NewSSHSession("test-" + t.Name())
	s.mu.Lock()
	s.clientRef = newSSHClientRef(client)
	s.mu.Unlock()
	if err := s.attach(client, noIntegration()); err != nil {
		t.Fatalf("attach: %v", err)
	}
	return s
}

func TestSSHChannelCloneSharesConnection(t *testing.T) {
	addr, tcpConns, channels := startChannelTestServer(t)

	parent := connectTestSSHSession(t, addr)
	if parent.Status() != StatusConnected {
		t.Fatalf("parent status = %s, want connected", parent.Status())
	}

	clone := NewSSHChannelSession("clone-1", parent)
	if !clone.IsChannelClone() {
		t.Fatal("clone reports IsChannelClone() = false")
	}
	if clone.RemoteOS() != parent.RemoteOS() {
		t.Fatalf("clone remoteOS %q != parent %q", clone.RemoteOS(), parent.RemoteOS())
	}
	if err := clone.Connect(noIntegration()); err != nil {
		t.Fatalf("clone connect: %v", err)
	}
	defer clone.Disconnect()

	if clone.Status() != StatusConnected {
		t.Fatalf("clone status = %s, want connected", clone.Status())
	}

	// Core assertion (issue #983): one TCP connection, two session channels.
	if got := *tcpConns; got != 1 {
		t.Fatalf("server accepted %d TCP connections, want 1 (clone must not re-dial)", got)
	}
	if got := *channels; got != 2 {
		t.Fatalf("server opened %d channels, want 2 (parent + clone)", got)
	}
}

func TestSSHChannelCloneEcho(t *testing.T) {
	addr, _, _ := startChannelTestServer(t)
	parent := connectTestSSHSession(t, addr)
	defer parent.Disconnect()

	clone := NewSSHChannelSession("clone-2", parent)
	if err := clone.Connect(noIntegration()); err != nil {
		t.Fatalf("clone connect: %v", err)
	}

	var got bytes.Buffer
	clone.SetOnDataCallback(func(data []byte) { got.Write(data) })

	if err := clone.Write([]byte("ping")); err != nil {
		t.Fatalf("clone write: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(got.String(), "ping") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(got.String(), "ping") {
		t.Fatalf("clone did not echo input; got %q", got.String())
	}
	clone.Disconnect()
}

func TestSSHChannelCloneOutlivesParent(t *testing.T) {
	addr, _, _ := startChannelTestServer(t)
	parent := connectTestSSHSession(t, addr)

	clone := NewSSHChannelSession("clone-3", parent)
	if err := clone.Connect(noIntegration()); err != nil {
		t.Fatalf("clone connect: %v", err)
	}

	// Closing the source tab must not tear down the shared client.
	parent.Disconnect()
	if parent.Status() != StatusDisconnected {
		t.Fatalf("parent status = %s, want disconnected", parent.Status())
	}
	if clone.Status() != StatusConnected {
		t.Fatalf("clone status = %s after parent disconnect, want connected", clone.Status())
	}

	var got bytes.Buffer
	clone.SetOnDataCallback(func(data []byte) { got.Write(data) })
	if err := clone.Write([]byte("still-alive")); err != nil {
		t.Fatalf("clone write after parent disconnect: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(got.String(), "still-alive") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(got.String(), "still-alive") {
		t.Fatalf("clone not usable after parent disconnect; got %q", got.String())
	}

	// The last holder's disconnect closes the shared client.
	clone.Disconnect()
	if clone.Status() != StatusDisconnected {
		t.Fatalf("clone status = %s, want disconnected", clone.Status())
	}
}

func TestSSHChannelCloneWithoutSourceClient(t *testing.T) {
	parent := NewSSHSession("orphan-parent")
	clone := NewSSHChannelSession("clone-4", parent)
	err := clone.Connect(noIntegration())
	if err == nil {
		clone.Disconnect()
		t.Fatal("expected error when source session never connected")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("error %q does not mention the missing source connection", err.Error())
	}
}
