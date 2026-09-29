package session

import (
	"testing"
	"time"
)

func TestDisplayWatchdog_ChangingFrameNeverKicks(t *testing.T) {
	w := newDisplayWatchdog()
	base := time.Unix(1_700_000_000, 0)

	w.observeInput(base)
	for i := range 100 {
		now := base.Add(time.Duration(i) * displaySampleInterval)
		if got := w.tick(now, uint64(i), 0.5); got != displayKickNone {
			t.Fatalf("sample %d: kick = %v, want none", i, got)
		}
	}
}

func TestDisplayWatchdog_StaticFrameWithoutRecentInputNeverKicks(t *testing.T) {
	w := newDisplayWatchdog()
	base := time.Unix(1_700_000_000, 0)

	// One click at the start, then the user walks away: a static remote desktop
	// nobody waits on must not be kicked, no matter how long it stays static.
	w.observeInput(base)
	if k := w.tick(base, 42, 0.0); k != displayKickNone {
		t.Fatalf("first sample: kick = %v, want none", k)
	}
	for i := range 100 {
		now := base.Add(time.Duration(i+30) * displaySampleInterval)
		if got := w.tick(now, 42, 0.0); got != displayKickNone {
			t.Fatalf("sample %d: kick = %v, want none", i, got)
		}
	}

	// The user comes back and clicks on the frozen picture: recovery must start.
	now := base.Add(time.Duration(130) * displaySampleInterval)
	w.observeInput(now)
	if k := w.tick(now, 42, 0.0); k != displayKickRepaint {
		t.Fatalf("after returning input: kick = %v, want repaint", k)
	}
}

func TestDisplayWatchdog_EscalatesLadderOnStallWithRecentInput(t *testing.T) {
	w := newDisplayWatchdog()
	base := time.Unix(1_700_000_000, 0)

	w.observeInput(base)
	// Static (non-black) frame; the user keeps interacting.
	var kicks []displayKick
	for i := range 20 {
		now := base.Add(time.Duration(i) * displaySampleInterval)
		w.observeInput(now)
		if k := w.tick(now, 42, 0.0); k != displayKickNone {
			kicks = append(kicks, k)
		}
	}

	if len(kicks) != 2 || kicks[0] != displayKickRepaint || kicks[1] != displayKickRelayout {
		t.Fatalf("kicks = %v, want [repaint relayout]", kicks)
	}
}

func TestDisplayWatchdog_RenegotiatesOnlyOnBlackFrame(t *testing.T) {
	w := newDisplayWatchdog()
	base := time.Unix(1_700_000_000, 0)

	w.observeInput(base)
	var kicks []displayKick
	for i := range 20 {
		now := base.Add(time.Duration(i) * displaySampleInterval)
		w.observeInput(now)
		if k := w.tick(now, 42, displayDarkThreshold); k != displayKickNone {
			kicks = append(kicks, k)
		}
	}

	want := []displayKick{displayKickRepaint, displayKickRelayout, displayKickRenegotiate}
	if len(kicks) != len(want) {
		t.Fatalf("kicks = %v, want %v", kicks, want)
	}
	for i := range want {
		if kicks[i] != want[i] {
			t.Fatalf("kicks = %v, want %v", kicks, want)
		}
	}
}

func TestDisplayWatchdog_FrameChangeResetsLadder(t *testing.T) {
	w := newDisplayWatchdog()
	base := time.Unix(1_700_000_000, 0)

	w.observeInput(base)
	if k := w.tick(base, 1, 1.0); k != displayKickNone {
		t.Fatalf("first sample: kick = %v, want none", k)
	}
	t1 := base.Add(displayStallAfter)
	w.observeInput(t1)
	if k := w.tick(t1, 1, 1.0); k != displayKickRepaint {
		t.Fatalf("stalled sample: kick = %v, want repaint", k)
	}

	// The frame moves again (recovered): the ladder must restart from scratch,
	// so the next stall starts with a repaint, not a renegotiation.
	t2 := t1.Add(displaySampleInterval)
	if k := w.tick(t2, 2, 1.0); k != displayKickNone {
		t.Fatalf("changed sample: kick = %v, want none", k)
	}
	t3 := t2.Add(displayStallAfter)
	w.observeInput(t3)
	if k := w.tick(t3, 2, 1.0); k != displayKickRepaint {
		t.Fatalf("re-stalled sample: kick = %v, want repaint", k)
	}
}

func TestDisplayWatchdog_RenegotiateRespectsCooldown(t *testing.T) {
	w := newDisplayWatchdog()
	base := time.Unix(1_700_000_000, 0)

	// First stall cycle: full ladder including the renegotiation.
	runStall := func(start time.Time) []displayKick {
		var kicks []displayKick
		now := start
		for range 20 {
			w.observeInput(now)
			if k := w.tick(now, 7, 1.0); k != displayKickNone {
				kicks = append(kicks, k)
			}
			now = now.Add(displaySampleInterval)
		}
		return kicks
	}

	if kicks := runStall(base); len(kicks) != 3 {
		t.Fatalf("first stall kicks = %v, want 3 (repaint, relayout, renegotiate)", kicks)
	}

	// Second stall right after: the frame must move first (simulated by the
	// ladder reset inside runStall's first sample hash change), then stall
	// again — but the renegotiation is on cooldown, so it must be skipped.
	w.tick(base.Add(25*displaySampleInterval), 8, 1.0) // frame changed → ladder reset
	kicks := runStall(base.Add(25*displaySampleInterval + displayStallAfter))
	for _, k := range kicks {
		if k == displayKickRenegotiate {
			t.Fatalf("second stall kicks = %v, renegotiate must be on cooldown", kicks)
		}
	}
	if len(kicks) != 2 {
		t.Fatalf("second stall kicks = %v, want [repaint relayout]", kicks)
	}
}
