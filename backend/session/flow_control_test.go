package session

import (
	"sync"
	"testing"
	"time"
)

// P-02: end-to-end flow control. The frontend pauses a session's output
// while its xterm write backlog is above the high-water mark; the readLoop
// must then block in queueEmit (backpressure reaches the SSH/PTY channel),
// resume must release it in order, zmodem must ignore/override the pause,
// and a watchdog must bound every pause so a lost frontend cannot wedge a
// session.

type pauseHarness struct {
	s   *baseSession
	mu  sync.Mutex
	got []byte
}

func newPauseHarness() *pauseHarness {
	h := &pauseHarness{s: &baseSession{}}
	h.s.SetOnDataCallback(func(data []byte) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.got = append(h.got, data...)
	})
	h.s.startEmitLoop()
	return h
}

func (h *pauseHarness) stop() { h.s.stopEmitLoop() }

func (h *pauseHarness) delivered() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return string(h.got)
}

// emitAsync runs emitData on its own goroutine, like a readLoop would, and
// returns a channel closed when the call returned (i.e. the chunk is past
// queueEmit — queued or emitted synchronously).
func (h *pauseHarness) emitAsync(data []byte) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		h.s.emitData(data)
		close(done)
	}()
	return done
}

func TestOutputPauseBlocksEmitAndResumeDeliversInOrder(t *testing.T) {
	h := newPauseHarness()
	t.Cleanup(h.stop)

	h.s.emitData([]byte("before"))
	h.s.SetOutputPaused(true)

	blocked := h.emitAsync([]byte("during"))
	select {
	case <-blocked:
		t.Fatal("emitData returned while output was paused; the readLoop must block")
	case <-time.After(50 * time.Millisecond):
	}
	if got := h.delivered(); got != "before" {
		t.Fatalf("delivered %q while paused, want only the pre-pause chunk", got)
	}

	h.s.SetOutputPaused(false)
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("resume did not unblock the parked emitData")
	}

	// The emit loop coalesces on its first flush; give it a beat and check
	// both chunks arrived, in order.
	deadline := time.Now().Add(2 * time.Second)
	for h.delivered() != "beforeduring" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := h.delivered(); got != "beforeduring" {
		t.Fatalf("delivered %q, want %q (order must survive the pause)", got, "beforeduring")
	}
}

func TestOutputPauseWatchdogAutoResumes(t *testing.T) {
	old := outputPauseTimeout
	outputPauseTimeout = 60 * time.Millisecond
	t.Cleanup(func() { outputPauseTimeout = old })

	h := newPauseHarness()
	t.Cleanup(h.stop)

	h.s.SetOutputPaused(true)
	done := h.emitAsync([]byte("orphaned"))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pause watchdog did not auto-resume; a lost frontend would wedge the session")
	}
}

func TestOutputPauseIdempotent(t *testing.T) {
	old := outputPauseTimeout
	outputPauseTimeout = 120 * time.Millisecond
	t.Cleanup(func() { outputPauseTimeout = old })

	h := newPauseHarness()
	t.Cleanup(h.stop)

	// A second pause request while already paused must not shorten (or
	// extend) the watchdog: the first pause's timeout still bounds it.
	h.s.SetOutputPaused(true)
	time.Sleep(80 * time.Millisecond)
	start := time.Now()
	h.s.SetOutputPaused(true)
	done := h.emitAsync([]byte("x"))
	select {
	case <-done:
		// Unblocked by the FIRST pause's watchdog (120ms from t=0), i.e.
		// ~40ms after the duplicate request. A re-armed watchdog would
		// push this to ~120ms.
		if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
			t.Fatalf("duplicate pause re-armed the watchdog: unblocked after %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not bound the pause; a duplicate pause re-armed it")
	}
}

func TestOutputPauseIgnoredDuringZmodem(t *testing.T) {
	h := newPauseHarness()
	t.Cleanup(h.stop)

	h.s.SetZmodemMode(true)
	t.Cleanup(func() { h.s.SetZmodemMode(false) })

	// Pause is refused while a transfer owns the stream…
	h.s.SetOutputPaused(true)
	// …and binary-mode data never touches the pause path anyway.
	h.s.emitData([]byte("binary-ish"))
	if got := h.delivered(); got != "binary-ish" {
		t.Fatalf("delivered %q, want synchronous delivery during zmodem", got)
	}
}

func TestEnteringZmodemReleasesOutputPause(t *testing.T) {
	h := newPauseHarness()
	t.Cleanup(h.stop)

	h.s.SetOutputPaused(true)
	blocked := h.emitAsync([]byte("queued"))
	select {
	case <-blocked:
		t.Fatal("emitData returned while paused")
	case <-time.After(50 * time.Millisecond):
	}

	// ZMODEM takes over the stream: an active hold must be dropped so the
	// transfer's synchronous path can never stall behind it.
	h.s.SetZmodemMode(true)
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("entering zmodem did not release the output pause")
	}
}

func TestOutputPauseWithoutEmitLoopIsNoop(t *testing.T) {
	// Zero-value sessions (no startEmitLoop) deliver synchronously and
	// have no cond to park on — pause must neither block nor panic.
	s := &baseSession{}
	var got string
	s.SetOnDataCallback(func(data []byte) { got += string(data) })
	s.SetOutputPaused(true)
	s.emitData([]byte("sync"))
	if got != "sync" {
		t.Fatalf("delivered %q, want %q", got, "sync")
	}
}
