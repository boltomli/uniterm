package main

import "testing"

// The stranded-window fix has two halves: WebView2 must never stop compositing
// because Chromium believes the window is occluded (the disabled feature), and
// the app must re-assert the window after a resume (window_recovery.go).
// Pin the Chromium half: dropping the feature silently reintroduces the bug
// where the frameless window vanishes after sleep and the taskbar button does
// nothing.
func TestWindowsDisabledFeaturesDisablesNativeWinOcclusion(t *testing.T) {
	features := windowsDisabledFeatures()

	seen := make(map[string]int, len(features))
	for _, f := range features {
		seen[f]++
	}
	for _, required := range []string{"CalculateNativeWinOcclusion", "AutofillAiWalletPrivatePasses"} {
		switch seen[required] {
		case 0:
			t.Errorf("windowsDisabledFeatures() must include %q", required)
		case 1:
			// expected
		default:
			t.Errorf("windowsDisabledFeatures() lists %q %d times", required, seen[required])
		}
	}
}

// The resume repair must not resurrect a window the user deliberately hid to
// the tray, so show/hide intent has to be recorded by the app's own paths.
func TestWindowVisibleIntentTracksIntent(t *testing.T) {
	t.Cleanup(func() { windowVisibleIntent.Store(true) })

	noteWindowHidden()
	if windowVisibleIntent.Load() {
		t.Fatal("noteWindowHidden() must record that the window should stay hidden")
	}
	noteWindowShown()
	if !windowVisibleIntent.Load() {
		t.Fatal("noteWindowShown() must record that the window should be visible")
	}
}

// A repair scheduled before the window exists (or with autotest's hidden
// window) must be a no-op rather than a panic.
func TestRecoverMainWindowIgnoresMissingWindow(t *testing.T) {
	recoverMainWindow(nil, "test")
	scheduleWindowRecovery(nil, "test")
	setupWindowRecovery(nil, nil)
}
