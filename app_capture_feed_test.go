package main

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ys-ll/uniterm/backend/session"
)

// P-09 follow-up: Screen.Feed must run OFF the session readLoop. The capture
// tap used to parse inline, so a multi-megabyte flood delivered itself at
// the emulator's throughput before emitData could queue anything for the
// frontend. These tests pin the three properties of the queued design:
//   - the tap never blocks on capture readers (no inline Feed under mu),
//   - chunks reach the emulator in tap order,
//   - closeFeed applies the queued tail before returning (SessionResize
//     snapshots PromptSeed right after), and taps stop enqueueing past it.

// fakeCaptureSession is the minimal session.Session + tapSource harness: its
// taps fire the way baseSession.emitData walks them on the readLoop.
type fakeCaptureSession struct {
	id         string
	mu         sync.Mutex
	dataTaps   []func([]byte)
	statusTaps []func(session.SessionStatus)
}

func (f *fakeCaptureSession) ID() string                             { return f.id }
func (f *fakeCaptureSession) Type() string                           { return "local" }
func (f *fakeCaptureSession) Title() string                          { return f.id }
func (f *fakeCaptureSession) Status() session.SessionStatus          { return session.StatusConnected }
func (f *fakeCaptureSession) Connect(session.ConnectionConfig) error { return nil }
func (f *fakeCaptureSession) Disconnect() error                      { return nil }
func (f *fakeCaptureSession) IsConnected() bool                      { return true }
func (f *fakeCaptureSession) Resize(cols, rows int) error            { return nil }
func (f *fakeCaptureSession) SetPendingSize(cols, rows int)          {}
func (f *fakeCaptureSession) Write([]byte) error                     { return nil }
func (f *fakeCaptureSession) SetOnDataCallback(func([]byte))         {}
func (f *fakeCaptureSession) SetOnBinaryCallback(func([]byte))       {}
func (f *fakeCaptureSession) SetOnStatusChangeCallback(func(session.SessionStatus)) {
}
func (f *fakeCaptureSession) SetZmodemMode(bool)   {}
func (f *fakeCaptureSession) EndZmodem([]byte)     {}
func (f *fakeCaptureSession) IsZmodemMode() bool   { return false }
func (f *fakeCaptureSession) SetOutputPaused(bool) {}

func (f *fakeCaptureSession) AddDataTap(cb func([]byte)) func() {
	f.mu.Lock()
	f.dataTaps = append(f.dataTaps, cb)
	idx := len(f.dataTaps) - 1
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.dataTaps[idx] = nil
		f.mu.Unlock()
	}
}

func (f *fakeCaptureSession) AddStatusTap(cb func(session.SessionStatus)) func() {
	f.mu.Lock()
	f.statusTaps = append(f.statusTaps, cb)
	idx := len(f.statusTaps) - 1
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.statusTaps[idx] = nil
		f.mu.Unlock()
	}
}

func (f *fakeCaptureSession) fire(b []byte) {
	f.mu.Lock()
	taps := slices.Clone(f.dataTaps)
	f.mu.Unlock()
	for _, t := range taps {
		if t != nil {
			t(b)
		}
	}
}

func TestSessionCaptureFeedsOffReadLoop(t *testing.T) {
	a := &App{}
	s := &fakeCaptureSession{id: "cap-feed"}
	c := a.sessionCaptureFor(s)

	// The tap must not contend with capture readers: hold the lock the way
	// CaptureScreen/PromptSeed do and fire — the old inline Feed would
	// serialize behind it (and stall the readLoop with it).
	c.mu.Lock()
	fired := make(chan struct{})
	go func() { s.fire([]byte("probe\r\n")); close(fired) }()
	select {
	case <-fired:
		c.mu.Unlock()
	case <-time.After(2 * time.Second):
		c.mu.Unlock()
		t.Fatal("tap blocked while a reader held the capture lock; Feed is still on the readLoop path")
	}

	// Queued chunks reach the emulator, in tap order.
	s.fire([]byte("first-line\r\n"))
	s.fire([]byte("second-line\r\n"))
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		tail := c.screen.Tail(50)
		c.mu.Unlock()
		i1, i2 := strings.Index(tail, "first-line"), strings.Index(tail, "second-line")
		if i1 >= 0 && i2 >= 0 {
			if i1 > i2 {
				t.Fatalf("chunks applied out of order: %q", tail)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("feed worker never applied the queued chunks, tail=%q", tail)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSessionCaptureCloseFeedDrainsQueuedTail(t *testing.T) {
	a := &App{}
	s := &fakeCaptureSession{id: "cap-drain"}
	c := a.sessionCaptureFor(s)

	// Queue without waiting for the worker, then close: everything queued
	// before the close must be applied when closeFeed returns — the
	// SessionResize seed snapshot reads the emulator immediately after.
	s.fire([]byte("queued-prompt$ \r\n"))
	s.fire([]byte("after-prompt\r\n"))
	c.closeFeed()
	c.mu.Lock()
	tail := c.screen.Tail(50)
	c.mu.Unlock()
	if !strings.Contains(tail, "queued-prompt") || !strings.Contains(tail, "after-prompt") {
		t.Fatalf("closeFeed returned before the queued tail was applied: %q", tail)
	}

	// Idempotent, and the tap stops enqueueing past quit.
	c.closeFeed()
	s.fire([]byte("dropped-after-close\r\n"))
	if n := len(c.feedCh); n != 0 {
		t.Fatalf("tap enqueued %d chunk(s) after closeFeed", n)
	}
}

func TestRemoveCaptureStopsFeedWorker(t *testing.T) {
	a := &App{}
	s := &fakeCaptureSession{id: "cap-remove"}
	c := a.sessionCaptureFor(s)

	a.removeCapture("cap-remove")
	if _, ok := a.captures["cap-remove"]; ok {
		t.Fatal("capture still registered after removeCapture")
	}
	// Taps are detached and the worker is joined; late fires are dropped
	// without touching the queue (and without panicking).
	s.fire([]byte("after-remove\r\n"))
	if n := len(c.feedCh); n != 0 {
		t.Fatalf("tap enqueued %d chunk(s) after removeCapture", n)
	}
	// Second stop path (status tap racing an explicit remove) stays safe.
	a.removeCapture("cap-remove")
	c.closeFeed()
}
