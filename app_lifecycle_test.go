package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── initStores single-shot ────────────────────────────────────────────────

// Regression (security.md medium): initStores assigned ~15 store pointers,
// dataDir and mcpApprovals with no guard, and it starts background
// goroutines (auto-start tunnels, foreground watcher, sync). Two racing
// callers — a first-run SetDataDir plus another binding — ran all of it
// twice. The bootstrap must execute exactly once.
func TestInitStoresRunsOnceUnderConcurrency(t *testing.T) {
	a := NewApp(t.TempDir())
	dir := t.TempDir()
	var runs atomic.Int32
	a.initStoresBody = func(string, bool) {
		runs.Add(1)
		time.Sleep(5 * time.Millisecond) // widen the race window
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.initStores(dir, false)
		}()
	}
	wg.Wait()

	if got := runs.Load(); got != 1 {
		t.Fatalf("store bootstrap ran %d times, want exactly 1", got)
	}
}

// Regression (security.md medium): shutdown left the in-flight AI stream
// running and never stopped the WndProc move/resize forwarder goroutine.
func TestShutdownCancelsChatAndStopsMoveResizePump(t *testing.T) {
	a := &App{webviewDataPath: t.TempDir()}
	var cancelled atomic.Bool
	fn := context.CancelFunc(func() { cancelled.Store(true) })
	a.chatCancel.Store(&fn)

	a.startMoveResizePump()
	a.shutdown()

	if !cancelled.Load() {
		t.Fatal("in-flight chat stream was not cancelled at shutdown")
	}
	select {
	case <-a.moveResizeDone:
	default:
		t.Fatal("move/resize forwarder goroutine still running after shutdown")
	}
}

// ── auto-sync coalescing ──────────────────────────────────────────────────

// Regression (security.md low): triggerAutoSync started a full network sync
// goroutine per save with no debounce, so a burst of N saves queued N
// serial syncs behind SyncService.mu.
func TestScheduleAutoSyncCoalescesBurst(t *testing.T) {
	a := &App{}
	var runs atomic.Int32
	run := func() { runs.Add(1) }

	for range 10 {
		a.scheduleAutoSync(20*time.Millisecond, run)
	}
	time.Sleep(250 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("10 triggers produced %d sync runs, want 1", got)
	}

	// The pending slot is released before the run, so a later burst syncs again.
	a.scheduleAutoSync(10*time.Millisecond, run)
	time.Sleep(200 * time.Millisecond)
	if got := runs.Load(); got != 2 {
		t.Fatalf("second burst produced %d sync runs total, want 2", got)
	}
}

// Without a sync service nothing may be scheduled — no timer, no goroutine.
func TestTriggerAutoSyncWithoutServiceSchedulesNothing(t *testing.T) {
	a := &App{}
	for range 10 {
		a.triggerAutoSync()
	}
	if a.autoSyncTimer != nil {
		a.autoSyncTimer.Stop()
		t.Fatal("triggerAutoSync armed a timer with no sync service")
	}
}
