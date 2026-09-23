package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

// TestStartPprofIfEnabled_EnvDisabled verifies that startPprofIfEnabled is a
// no-op unless UNITERM_PPROF=1 (the default for every build, dev or
// production). The function must not bind any TCP socket on localhost:6060.
//
// F-201 audit §8.2: production binaries must never expose the debug listener
// to end users — the env gate makes that independent of the ldflags version.
func TestStartPprofIfEnabled_EnvDisabled(t *testing.T) {
	// A dev instance running on this machine may already hold 6060; the test
	// can only prove "no NEW listener" on a free port.
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:6060", 200*time.Millisecond); err == nil {
		conn.Close()
		t.Skip("port 6060 already in use by another process")
	}
	t.Setenv("UNITERM_PPROF", "")

	startPprofIfEnabled()

	// Give any background goroutine a chance to race into ListenAndServe.
	// If startPprofIfEnabled really did spawn one, it would attempt to bind
	// 6060 immediately. Poll for a short window to be sure.
	time.Sleep(100 * time.Millisecond)

	conn, err := net.DialTimeout("tcp", "127.0.0.1:6060", 200*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatalf("pprof listener bound 6060 without UNITERM_PPROF=1; want no listener")
	}
	if !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "timeout") {
		// Some platforms return "connection refused", others "i/o timeout"
		// when the port is closed; both signal no listener. Anything else
		// (e.g. "permission denied" on macOS without loopback allow) would
		// indicate a real bind problem and warrants investigation.
		t.Fatalf("unexpected dial error: %v", err)
	}
}

// TestStartPprofIfEnabled_EnvOpensListener verifies that the explicit
// UNITERM_PPROF=1 opt-in actually serves net/http/pprof on localhost:6060.
// We poll for the TCP listener instead of hitting /debug/pprof/ so the test
// stays independent of the net/http/pprof import side-effect.
//
// F-201: ensures the perf-reproduction endpoint is wired up on demand.
func TestStartPprofIfEnabled_EnvOpensListener(t *testing.T) {
	t.Setenv("UNITERM_PPROF", "1")

	startPprofIfEnabled()

	// Poll for the listener to come up: the goroutine binds concurrently.
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:6060", 200*time.Millisecond)
		if err == nil {
			conn.Close()
			lastErr = nil
			break
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("pprof listener never came up on 6060 (UNITERM_PPROF=1): %v", lastErr)
	}
}