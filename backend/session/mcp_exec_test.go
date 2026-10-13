package session

import (
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// mcpHangMarker marks an exec command that the test server acknowledges but
// never completes: the exec request is accepted, no exit-status is ever sent
// and the channel stays open until the client tears it down. That is the
// controllable "command that never finishes" the async MCPExec path needs.
const mcpHangMarker = "uniterm-hang-forever"

// startMCPTestServer serves SSH connections whose session channels answer
// `pty-req`/`shell` (so attach succeeds) and answer `exec` with an immediate
// exit status — unless the command carries mcpHangMarker.
func startMCPTestServer(t *testing.T) string {
	t.Helper()
	signer, err := ssh.ParsePrivateKey([]byte(testHostKeyPEM))
	if err != nil {
		t.Fatalf("parse test host key: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, got []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
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
					go func() {
						defer ch.Close()
						for req := range chReqs {
							if !req.WantReply {
								continue
							}
							switch req.Type {
							case "pty-req", "shell":
								req.Reply(true, nil)
							case "exec":
								cmd := ""
								if len(req.Payload) >= 4 {
									cmd = string(req.Payload[4:])
								}
								req.Reply(true, nil)
								if strings.Contains(cmd, mcpHangMarker) {
									// Never send exit-status: the command runs
									// until the client closes the channel.
									continue
								}
								_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
								ch.CloseWrite()
								ch.Close()
								return
							default:
								req.Reply(false, nil)
							}
						}
					}()
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// mcpRegistryRunning reports how many commands currently hold a concurrency
// slot (package state, locked for the test).
func mcpRegistryRunning() int {
	mcpRegistry.mu.Lock()
	defer mcpRegistry.mu.Unlock()
	return mcpRegistry.running
}

// The sync-timeout path must hand back the commandId as soon as the sync
// budget passes, even though the command is still running: MCPExec used to
// block on a deferred `<-c.done`, so the agent only ever received the handle
// after the command had already finished (or hit the hard timeout).
func TestMCPExecAsyncHandleReturnsBeforeCommandFinishes(t *testing.T) {
	addr := startMCPTestServer(t)
	s := connectTestSSHSession(t, addr)
	defer s.Disconnect()

	type execResult struct {
		commandID string
		err       error
	}
	got := make(chan execResult, 1)
	go func() {
		_, _, _, commandID, err := s.MCPExec("echo "+mcpHangMarker, 500, 60000, DefaultMCPMaxOutput)
		got <- execResult{commandID: commandID, err: err}
	}()

	var r execResult
	select {
	case r = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("MCPExec blocked on the sync-timeout path instead of returning the commandId while the command is still running")
	}
	if r.err != nil {
		t.Fatalf("MCPExec: %v", r.err)
	}
	if r.commandID == "" {
		t.Fatal("sync-timeout path returned no commandId")
	}

	// The handle must be pollable while the command is still running.
	_, _, _, state, err := s.MCPLatestOutput(r.commandID, 0, DefaultMCPMaxOutput)
	if err != nil {
		t.Fatalf("MCPLatestOutput(%s): %v", r.commandID, err)
	}
	if state != "running" {
		t.Fatalf("state = %q, want running while the command is unfinished", state)
	}

	// The concurrency slot belongs to the running command, not to the
	// already-returned MCPExec call.
	if got := mcpRegistryRunning(); got != 1 {
		t.Fatalf("running = %d, want 1 while the command is still running", got)
	}

	if err := s.MCPInterrupt(r.commandID, "SIGKILL"); err != nil {
		t.Fatalf("MCPInterrupt: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for mcpRegistryRunning() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := mcpRegistryRunning(); got != 0 {
		t.Errorf("running = %d after the command finished, want 0", got)
	}
}
