package session

import (
	"bytes"
	"testing"
)

// P-09: lastRecv only feeds tailHex(last, 64) in disconnect logs, so the
// per-read copy must be capped — not the whole ≤16KiB read buffer.
func TestKeepTail(t *testing.T) {
	if got := keepTail(nil, 64); len(got) != 0 {
		t.Fatalf("keepTail(nil) = %v, want empty", got)
	}

	in := []byte("abcdef")
	if got := keepTail(in, 3); string(got) != "def" {
		t.Fatalf("keepTail(abcdef, 3) = %q, want %q", got, "def")
	}
	if got := keepTail(in, 64); string(got) != "abcdef" {
		t.Fatalf("keepTail(abcdef, 64) = %q, want %q", got, "abcdef")
	}

	// Independent copy: the read buffer is reused across iterations, so a
	// mutation of the source must never show up in the stored tail.
	got := keepTail(in, 3)
	in[5] = 'X'
	if string(got) != "def" {
		t.Fatalf("keepTail result aliases the input buffer: %q", got)
	}

	long := bytes.Repeat([]byte{9}, 100)
	got = keepTail(long, 64)
	if !bytes.Equal(got, bytes.Repeat([]byte{9}, 64)) {
		t.Fatalf("keepTail(100×0x09, 64) = %v, want the last 64 bytes", got)
	}
}
