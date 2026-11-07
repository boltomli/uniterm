//go:build windows

package main

import (
	"syscall"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var procRedrawWindow = syscall.NewLazyDLL("user32.dll").NewProc("RedrawWindow")

// forceWindowRepaint invalidates the whole window tree (frame and children) so
// DWM and the host window run a fresh paint cycle after a resume. The size
// nudge in recoverMainWindow is what makes WebView2 re-upload its bounds; this
// repaint is the companion half for the native frame/background.
//
// Deliberately not RDW_UPDATENOW: that would wait synchronously for the UI
// thread to paint, which is the one thread a recovery path must never block on.
func forceWindowRepaint(window *application.WebviewWindow) {
	if window == nil {
		return
	}
	hwnd := window.NativeWindow()
	if hwnd == nil {
		return
	}
	const (
		rdwInvalidate  = 0x0001
		rdwErase       = 0x0004
		rdwFrame       = 0x0010
		rdwAllChildren = 0x0080
	)
	procRedrawWindow.Call(uintptr(hwnd), 0, 0, rdwInvalidate|rdwErase|rdwFrame|rdwAllChildren)
}
