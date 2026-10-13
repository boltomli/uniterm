package session

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// --- fakes for ssh.NewChannel / ssh.Channel ---

type fakeStderrRW struct {
	io.Reader
	io.Writer
}

// fakeX11Channel implements ssh.Channel over an in-memory reader; `written`
// captures anything the forwarder would send back to the remote X client.
type fakeX11Channel struct {
	data    io.Reader
	written bytes.Buffer
	closed  bool
}

func (c *fakeX11Channel) Read(p []byte) (int, error)  { return c.data.Read(p) }
func (c *fakeX11Channel) Write(p []byte) (int, error) { return c.written.Write(p) }
func (c *fakeX11Channel) Close() error                { c.closed = true; return nil }
func (c *fakeX11Channel) CloseWrite() error           { return nil }
func (c *fakeX11Channel) SendRequest(string, bool, []byte) (bool, error) {
	return false, nil
}
func (c *fakeX11Channel) Stderr() io.ReadWriter {
	return &fakeStderrRW{Reader: bytes.NewReader(nil), Writer: io.Discard}
}

// fakeX11NewChannel implements ssh.NewChannel presenting a scripted
// connection-setup byte stream as the channel data.
type fakeX11NewChannel struct {
	ch    *fakeX11Channel
	extra []byte
}

func (n *fakeX11NewChannel) Accept() (ssh.Channel, <-chan *ssh.Request, error) {
	return n.ch, make(chan *ssh.Request), nil
}
func (n *fakeX11NewChannel) Reject(ssh.RejectionReason, string) error { return nil }
func (n *fakeX11NewChannel) ChannelType() string                      { return "x11" }
func (n *fakeX11NewChannel) ExtraData() []byte                        { return n.extra }

// buildX11SetupPacket crafts an X11 connection-request packet with correct
// per-field 4-byte padding.
func buildX11SetupPacket(t *testing.T, littleEndian bool, proto string, cookie []byte) []byte {
	t.Helper()
	var order binary.ByteOrder = binary.BigEndian
	bo := byte('B')
	if littleEndian {
		order = binary.LittleEndian
		bo = 'l'
	}
	pad4 := func(x int) int { return (4 - (x % 4)) % 4 }
	buf := make([]byte, 12)
	buf[0] = bo
	order.PutUint16(buf[2:4], 11) // protocol-major-version
	order.PutUint16(buf[4:6], 0)  // protocol-minor-version
	order.PutUint16(buf[6:8], uint16(len(proto)))
	order.PutUint16(buf[8:10], uint16(len(cookie)))
	buf = append(buf, proto...)
	buf = append(buf, make([]byte, pad4(len(proto)))...)
	buf = append(buf, cookie...)
	buf = append(buf, make([]byte, pad4(len(cookie)))...)
	return buf
}

// listenLocalX starts a fake local X server on the first free port in the
// 6000-6009 range (display numbers 0-9; dialLocal's TCP port scheme only
// works for single-digit displays) and returns the listener plus the
// "127.0.0.1:N" display string pointing at it.
func listenLocalX(t *testing.T) (net.Listener, string) {
	t.Helper()
	for disp := 9; disp >= 0; disp-- {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", 6000+disp))
		if err != nil {
			continue
		}
		t.Cleanup(func() { ln.Close() })
		return ln, fmt.Sprintf("127.0.0.1:%d", disp)
	}
	t.Skip("no free X display port in 6000-6009")
	return nil, ""
}

// deadLocalX returns a display string whose port nobody listens on.
func deadLocalX(t *testing.T) string {
	t.Helper()
	for disp := 9; disp >= 0; disp-- {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", 6000+disp), 200*time.Millisecond)
		if err == nil {
			c.Close()
			continue // something (e.g. a real X server) is listening here
		}
		return fmt.Sprintf("127.0.0.1:%d", disp)
	}
	t.Skip("all display ports 6000-6009 are in use")
	return ""
}

// expectNoLocalConn asserts that nothing dialed the fake local X server
// within a short window.
func expectNoLocalConn(t *testing.T, ln net.Listener) {
	t.Helper()
	ln.(*net.TCPListener).SetDeadline(time.Now().Add(300 * time.Millisecond))
	c, err := ln.Accept()
	if err == nil {
		c.Close()
		t.Fatal("local X server was contacted despite the X11 connection being rejected")
	}
}

// TestHandleX11RejectsWrongCookie: an inbound x11 channel whose connection
// setup presents a cookie other than the fake one sent in x11-req must be
// refused without the local X server ever seeing a connection — in both the
// cookie-replacement mode and the trusted-fallback (no real cookie) mode.
func TestHandleX11RejectsWrongCookie(t *testing.T) {
	realProto := "MIT-MAGIC-COOKIE-1"
	realCookie := bytes.Repeat([]byte{0xAA}, 16)
	wrongCookie := bytes.Repeat([]byte{0xBB}, 16)

	tests := []struct {
		name       string
		realProto  string
		realCookie []byte
	}{
		{"cookie replacement mode", realProto, realCookie},
		{"trusted fallback mode", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ln, display := listenLocalX(t)
			pkt := buildX11SetupPacket(t, true, realProto, wrongCookie)
			f := &x11Forwarder{realProto: tc.realProto, realCookie: tc.realCookie}
			ch := &fakeX11NewChannel{ch: &fakeX11Channel{data: bytes.NewReader(pkt)}}

			f.handleX11(ch, display)

			if !ch.ch.closed {
				t.Error("x11 channel was not closed after auth rejection")
			}
			if ch.ch.written.Len() != 0 {
				t.Errorf("wrote %d bytes to the remote channel after rejection", ch.ch.written.Len())
			}
			expectNoLocalConn(t, ln)
		})
	}
}

// TestReadAndCheckSetup pins the connection-setup parser/verifier: the
// matching fake cookie must be accepted in both byte orders (and with
// padded fields), while wrong cookies, wrong protocol names, empty auth,
// and truncated packets must all be rejected.
func TestReadAndCheckSetup(t *testing.T) {
	fakeProto := "MIT-MAGIC-COOKIE-1"
	fakeCookie := bytes.Repeat([]byte{0x42}, 16)
	f := &x11Forwarder{fakeProto: fakeProto, fakeCookie: fakeCookie}

	tests := []struct {
		name    string
		build   func(t *testing.T) []byte
		wantErr bool
	}{
		{
			name:  "matching cookie LSB",
			build: func(t *testing.T) []byte { return buildX11SetupPacket(t, true, fakeProto, fakeCookie) },
		},
		{
			name:  "matching cookie MSB",
			build: func(t *testing.T) []byte { return buildX11SetupPacket(t, false, fakeProto, fakeCookie) },
		},
		{
			// Empty name/data (no auth at all) must not sneak past the
			// check even though the local server may not enforce auth.
			name:    "empty auth name and data",
			build:   func(t *testing.T) []byte { return buildX11SetupPacket(t, true, "", nil) },
			wantErr: true,
		},
		{
			name: "wrong cookie",
			build: func(t *testing.T) []byte {
				return buildX11SetupPacket(t, true, fakeProto, bytes.Repeat([]byte{0x43}, 16))
			},
			wantErr: true,
		},
		{
			name: "wrong protocol name",
			build: func(t *testing.T) []byte {
				return buildX11SetupPacket(t, true, "XDM-AUTHORIZATION-1", fakeCookie)
			},
			wantErr: true,
		},
		{
			name: "cookie one byte short",
			build: func(t *testing.T) []byte {
				return buildX11SetupPacket(t, true, fakeProto, fakeCookie[:15])
			},
			wantErr: true,
		},
		{
			name: "truncated header",
			build: func(t *testing.T) []byte {
				return buildX11SetupPacket(t, true, fakeProto, fakeCookie)[:8]
			},
			wantErr: true,
		},
		{
			name: "truncated auth data",
			build: func(t *testing.T) []byte {
				return buildX11SetupPacket(t, true, fakeProto, fakeCookie)[:30]
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			header, order, authName, authData, err := f.readAndCheckSetup(bytes.NewReader(tc.build(t)))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("readAndCheckSetup accepted bad setup (authName=%q authData=% x)", authName, authData)
				}
				return
			}
			if err != nil {
				t.Fatalf("readAndCheckSetup: %v", err)
			}
			if len(header) != 12 {
				t.Errorf("header = %d bytes, want 12", len(header))
			}
			var wantOrder binary.ByteOrder = binary.LittleEndian
			if header[0] == 'B' {
				wantOrder = binary.BigEndian
			}
			if order != wantOrder {
				t.Errorf("order mismatch: got %v", order)
			}
			if string(authName) != fakeProto {
				t.Errorf("authName = %q, want %q", authName, fakeProto)
			}
			if !bytes.Equal(authData, fakeCookie) {
				t.Errorf("authData = % x, want % x", authData, fakeCookie)
			}
		})
	}
}

// TestReadAndCheckSetupNoFakeCredentials: a forwarder that somehow carries
// no fake credentials must reject everything rather than trust the remote.
func TestReadAndCheckSetupNoFakeCredentials(t *testing.T) {
	f := &x11Forwarder{}
	pkt := buildX11SetupPacket(t, true, "MIT-MAGIC-COOKIE-1", bytes.Repeat([]byte{0x42}, 16))
	if _, _, _, _, err := f.readAndCheckSetup(bytes.NewReader(pkt)); err == nil {
		t.Fatal("readAndCheckSetup accepted a setup on a forwarder without fake credentials")
	}
}

// TestHandleX11AcceptsMatchingCookie: with the correct fake cookie the
// connection is bridged and the local X server sees the REAL cookie
// substituted into the setup packet.
func TestHandleX11AcceptsMatchingCookie(t *testing.T) {
	realProto := "MIT-MAGIC-COOKIE-1"
	realCookie := bytes.Repeat([]byte{0xAA}, 16)
	fakeCookie := bytes.Repeat([]byte{0xCC}, 16)

	ln, display := listenLocalX(t)
	want := buildX11SetupPacket(t, true, realProto, realCookie)
	gotSetup := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// forwardX11Setup emits header/name/cookie in several Write
		// calls, so read exactly the expected number of bytes.
		buf := make([]byte, len(want))
		if _, err := io.ReadFull(c, buf); err != nil {
			gotSetup <- nil
			return
		}
		gotSetup <- buf
		// Minimal successful X11 setup response so the forwarder can
		// proceed to the bridge.
		c.Write([]byte{1, 0, 0, 11, 0, 0, 0, 0})
	}()

	f := &x11Forwarder{realProto: realProto, realCookie: realCookie, fakeProto: realProto, fakeCookie: fakeCookie}
	pkt := buildX11SetupPacket(t, true, realProto, fakeCookie)
	// The packet is the entire channel stream: once the forwarder has
	// consumed it, the channel side of the bridge sees EOF and exits.
	ch := &fakeX11NewChannel{ch: &fakeX11Channel{data: bytes.NewReader(pkt)}}

	done := make(chan struct{})
	go func() { defer close(done); f.handleX11(ch, display) }()

	var setup []byte
	select {
	case setup = <-gotSetup:
	case <-time.After(3 * time.Second):
		t.Fatal("local X server never received the connection setup")
	}

	if !bytes.Equal(setup, want) {
		t.Fatalf("local setup = % x, want % x", setup, want)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handleX11 did not return after the channel hit EOF")
	}
	if !ch.ch.closed {
		t.Error("x11 channel was not closed")
	}
}
func TestHandleX11NilOnErrorsDoesNotPanic(t *testing.T) {
	realProto := "MIT-MAGIC-COOKIE-1"
	realCookie := bytes.Repeat([]byte{0xAA}, 16)
	fakeCookie := bytes.Repeat([]byte{0xCC}, 16)
	display := deadLocalX(t)

	// Set matching fake credentials so verification passes and the
	// previously-panicking path (local X server dial failure → errorf)
	// is actually exercised.
	f := &x11Forwarder{realProto: realProto, realCookie: realCookie, fakeProto: realProto, fakeCookie: fakeCookie} // onError intentionally nil
	pkt := buildX11SetupPacket(t, true, realProto, fakeCookie)
	ch := &fakeX11NewChannel{ch: &fakeX11Channel{data: bytes.NewReader(pkt)}}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleX11 panicked with nil onError: %v", r)
		}
	}()
	f.handleX11(ch, display)

	// errorf itself must also be a no-op with a nil callback.
	f.errorf("boom %d", 1)
}
