package main

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/ys-ll/uniterm/backend/log"
)

// Window recovery after a long idle period.
//
// Symptom this file defends against: the machine sleeps/hibernates or the
// session stays locked for hours; on the way back the uniTerm window is simply
// gone from the desktop, the taskbar button no longer brings anything back and
// the tray icon looks dead. The window is frameless, so there is not even a
// frame left to click — the only proof the process is alive is Task Manager.
//
// Three independent mechanisms can produce that state, and each is covered
// here:
//
//  1. Chromium's native window occlusion tracker (the
//     CalculateNativeWinOcclusion feature, on by default on Windows) decides
//     the window is not visible — display off, lock screen, another window
//     covering it — and stops compositing. Across a power transition it does
//     not always notice that the window is visible again, and a frameless
//     window with nothing composited looks deleted even though WS_VISIBLE is
//     still set. main.go disables the feature outright; this file also
//     re-asserts the WebView2 surface after a resume in case the renderer was
//     already stopped before that change shipped.
//
//  2. The display layout changes across the sleep (dock undocked, monitor
//     powered off, remote session re-negotiated). The window keeps its old
//     rectangle, no current screen covers it, and clicking the taskbar button
//     focuses a window the user cannot see: "the taskbar icon does nothing".
//
//  3. The session was locked/unlocked (or the window was hidden by the shell
//     for another reason) and nothing re-shows a window the user still
//     expects to be open.
//
// Every repair runs on its own goroutine. The triggers fire either on the UI
// thread (WndProc) or while WebView2 is still restarting, and the repair calls
// back into Wails window APIs that dispatch to that same UI thread: running
// them inline would freeze the very window being repaired.

// windowVisibleIntent records the user's last explicit show/hide choice
// (tray click, tray menu, global hotkey, single-instance raise). A wake or a
// display change must not resurrect a window the user deliberately hid to the
// tray, so the repair consults this instead of the raw WS_VISIBLE bit.
var windowVisibleIntent atomic.Bool

func init() {
	windowVisibleIntent.Store(true)
}

// noteWindowShown and noteWindowHidden record that the app itself has just
// shown/hidden the window, which is what "the user wants it visible" means.
// Wiring them into every show/hide path keeps the intent honest; a hide the
// OS performs on its own never touches it.
func noteWindowShown()  { windowVisibleIntent.Store(true) }
func noteWindowHidden() { windowVisibleIntent.Store(false) }

// recoveryDelay is how long a repair waits before touching the window. Right
// after a resume the shell, DWM and the WebView2 browser/GPU processes are
// still coming back; calling into WebView2 while its GPU device is being
// re-created is exactly the kind of synchronous COM call that blocks the UI
// thread and turns a blank window into a dead one. A short delay lets the
// compositor settle first.
var recoveryDelay = 2 * time.Second

// recoveryMu serializes repairs: a resume and a display change usually arrive
// together, and two overlapping 1px resize nudge pairs would fight over the
// same window size. lastRecoveryEnd de-duplicates those bursts — a wake emits
// both PBT_APMRESUMEAUTOMATIC (also mapped to the common SystemDidWake event)
// and PBT_APMRESUMESUSPEND, and Windows follows it with WM_DISPLAYCHANGE, so
// without the guard the user would see four resize nudges (and four terminal
// refits) for one resume.
var (
	recoveryMu      sync.Mutex
	lastRecoveryEnd time.Time
)

// recoveryDedupeWindow is how long after a completed repair further triggers
// are ignored. Long enough to swallow one resume burst, short enough that a
// genuine later display change still gets its own repair.
const recoveryDedupeWindow = 10 * time.Second

// setupWindowRecovery subscribes to the OS lifecycle events that can leave the
// main window invisible or unpainted. Application-event callbacks already run
// off the UI thread; the repair is spawned per event so a slow window call can
// never back up the event dispatcher.
func setupWindowRecovery(w3app *application.App, window *application.WebviewWindow) {
	if w3app == nil || window == nil {
		return
	}
	w3app.Event.OnApplicationEvent(events.Common.SystemDidWake, func(*application.ApplicationEvent) {
		scheduleWindowRecovery(window, "system-wake")
	})
	// A resume triggered by user input arrives as PBT_APMRESUMESUSPEND, which
	// Wails does not map onto the common SystemDidWake event.
	w3app.Event.OnApplicationEvent(events.Windows.APMResumeSuspend, func(*application.ApplicationEvent) {
		scheduleWindowRecovery(window, "system-resume")
	})
}

// scheduleWindowRecovery starts the repair on a background goroutine. Safe to
// call from the WndProc: it only spawns the goroutine and returns.
func scheduleWindowRecovery(window *application.WebviewWindow, reason string) {
	if window == nil {
		return
	}
	go recoverMainWindow(window, reason)
}

// recoverMainWindow restores the window to a visible, on-screen, painting
// state after a power/display/session transition. It is deliberately
// conservative: a window the user hid to the tray stays hidden, and a
// minimised window is left minimised (its restore path re-asserts the WebView2
// surface by itself).
func recoverMainWindow(window *application.WebviewWindow, reason string) {
	if window == nil {
		return
	}
	// Headless update e2e runs must never flash a window (see Hidden in main.go).
	if autotestEnabled() {
		return
	}

	recoveryMu.Lock()
	defer recoveryMu.Unlock()

	if since := time.Since(lastRecoveryEnd); !lastRecoveryEnd.IsZero() && since < recoveryDedupeWindow {
		log.Writef("window recovery: skipping %s, another repair finished %s ago", reason, since.Round(time.Millisecond))
		return
	}
	defer func() { lastRecoveryEnd = time.Now() }()

	time.Sleep(recoveryDelay)

	log.Writef("window recovery: begin (%s)", reason)

	// 3. The window was hidden without the user asking for it.
	if windowVisibleIntent.Load() {
		if !window.IsVisible() {
			log.Writef("window recovery: window was not visible, showing it")
			window.Show()
		}
	} else if !window.IsVisible() {
		// Hidden to the tray on purpose; a wake must not pop it back up.
		log.Writef("window recovery: window hidden by user, skipping")
		return
	}

	minimised := window.IsMinimised()

	// 2. The rectangle may no longer be covered by any attached screen.
	if !minimised {
		x, y := window.Position()
		w, h := window.Size()
		if !windowGeometryVisible(x, y, w, h, allScreens()) {
			log.Writef("window recovery: rect (%d,%d %dx%d) is off-screen, recentering", x, y, w, h)
			centerWindowOnPrimary(window, w, h)
		}
	}

	// 1. Re-assert the WebView2 surface. InvalidateRect on the host window
	// never reaches the Chromium child, which owns its own compositor: the
	// smallest thing that makes WebView2 re-upload its bounds and repaint is
	// an actual size change, so nudge the window by one pixel and put it
	// straight back. Skipped while minimised (the restore path re-asserts the
	// surface itself) and while maximised (SetWindowPos would edit the
	// restored size instead of the displayed one, and a 1px resize of a
	// maximised window is a no-op).
	if !minimised && !window.IsMaximised() {
		if w, h := window.Size(); w > 2 && h > 2 {
			window.SetSize(w-1, h)
			window.SetSize(w, h)
		}
	}
	forceWindowRepaint(window)

	log.Writef("window recovery: done (%s)", reason)
}

// allScreens returns the cached screen list, or nil when the Wails runtime is
// not up (unit tests): windowGeometryVisible treats nil as "cannot tell" and
// keeps the current geometry.
func allScreens() []*application.Screen {
	app := application.Get()
	if app == nil {
		return nil
	}
	return app.Screen.GetAll()
}

// centerWindowOnPrimary puts a window of w x h in the middle of the primary
// screen's work area, clamping the size so the taskbar is not covered. Shared
// by the recovery path; resetWindowGeometry has the same body but also
// persists the new geometry and raises the window.
func centerWindowOnPrimary(window *application.WebviewWindow, w, h int) {
	if window == nil {
		return
	}
	app := application.Get()
	if app == nil {
		return
	}
	var wa application.Rect
	if screen := app.Screen.GetPrimary(); screen != nil {
		wa = screen.WorkArea
	}
	if wa.Width <= 0 || wa.Height <= 0 {
		// Screen API unavailable — at least centre it on the default screen.
		window.Center()
		return
	}
	if w <= 0 || w > wa.Width {
		w = wa.Width
	}
	if h <= 0 || h > wa.Height {
		h = wa.Height
	}
	window.SetSize(w, h)
	window.SetPosition(wa.X+(wa.Width-w)/2, wa.Y+(wa.Height-h)/2)
}
