//go:build !windows

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// forceWindowRepaint is a no-op outside Windows: the WKWebView/GTK backends
// repaint on their own after a wake, and the recovery's size nudge already
// gives them a fresh layout.
func forceWindowRepaint(_ *application.WebviewWindow) {}
