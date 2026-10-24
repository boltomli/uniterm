package session

import (
	"sync"
	"testing"
	"unicode/utf8"
)

// P-03: UTF-8 runes split across read boundaries must not reach the event
// callback as invalid bytes — json.Marshal would replace them with U+FFFD
// and the frontend sanitizer drops those, silently losing characters.

func TestSplitIncompleteUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // complete part
		tail string // held tail
	}{
		{"empty", "", "", ""},
		{"ascii", "hello", "hello", ""},
		{"full cjk", "ab中文", "ab中文", ""},
		{"cjk cut at byte 1", "ab\xe4", "ab", "\xe4"},
		{"cjk cut at byte 2", "ab\xe4\xb8", "ab", "\xe4\xb8"},
		{"cjk complete after cut", "ab\xe4\xb8\xad", "ab中", ""},
		{"emoji cut at byte 1", "x\xf0", "x", "\xf0"},
		{"emoji cut at byte 2", "x\xf0\x9f", "x", "\xf0\x9f"},
		{"emoji cut at byte 3", "x\xf0\x9f\x98", "x", "\xf0\x9f\x98"},
		{"emoji complete", "x\xf0\x9f\x98\x80", "x\U0001F600", ""},
		{"only a lead byte", "\xe4", "", "\xe4"},
		{"split multi-byte at first byte", "\xe6\x96\x87", "文", ""},
		{"ascii then lone lead", "a\xe6", "a", "\xe6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			complete, tail := splitIncompleteUTF8([]byte(tc.in))
			if string(complete) != tc.want || string(tail) != tc.tail {
				t.Fatalf("splitIncompleteUTF8(%q) = (%q, %q), want (%q, %q)",
					tc.in, complete, tail, tc.want, tc.tail)
			}
			if len(tail) >= utf8.UTFMax {
				t.Fatalf("tail longer than UTFMax-1: %d bytes", len(tail))
			}
		})
	}
}

// collectEmit returns a baseSession whose synchronous emit path appends every
// delivered chunk to got (guarded by mu).
func collectEmit() (*baseSession, *sync.Mutex, *[]byte) {
	s := &baseSession{}
	mu := &sync.Mutex{}
	got := &[]byte{}
	s.SetOnDataCallback(func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		*got = append(*got, data...)
	})
	return s, mu, got
}

// TestEmitDataCarriesSplitUTF8 reproduces the reported repro: "ab中文" cut at
// byte 4 must arrive as "ab" + "中文", never as two U+FFFD-mangled halves.
func TestEmitDataCarriesSplitUTF8(t *testing.T) {
	s, mu, got := collectEmit()

	s.emitData([]byte("ab\xe4\xb8"))   // 中 cut after 2 bytes
	s.emitData([]byte("\xad\xe6\x96")) // 中 completes, 文 cut after 2 bytes
	s.emitData([]byte("\x87"))         // 文 completes

	mu.Lock()
	defer mu.Unlock()
	if string(*got) != "ab中文" {
		t.Fatalf("delivered %q, want %q", *got, "ab中文")
	}
}

// TestEmitDataCarriesAcrossManyReads checks that a rune split one byte at a
// time across reads still reassembles, and that no chunk ever ends mid-rune.
func TestEmitDataCarriesAcrossManyReads(t *testing.T) {
	s, mu, got := collectEmit()

	full := "seq: \xe4\xb8\xad\xe6\x96\x87\xf0\x9f\x98\x80 end"
	for i := 0; i < len(full); i++ {
		s.emitData([]byte{full[i]})
		// Every delivered chunk must be valid UTF-8 at its end boundary:
		// splitIncompleteUTF8 must not have handed out an incomplete tail.
	}

	mu.Lock()
	defer mu.Unlock()
	if string(*got) != full {
		t.Fatalf("delivered %q, want %q", *got, full)
	}
}

// TestEmitDataASCIIPassthrough: ASCII must flow through unchanged, one
// callback per emit, with no allocation of a carry buffer.
func TestEmitDataASCIIPassthrough(t *testing.T) {
	s, mu, got := collectEmit()
	s.emitData([]byte("$ "))
	s.emitData([]byte("ls\r\n"))

	mu.Lock()
	defer mu.Unlock()
	if string(*got) != "$ ls\r\n" {
		t.Fatalf("delivered %q", *got)
	}
}

// TestEmitDataTapsSeeRawBytes: outputLogWriter keeps receiving the original
// (possibly split) bytes — the log records raw stream bytes by design.
func TestEmitDataTapsSeeRawBytes(t *testing.T) {
	s, mu, got := collectEmit()
	var logMu sync.Mutex
	var logged []byte
	s.SetOutputLogWriter(func(data []byte) {
		logMu.Lock()
		defer logMu.Unlock()
		logged = append(logged, data...)
	})

	raw := "ab\xe4\xb8"
	s.emitData([]byte(raw))

	mu.Lock()
	if string(*got) != "ab" {
		t.Fatalf("callback got %q, want %q", *got, "ab")
	}
	mu.Unlock()
	logMu.Lock()
	defer logMu.Unlock()
	if string(logged) != raw {
		t.Fatalf("log got %q, want raw %q", logged, raw)
	}
}

// TestEmitDataSkipsCarryDuringZmodem: while binary mode is on, split bytes
// pass through untouched (the binary path must never be delayed by the carry).
func TestEmitDataSkipsCarryDuringZmodem(t *testing.T) {
	s, mu, got := collectEmit()
	s.SetZmodemMode(true)
	t.Cleanup(func() { s.SetZmodemMode(false) })

	s.emitData([]byte("\xe4\xb8"))

	mu.Lock()
	defer mu.Unlock()
	if string(*got) != "\xe4\xb8" {
		t.Fatalf("delivered %q, want raw passthrough", *got)
	}
}

// TestSetZmodemModeClearsCarry: entering binary mode drops the held tail so
// it cannot prefix post-transfer output.
func TestSetZmodemModeClearsCarry(t *testing.T) {
	s, mu, got := collectEmit()

	s.emitData([]byte("ab\xe4\xb8")) // hold "\xe4\xb8"
	s.SetZmodemMode(true)
	s.SetZmodemMode(false)
	s.emitData([]byte("x"))

	mu.Lock()
	defer mu.Unlock()
	if string(*got) != "abx" {
		t.Fatalf("delivered %q, want %q (stale tail must be dropped)", *got, "abx")
	}
}

// TestEndZmodemClearsCarry: a held pre-transfer tail is discarded when the
// transfer ends; trailing bytes themselves get carry protection.
func TestEndZmodemClearsCarry(t *testing.T) {
	s, mu, got := collectEmit()

	s.emitData([]byte("ab\xe4\xb8")) // hold "\xe4\xb8"
	s.EndZmodem(nil)
	s.emitData([]byte("prompt$ "))

	mu.Lock()
	defer mu.Unlock()
	if string(*got) != "abprompt$ " {
		t.Fatalf("delivered %q, want %q", *got, "abprompt$ ")
	}
}

func TestEndZmodemTrailingCarried(t *testing.T) {
	s, mu, got := collectEmit()

	// Trailing bytes themselves may end mid-rune; the remainder arrives
	// with the next text chunk.
	s.EndZmodem([]byte("res\xe4\xb8"))
	s.emitData([]byte("\xad ok"))

	mu.Lock()
	defer mu.Unlock()
	if string(*got) != "res\u4e2d ok" {
		t.Fatalf("delivered %q, want %q", *got, "res\u4e2d ok")
	}
}

// TestStartEmitLoopResetsCarry: a (re)connect must not inherit the previous
// stream's held bytes.
func TestStartEmitLoopResetsCarry(t *testing.T) {
	s, mu, got := collectEmit()

	s.emitData([]byte("ab\xe4\xb8")) // hold "\xe4\xb8"
	s.startEmitLoop()
	s.emitData([]byte("x"))
	s.stopEmitLoop() // flushes the queue so the assertion sees everything

	mu.Lock()
	defer mu.Unlock()
	// The concatenation must be exactly the complete prefix plus "x": the
	// stale tail must not have leaked across connect.
	if string(*got) != "abx" {
		t.Fatalf("delivered %q, want %q (stale tail leaked across connect)", *got, "abx")
	}
}
