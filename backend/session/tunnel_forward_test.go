package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

// A loopback SSH server supports both direct-tcpip and tcpip-forward, so the
// reconnect tests exercise real listeners and forwarded traffic in every mode.
func tunnelTestServer(t *testing.T) (ConnectionConfig, <-chan *ssh.ServerConn) {
	t.Helper()
	return tunnelTestServerConfig(t, &ssh.ServerConfig{NoClientAuth: true})
}

func tunnelTestServerConfig(t *testing.T, config *ssh.ServerConfig) (ConnectionConfig, <-chan *ssh.ServerConn) {
	t.Helper()
	signer, err := ssh.ParsePrivateKey([]byte(testHostKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	config.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sockets []net.Conn
	connected := make(chan *ssh.ServerConn, 16)
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range sockets {
			conn.Close()
		}
	})
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			sockets = append(sockets, raw)
			mu.Unlock()
			go func() {
				defer raw.Close()
				conn, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				connected <- conn
				go func() {
					var listeners []net.Listener
					defer func() {
						for _, listener := range listeners {
							listener.Close()
						}
					}()
					for req := range requests {
						if req.Type != "tcpip-forward" {
							req.Reply(false, nil)
							continue
						}
						var bind struct {
							Host string
							Port uint32
						}
						if ssh.Unmarshal(req.Payload, &bind) != nil {
							req.Reply(false, nil)
							continue
						}
						listener, err := net.Listen("tcp", net.JoinHostPort(bind.Host, strconv.Itoa(int(bind.Port))))
						if err != nil {
							req.Reply(false, nil)
							continue
						}
						listeners = append(listeners, listener)
						port := uint32(listener.Addr().(*net.TCPAddr).Port)
						req.Reply(true, ssh.Marshal(struct{ Port uint32 }{port}))
						go func() {
							for {
								local, err := listener.Accept()
								if err != nil {
									return
								}
								go func() {
									origin := local.RemoteAddr().(*net.TCPAddr)
									payload := struct {
										Host       string
										Port       uint32
										OriginHost string
										OriginPort uint32
									}{bind.Host, port, origin.IP.String(), uint32(origin.Port)}
									channel, reqs, err := conn.OpenChannel("forwarded-tcpip", ssh.Marshal(payload))
									if err != nil {
										local.Close()
										return
									}
									go ssh.DiscardRequests(reqs)
									tunnelTestPipe(channel, local)
								}()
							}
						}()
					}
				}()
				for request := range channels {
					if request.ChannelType() != "direct-tcpip" {
						request.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					go func() {
						var target struct {
							Host       string
							Port       uint32
							OriginHost string
							OriginPort uint32
						}
						if ssh.Unmarshal(request.ExtraData(), &target) != nil {
							request.Reject(ssh.ConnectionFailed, "invalid target")
							return
						}
						remote, err := net.Dial("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
						if err != nil {
							request.Reject(ssh.ConnectionFailed, err.Error())
							return
						}
						channel, reqs, err := request.Accept()
						if err != nil {
							remote.Close()
							return
						}
						go ssh.DiscardRequests(reqs)
						tunnelTestPipe(channel, remote)
					}()
				}
			}()
		}
	}()
	return ConnectionConfig{ID: "exit", Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "tester", AuthType: "password"}, connected
}

func tunnelTestPipe(a io.ReadWriteCloser, b io.ReadWriteCloser) {
	defer a.Close()
	defer b.Close()
	go func() {
		io.Copy(a, b)
		a.Close()
	}()
	io.Copy(b, a)
}

func tunnelTestReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tunnel")
		var zero T
		return zero
	}
}

func tunnelTestState(t *testing.T, events <-chan TunnelState, want TunnelStatus) TunnelState {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case st := <-events:
			if st.Status == want {
				return st
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for state %s", want)
			return TunnelState{}
		}
	}
}

func TestUserTunnelReconnectForwardsTraffic(t *testing.T) {
	for _, mode := range []TunnelMode{TunnelLocal, TunnelRemote, TunnelDynamic} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			cfg, connected := tunnelTestServer(t)
			echo, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { echo.Close() })
			go func() {
				for {
					c, err := echo.Accept()
					if err != nil {
						return
					}
					go func() { defer c.Close(); io.Copy(c, c) }()
				}
			}()
			ts := NewTunnelService()
			t.Cleanup(ts.Shutdown)
			events := make(chan TunnelState, 32)
			ts.SetStateCallback(func(st TunnelState) { events <- st })
			tunnel := Tunnel{ID: "forward", Mode: mode, SSHConnID: cfg.ID, TargetHost: "127.0.0.1", TargetPort: echo.Addr().(*net.TCPAddr).Port, AutoReconnect: true, ReconnectInterval: 1}
			st := ts.StartTunnel(tunnel, func(id string) (ConnectionConfig, bool) { return cfg, id == cfg.ID })
			if st.Status != TunnelRunning || st.LocalPort == 0 {
				t.Fatalf("start = %+v", st)
			}
			assertTraffic := func(st TunnelState) {
				t.Helper()
				addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(st.LocalPort))
				var c net.Conn
				var err error
				if mode == TunnelDynamic {
					dialer, e := proxy.SOCKS5("tcp", addr, nil, &net.Dialer{Timeout: time.Second})
					if e != nil {
						t.Fatal(e)
					}
					c, err = dialer.Dial("tcp", echo.Addr().String())
				} else {
					c, err = net.DialTimeout("tcp", addr, time.Second)
				}
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(2 * time.Second))
				if _, err := c.Write([]byte("tunnel works")); err != nil {
					t.Fatal(err)
				}
				buf := make([]byte, len("tunnel works"))
				if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "tunnel works" {
					t.Fatalf("forwarded reply = %q, error = %v", buf, err)
				}
			}
			assertTraffic(st)
			first := tunnelTestReceive(t, connected)
			tunnelTestState(t, events, TunnelRunning)
			first.Close()
			tunnelTestState(t, events, TunnelReconnecting)
			st = tunnelTestState(t, events, TunnelRunning)
			assertTraffic(st)
			ts.StopTunnel(tunnel.ID)
			if states := ts.TunnelStates(); len(states) != 1 || states[0].Status != TunnelStopped {
				t.Fatalf("state after stop = %+v", states)
			}
		})
	}
}

func TestUserTunnelRetriesUntilStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts := NewTunnelService()
		var attempts atomic.Int32
		resolve := func(string) (ConnectionConfig, bool) { attempts.Add(1); return ConnectionConfig{}, false }
		tunnel := Tunnel{ID: "missing", SSHConnID: "missing", AutoReconnect: true, ReconnectInterval: 1}
		st := ts.StartTunnel(tunnel, resolve)
		if st.Status != TunnelReconnecting || st.Error == "" {
			t.Fatalf("failed start = %+v", st)
		}
		for want := 2; want <= 4; want++ {
			time.Sleep(time.Second)
			synctest.Wait()
			if got := attempts.Load(); got != int32(want) {
				t.Fatalf("attempts = %d, want %d", got, want)
			}
		}
		ts.StopTunnel(tunnel.ID)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if attempts.Load() != 4 || ts.TunnelStates()[0].Status != TunnelStopped {
			t.Fatalf("stopped tunnel restarted: attempts=%d states=%+v", attempts.Load(), ts.TunnelStates())
		}
	})
}

func TestUserTunnelShutdownCancelsRetryAndRejectsStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts := NewTunnelService()
		attempts := 0
		resolve := func(string) (ConnectionConfig, bool) { attempts++; return ConnectionConfig{}, false }
		tunnel := Tunnel{ID: "shutdown", SSHConnID: "missing", AutoReconnect: true, ReconnectInterval: 1}
		ts.StartTunnel(tunnel, resolve)
		ts.Shutdown()
		if st := ts.StartTunnel(tunnel, resolve); st.Status != TunnelStopped {
			t.Fatalf("start after shutdown = %+v", st)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if attempts != 1 || ts.TunnelStates()[0].Status != TunnelStopped {
			t.Fatalf("shutdown tunnel restarted: attempts=%d states=%+v", attempts, ts.TunnelStates())
		}
	})
}

func TestUserTunnelStopCancelsStalledConnect(t *testing.T) {
	for _, mode := range []string{"ssh", "http", "socks5"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ln.Close() })
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, err := ln.Accept()
				if err == nil {
					accepted <- conn
				}
			}()
			ts := NewTunnelService()
			t.Cleanup(ts.Shutdown)
			cfg := ConnectionConfig{ID: "exit", Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "tester", AuthType: "password"}
			tunnel := Tunnel{ID: "stalled", SSHConnID: cfg.ID, Mode: TunnelLocal, AutoReconnect: true, ReconnectInterval: 1}
			if mode != "ssh" {
				tunnel.Upstream = &SocksProxy{Kind: mode, Host: cfg.Host, Port: cfg.Port}
			}
			started := make(chan TunnelState, 1)
			go func() { started <- ts.StartTunnel(tunnel, func(string) (ConnectionConfig, bool) { return cfg, true }) }()
			conn := tunnelTestReceive(t, accepted)
			defer conn.Close()
			stopped := make(chan bool, 1)
			go func() { ts.StopTunnel(tunnel.ID); stopped <- true }()
			tunnelTestReceive(t, stopped)
			if st := tunnelTestReceive(t, started); st.Status != TunnelStopped {
				t.Fatalf("cancelled start = %+v", st)
			}
			if states := ts.TunnelStates(); len(states) != 1 || states[0].Status != TunnelStopped {
				t.Fatalf("late state after cancellation = %+v", states)
			}
		})
	}
}

func TestUserTunnelDuplicateStartKeepsSingleConnection(t *testing.T) {
	cfg, connected := tunnelTestServer(t)
	ts := NewTunnelService()
	t.Cleanup(ts.Shutdown)
	var calls atomic.Int32
	resolve := func(string) (ConnectionConfig, bool) { calls.Add(1); return cfg, true }
	tunnel := Tunnel{ID: "duplicate", Mode: TunnelLocal, SSHConnID: cfg.ID}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			st := ts.StartTunnel(tunnel, resolve)
			if st.Status != TunnelConnecting && st.Status != TunnelRunning {
				t.Errorf("concurrent start = %+v", st)
			}
		})
	}
	wg.Wait()
	tunnelTestReceive(t, connected)
	st := ts.StartTunnel(tunnel, resolve)
	if calls.Load() != 1 || st.Status != TunnelRunning || st.LocalPort == 0 {
		t.Fatalf("duplicate start: calls=%d state=%+v", calls.Load(), st)
	}
	ts.StopTunnel(tunnel.ID)
	// A stopped listener must release its fixed port before the next start.
	tunnel.ListenPort = st.LocalPort
	if restarted := ts.StartTunnel(tunnel, resolve); restarted.Status != TunnelRunning || restarted.LocalPort != st.LocalPort {
		t.Fatalf("restart after stop = %+v", restarted)
	}
}

func TestUserTunnelDefaultsAndTestNeverRetry(t *testing.T) {
	var legacy Tunnel
	if err := json.Unmarshal([]byte(`{"id":"legacy","autoStart":true}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.AutoReconnect || legacy.ReconnectJitter != 0 || !legacy.AutoStart {
		t.Fatalf("legacy settings changed: %+v", legacy)
	}
	for _, seconds := range []int{0, -1, 86401} {
		if got := tunnelReconnectDelay(seconds, 0); got != 120*time.Second {
			t.Errorf("delay(%d) = %v", seconds, got)
		}
	}
	if got := tunnelReconnectDelay(86400, 0); got != 24*time.Hour {
		t.Fatalf("maximum delay = %v", got)
	}
	synctest.Test(t, func(t *testing.T) {
		ts := NewTunnelService()
		ts.SetStateCallback(func(st TunnelState) { t.Errorf("test emitted state: %+v", st) })
		attempts := 0
		resolve := func(string) (ConnectionConfig, bool) { attempts++; return ConnectionConfig{}, false }
		st := ts.TestTunnel(Tunnel{SSHConnID: "missing", AutoReconnect: true, ReconnectInterval: 1}, resolve)
		if st.Status != TunnelError {
			t.Fatalf("test state = %+v", st)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if attempts != 1 || len(ts.TunnelStates()) != 0 {
			t.Fatalf("test left a retry/state: attempts=%d states=%+v", attempts, ts.TunnelStates())
		}
	})
}

func TestUserTunnelDisabledReconnectStopsAfterFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts := NewTunnelService()
		attempts := 0
		resolve := func(string) (ConnectionConfig, bool) { attempts++; return ConnectionConfig{}, false }
		tunnel := Tunnel{ID: "manual", SSHConnID: "missing", ReconnectInterval: 1}
		if st := ts.StartTunnel(tunnel, resolve); st.Status != TunnelError {
			t.Fatalf("failed start = %+v", st)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if attempts != 1 {
			t.Fatalf("disabled retry made %d attempts", attempts)
		}
		if st := ts.StartTunnel(tunnel, resolve); st.Status != TunnelError || attempts != 2 {
			t.Fatalf("manual restart failed: state=%+v attempts=%d", st, attempts)
		}
	})
}

func TestUserTunnelReconnectJitterBounds(t *testing.T) {
	for _, jitter := range []int{-1, 0, 101} {
		if got := tunnelReconnectDelay(120, jitter); got != 120*time.Second {
			t.Errorf("invalid/disabled jitter %d: %v", jitter, got)
		}
	}
	values := make(map[time.Duration]bool)
	for range 100 {
		delay := tunnelReconnectDelay(120, 20)
		if delay < 96*time.Second || delay > 144*time.Second {
			t.Fatalf("20%% jitter = %v, want 96..144 seconds", delay)
		}
		values[delay] = true
		if delay := tunnelReconnectDelay(1, 100); delay < time.Second || delay > 2*time.Second {
			t.Fatalf("minimum delay = %v", delay)
		}
	}
	if len(values) < 2 {
		t.Fatal("jitter never changed between retries")
	}
}

func TestUserTunnelStartupFailureRecovers(t *testing.T) {
	t.Parallel()
	cfg, connected := tunnelTestServer(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { occupied.Close() })
	ts := NewTunnelService()
	t.Cleanup(ts.Shutdown)
	events := make(chan TunnelState, 16)
	ts.SetStateCallback(func(st TunnelState) { events <- st })
	tunnel := Tunnel{ID: "occupied", SSHConnID: cfg.ID, Mode: TunnelLocal, ListenPort: occupied.Addr().(*net.TCPAddr).Port, AutoReconnect: true, ReconnectInterval: 1}
	st := ts.StartTunnel(tunnel, func(string) (ConnectionConfig, bool) { return cfg, true })
	if st.Status != TunnelReconnecting || st.Error == "" {
		t.Fatalf("occupied listen port = %+v", st)
	}
	failedConn := tunnelTestReceive(t, connected)
	closed := make(chan bool, 1)
	go func() { failedConn.Wait(); closed <- true }()
	tunnelTestReceive(t, closed)
	occupied.Close()
	st = tunnelTestState(t, events, TunnelRunning)
	if st.LocalPort != tunnel.ListenPort {
		t.Fatalf("recovered listener port = %d, want %d", st.LocalPort, tunnel.ListenPort)
	}
}

// ssh.Client embeds ssh.Conn. Supply only the transport operations exercised by
// the heartbeat, allowing the timeout to run under synctest's virtual clock.
type tunnelHeartbeatConn struct {
	ssh.Conn
	send  func() (bool, []byte, error)
	close func()
	once  sync.Once
}

func (c *tunnelHeartbeatConn) SendRequest(_ string, wantReply bool, _ []byte) (bool, []byte, error) {
	if !wantReply {
		panic("heartbeat must require a reply")
	}
	return c.send()
}

func (c *tunnelHeartbeatConn) Close() error {
	c.once.Do(c.close)
	return nil
}

func TestUserTunnelHeartbeatClosesWholeChain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		outerClosed := make(chan struct{})
		closed := make(chan string, 2)
		outer := &ssh.Client{Conn: &tunnelHeartbeatConn{close: func() {
			closed <- "outer"
			close(outerClosed)
		}}}
		exit := &ssh.Client{Conn: &tunnelHeartbeatConn{
			send:  func() (bool, []byte, error) { <-outerClosed; return false, nil, io.EOF },
			close: func() { closed <- "exit" },
		}}
		done := make(chan struct{})
		go func() { userTunnelKeepAlive(context.Background(), []*ssh.Client{outer, exit}); close(done) }()
		time.Sleep(sshKeepAliveInterval)
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("closed chain before heartbeat timeout")
		default:
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		<-done
		first, second := <-closed, <-closed
		if first != "outer" || second != "exit" {
			t.Fatalf("close order = %s, %s; want outer then exit", first, second)
		}
	})
}

func TestUserTunnelHeartbeatAcceptsUnsupportedReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		replies := 0
		client := &ssh.Client{Conn: &tunnelHeartbeatConn{
			send:  func() (bool, []byte, error) { replies++; return false, nil, nil },
			close: func() { t.Error("closed a healthy server that rejects keepalive requests") },
		}}
		ctx, cancel := context.WithCancel(context.Background())
		go userTunnelKeepAlive(ctx, []*ssh.Client{client})
		time.Sleep(2 * sshKeepAliveInterval)
		synctest.Wait()
		cancel()
		synctest.Wait()
		if replies != 2 {
			t.Fatalf("heartbeat replies = %d, want 2", replies)
		}
	})
}

func TestUserTunnelStoppedEventMeansListenerReleased(t *testing.T) {
	cfg, _ := tunnelTestServer(t)
	ts := NewTunnelService()
	t.Cleanup(ts.Shutdown)
	tunnel := Tunnel{ID: "released", Mode: TunnelLocal, SSHConnID: cfg.ID}
	st := ts.StartTunnel(tunnel, func(string) (ConnectionConfig, bool) { return cfg, true })
	if st.Status != TunnelRunning {
		t.Fatalf("start = %+v", st)
	}
	ts.SetStateCallback(func(next TunnelState) {
		if next.Status != TunnelStopped {
			return
		}
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(st.LocalPort)))
		if err != nil {
			t.Errorf("stopped event published before releasing listener: %v", err)
			return
		}
		ln.Close()
		ts.mu.Lock()
		_, exists := ts.userTunnels[tunnel.ID]
		ts.mu.Unlock()
		if exists {
			t.Error("stopped event published before a new start can reserve the tunnel")
		}
	})
	ts.StopTunnel(tunnel.ID)
}

func TestUserTunnelCancelledPreparationDoesNotStart(t *testing.T) {
	ts := NewTunnelService()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := ts.StartTunnelContext(ctx, Tunnel{ID: "cancelled", AutoReconnect: true}, func(string) (ConnectionConfig, bool) {
		t.Error("resolved a tunnel after its start was cancelled")
		return ConnectionConfig{}, false
	})
	if st.Status != TunnelStopped || len(ts.TunnelStates()) != 0 {
		t.Fatalf("cancelled preparation = %+v; states = %+v", st, ts.TunnelStates())
	}
}

func TestUserTunnelParentCancellationStopsLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts := NewTunnelService()
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		resolve := func(string) (ConnectionConfig, bool) { attempts++; return ConnectionConfig{}, false }
		tunnel := Tunnel{ID: "parent", SSHConnID: "missing", AutoReconnect: true, ReconnectInterval: 1}
		if st := ts.StartTunnelContext(ctx, tunnel, resolve); st.Status != TunnelReconnecting {
			t.Fatalf("start = %+v", st)
		}
		cancel()
		synctest.Wait()
		states := ts.TunnelStates()
		if len(states) != 1 || states[0].Status != TunnelStopped {
			t.Fatalf("parent cancellation state = %+v", states)
		}
		if st := ts.StartTunnel(tunnel, resolve); st.Status != TunnelReconnecting || attempts != 2 {
			t.Fatalf("restart after cancellation = %+v, attempts=%d", st, attempts)
		}
		ts.StopTunnel(tunnel.ID)
	})
}

func TestUserTunnelImmediateFailurePreservesResult(t *testing.T) {
	ts := NewTunnelService()
	resolve := func(string) (ConnectionConfig, bool) { return ConnectionConfig{}, false }
	for range 500 {
		st := ts.StartTunnel(Tunnel{ID: "failure", SSHConnID: "missing"}, resolve)
		if st.Status != TunnelError || st.Error == "" {
			t.Fatalf("first failure lost to lifecycle cleanup: %+v", st)
		}
		st = ts.TestTunnel(Tunnel{SSHConnID: "missing", AutoReconnect: true}, resolve)
		if st.Status != TunnelError || st.Error == "" {
			t.Fatalf("test failure lost to lifecycle cleanup: %+v", st)
		}
	}
}

// TestStartTunnelReservesIDBeforeDialing covers the check-then-act race in
// StartTunnel: the exists-check and the insert used to sit on either side of
// the long dial, so an auto-start and a manual start of the same ID both
// dialed — the loser either overwrote the winner's entry (orphaning a listener
// and SSH chain nothing could stop) or flipped a running tunnel to error.
// With an ID reserved under the lock before dialing, only one goroutine ever
// reaches the dialer.
func TestStartTunnelReservesIDBeforeDialing(t *testing.T) {
	ts := NewTunnelService()
	var dials atomic.Int32
	gate := make(chan struct{})
	ts.dialHop = func(addr string, upstream *SocksProxy) (net.Conn, error) {
		dials.Add(1)
		<-gate
		return nil, errors.New("dial refused (test)")
	}
	resolve := func(id string) (ConnectionConfig, bool) {
		return ConnectionConfig{ID: id, Host: "127.0.0.1", Port: 22, User: "tester"}, true
	}
	tun := Tunnel{ID: "race-tunnel", Mode: TunnelLocal, SSHConnID: "exit-conn", ListenHost: "127.0.0.1"}

	const n = 6
	states := make([]TunnelState, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states[i] = ts.StartTunnel(tun, resolve)
		}()
	}

	// Wait until the first dial is in flight, then give the remaining
	// goroutines time to reach the start path.
	deadline := time.Now().Add(5 * time.Second)
	for dials.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	dialCount := dials.Load()
	close(gate)
	wg.Wait()

	if dialCount != 1 {
		t.Fatalf("concurrent StartTunnel dialed %d times, want exactly 1", dialCount)
	}
	var errorsSeen, pending int
	for i, st := range states {
		switch st.Status {
		case TunnelError:
			errorsSeen++
			if st.Error == "" {
				t.Errorf("state[%d] = error without a message", i)
			}
		case TunnelConnecting, TunnelRunning:
			// Concurrent callers observe the already-reserved lifecycle; the
			// dialer winner reports the failure itself.
			pending++
		default:
			t.Errorf("state[%d] = %s (%s), want connecting/running or error", i, st.Status, st.Error)
		}
	}
	if errorsSeen != 1 || pending != n-1 {
		t.Fatalf("got %d error and %d non-error states, want 1 and %d", errorsSeen, pending, n-1)
	}
	// The failed dial must not leave a reservation behind: the tunnel is
	// stopped and can be started again.
	ts.mu.Lock()
	_, left := ts.userTunnels[tun.ID]
	ts.mu.Unlock()
	if left {
		t.Fatal("failed StartTunnel must release its ID reservation")
	}
}

// testResolveConn is a ConnResolver that resolves any id to a dialable-looking
// config; the hop dialer is stubbed in each test, so nothing is ever reached.
func testResolveConn(id string) (ConnectionConfig, bool) {
	return ConnectionConfig{ID: id, Host: "127.0.0.1", Port: 22, User: "tester"}, true
}

// A dynamic tunnel bound to a non-loopback address is a SOCKS proxy for the
// whole network: starting it without credentials and without an explicit
// opt-in must be refused BEFORE anything is dialed, with a message that says
// what to do about it.
func TestDynamicTunnelNonLoopbackRequiresCredentials(t *testing.T) {
	ts := NewTunnelService()
	var dials atomic.Int32
	ts.dialHop = func(addr string, upstream *SocksProxy) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("dial must not happen")
	}
	tun := Tunnel{
		ID:         "open-proxy",
		Mode:       TunnelDynamic,
		SSHConnID:  "exit-conn",
		ListenHost: "0.0.0.0",
		ListenPort: 1080,
	}
	st := ts.StartTunnel(tun, testResolveConn)
	if st.Status != TunnelError {
		t.Fatalf("status = %s (%s), want error", st.Status, st.Error)
	}
	if dials.Load() != 0 {
		t.Fatalf("exposure check must reject before dialing, but dialed %d times", dials.Load())
	}
	msg := st.Error
	for _, want := range []string{"socks", "password", "127.0.0.1", "socksAllowUnauthenticated"} {
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
			t.Errorf("error must mention %q (readable guidance): %s", want, msg)
		}
	}
}

// The explicit opt-in (or real credentials) must let the same tunnel start.
func TestDynamicTunnelNonLoopbackAllowedWithCredentialsOrOptIn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tun   Tunnel
		allow bool
	}{
		{"credentials", Tunnel{Mode: TunnelDynamic, SOCKSUser: "u", SOCKSPass: "p"}, false},
		{"opt-in", Tunnel{Mode: TunnelDynamic, SOCKSAllowUnauthenticated: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSOCKSExposure(tc.tun); err != nil {
				t.Fatalf("validateSOCKSExposure: %v", err)
			}
		})
	}
	// Loopback binds stay open by default (the historical behaviour).
	if err := validateSOCKSExposure(Tunnel{Mode: TunnelDynamic, ListenHost: "127.0.0.1"}); err != nil {
		t.Fatalf("loopback bind must be allowed: %v", err)
	}
	if err := validateSOCKSExposure(Tunnel{Mode: TunnelDynamic}); err != nil {
		t.Fatalf("default bind must be allowed: %v", err)
	}
	// Local (non-SOCKS) tunnels on 0.0.0.0 are unaffected.
	if err := validateSOCKSExposure(Tunnel{Mode: TunnelLocal, ListenHost: "0.0.0.0"}); err != nil {
		t.Fatalf("local tunnel must be unaffected: %v", err)
	}
}

// An idle SOCKS client must not pin an accept-loop goroutine forever: the
// handshake is bounded and the connection is closed when it lapses.
func TestDynamicTunnelSOCKSHandshakeTimeout(t *testing.T) {
	old := socks5HandshakeTimeout
	socks5HandshakeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { socks5HandshakeTimeout = old })

	ts := NewTunnelService()
	ln, err := ts.startListener(Tunnel{Mode: TunnelDynamic, ListenHost: "127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("startListener: %v", err)
	}
	defer ln.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer conn.Close()
	// Send only the version byte, then nothing: the server must give up on
	// its own rather than waiting for a client that never speaks.
	if _, err := conn.Write([]byte{0x05}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	start := time.Now()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	if err == nil {
		t.Fatal("server must close a stalled SOCKS handshake")
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("connection still open after the handshake deadline: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("handshake close took %s, want around %s", elapsed, socks5HandshakeTimeout)
	}
}

// socks5ClientTalk runs the client half of a SOCKS5 exchange against a server
// that answers the greeting with method selection (and the RFC 1929 sub-
// negotiation when asked), then sends a CONNECT to 127.0.0.1:80. It returns
// the selected method and the auth reply status when authentication ran.
func socks5ClientTalk(conn net.Conn, methods []byte, user, pass string) (method byte, authStatus byte, err error) {
	hello := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err = conn.Write(hello); err != nil {
		return 0, 0, err
	}
	sel := make([]byte, 2)
	if _, err = io.ReadFull(conn, sel); err != nil {
		return 0, 0, err
	}
	method = sel[1]
	switch method {
	case 0x00:
	case 0x02:
		req := []byte{0x01, byte(len(user))}
		req = append(req, user...)
		req = append(req, byte(len(pass)))
		req = append(req, pass...)
		if _, err = conn.Write(req); err != nil {
			return method, 0, err
		}
		rep := make([]byte, 2)
		if _, err = io.ReadFull(conn, rep); err != nil {
			return method, 0, err
		}
		authStatus = rep[1]
		if authStatus != 0x00 {
			return method, authStatus, nil
		}
	case 0xFF:
		return method, 0, fmt.Errorf("server declined every method")
	default:
		return method, 0, fmt.Errorf("unexpected method 0x%02x", method)
	}
	// CONNECT 127.0.0.1:80
	req := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0x00, 0x50}
	if _, err = conn.Write(req); err != nil {
		return method, 0, err
	}
	return method, authStatus, nil
}

// socks5ServerOnce accepts one client, runs socks5Negotiate with the SERVER
// credentials and reports (target, server error). The CLIENT talks with its
// own credentials, so a wrong-password case is expressible. When negotiate
// succeeds the server sleeps past the handshake deadline and writes a byte —
// a deadline that was not cleared kills that write and fails the test.
func socks5ServerOnce(t *testing.T, serverUser, serverPass, clientUser, clientPass string, after time.Duration) (string, error, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	type result struct {
		target   string
		err      error
		writeErr error
	}
	resCh := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer c.Close()
		target, nerr := socks5Negotiate(c, serverUser, serverPass)
		r := result{target: target, err: nerr}
		if nerr == nil {
			time.Sleep(after)
			_, r.writeErr = c.Write([]byte("x"))
		}
		resCh <- r
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	methods := []byte{0x00}
	if clientUser != "" {
		methods = []byte{0x02, 0x00}
	}
	_, _, clientErr := socks5ClientTalk(conn, methods, clientUser, clientPass)
	if clientErr == nil {
		// Drain the post-handshake byte so the server's write has a reader.
		buf := make([]byte, 1)
		_, _ = io.ReadFull(conn, buf)
	}
	res := <-resCh
	return res.target, res.err, res.writeErr
}

// Configured credentials must actually gate the proxy, the no-auth path must
// keep working for loopback tunnels, and the handshake deadline must be
// cleared once negotiation succeeds.
func TestSOCKSNegotiateAuthAndDeadlineClearing(t *testing.T) {
	old := socks5HandshakeTimeout
	socks5HandshakeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { socks5HandshakeTimeout = old })

	t.Run("no credentials keeps no-auth", func(t *testing.T) {
		target, err, writeErr := socks5ServerOnce(t, "", "", "", "", 0)
		if err != nil {
			t.Fatalf("negotiate: %v", err)
		}
		if target != "127.0.0.1:80" {
			t.Fatalf("target = %q, want 127.0.0.1:80", target)
		}
		if writeErr != nil {
			t.Fatalf("post-handshake write failed: %v", writeErr)
		}
	})

	t.Run("credentials accepted and deadline cleared", func(t *testing.T) {
		// 4× the handshake timeout: a leftover deadline would fail the write.
		target, err, writeErr := socks5ServerOnce(t, "alice", "s3cret", "alice", "s3cret", 4*socks5HandshakeTimeout)
		if err != nil {
			t.Fatalf("negotiate: %v", err)
		}
		if target != "127.0.0.1:80" {
			t.Fatalf("target = %q, want 127.0.0.1:80", target)
		}
		if writeErr != nil {
			t.Fatalf("handshake deadline still applied after negotiation: %v", writeErr)
		}
	})

	t.Run("wrong password rejected", func(t *testing.T) {
		_, err, _ := socks5ServerOnce(t, "alice", "s3cret", "alice", "wrong", 0)
		if err == nil {
			t.Fatal("wrong password must be rejected")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "authentication") {
			t.Fatalf("error must report the failed authentication, got: %v", err)
		}
	})

	t.Run("client without auth method rejected", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer ln.Close()
		resCh := make(chan error, 1)
		go func() {
			c, err := ln.Accept()
			if err != nil {
				resCh <- err
				return
			}
			defer c.Close()
			_, nerr := socks5Negotiate(c, "alice", "s3cret")
			resCh <- nerr
		}()
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("set deadline: %v", err)
		}
		// Offer no-auth only: the server requires username/password.
		method, _, cerr := socks5ClientTalk(conn, []byte{0x00}, "", "")
		if cerr == nil && method == 0x02 {
			t.Fatal("server must not select a method the client did not offer")
		}
		if nerr := <-resCh; nerr == nil {
			t.Fatal("negotiate must fail when the client offers no credentials method")
		} else if !strings.Contains(strings.ToLower(nerr.Error()), "username/password") {
			t.Fatalf("error must name the required method, got: %v", nerr)
		}
	})
}
