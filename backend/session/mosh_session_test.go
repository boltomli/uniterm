package session

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

// Mosh dials its bootstrap SSH connection itself, and used to do it with a
// bare net.DialTimeout — the configured proxy was ignored, so mosh sessions
// either failed on proxy-only networks or bypassed the user's proxy policy.
func TestMoshConnectRoutesThroughConfiguredProxy(t *testing.T) {
	proxyAddr, targets := startRecordingSOCKS5(t)
	cfg := ConnectionConfig{
		Host:  "uniterm-no-such-host.example",
		Port:  22,
		User:  "tester",
		Proxy: socksProxyFor(t, proxyAddr),
	}

	s := NewMoshSession("mosh-proxy-test")
	err := s.Connect(cfg)
	if err == nil {
		t.Fatal("expected Connect to fail: the fake proxy has no SSH server behind it")
	}
	// The handshake runs only when the dial went through the proxy; a direct
	// dial dies in DNS resolution instead.
	if !strings.Contains(err.Error(), "handshake") {
		t.Fatalf("error = %v, want a handshake failure (i.e. the proxy was used)", err)
	}
	want := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	if got := targets(); len(got) == 0 || got[0] != want {
		t.Fatalf("proxy saw %v, want CONNECT to %s", got, want)
	}
}
