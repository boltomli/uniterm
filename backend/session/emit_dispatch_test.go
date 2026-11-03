package session

import (
	"testing"
	"time"
)

// Dispatch-stall diagnostics: flushEmitChunks timing gates the "frontend
// delivery chain busy" warning — below the threshold never, first breach
// once, again only after the per-session throttle window.
func TestDispatchStallLogGate(t *testing.T) {
	after := emitDispatchWarnAfter
	interval := emitDispatchWarnInterval
	t.Cleanup(func() {
		emitDispatchWarnAfter = after
		emitDispatchWarnInterval = interval
	})

	s := &baseSession{}

	emitDispatchWarnAfter = time.Hour
	if s.shouldLogDispatchStall(10 * time.Millisecond) {
		t.Fatal("stall below the threshold must not log")
	}

	emitDispatchWarnAfter = 0
	emitDispatchWarnInterval = time.Hour
	if !s.shouldLogDispatchStall(time.Millisecond) {
		t.Fatal("first stall past the threshold must log")
	}
	if s.shouldLogDispatchStall(time.Millisecond) {
		t.Fatal("a stall inside the throttle window must be suppressed")
	}

	emitDispatchWarnInterval = 0
	if !s.shouldLogDispatchStall(time.Millisecond) {
		t.Fatal("a stall after the throttle window must log again")
	}
}
