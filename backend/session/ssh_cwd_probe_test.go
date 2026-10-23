package session

// SSH connections must never execute cwd probes or type integration commands.

import (
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// probeConnActivity records the channel requests seen on one TCP connection.
type probeConnActivity struct {
	exec   int32
	shell  int32
	killed int32
}

// startProbeTrackingServer accepts SSH connections with password auth. When
// killOnExec is set it simulates a CLI-only appliance: an exec request tears
// down the whole transport (not just the channel). Otherwise exec is served
// one-shot like the channel test server. Every connection gets an activity
// record appended before the handshake.
func startProbeTrackingServer(t *testing.T, killOnExec bool) (addr string, conns chan *probeConnActivity) {
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

	conns = make(chan *probeConnActivity, 16)

	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			act := &probeConnActivity{}
			conns <- act
			go func(raw net.Conn, act *probeConnActivity) {
				_, chans, reqs, err := ssh.NewServerConn(raw, config)
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
					go func(ch ssh.Channel, chReqs <-chan *ssh.Request) {
						defer ch.Close()
						for req := range chReqs {
							switch req.Type {
							case "shell", "pty-req":
								atomic.AddInt32(&act.shell, 1)
								if req.WantReply {
									req.Reply(true, nil)
								}
							case "exec":
								atomic.AddInt32(&act.exec, 1)
								if killOnExec {
									// Appliance behavior: the exec request kills
									// the whole transport, not just the channel.
									atomic.AddInt32(&act.killed, 1)
									raw.Close()
									return
								}
								if req.WantReply {
									req.Reply(true, nil)
								}
								_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
								ch.CloseWrite()
								ch.Close()
							default:
								if req.WantReply {
									req.Reply(false, nil)
								}
							}
						}
					}(ch, chReqs)
				}
			}(raw, act)
		}
	}()
	return ln.Addr().String(), conns
}

func probeTestConfig(addr string) ConnectionConfig {
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	return ConnectionConfig{
		User:     "admin",
		Host:     host,
		Port:     port,
		AuthType: "password",
		Password: "pw",
		Name:     "probe-test",
	}
}

// collectProbeConns drains the connection channel non-blockingly into a slice.
func collectProbeConns(conns chan *probeConnActivity) []*probeConnActivity {
	var out []*probeConnActivity
	for {
		select {
		case act := <-conns:
			out = append(out, act)
		default:
			return out
		}
	}
}

// waitForProbeConns polls until the server has recorded at least n
// connections or the deadline passes, accumulating whatever has arrived.
func waitForProbeConns(t *testing.T, conns chan *probeConnActivity, n int) []*probeConnActivity {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got []*probeConnActivity
	for time.Now().Before(deadline) {
		got = append(got, collectProbeConns(conns)...)
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server accepted %d connections, want >= %d", len(got), n)
	return nil
}

func TestSSHConnectNeverExecutesCwdProbe(t *testing.T) {
	for _, killOnExec := range []bool{false, true} {
		t.Run(strconv.FormatBool(killOnExec), func(t *testing.T) {
			addr, conns := startProbeTrackingServer(t, killOnExec)
			s := NewSSHSession("no-cwd-exec-" + t.Name())
			if err := s.Connect(probeTestConfig(addr)); err != nil {
				t.Fatal(err)
			}
			defer s.Disconnect()
			if s.Status() != StatusConnected {
				t.Fatalf("status = %s", s.Status())
			}
			acts := waitForProbeConns(t, conns, 1)
			if len(acts) != 1 {
				t.Fatalf("got %d transports, want one", len(acts))
			}
			if atomic.LoadInt32(&acts[0].shell) != 2 || atomic.LoadInt32(&acts[0].exec) != 0 {
				t.Fatal("expected pty-req + shell without exec")
			}
		})
	}
}
