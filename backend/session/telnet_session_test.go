package session

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// telnetRecConn is an in-memory net.Conn that records everything written to
// it, so telnet protocol replies (negotiation, sub-negotiation) and line-mode
// command flushes can be asserted without a live socket.
type telnetRecConn struct {
	mu     sync.Mutex
	writes [][]byte
}

func (c *telnetRecConn) Read(p []byte) (int, error)         { return 0, io.EOF }
func (c *telnetRecConn) Close() error                       { return nil }
func (c *telnetRecConn) LocalAddr() net.Addr                { return nil }
func (c *telnetRecConn) RemoteAddr() net.Addr               { return nil }
func (c *telnetRecConn) SetDeadline(t time.Time) error      { return nil }
func (c *telnetRecConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *telnetRecConn) SetWriteDeadline(t time.Time) error { return nil }

func (c *telnetRecConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), b...))
	return len(b), nil
}

// snapshot returns a copy of all writes received so far.
func (c *telnetRecConn) snapshot() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.writes))
	for i, w := range c.writes {
		out[i] = append([]byte(nil), w...)
	}
	return out
}

// newTelnetTestSession builds a minimal TelnetSession wired to a recording
// conn — no socket, no read loop.
func newTelnetTestSession(t *testing.T) (*TelnetSession, *telnetRecConn) {
	t.Helper()
	s := NewTelnetSession("telnet-test")
	s.termType = EffectiveTermType("")
	conn := &telnetRecConn{}
	s.conn = conn
	return s, conn
}

// TestTelnetEncodeInputGBK: SetEncoding("gbk") must make encodeInput actually
// convert UTF-8 keystrokes to GBK. The per-session encoder fed a zero-length
// scratch to Transform (always ErrShortDst) and returned the raw UTF-8, so
// GBK input never reached the host.
func TestTelnetEncodeInputGBK(t *testing.T) {
	s, _ := newTelnetTestSession(t)
	s.SetEncoding("gbk")

	got := s.encodeInput([]byte("中文"))
	want := []byte{0xD6, 0xD0, 0xCE, 0xC4} // GBK "中文"
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeInput(中文) with gbk = % x, want % x", got, want)
	}

	// Clearing the encoding must return to UTF-8 passthrough.
	s.SetEncoding("")
	in := []byte("中文")
	if got := s.encodeInput(in); !bytes.Equal(got, in) {
		t.Fatalf("encodeInput with encoding cleared = % x, want raw % x", got, in)
	}
}

// TestTelnetFilterIACSplitWILL: a read ending in IAC WILL (option byte in the
// next read) previously spun forever in filterIAC — the switch case neither
// advanced nor broke out. The parser must return promptly, hold the tail, and
// negotiate once the option byte arrives.
func TestTelnetFilterIACSplitWILL(t *testing.T) {
	s, conn := newTelnetTestSession(t)

	done := make(chan []byte, 1)
	go func() { done <- s.filterIAC([]byte{telnetIAC, telnetWILL}) }()

	select {
	case out := <-done:
		if len(out) != 0 {
			t.Fatalf("incomplete IAC WILL emitted data bytes: % x", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("filterIAC never returned for a read ending in IAC WILL (infinite loop)")
	}

	// The option byte arrives in the next read: the negotiation must fire.
	out := s.filterIAC([]byte{telnetOptBinary})
	if len(out) != 0 {
		t.Fatalf("completed IAC WILL emitted data bytes: % x", out)
	}
	writes := conn.snapshot()
	want := [][]byte{{telnetIAC, telnetDO, telnetOptBinary}}
	if len(writes) != 1 || !bytes.Equal(writes[0], want[0]) {
		t.Fatalf("negotiation replies = % x, want % x", writes, want)
	}
}

// TestTelnetSubNegotiationSplitAcrossReads: IAC SB ... IAC SE split across two
// reads must accumulate (no payload byte leaks into the terminal output) and
// invoke the sub-negotiation handler exactly once with the full payload.
func TestTelnetSubNegotiationSplitAcrossReads(t *testing.T) {
	s, conn := newTelnetTestSession(t)
	var emitted []byte
	s.SetOnDataCallback(func(d []byte) { emitted = append(emitted, d...) })

	// Split right after IAC SB <terminal-type>: the SEND byte and payload
	// arrive with the terminating IAC SE.
	s.handleRead([]byte{telnetIAC, telnetSB, telnetOptTerminalType})
	if len(emitted) != 0 {
		t.Fatalf("partial sub-negotiation emitted data: % x", emitted)
	}
	s.handleRead([]byte{telnetTTYPESend, 0x41, telnetIAC, telnetSE})
	if len(emitted) != 0 {
		t.Fatalf("sub-negotiation payload leaked as data: % x", emitted)
	}

	// The TTYPE handler must fire exactly once, with the full payload,
	// replying IAC SB TTYPE IS "xterm-256color" IAC SE.
	term := []byte("xterm-256color")
	want := make([]byte, 0, 4+len(term)+2)
	want = append(want, telnetIAC, telnetSB, telnetOptTerminalType, telnetTTYPEIs)
	want = append(want, term...)
	want = append(want, telnetIAC, telnetSE)
	writes := conn.snapshot()
	if len(writes) != 1 || !bytes.Equal(writes[0], want) {
		t.Fatalf("sub-negotiation replies = % x, want exactly % x", writes, want)
	}
}

// TestTelnetFilterIACHoldsTrailingIAC: a lone trailing IAC byte is the first
// half of an escaped 0xFF (IAC IAC) or a command; it must be held for the
// next read, never emitted as terminal data.
func TestTelnetFilterIACHoldsTrailingIAC(t *testing.T) {
	s, _ := newTelnetTestSession(t)

	out := s.filterIAC([]byte{'A', telnetIAC})
	if !bytes.Equal(out, []byte{'A'}) {
		t.Fatalf("filterIAC with trailing lone IAC = % x, want % x", out, []byte{'A'})
	}

	// The next read completes the escape: IAC IAC yields one 0xFF byte.
	out = s.filterIAC([]byte{telnetIAC, 'B'})
	if !bytes.Equal(out, []byte{0xFF, 'B'}) {
		t.Fatalf("filterIAC after completed IAC IAC = % x, want % x", out, []byte{0xFF, 'B'})
	}
}

// TestTelnetLineModeFlushesEveryCommand: line mode used to return right after
// flushing the first newline-terminated line, dropping everything after it —
// pasting "a\rb\r" sent only "a".
func TestTelnetLineModeFlushesEveryCommand(t *testing.T) {
	s, conn := newTelnetTestSession(t)
	s.telnetSendMode = "line"

	if err := s.Write([]byte("a\rb\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var got []byte
	for _, w := range conn.snapshot() {
		got = append(got, w...)
	}
	if !bytes.Equal(got, []byte("a\rb\r")) {
		t.Fatalf("line-mode writes = % q, want both commands % q", got, "a\rb\r")
	}
	if len(s.lineBuf) != 0 {
		t.Fatalf("lineBuf after complete lines = % q, want empty", s.lineBuf)
	}
}

// TestTelnetLineModeCRLF: a \r\n pair must flush as a single line — the old
// flush-at-every-newline behaviour would have sent a second, empty "\n" line.
func TestTelnetLineModeCRLF(t *testing.T) {
	s, conn := newTelnetTestSession(t)
	s.telnetSendMode = "line"

	if err := s.Write([]byte("x\r\ny\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	writes := conn.snapshot()
	var got []byte
	for _, w := range writes {
		if len(w) == 0 {
			t.Fatalf("spurious empty line written: % x", writes)
		}
		got = append(got, w...)
	}
	if !bytes.Equal(got, []byte("x\r\ny\r\n")) {
		t.Fatalf("line-mode writes = % x, want % x", got, "x\r\ny\r\n")
	}
	if len(writes) != 2 {
		t.Fatalf("\\r\\n produced %d writes (% x), want one flush per line", len(writes), writes)
	}
}

// TestTelnetLineModeLocalEchoGated: the local echo in line mode used to fire
// unconditionally; it must depend on localEcho and cover only the bytes that
// remain buffered (the trailing partial line).
func TestTelnetLineModeLocalEchoGated(t *testing.T) {
	s, _ := newTelnetTestSession(t)
	s.telnetSendMode = "line"

	var got []byte
	s.SetOnDataCallback(func(d []byte) { got = append(got, d...) })

	if err := s.Write([]byte("abc")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("echoed %q with localEcho disabled, want nothing", got)
	}

	s.localEcho = true
	if err := s.Write([]byte("d")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Equal(got, []byte("d")) {
		t.Fatalf("echo = %q, want only the bytes buffered by this call %q", got, "d")
	}
}
