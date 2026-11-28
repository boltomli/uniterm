package session

import (
	"bytes"
	"context"
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

// readNegotiation reads n bytes from the pipe with a deadline, so the
// blocking net.Pipe writes inside sendActiveNegotiation/sendNAWS complete.
func readNegotiation(t *testing.T, server net.Conn, n int) []byte {
	t.Helper()
	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, n)
	total := 0
	for total < n {
		k, err := server.Read(got[total:])
		if err != nil {
			t.Fatalf("read negotiation bytes: %v (got %d/%d)", err, total, n)
		}
		total += k
	}
	return got
}

// expectSilent asserts the session sends nothing on the pipe within a short
// deadline.
func expectSilent(t *testing.T, server net.Conn) {
	t.Helper()
	server.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	reply := make([]byte, 64)
	if n, err := server.Read(reply); err != io.EOF && n > 0 {
		t.Fatalf("unexpected bytes sent: % x", reply[:n])
	}
}

func TestActiveNegotiationDoesNotOfferBinary(t *testing.T) {
	s := NewTelnetSession("test")

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	s.conn = client

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.sendActiveNegotiation()
	}()

	// DO SGA (3) + WILL TTYPE (3) + WILL NAWS (3) = 9 bytes. The window-size
	// sub-negotiation itself must NOT appear here: it is only sent after the
	// server answers DO NAWS, otherwise peers that never negotiated NAWS
	// (IRIS/Cach茅, legacy consoles) read its four payload bytes as keystrokes.
	got := readNegotiation(t, server, 9)
	<-done

	want := append([]byte{},
		telnetIAC, telnetDO, telnetOptSuppressGoAhead,
		telnetIAC, telnetWILL, telnetOptTerminalType,
		telnetIAC, telnetWILL, telnetOptNAWS)
	if !bytes.Equal(got, want) {
		t.Fatalf("active negotiation = % x, want % x", got, want)
	}
	if bytes.Contains(got, []byte{telnetIAC, telnetWILL, telnetOptBinary}) {
		t.Fatal("active negotiation offered WILL BINARY (Cisco echoes passwords in binary mode)")
	}
}

// TestNAWSSentAfterDoNaws: the DO NAWS handshake is what authorizes the
// sub-negotiation; the pending size is delivered immediately on agreement.
func TestNAWSSentAfterDoNaws(t *testing.T) {
	s := NewTelnetSession("test")
	s.SetPendingSize(132, 43)

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	s.conn = client

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleNegotiation(telnetDO, telnetOptNAWS)
	}()

	// WILL NAWS (3) + SB NAWS 132x43 (9) = 12 bytes.
	got := readNegotiation(t, server, 12)
	<-done

	want := append([]byte{},
		telnetIAC, telnetWILL, telnetOptNAWS,
		telnetIAC, telnetSB, telnetOptNAWS,
		0, 132, 0, 43,
		telnetIAC, telnetSE)
	if !bytes.Equal(got, want) {
		t.Fatalf("DO NAWS reply = % x, want % x", got, want)
	}
}

// TestNAWSNotSentWithoutAgreement: Resize before any DO NAWS handshake must
// stay silent on the wire.
func TestNAWSNotSentWithoutAgreement(t *testing.T) {
	s := NewTelnetSession("test")

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	s.conn = client

	if err := s.Resize(115, 41); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	expectSilent(t, server)
}

// TestNAWSStoppedAfterDont: after the server withdraws agreement (WONT/DONT
// NAWS) further resizes must not send anything.
func TestNAWSStoppedAfterDont(t *testing.T) {
	s := NewTelnetSession("test")
	s.SetPendingSize(100, 30)

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	s.conn = client

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleNegotiation(telnetDO, telnetOptNAWS)
	}()
	readNegotiation(t, server, 12) // WILL NAWS + initial sub-negotiation
	<-done

	done = make(chan struct{})
	go func() {
		defer close(done)
		s.handleNegotiation(telnetDONT, telnetOptNAWS)
		s.Resize(120, 40)
	}()
	expectSilent(t, server)
	<-done
}

func TestSendNAWSExactBytes(t *testing.T) {
	cases := []struct {
		name       string
		cols, rows int
		want       []byte
	}{
		{
			"normal size",
			80, 24,
			[]byte{telnetIAC, telnetSB, telnetOptNAWS, 0, 80, 0, 24, telnetIAC, telnetSE},
		},
		{
			"cols low byte 255 must be IAC IAC escaped (RFC 1073)",
			255, 24,
			[]byte{telnetIAC, telnetSB, telnetOptNAWS, 0, telnetIAC, telnetIAC, 0, 24, telnetIAC, telnetSE},
		},
		{
			"rows 255 escaped",
			80, 255,
			[]byte{telnetIAC, telnetSB, telnetOptNAWS, 0, 80, 0, telnetIAC, telnetIAC, telnetIAC, telnetSE},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTelnetSession("test")
			s.nawsAgreed = true

			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			s.conn = client

			done := make(chan struct{})
			go func() {
				defer close(done)
				s.sendNAWS(tc.cols, tc.rows)
			}()

			got := readNegotiation(t, server, len(tc.want))
			<-done
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("NAWS = % x, want % x", got, tc.want)
			}
		})
	}
}

func newFilterTestSession(t *testing.T) *TelnetSession {
	t.Helper()
	s := NewTelnetSession("test")
	s.loginOutput = newPostLoginOutputBuffer()
	return s
}

func TestFilterIACNegotiationAndData(t *testing.T) {
	s := newFilterTestSession(t)
	got := s.filterIAC([]byte{'h', 'i', telnetIAC, telnetWILL, telnetOptEcho, 'x'})
	if want := []byte("hix"); !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFilterIACSequenceSplitAcrossReads(t *testing.T) {
	s := newFilterTestSession(t)
	// "IAC WILL ECHO" split between the WILL and the option byte: the
	// first read must not leak the IAC/WILL bytes and must not spin.
	got1 := s.filterIAC([]byte{'a', telnetIAC, telnetWILL})
	if string(got1) != "a" {
		t.Fatalf("first read leaked %q", got1)
	}
	got2 := s.filterIAC([]byte{telnetOptEcho, 'b'})
	if string(got2) != "b" {
		t.Fatalf("second read leaked %q", got2)
	}
}

func TestFilterIACLoneIACTail(t *testing.T) {
	s := newFilterTestSession(t)
	got1 := s.filterIAC([]byte{'x', telnetIAC})
	if string(got1) != "x" {
		t.Fatalf("lone IAC leaked into data: %q", got1)
	}
	// The held IAC merges with the next read: "IAC WILL ECHO" completes.
	got2 := s.filterIAC([]byte{telnetWILL, telnetOptEcho, 'y'})
	if string(got2) != "y" {
		t.Fatalf("held IAC not merged: %q", got2)
	}
}

func TestFilterIACSubNegotiationSpansReads(t *testing.T) {
	s := newFilterTestSession(t)
	// TTYPE SEND sub-negotiation split after "IAC SB TTYPE".
	got1 := s.filterIAC([]byte{'a', telnetIAC, telnetSB, telnetOptTerminalType})
	if string(got1) != "a" {
		t.Fatalf("split sub-negotiation leaked %q", got1)
	}
	got2 := s.filterIAC([]byte{telnetTTYPESend, telnetIAC, telnetSE, 'b'})
	if string(got2) != "b" {
		t.Fatalf("sub-negotiation tail leaked %q", got2)
	}
}

func TestFilterIACConsumesDataMark(t *testing.T) {
	s := newFilterTestSession(t)
	// SYNCH: 0xFF sent out-of-band, Data Mark 0xF2 in-band. With
	// SO_OOBINLINE both arrive inline and must be swallowed whole.
	got := s.filterIAC([]byte{'a', telnetIAC, telnetDM, 'b'})
	if string(got) != "ab" {
		t.Fatalf("IAC DM leaked into data: %q", got)
	}
}

func TestFilterIACCRNULCollapsed(t *testing.T) {
	s := newFilterTestSession(t)
	got := s.filterIAC([]byte{'o', 'k', '\r', telnetNUL, 'n', 'e', 'x', 't', '\r', telnetNUL})
	if want := []byte("ok\rnext\r"); !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFilterIACCRNULSplitAcrossReads(t *testing.T) {
	s := newFilterTestSession(t)
	got1 := s.filterIAC([]byte{'o', 'k', '\r'})
	if string(got1) != "ok\r" {
		t.Fatalf("CR lost: %q", got1)
	}
	got2 := s.filterIAC([]byte{telnetNUL, 'x'})
	if string(got2) != "x" {
		t.Fatalf("NUL after split CR not collapsed: %q", got2)
	}
}

func TestFilterIACBinaryModeKeepsNUL(t *testing.T) {
	s := newFilterTestSession(t)
	s.serverBinary = true
	got := s.filterIAC([]byte{'o', 'k', '\r', telnetNUL})
	if want := []byte{'o', 'k', '\r', 0}; !bytes.Equal(got, want) {
		t.Fatalf("binary-mode NUL dropped: %q", got)
	}
}

func TestFilterIACServerBinaryTracked(t *testing.T) {
	s := newFilterTestSession(t)
	s.filterIAC([]byte{telnetIAC, telnetWILL, telnetOptBinary})
	if !s.serverBinary {
		t.Fatal("server WILL BINARY not tracked")
	}
	s.filterIAC([]byte{telnetIAC, telnetWONT, telnetOptBinary})
	if s.serverBinary {
		t.Fatal("server WONT BINARY not tracked")
	}
}

func TestFilterIACWONTailDoesNotSpin(t *testing.T) {
	s := newFilterTestSession(t)
	// A read ending exactly in "IAC DONT" (no option byte yet) must not
	// loop forever — regression for the pre-#1415 filterIAC busy loop.
	done := make(chan []byte, 1)
	go func() {
		done <- s.filterIAC([]byte{'z', telnetIAC, telnetDONT})
	}()
	select {
	case got := <-done:
		if string(got) != "z" {
			t.Fatalf("unexpected output %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("filterIAC spins on trailing IAC+cmd")
	}
}

func TestLoginPhaseOver(t *testing.T) {
	s := newFilterTestSession(t)
	if s.loginPhaseOver() {
		t.Fatal("empty output should not count as past login")
	}
	s.loginOutput.Append([]byte("Fedora Linux 44\r\nlocalhost login: "))
	if s.loginPhaseOver() {
		t.Fatal("login prompt seen, phase should still be open")
	}
	s.loginOutput.Append([]byte("zhanglq\r\n\r\n[telnettest@localhost ~]$ "))
	if !s.loginPhaseOver() {
		t.Fatal("shell prompt after login prompt should close the phase")
	}
}

func TestLoginPhaseOverPreAuthenticated(t *testing.T) {
	s := newFilterTestSession(t)
	// Servers that drop straight into a shell: prompt with no login prompt.
	s.loginOutput.Append([]byte("\x1b]0;user@host:~\x07\x1b[?2004h\x1b[32muser@host\x1b[0m:\x1b[32m~\x1b[0m$ "))
	if !s.loginPhaseOver() {
		t.Fatal("pre-authenticated prompt should close the phase")
	}
}

func TestSendAutoLoginPromptDriven(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	s := newFilterTestSession(t)
	s.conn = client

	var mu sync.Mutex
	var received []byte
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := server.Read(buf)
			mu.Lock()
			received = append(received, buf[:n]...)
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.sendAutoLogin(ctx, "telnettest", "secret123")

	// Server asks for the username, then for the password.
	deadline := time.Now().Add(3 * time.Second)
	waitFor := func(substr string) {
		t.Helper()
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := bytes.Contains(received, []byte(substr))
			mu.Unlock()
			if ok {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q in %q", substr, received)
	}

	s.loginOutput.Append([]byte("localhost login: "))
	waitFor("telnettest\r\n")
	s.loginOutput.Append([]byte("Password: "))
	waitFor("secret123\r\n")
}

func TestSendAutoLoginBlindFallback(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	s := newFilterTestSession(t)
	s.conn = client

	var mu sync.Mutex
	var received []byte
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := server.Read(buf)
			mu.Lock()
			received = append(received, buf[:n]...)
			mu.Unlock()
			if err != nil {
				close(done)
				return
			}
		}
	}()

	// Shorten the waits so the test does not take 8+ seconds of grace time.
	savedPromptTimeout, savedGrace := autoLoginPromptTimeout, autoLoginGraceTimeout
	autoLoginPromptTimeout, autoLoginGraceTimeout = 100*time.Millisecond, 100*time.Millisecond
	defer func() { autoLoginPromptTimeout, autoLoginGraceTimeout = savedPromptTimeout, savedGrace }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.sendAutoLogin(ctx, "u", "p")

	deadline := time.Now().Add(3 * time.Second)
	waitFor := func(substr string) {
		t.Helper()
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := bytes.Contains(received, []byte(substr))
			mu.Unlock()
			if ok {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q in %q", substr, received)
	}
	waitFor("u\r\n")
	waitFor("p\r\n")
}
