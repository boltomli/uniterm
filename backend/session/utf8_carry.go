package session

import "unicode/utf8"

// splitIncompleteUTF8 returns data minus any trailing bytes that start a
// multi-byte rune not yet complete, plus that tail (≤ utf8.UTFMax-1 bytes).
// complete and tail both alias data; neither is copied.
//
// A readLoop's read boundary can land in the middle of a multi-byte UTF-8
// rune. Emitting the split halves as-is would let json.Marshal (Wails
// CustomEvent.ToJSON) replace each invalid byte with U+FFFD, which the
// frontend sanitizer then drops — silently losing the character (CJK output
// was visibly affected). Holding the incomplete tail until the next read
// keeps every emitted chunk valid UTF-8 at its end boundary.
func splitIncompleteUTF8(data []byte) (complete, tail []byte) {
	// Look back at most utf8.UTFMax-1 bytes for a rune start.
	for i := len(data) - 1; i >= 0 && i >= len(data)-(utf8.UTFMax-1); i-- {
		if utf8.RuneStart(data[i]) {
			if !utf8.FullRune(data[i:]) {
				return data[:i], data[i:]
			}
			break
		}
	}
	return data, nil
}

// carryUTF8 appends data to any incomplete trailing rune bytes held from the
// previous read and returns the bytes that are safe to emit now, holding back
// a new incomplete tail (≤3 bytes) for the next call. Returns nil when the
// combined buffer contains only an incomplete rune.
//
// This is the single choke point for the text-output path (emitData); binary
// output (emitBinary, zmodem) must never pass through it. utf8Mu serializes
// against EndZmodem, which may run outside the readLoop goroutine.
func (s *baseSession) carryUTF8(data []byte) []byte {
	s.utf8Mu.Lock()
	defer s.utf8Mu.Unlock()
	buf := data
	if len(s.utf8Tail) > 0 {
		buf = make([]byte, 0, len(s.utf8Tail)+len(data))
		buf = append(buf, s.utf8Tail...)
		buf = append(buf, data...)
	}
	complete, tail := splitIncompleteUTF8(buf)
	if len(tail) > 0 {
		// Copy: tail may alias the readLoop's reusable read buffer.
		s.utf8Tail = append(s.utf8Tail[:0], tail...)
	} else {
		s.utf8Tail = s.utf8Tail[:0]
	}
	return complete
}

// resetUTF8Carry drops any held incomplete-rune tail. Called when entering
// zmodem/binary mode (the held bytes belong to the pre-transfer text stream
// and must not leak into the binary path), when a transfer ends, and on
// (re)connect. A discarded tail was already incomplete, so no complete
// character is lost.
func (s *baseSession) resetUTF8Carry() {
	s.utf8Mu.Lock()
	s.utf8Tail = s.utf8Tail[:0]
	s.utf8Mu.Unlock()
}
