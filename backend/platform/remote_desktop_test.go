package platform

import (
	"sync"
	"testing"
	"time"
)

func TestVNCKeyboardShortcutCapture(t *testing.T) {
	var state rdpKeyboardState
	state.activate("vnc")
	for _, scan := range []uint16{0x1e, 0x1d, 0x2a, 0xe038, 0x0f} {
		if state.shortcutKey(scan, false) {
			t.Fatalf("ordinary typing/AltGr must stay with noVNC: %x", scan)
		}
	}
	for _, chord := range [][]uint16{{0x38, 0x2a, 0x0f}, {0xe05b, 0x12}, {0xe05c}, {0x38, 0x3e}} {
		for _, scan := range chord {
			if !state.shortcutKey(scan, false) {
				t.Fatalf("shortcut down was not captured: %x", scan)
			}
			if _, swallow := state.key(scan, true, true, false); !swallow {
				t.Fatal("shortcut was allowed to reach Windows")
			}
		}
		for _, scan := range chord {
			if !state.shortcutKey(scan, false) {
				t.Fatalf("held shortcut release was not captured: %x", scan)
			}
			state.key(scan, false, true, false)
		}
	}
	if !state.shortcutKey(0x0f, true) {
		t.Fatal("right-Alt+Tab must also be captured")
	}
}

func TestRDPKeyboardRemoteChords(t *testing.T) {
	for _, chord := range []struct {
		name  string
		scans []uint16
	}{
		{"AltTab", []uint16{0x38, 0x0f}},
		{"AltShiftTab", []uint16{0x38, 0x2a, 0x0f}},
		{"LeftWin", []uint16{0xe05b}},
		{"RightWin", []uint16{0xe05c}},
		{"WinE", []uint16{0xe05b, 0x12}},
		{"AltF4", []uint16{0x38, 0x3e}},
		{"CtrlShiftEscape", []uint16{0x1d, 0x2a, 0x01}},
	} {
		t.Run(chord.name, func(t *testing.T) {
			var state rdpKeyboardState
			state.activate("remote")
			for _, scan := range chord.scans {
				event, swallow := state.key(scan, true, true, false)
				if !swallow || event.CaptureID != "remote" || event.Scancode != scan || !event.Down || event.Repeat {
					t.Fatalf("key down must reach only remote: %+v, swallow=%v", event, swallow)
				}
			}
			for i := len(chord.scans) - 1; i >= 0; i-- {
				event, swallow := state.key(chord.scans[i], false, true, false)
				if !swallow || event.CaptureID != "remote" || event.Down {
					t.Fatalf("key up: %+v, %v", event, swallow)
				}
			}
			if len(state.pressed) != 0 {
				t.Fatal("stuck remote keys")
			}
		})
	}
}

func TestRDPKeyboardWinReleaseAfterBlur(t *testing.T) {
	var state rdpKeyboardState
	state.activate("remote")
	state.key(0xe05b, true, true, false)
	state.activate("")
	if event, swallow := state.key(0xe05b, false, false, false); !swallow || event.CaptureID != "" {
		t.Fatalf("Win release must not open local Start menu: %+v, %v", event, swallow)
	}
	if _, swallow := state.key(0xe05b, true, false, false); swallow {
		t.Fatal("new local Win press swallowed after blur")
	}
}

func TestRDPKeyboardPanelChangeDoesNotInheritHeldKeys(t *testing.T) {
	var state rdpKeyboardState
	state.activate("first")
	state.key(0x38, true, true, false)
	state.activate("second")
	for _, down := range []bool{true, false} {
		if event, swallow := state.key(0x38, down, true, false); !swallow || event.CaptureID != "" {
			t.Fatalf("old held key leaked into new panel: %+v, %v", event, swallow)
		}
	}
	if event, _ := state.key(0x38, true, true, false); event.CaptureID != "second" {
		t.Fatal("new key must reach new panel")
	}
}

func TestRDPKeyboardPasteModifiersAndRepeat(t *testing.T) {
	var state rdpKeyboardState
	state.activate("remote")
	state.key(0xe01d, true, true, false)
	for i := 0; i < 2; i++ {
		event, _ := state.key(0x2f, true, true, false)
		if !event.Ctrl || event.Repeat != (i == 1) {
			t.Fatalf("Ctrl+V state: %+v", event)
		}
	}
	state.key(0x2f, false, true, false)
	state.key(0xe01d, false, true, false)
	state.key(0x36, true, true, false)
	if event, _ := state.key(0xe052, true, true, false); !event.Shift || event.Ctrl {
		t.Fatalf("Shift+Insert state: %+v", event)
	}
}

func TestRDPKeyboardLocalKeysAndUnmatchedRelease(t *testing.T) {
	var state rdpKeyboardState
	state.activate("remote")
	for _, down := range []bool{true, false} {
		if _, swallow := state.key(0x58, down, true, true); swallow {
			t.Fatal("F12 must stay local")
		}
		if _, swallow := state.key(0x1e, down, false, false); swallow {
			t.Fatal("background typing must stay local")
		}
		if _, swallow := state.key(0, down, true, false); swallow {
			t.Fatal("unsupported media key swallowed")
		}
	}
	if _, swallow := state.key(0x1d, false, true, false); swallow {
		t.Fatal("modifier held before capture must release locally")
	}
}

type desktopKeyboardTestBackend struct {
	mu         sync.Mutex
	foreground bool
	accept     bool
	captures   int
	releases   int
	closed     int
	onCapture  func()
}

func (b *desktopKeyboardTestBackend) capture(uintptr) bool {
	b.mu.Lock()
	b.captures++
	accept, callback := b.accept, b.onCapture
	b.mu.Unlock()
	if callback != nil {
		callback()
	}
	return accept
}
func (b *desktopKeyboardTestBackend) focused() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.foreground
}
func (b *desktopKeyboardTestBackend) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.releases++
}
func (b *desktopKeyboardTestBackend) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed++
}

func desktopKeyboardFixture(t *testing.T) (*desktopKeyboardCapture, *desktopKeyboardTestBackend, chan RDPKeyEvent) {
	t.Helper()
	events := make(chan RDPKeyEvent, 128)
	capture := newDesktopKeyboardCapture(func(event RDPKeyEvent) { events <- event })
	backend := &desktopKeyboardTestBackend{foreground: true, accept: true}
	capture.backend = backend
	go capture.run()
	t.Cleanup(func() { capture.Close(); <-capture.done })
	return capture, backend, events
}

func desktopKeyboardNextEvent(t *testing.T, events chan RDPKeyEvent) RDPKeyEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("native keyboard event was not dispatched")
		return RDPKeyEvent{}
	}
}

func TestDesktopKeyboardNativeEventsDuringCapture(t *testing.T) {
	c, backend, events := desktopKeyboardFixture(t)
	backend.onCapture = func() {
		if !c.key(0x1d, true, true, true, false, false, false) {
			t.Error("native callback could not capture while the UI operation was in progress")
		}
	}
	if !c.SetCapture("rdp", 1) {
		t.Fatal("capture not enabled")
	}
	event := desktopKeyboardNextEvent(t, events)
	if event.CaptureID != "rdp" || !event.Ctrl || !event.Down || event.Scancode != 0x1d {
		t.Fatalf("incorrect native event: %+v", event)
	}
}

func TestDesktopKeyboardLeaseAndStaleRelease(t *testing.T) {
	c, backend, events := desktopKeyboardFixture(t)
	if !c.SetCapture("first", 1) || !c.key(0xe05b, true, true, false, false, true, false) {
		t.Fatal("initial capture failed")
	}
	desktopKeyboardNextEvent(t, events)
	if !c.SetCapture("first", 1) {
		t.Fatal("renewal failed")
	}
	backend.mu.Lock()
	calls := backend.captures
	backend.mu.Unlock()
	if calls != 1 {
		t.Fatal("lease renewal unnecessarily replaced the native grab")
	}
	c.SetCapture("second", 1)
	c.Release("first")
	if event := desktopKeyboardNextEvent(t, events); !event.Reset || event.CaptureID != "first" {
		t.Fatalf("old desktop was not reset: %+v", event)
	}
	if !c.key(0xe05b, false, true, false, false, false, false) {
		t.Fatal("old Windows-key release must be swallowed")
	}
	if !c.key(0x1e, true, true, false, false, false, false) {
		t.Fatal("stale release disabled the newly focused desktop")
	}
	if event := desktopKeyboardNextEvent(t, events); event.CaptureID != "second" || event.Scancode != 0x1e {
		t.Fatalf("input crossed capture owners: %+v", event)
	}
}

func TestDesktopKeyboardReleasesOnExpiryAndFocusLoss(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "focus", true: "expiry"}[expired], func(t *testing.T) {
			c, backend, events := desktopKeyboardFixture(t)
			c.SetCapture("rdp", 1)
			if expired {
				c.mu.Lock()
				c.expires = time.Now().Add(-time.Second)
				c.mu.Unlock()
			} else {
				backend.mu.Lock()
				backend.foreground = false
				backend.mu.Unlock()
			}
			c.checkLease()
			if event := desktopKeyboardNextEvent(t, events); !event.Reset || event.CaptureID != "rdp" {
				t.Fatalf("remote held keys were not reset: %+v", event)
			}
			if c.key(0x1e, true, true, false, false, false, false) {
				t.Fatal("expired capture swallowed local typing")
			}
			backend.mu.Lock()
			defer backend.mu.Unlock()
			if backend.releases != 1 {
				t.Fatal("native shortcuts were not restored")
			}
		})
	}
}

func TestDesktopKeyboardShortcutMode(t *testing.T) {
	c, _, events := desktopKeyboardFixture(t)
	c.SetShortcutCapture("vnc", 1)
	if c.key(0x1e, true, true, false, false, false, false) {
		t.Fatal("ordinary VNC typing must remain with the webview")
	}
	if !c.key(0x38, true, true, false, false, false, true) ||
		!c.key(0x0f, true, true, false, true, false, true) {
		t.Fatal("Alt+Shift+Tab was not captured")
	}
	desktopKeyboardNextEvent(t, events)
	event := desktopKeyboardNextEvent(t, events)
	if !event.Shift || event.Scancode != 0x0f {
		t.Fatalf("modifier flags lost: %+v", event)
	}
	if c.key(0x58, true, true, false, false, false, true) {
		t.Fatal("F12 must remain local")
	}
}

func TestDesktopKeyboardFailedCaptureAndClose(t *testing.T) {
	c, backend, _ := desktopKeyboardFixture(t)
	backend.accept = false
	if c.SetCapture("invalid", 0) || c.SetCapture("", 1) || c.SetCapture("rdp", 1) {
		t.Fatal("unavailable native capture reported success")
	}
	if c.key(0x1e, true, true, false, false, false, false) {
		t.Fatal("failed capture swallowed local input")
	}
	c.Close()
	c.Close()
	<-c.done
	if c.SetCapture("closed", 1) || backend.closed != 1 {
		t.Fatal("capture did not close exactly once")
	}
	var absent *desktopKeyboardCapture
	if absent.SetCapture("absent", 1) {
		t.Fatal("zero-value capture must be inert")
	}
	absent.Release("absent")
	absent.Close()
}

func TestDesktopKeyboardCloseWhileNativeCaptureWaitsForUI(t *testing.T) {
	c, backend, _ := desktopKeyboardFixture(t)
	started, resume := make(chan struct{}), make(chan struct{})
	backend.onCapture = func() {
		close(started)
		<-resume
	}
	captured := make(chan bool, 1)
	go func() { captured <- c.SetCapture("rdp", 1) }()
	<-started
	closed := make(chan struct{})
	go func() {
		c.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		close(resume)
		t.Fatal("shutdown blocked on a native operation waiting for the UI thread")
	}
	close(resume)
	if <-captured {
		t.Fatal("capture succeeded after shutdown")
	}
	<-c.done
	if backend.closed != 1 {
		t.Fatal("native capture was not cleaned up after the UI operation completed")
	}
}
