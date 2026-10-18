package session

import (
	"net"
	"testing"
	"time"
)

func TestEffectiveTermType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty falls back to default", "", DefaultTermType},
		{"valid passthrough xterm", "xterm", "xterm"},
		{"valid passthrough vt100", "vt100", "vt100"},
		{"valid passthrough vt320", "vt320", "vt320"},
		{"invalid value falls back to default", "foo-bar", DefaultTermType},
		{"injection attempt falls back to default", "xterm; rm -rf /", DefaultTermType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveTermType(tc.in); got != tc.want {
				t.Fatalf("EffectiveTermType(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExplicitTermType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty means no explicit choice", "", ""},
		{"valid explicit value", "vt100", "vt100"},
		{"invalid value is ignored", "foo-bar", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExplicitTermType(tc.in); got != tc.want {
				t.Fatalf("ExplicitTermType(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ttypeReply runs handleSubNegotiation against a TTYPE SEND subnegotiation and
// returns the IS reply the session writes back.
func ttypeReply(t *testing.T, termType string) []byte {
	t.Helper()

	s := NewTelnetSession("test")
	s.termType = termType

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	s.conn = client

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := []byte{telnetIAC, telnetSB, telnetOptTerminalType, telnetTTYPESend, telnetIAC, telnetSE}
		s.handleSubNegotiation(buf, 0)
	}()

	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	reply := make([]byte, 64)
	n, err := server.Read(reply)
	if err != nil {
		t.Fatalf("read TTYPE reply: %v", err)
	}
	<-done
	return reply[:n]
}

func TestTelnetTTYPEUsesConfiguredTermType(t *testing.T) {
	reply := ttypeReply(t, "vt100")
	want := append([]byte{telnetIAC, telnetSB, telnetOptTerminalType, telnetTTYPEIs}, []byte("vt100")...)
	want = append(want, telnetIAC, telnetSE)
	if string(reply) != string(want) {
		t.Fatalf("TTYPE reply = % x, want % x", reply, want)
	}
}

func TestTelnetTTYPEDefaultTermType(t *testing.T) {
	reply := ttypeReply(t, DefaultTermType)
	want := append(append([]byte{telnetIAC, telnetSB, telnetOptTerminalType, telnetTTYPEIs}, DefaultTermType...), telnetIAC, telnetSE)
	if string(reply) != string(want) {
		t.Fatalf("TTYPE reply = % x, want default term %s", reply, DefaultTermType)
	}
}
