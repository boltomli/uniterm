package main

import (
	"strings"
	"testing"
)

// Regression (security.md medium): FrontendLog handed the renderer's message
// straight to the app log with no length bound, so the webview could append
// megabytes per call (the app log buffers and rotates by size, and one huge
// line stalls every logger). Messages are capped; the truncation is marked.
func TestFrontendLogMessageIsCapped(t *testing.T) {
	huge := strings.Repeat("A", maxFrontendLogMessage*3)
	got := truncateFrontendLogMessage(huge)

	if len(got) > maxFrontendLogMessage+len(frontendLogTruncationMarker) {
		t.Fatalf("truncated message is %d bytes, want at most %d", len(got), maxFrontendLogMessage+len(frontendLogTruncationMarker))
	}
	if !strings.HasSuffix(got, frontendLogTruncationMarker) {
		t.Fatalf("truncated message must end with %q, tail is %q", frontendLogTruncationMarker, got[len(got)-len(frontendLogTruncationMarker):])
	}

	if got := truncateFrontendLogMessage("short line"); got != "short line" {
		t.Fatalf("short message rewritten to %q", got)
	}
}

// F-08: renderer-derived text (terminal content) reaches FrontendLog verbatim
// apart from this escaping — a raw CR/LF would let it impersonate extra log
// lines, and a raw ESC could inject ANSI sequences into a log viewer.
func TestEscapeFrontendLogText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain preserved", "ls -la", "ls -la"},
		{"newline escaped", "a\nb", `a\nb`},
		{"crlf escaped", "a\r\nb", `a\r\nb`},
		{"esc escaped", "\x1b[31mred", `\x1b[31mred`},
		{"tab kept", "col1\tcol2", "col1\tcol2"},
		{"unicode line separator escaped", "a\u2028b", `a\x{2028}b`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := escapeFrontendLogText(tc.in)
			if got != tc.want {
				t.Fatalf("escapeFrontendLogText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Forged log line must be impossible: no CR/LF survives.
	if out := escapeFrontendLogText("msg\n[FAKE] injected\r\nreal"); strings.ContainsAny(out, "\n\r") {
		t.Fatalf("escaping left line breaks behind: %q", out)
	}
}
