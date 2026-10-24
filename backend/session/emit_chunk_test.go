package session

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// P-08: a merged flush can reach ~1MB; Wails routes events over its inline
// limit through a fetch whose promise chain serializes EVERY later event
// behind it, so key echo waited behind a huge JSON parse. flushEmitChunks
// now caps each callback at emitChunkMaxBytes, cutting only on complete
// UTF-8 rune boundaries (P-03's guarantee must survive the split).

func withEmitChunkCap(t *testing.T, cap int) {
	t.Helper()
	old := emitChunkMaxBytes
	emitChunkMaxBytes = cap
	t.Cleanup(func() { emitChunkMaxBytes = old })
}

func TestEmitChunkedSplitsOnRuneBoundaries(t *testing.T) {
	withEmitChunkCap(t, 64)

	// 63 ASCII bytes then CJK: the first cut lands inside a rune.
	data := []byte(strings.Repeat("a", 63) + strings.Repeat("中文xyz", 40))
	var pieces [][]byte
	emitChunked(func(b []byte) {
		pieces = append(pieces, append([]byte(nil), b...))
	}, data)

	if len(pieces) < 2 {
		t.Fatalf("expected multiple pieces for %d bytes, got %d", len(data), len(pieces))
	}
	if joined := bytes.Join(pieces, nil); !bytes.Equal(joined, data) {
		t.Fatalf("split lost or reordered bytes: %d != %d", len(joined), len(data))
	}
	for i, p := range pieces {
		if len(p) > 64 {
			t.Fatalf("piece %d exceeds the cap: %d bytes", i, len(p))
		}
		if !utf8.Valid(p) {
			t.Fatalf("piece %d is not valid UTF-8 (cut mid-rune): %q", i, p)
		}
	}
	// The straddling rune must land WHOLLY in the second piece.
	if !bytes.HasPrefix(pieces[1], []byte("中文xyz")) {
		t.Fatalf("second piece starts %q, want the intact CJK rune", pieces[1][:min(len(pieces[1]), 9)])
	}
}

func TestEmitChunkedSmallDataIsOneCall(t *testing.T) {
	withEmitChunkCap(t, 64)

	var calls int
	var got []byte
	emitChunked(func(b []byte) {
		calls++
		got = append(got, b...)
	}, []byte("hello"))

	if calls != 1 || string(got) != "hello" {
		t.Fatalf("calls=%d got=%q, want one call with %q", calls, got, "hello")
	}
}

func TestEmitChunkedExactMultipleHasNoEmptyTail(t *testing.T) {
	withEmitChunkCap(t, 64)

	var pieces [][]byte
	emitChunked(func(b []byte) {
		pieces = append(pieces, b)
	}, bytes.Repeat([]byte("z"), 128))

	if len(pieces) != 2 {
		t.Fatalf("got %d pieces, want exactly 2 (no empty trailing call)", len(pieces))
	}
}

func TestFlushEmitChunksCapsEachCallback(t *testing.T) {
	withEmitChunkCap(t, 1024)

	s := &baseSession{}
	var calls int
	var lens []int
	var got []byte
	s.SetOnDataCallback(func(data []byte) {
		calls++
		lens = append(lens, len(data))
		got = append(got, data...)
	})

	chunks := [][]byte{
		[]byte(strings.Repeat("a", 600)), // ends cleanly
		[]byte("中文" + strings.Repeat("b", 900)),
	}
	want := append(append([]byte(nil), chunks[0]...), chunks[1]...)
	s.flushEmitChunks(chunks)

	if !bytes.Equal(got, want) {
		t.Fatalf("concatenated callbacks lost bytes: %d != %d", len(got), len(want))
	}
	if calls < 2 {
		t.Fatalf("expected the coalesced flush to split, got %d call(s)", calls)
	}
	for i, n := range lens {
		if n > 1024 {
			t.Fatalf("callback %d exceeds the cap: %d bytes", i, n)
		}
	}
}
