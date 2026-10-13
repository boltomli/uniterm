package session

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
	var errorsSeen, running int
	for i, st := range states {
		switch st.Status {
		case TunnelError:
			errorsSeen++
			if st.Error == "" {
				t.Errorf("state[%d] = error without a message", i)
			}
		case TunnelRunning:
			running++
		default:
			t.Errorf("state[%d] = %s (%s), want running or error", i, st.Status, st.Error)
		}
	}
	if errorsSeen != 1 || running != n-1 {
		t.Fatalf("got %d error and %d running states, want 1 and %d", errorsSeen, running, n-1)
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
	quit := make(chan struct{})
	ln, err := ts.startListener(Tunnel{Mode: TunnelDynamic, ListenHost: "127.0.0.1"}, nil, quit)
	if err != nil {
		t.Fatalf("startListener: %v", err)
	}
	defer func() {
		close(quit)
		ln.Close()
	}()

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
