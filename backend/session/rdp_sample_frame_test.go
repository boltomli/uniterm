//go:build windows

package session

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SampleFrame reads the window's presented content: a hidden window must not
// be mistaken for a black screen (the watchdog gates on visibility for this),
// and identical content must hash identically so a static remote desktop is
// not mistaken for an animating one.
func TestSampleFrameReadsWindowContent(t *testing.T) {
	className, _ := windows.UTF16PtrFromString("STATIC")
	title, _ := windows.UTF16PtrFromString("uniterm-sampleframe-test")
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		0x00CF0000, // WS_OVERLAPPEDWINDOW
		100, 100, 200, 120,
		0, 0, 0, 0,
	)
	if hwnd == 0 {
		// No desktop to create windows on (headless agent) — nothing to sample.
		t.Skipf("CreateWindowExW unavailable: %v", err)
	}
	defer procDestroyWindow.Call(hwnd)
	// Show + pin on top: GetDC's DC is clipped to the visible region, so a
	// hidden/occluded window samples as an empty (black) surface.
	procShowWindow.Call(hwnd, SW_SHOWNOACTIVATE)
	procSetWindowPos.Call(hwnd, ^uintptr(0), 0, 0, 0, 0, // HWND_TOPMOST
		SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE)

	s := &RDPSession{}
	s.mu.Lock()
	s.hwnd = hwnd
	s.mu.Unlock()

	// Paint white first so the "content changed" assertion is symmetric.
	paintWindow(t, hwnd, 0x00FFFFFF)

	hash1, dark1, ok := s.sampleFrame()
	if !ok {
		t.Fatal("sampleFrame returned ok=false on a live window")
	}
	t.Logf("white:   hash=%x dark=%.3f", hash1, dark1)
	if dark1 > 0.02 {
		t.Fatalf("dark = %.3f after painting white, want ~0", dark1)
	}

	// Paint the client area black, then sample again: the hash must move and
	// the dark ratio must go to ~1 — the exact signal the watchdog keys on for
	// the black-screen case.
	paintWindow(t, hwnd, 0x00000000)

	hash2, dark2, ok := s.sampleFrame()
	if !ok {
		t.Fatal("sampleFrame failed after painting black")
	}
	t.Logf("black:   hash=%x dark=%.3f", hash2, dark2)
	if hash2 == hash1 {
		t.Fatalf("hash unchanged after repaint (%x) — sampling is not reading the window", hash1)
	}
	if dark2 < 0.98 {
		t.Fatalf("dark = %.3f after painting black, want >= 0.98", dark2)
	}

	// Same content again: the hash must be stable (a noisy hash would make the
	// watchdog believe the frame is animating).
	hash3, _, ok := s.sampleFrame()
	if !ok {
		t.Fatal("third sampleFrame failed")
	}
	if hash3 != hash2 {
		t.Fatalf("hash not stable on identical content: %x vs %x", hash2, hash3)
	}
}

func paintWindow(t *testing.T, hwnd uintptr, colorref uintptr) {
	t.Helper()
	user32 := windows.NewLazySystemDLL("user32.dll")
	gdi32 := windows.NewLazySystemDLL("gdi32.dll")
	procFillRect := user32.NewProc("FillRect")
	procCreateSolidBrush := gdi32.NewProc("CreateSolidBrush")
	dc, _, _ := procGetDC.Call(hwnd)
	if dc == 0 {
		t.Fatal("GetDC failed")
	}
	brush, _, _ := procCreateSolidBrush.Call(colorref)
	r := rect{0, 0, 200, 120}
	procFillRect.Call(dc, uintptr(unsafe.Pointer(&r)), brush)
	procReleaseDC.Call(hwnd, dc)
}
