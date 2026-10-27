//go:build windows

package main

import (
	"reflect"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// releaseWebView2 disposes the WebView2 COM controller for the given window so
// that the WebView2 browser subprocess (msedgewebview2.exe) terminates before
// the process calls ExitProcess.
//
// Why this is needed: wails v3 beta.27's app-close path (Application.Quit ->
// cleanup -> window.Close) never actually releases the WebView2 COM objects.
// WebviewWindow.Close() only emits the WindowClosing event, and
// edge.Chromium.ShuttingDown() only sets a flag — the ICoreWebView2Controller,
// ICoreWebView2 and ICoreWebView2Environment COM references are retained for
// the life of the process. The WebView2 runtime therefore keeps its browser
// subprocess alive, and when ExitProcess runs its DLL-detach teardown it
// blocks forever waiting on that subprocess, leaving a frozen, unresponsive
// window (the "total-close fake hang").
//
// Calling ICoreWebView2Controller::Close() is the documented, graceful way to
// close the webview and, when it is the last controller on the environment,
// terminate the browser subprocess. Releasing the controller/webview and the
// environment afterwards lets the WebView2 runtime unwind cleanly, so the
// subsequent ExitProcess has nothing left to wait on and the app exits
// normally.
//
// The controller/webview/environment live behind wails' internal
// application.windowsWebviewWindow.chromium field (a *edge.Chromium), which is
// unexported and in an internal package that this module cannot import. We
// reach it via reflection and call only exported COM-wrapper methods. This is
// best-effort: any unexpected shape silently no-ops and never panics. It MUST
// run on the WebView2 UI thread while the message loop is still alive (see
// shutdown()).
func releaseWebView2(window *application.WebviewWindow) {
	defer func() { _ = recover() }()

	if window == nil {
		return
	}
	windowV := reflect.ValueOf(window)
	if windowV.Kind() != reflect.Pointer || windowV.IsNil() {
		return
	}

	// window.impl -> webviewWindowImpl interface holding *windowsWebviewWindow.
	// untaint() re-wraps the field so reflect does not treat it as read-only
	// when we later call methods (see untaint).
	impl := untaint(windowV.Elem().FieldByName("impl"))
	if !impl.IsValid() || impl.Kind() != reflect.Interface || impl.IsNil() {
		return
	}
	impl = impl.Elem() // unwrap interface -> *windowsWebviewWindow
	if !impl.IsValid() || impl.Kind() != reflect.Pointer || impl.IsNil() {
		return
	}
	impl = impl.Elem() // deref -> windowsWebviewWindow struct
	if !impl.IsValid() || impl.Kind() != reflect.Struct {
		return
	}

	// impl.chromium -> *edge.Chromium
	chromium := untaint(impl.FieldByName("chromium"))
	if !chromium.IsValid() || chromium.Kind() != reflect.Pointer || chromium.IsNil() {
		return
	}

	callMethod(chromium, "ReleaseCompositionResources")

	// controller := chromium.GetController(); controller.Close(); controller.Release()
	if ctrl := callMethod(chromium, "GetController"); ctrl.IsValid() && ctrl.Kind() == reflect.Pointer && !ctrl.IsNil() {
		callMethod(ctrl, "Close")
		callMethod(ctrl, "Release")
	}

	// webview field (*ICoreWebView2) — Release
	if wv := untaint(chromium.Elem().FieldByName("webview")); wv.IsValid() && wv.Kind() == reflect.Pointer && !wv.IsNil() {
		callMethod(wv, "Release")
	}

	// environment := chromium.Environment(); environment.Release()
	if env := callMethod(chromium, "Environment"); env.IsValid() && env.Kind() == reflect.Pointer && !env.IsNil() {
		callMethod(env, "Release")
	}
}

// untaint returns a fresh, addressable reflect.Value wrapping the same memory
// as v. Go's reflect marks values obtained through unexported fields as
// read-only (flagRO); calling a method (Value.Call) on such a value panics
// with "reflect: reflect.Value.Call using value obtained using unexported
// field". Re-wrapping the address with reflect.NewAt drops that flag while
// keeping the target identical, so exported methods can be invoked. v must be
// addressable; fields of a struct reached through a pointer are.
func untaint(v reflect.Value) reflect.Value {
	if !v.IsValid() || !v.CanAddr() {
		return v
	}
	return reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
}

// callMethod invokes the named exported method on v (a reflect.Value) and
// returns the first return value. Best-effort: returns an invalid Value and
// recovers on any failure.
func callMethod(v reflect.Value, name string) reflect.Value {
	defer func() { _ = recover() }() // reflect panics on phantom methods / bad calls
	if !v.IsValid() {
		return reflect.Value{}
	}
	m := v.MethodByName(name)
	if !m.IsValid() {
		return reflect.Value{}
	}
	out := m.Call(nil)
	if len(out) == 0 {
		return reflect.Value{}
	}
	return out[0]
}
