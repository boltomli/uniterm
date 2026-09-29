package session

import "time"

// Display-watchdog decision logic for the hosted RDP ActiveX control.
//
// Symptom it recovers from: after a long connection the presented frame stops
// updating (usually a black surface) while the session itself stays alive —
// clicks and keystrokes still reach the remote desktop, only the picture is
// frozen. The control is hosted as a WS_CHILD, so it never receives the
// top-level broadcast notifications (WM_DISPLAYCHANGE, power/session changes)
// that make mstsc.exe resync its rendering surface, and a stalled graphics
// stream never recovers without a client-side kick.
//
// The watchdog is pure decision logic (no Win32) so it is unit-testable on any
// platform; rdp_session.go supplies the frame samples and executes the kicks.

// Resync ladder stages, in escalation order.
type displayKick int

const (
	displayKickNone displayKick = iota
	// displayKickRepaint: InvalidateRect + RedrawWindow — repaint the presented
	// frame. Visually neutral.
	displayKickRepaint
	// displayKickRelayout: re-place the control at its tracked rect, post
	// WM_SIZE and forward the WM_DISPLAYCHANGE a WS_CHILD never receives, then
	// repaint. Visually neutral when the size is unchanged.
	displayKickRelayout
	// displayKickRenegotiate: briefly shrink the remote desktop by 2px to force
	// the server to resynchronize the graphics stream (a full-frame redraw —
	// the recovery a window resize gives mstsc users). The remote side sees a
	// transient resize, so it is only used when the frame is essentially black
	// (visually broken anyway) and rate-limited.
	displayKickRenegotiate
)

const (
	// displaySampleInterval is how often the frame is sampled.
	displaySampleInterval = 2 * time.Second
	// displayStallAfter: a frame unchanged this long is a stall candidate.
	displayStallAfter = 15 * time.Second
	// displayInputWindow: a stall is only acted on when the user interacted
	// with the RDP window this recently — that is exactly "clicks work but the
	// picture does not refresh". A static remote desktop nobody touches must
	// not be kicked.
	displayInputWindow = 30 * time.Second
	// displayStageGrace: gap between ladder stages, so a kick gets time to take
	// effect before escalating.
	displayStageGrace = 10 * time.Second
	// displayRenegotiateCooldown: minimum spacing between remote desktop
	// renegotiations (they make remote windows reflow).
	displayRenegotiateCooldown = 5 * time.Minute
	// displayDarkThreshold: fraction of near-black pixels above which the frame
	// counts as "black screen" and the renegotiation stage is allowed.
	displayDarkThreshold = 0.98
)

// displayWatchdog decides when the RDP surface needs a resync kick. All methods
// are called from the message-pump (COM STA) thread, so no locking is needed.
type displayWatchdog struct {
	hash        uint64
	hashSet     bool
	lastChange  time.Time
	lastInput   time.Time
	lastKick    time.Time
	lastRenegot time.Time
	stage       int // ladder progress; 0 = healthy, 1/2 = kicks sent, 3 = parked until the frame changes
}

func newDisplayWatchdog() *displayWatchdog {
	return &displayWatchdog{}
}

// observeInput records that the user interacted with the RDP window.
func (w *displayWatchdog) observeInput(now time.Time) {
	w.lastInput = now
}

// tick feeds one frame sample and returns the next resync action, if any.
// hash identifies the sampled frame content; dark is the fraction of near-black
// pixels. A changing frame resets the ladder.
func (w *displayWatchdog) tick(now time.Time, hash uint64, dark float64) displayKick {
	if !w.hashSet {
		w.hashSet = true
		w.hash = hash
		w.lastChange = now
		return displayKickNone
	}
	if hash != w.hash {
		w.hash = hash
		w.lastChange = now
		w.stage = 0
		return displayKickNone
	}
	if now.Sub(w.lastChange) < displayStallAfter {
		return displayKickNone
	}
	if now.Sub(w.lastInput) > displayInputWindow {
		return displayKickNone
	}

	switch {
	case w.stage == 0:
		w.stage = 1
		w.lastKick = now
		return displayKickRepaint
	case w.stage == 1 && now.Sub(w.lastKick) >= displayStageGrace:
		w.stage = 2
		w.lastKick = now
		return displayKickRelayout
	case w.stage == 2 && now.Sub(w.lastKick) >= displayStageGrace:
		w.stage = 3
		if dark >= displayDarkThreshold && now.Sub(w.lastRenegot) >= displayRenegotiateCooldown {
			w.lastRenegot = now
			w.lastKick = now
			return displayKickRenegotiate
		}
		return displayKickNone
	}
	return displayKickNone
}
