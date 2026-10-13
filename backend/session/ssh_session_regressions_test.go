package session

// Regression tests for the small SSH-stack findings in security.md:
//   - non-ASCII passwords typed at the interactive prompt were corrupted
//     (answer += string(b) converts each byte as a rune);
//   - the disconnect log wrote the last user input as hex, so a password
//     pasted just before a drop landed in the log file.

import (
	"strings"
	"testing"
)

// TestKeyboardInteractiveAnswerKeepsNonASCIIBytes: "pässword" typed at a
// keyboard-interactive prompt must reach the server as those exact bytes.
func TestKeyboardInteractiveAnswerKeepsNonASCIIBytes(t *testing.T) {
	s := NewSSHSession("auth-nonascii")
	s.mu.Lock()
	s.authAnswerCh = make(chan []byte, 4)
	s.mu.Unlock()

	var autoAnswered int32
	// Empty saved password → the interactive path collects the answer.
	cb := s.keyboardInteractiveChallenge(ConnectionConfig{AuthType: "password"}, &autoAnswered)

	// The terminal delivers raw UTF-8 bytes, one per keystroke.
	s.authAnswerCh <- []byte("pässword\r\n")
	answers, err := cb("", "", []string{"Password: "}, []bool{false})
	if err != nil {
		t.Fatalf("interactive challenge failed: %v", err)
	}
	if len(answers) != 1 || answers[0] != "pässword" {
		t.Fatalf("answer = %q, want %q — bytes ≥0x80 were re-encoded as runes, corrupting the password", answers[0], "pässword")
	}
}

// TestSentDiagLogsLengthOnly: the last-sent payload can be a just-typed
// password; disconnect diagnostics may report its length, never its bytes.
func TestSentDiagLogsLengthOnly(t *testing.T) {
	payload := []byte("hunter2-sudo")
	got := sentDiag(payload)
	if got != "len=12" {
		t.Fatalf("sentDiag = %q, want \"len=12\"", got)
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, "68 75 6e") {
		t.Fatalf("sentDiag leaks payload bytes: %q", got)
	}
}
