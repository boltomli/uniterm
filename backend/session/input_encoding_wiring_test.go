package session

// Regression tests for security.md P2 "encodeInput never takes effect": the
// seven per-session copies fed a zero-length scratch buffer to Transform, so
// every keystroke reached legacy-encoded hosts as raw UTF-8. The sessions now
// share inputEncoder (input_encoding.go); these tests pin the WIRING —
// SetEncoding must actually make encodeInput convert.

import (
	"bytes"
	"testing"
)

var (
	utf8Chinese = []byte("中文")
	gbkChinese  = []byte{0xD6, 0xD0, 0xCE, 0xC4} // GBK "中文"
)

// TestSessionInputEncodingWiring drives every session type that still exposes
// its own encodeInput wrapper: after configuring GBK, writing Chinese
// keystrokes must produce GBK bytes, not the raw UTF-8 input.
func TestSessionInputEncodingWiring(t *testing.T) {
	ssh := NewSSHSession("enc-ssh")
	ssh.SetEncoding("gbk")

	tcp := NewTCPSession("enc-tcp")
	tcp.setupEncoding("gbk")

	serial := NewSerialSession("enc-serial")
	serial.SetEncoding("gbk")

	mosh := NewMoshSession("enc-mosh")
	mosh.SetEncoding("gbk")

	local := NewLocalSession("enc-local")
	local.SetEncoding("gbk")

	cases := []struct {
		name   string
		encode func([]byte) []byte
	}{
		{"ssh", ssh.encodeInput},
		{"tcp", tcp.encodeInput},
		{"serial", serial.encodeInput},
		{"mosh", mosh.encodeInput},
		{"local", local.encodeInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.encode(utf8Chinese)
			if !bytes.Equal(got, gbkChinese) {
				t.Errorf("encodeInput(中文) = % x (%q), want GBK % x — input encoding is not wired", got, got, gbkChinese)
			}
		})
	}

	// Switching back to UTF-8 must return to passthrough.
	ssh.SetEncoding("")
	if got := ssh.encodeInput(utf8Chinese); !bytes.Equal(got, utf8Chinese) {
		t.Errorf("encodeInput after SetEncoding(\"\") = % x, want the raw UTF-8 input", got)
	}
}

// TestSessionInputEncodingConcurrent: encodeInput races with SetEncoding (an
// encoding change from settings while keystrokes are in flight). Run with
// -race; the shared inputEncoder owns the lock.
func TestSessionInputEncodingConcurrent(t *testing.T) {
	s := NewSSHSession("enc-race")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			s.SetEncoding("gbk")
			s.SetEncoding("")
		}
	}()
	for range 200 {
		_ = s.encodeInput(utf8Chinese)
	}
	<-done
}
