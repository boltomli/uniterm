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

func TestBuildMoshServerCommand(t *testing.T) {
	base := "mosh-server new -s"

	// No options configured: the default command is unchanged.
	got, err := buildMoshServerCommand(ConnectionConfig{})
	if err != nil || got != base {
		t.Fatalf("default = %q, %v; want %q, nil", got, err, base)
	}

	// Port range and locale are appended as -p / -l LANG=.
	got, err = buildMoshServerCommand(ConnectionConfig{MoshPort: "65530:65535", MoshLang: "en_US.UTF-8"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	want := base + " -p 65530:65535 -l LANG=en_US.UTF-8"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Values are trimmed and empty-after-trim is ignored.
	got, err = buildMoshServerCommand(ConnectionConfig{MoshPort: "  ", MoshLang: " zh_CN.UTF-8 "})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got != base+" -l LANG=zh_CN.UTF-8" {
		t.Fatalf("got %q", got)
	}

	// Shell metacharacters are rejected outright — the command line is
	// executed by the remote login shell.
	for _, bad := range []string{"65530;rm -rf /", "a b", "$(x)", "a|b", "a&b", "x'y"} {
		if _, err := buildMoshServerCommand(ConnectionConfig{MoshPort: bad}); err == nil {
			t.Fatalf("MoshPort %q: want error, got nil", bad)
		}
		if _, err := buildMoshServerCommand(ConnectionConfig{MoshLang: bad}); err == nil {
			t.Fatalf("MoshLang %q: want error, got nil", bad)
		}
	}
}
