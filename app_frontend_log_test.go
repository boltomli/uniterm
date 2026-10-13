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
