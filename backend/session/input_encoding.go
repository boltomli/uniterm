package session

import (
	"sync"

	"golang.org/x/text/transform"
)

// inputEncoder converts outgoing user input from UTF-8 to the session's
// configured character encoding (GBK, Big5, Shift-JIS, …).
//
// It replaces seven per-session copies of encodeInput (ssh, telnet, tcp,
// serial, local×2, mosh). Those copies fed a zero-length scratch buffer to
// Transform, which always failed with ErrShortDst, so non-ASCII keystrokes
// reached legacy-encoded hosts as raw UTF-8. The mutex serialises set (an
// encoding change from settings) with concurrent writers (keystrokes,
// post-login script, cwd hook), and encode returns a private copy so a later
// call cannot clobber bytes the caller is still writing to the socket.
type inputEncoder struct {
	mu      sync.Mutex
	tr      transform.Transformer // nil = UTF-8 passthrough
	scratch []byte
}

// set installs the transformer; nil selects UTF-8 passthrough.
func (e *inputEncoder) set(tr transform.Transformer) {
	e.mu.Lock()
	e.tr = tr
	e.mu.Unlock()
}

// encode converts data to the configured encoding, or returns it unchanged
// when no encoding is set.
func (e *inputEncoder) encode(data []byte) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tr == nil {
		return data
	}
	e.tr.Reset()
	dst := e.grow(len(data))
	nDst, _, err := e.tr.Transform(dst, data, true)
	if err == transform.ErrShortDst {
		// Output larger than input: grow once and restart (Reset undoes any
		// partial transformer state from the failed attempt).
		e.tr.Reset()
		dst = e.grow(len(data) * 2)
		nDst, _, err = e.tr.Transform(dst, data, true)
	}
	if err != nil && err != transform.ErrShortSrc {
		// Conversion failed mid-stream: fall back to raw UTF-8, matching the
		// previous per-session behaviour.
		return data
	}
	out := make([]byte, nDst)
	copy(out, dst[:nDst])
	return out
}

// grow returns a scratch buffer with length exactly n.
func (e *inputEncoder) grow(n int) []byte {
	if n <= 0 {
		return e.scratch[:0]
	}
	if cap(e.scratch) < n {
		e.scratch = make([]byte, n)
	} else {
		e.scratch = e.scratch[:n]
	}
	return e.scratch
}
