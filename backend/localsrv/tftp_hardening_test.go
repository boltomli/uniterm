package localsrv

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

// startTestTFTP launches a TFTP server on a free UDP port.
func startTestTFTP(t *testing.T, root string, blockSize int) int {
	t.Helper()
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()

	m := NewManager()
	id, err := m.StartTFTPServer(TFTPConfig{RootDir: root, Port: port, BlockSize: blockSize})
	if err != nil {
		t.Fatalf("StartTFTPServer: %v", err)
	}
	t.Cleanup(func() { m.Stop(id) })
	time.Sleep(100 * time.Millisecond)
	return port
}

// tftpNegotiate sends an RRQ with the given blksize option and returns the
// block size announced in the OACK ("" when the server did not answer).
func tftpNegotiate(t *testing.T, port int, blksize string) string {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client socket: %v", err)
	}
	defer c.Close()

	req := []byte{0, 1}
	req = append(req, []byte("a.bin\x00octet\x00blksize\x00"+blksize+"\x00")...)
	if _, err := c.WriteTo(req, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}); err != nil {
		t.Fatalf("write RRQ: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, _, err := c.ReadFrom(buf)
	if err != nil {
		return ""
	}
	if n < 2 || binary.BigEndian.Uint16(buf[:2]) != tftpOpOACK {
		t.Fatalf("expected OACK, got %q", string(buf[:n]))
	}
	payload := string(buf[2:n])
	const marker = "blksize\x00"
	i := strings.Index(payload, marker)
	if i < 0 {
		t.Fatalf("OACK without blksize: %q", payload)
	}
	return strings.TrimRight(payload[i+len(marker):], "\x00")
}

// TestTFTPBlkSizeIsClamped is the regression test for the remote crash: the
// attacker-supplied blksize must never size the transfer buffer directly.
//
// The server is configured at the RFC 2348 ceiling so these assertions pin the
// hostile-value clamp itself; the separate configured-ceiling rule is covered
// by TestTFTPNegotiationStaysWithinConfiguredBlockSize.
//
// The requested file deliberately does not exist: the server answers the RRQ
// with the OACK before it opens the file, so negotiating against a missing
// path exercises the clamp without leaving an open handle behind. A real file
// would keep the handler (and its handle) alive through the DATA ack retries
// this test never answers, which on Windows makes the temp-dir cleanup fail.
func TestTFTPBlkSizeIsClamped(t *testing.T) {
	root := t.TempDir()
	port := startTestTFTP(t, root, tftpMaxBlockSize)

	for _, requested := range []string{"999999999", "9223372036854775807"} {
		got := tftpNegotiate(t, port, requested)
		if got == "" {
			t.Fatalf("no OACK for blksize=%s", requested)
		}
		if got != "65464" {
			t.Fatalf("blksize=%s negotiated %s, want the RFC 2348 maximum 65464", requested, got)
		}
	}

	// A value that overflows int must be rejected outright (default 512)
	// rather than wrapping into a negative or huge allocation size.
	if got := tftpNegotiate(t, port, "18446744073709551616"); got != "512" {
		t.Fatalf("overflowing blksize negotiated %q, want the default 512", got)
	}

	// A legal request is still honoured (not clamped down to the default).
	if got := tftpNegotiate(t, port, "1428"); got != "1428" {
		t.Fatalf("legal blksize negotiated %q, want 1428", got)
	}

	// The server must still be alive after the overflow attempts.
	if got := tftpNegotiate(t, port, "512"); got != "512" {
		t.Fatalf("server did not answer after hostile requests: %q", got)
	}
}

// TestTFTPConfiguredBlockSizeIsClamped covers the config path. Like the test
// above it negotiates against a missing file, so no handle outlives the test.
func TestTFTPConfiguredBlockSizeIsClamped(t *testing.T) {
	root := t.TempDir()
	port := startTestTFTP(t, root, 1<<30)
	if got := tftpNegotiate(t, port, "512"); got != "512" {
		t.Fatalf("negotiated %q", got)
	}
}

// TestTFTPNegotiationStaysWithinConfiguredBlockSize pins main's blksize rule:
// the configured block size is the largest block the server will offer, so a
// client asking for more is answered with the configured value instead of the
// requested one. That keeps the negotiated block (which sizes the transfer
// buffer) bounded by configuration, not by the request.
func TestTFTPNegotiationStaysWithinConfiguredBlockSize(t *testing.T) {
	root := t.TempDir()
	port := startTestTFTP(t, root, 1024)
	if got := tftpNegotiate(t, port, "65464"); got != "1024" {
		t.Fatalf("blksize=65464 negotiated %q, want the configured ceiling 1024", got)
	}
	if got := tftpNegotiate(t, port, "512"); got != "512" {
		t.Fatalf("blksize=512 negotiated %q, want 512", got)
	}
}
