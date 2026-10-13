package session

// Regression tests for security.md P3 "SSH handshake has no deadline, and the
// session tunnel holds ts.mu through it": a server or middlebox that accepts
// TCP and then stalls leaves Connect stuck at "Connecting" forever, and one
// stalled jump host blocks every tunnel bookkeeping call.

import (
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startSilentSSHServer accepts one TCP connection and then never speaks SSH,
// holding the connection open until the test ends.
func startSilentSSHServer(t *testing.T) (addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	stall := make(chan struct{})
	t.Cleanup(func() {
		ln.Close()
		close(stall)
	})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		<-stall
	}()
	return ln.Addr().String()
}

func passwordFactory(timeout time.Duration) sshClientConfigFactory {
	return func() (*ssh.ClientConfig, func(), error) {
		return &ssh.ClientConfig{
			User:            "admin",
			Auth:            []ssh.AuthMethod{ssh.Password("pw")},
			Timeout:         timeout,
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		}, func() {}, nil
	}
}

// TestDialSSHTimesOutOnStalledHandshake: key exchange against a server that
// accepts TCP and then goes silent must abort once the configured timeout
// lapses. Before the fix no conn deadline was ever set (ClientConfig.Timeout
// only covers ssh.Dial's TCP connect), so the handshake blocked forever.
func TestDialSSHTimesOutOnStalledHandshake(t *testing.T) {
	addr := startSilentSSHServer(t)
	sets, err := resolveSSHDialAlgorithms(nil)
	if err != nil {
		t.Fatalf("algorithms: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := dialSSHWithCipherFallback(addr, sets, passwordFactory(300*time.Millisecond), func() (net.Conn, error) {
			return net.DialTimeout("tcp", addr, 2*time.Second)
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("handshake with a silent server succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("handshake never timed out — a stalled server blocks Connect forever")
	}
}

// TestDialSSHClearsHandshakeDeadline: once the handshake succeeds the conn
// deadline must be cleared, or every later keystroke fails the moment the
// (long past) handshake deadline lapses.
func TestDialSSHClearsHandshakeDeadline(t *testing.T) {
	serverCfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, got []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	addr := startAuthTestServer(t, serverCfg)
	sets, err := resolveSSHDialAlgorithms(nil)
	if err != nil {
		t.Fatalf("algorithms: %v", err)
	}

	client, err := dialSSHWithCipherFallback(addr, sets, passwordFactory(300*time.Millisecond), func() (net.Conn, error) {
		return net.DialTimeout("tcp", addr, 2*time.Second)
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Let the handshake deadline (300ms) lapse, then require working I/O.
	time.Sleep(600 * time.Millisecond)
	if _, _, err := client.SendRequest("keepalive@test", true, nil); err != nil {
		t.Fatalf("request after the handshake deadline lapsed — the deadline was not cleared: %v", err)
	}
}

// TestDialChainTimesOutOnStalledHandshake pins the same deadline on the
// jump-host tunnel chain (tunnel_forward.dialChain).
func TestDialChainTimesOutOnStalledHandshake(t *testing.T) {
	addr := startSilentSSHServer(t)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}

	restore := overrideHandshakeTimeout(t, 300*time.Millisecond)
	defer restore()

	ts := NewTunnelService()
	chain := []ConnectionConfig{{
		User:     "admin",
		Host:     host,
		Port:     port,
		AuthType: "password",
		Password: "pw",
	}}
	done := make(chan error, 1)
	go func() {
		_, _, err := ts.dialChain(chain, nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("dialChain against a silent jump host succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("dialChain never timed out — a stalled jump host blocks the tunnel forever")
	}
}

// TestTunnelServiceStartDoesNotHoldLockDuringDial: a jump host that accepts
// TCP and then stalls must not freeze TunnelStates/Stop or let a second Start
// dial the same session again (the slot is reserved before dialing).
func TestTunnelServiceStartDoesNotHoldLockDuringDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	stall := make(chan struct{})
	var stallOnce sync.Once
	// releaseStall closes the accepted connection; idempotent because both
	// the test body and the cleanup can call it.
	releaseStall := func() { stallOnce.Do(func() { close(stall) }) }
	accepted := make(chan struct{})
	t.Cleanup(func() {
		ln.Close()
		releaseStall()
	})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		close(accepted)
		<-stall
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	cfg := ConnectionConfig{
		User:     "admin",
		Host:     host,
		Port:     port,
		AuthType: "password",
		Password: "pw",
	}

	ts := NewTunnelService()
	started := make(chan error, 1)
	go func() {
		_, err := ts.Start("sess-stalled", cfg, "target.example.com", 22, nil)
		started <- err
	}()

	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatalf("silent jump host never accepted the tunnel dial")
	}

	// Bookkeeping must return promptly while the dial is in flight.
	assertPrompt := func(name string, fn func()) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			defer close(done)
			fn()
		}()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("%s blocked while a tunnel dial is in flight — ts.mu is held across network I/O", name)
		}
	}
	assertPrompt("TunnelStates", func() { _ = ts.TunnelStates() })

	// The in-flight dial must reserve the session slot: a second Start is
	// refused instead of dialing a second SSH connection for the same session.
	secondDone := make(chan error, 1)
	go func() {
		_, err := ts.Start("sess-stalled", cfg, "target.example.com", 22, nil)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if err == nil {
			t.Fatalf("second Start succeeded although the first dial is still in flight — the session slot is not reserved before dialing")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("second Start blocked — the session slot is not reserved before dialing")
	}

	assertPrompt("Stop", func() { ts.Stop("sess-stalled") })

	// Drop the stalled connection: the in-flight Start must fail cleanly.
	releaseStall()
	select {
	case err := <-started:
		if err == nil {
			t.Fatalf("Start succeeded after the tunnel was stopped mid-dial")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Start never returned after the stalled connection was closed")
	}
}

// overrideHandshakeTimeout swaps the package-level handshake deadline for the
// duration of a test.
func overrideHandshakeTimeout(t *testing.T, d time.Duration) func() {
	t.Helper()
	old := sshHandshakeTimeout
	sshHandshakeTimeout = d
	return func() { sshHandshakeTimeout = old }
}
