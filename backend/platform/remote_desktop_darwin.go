package platform

import (
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type RDPKeyboardCapture struct {
	*desktopKeyboardCapture
}

var darwinDesktopScans = [128]uint16{
	0: 0x1e, 1: 0x1f, 2: 0x20, 3: 0x21, 4: 0x23, 5: 0x22, 6: 0x2c, 7: 0x2d,
	8: 0x2e, 9: 0x2f, 10: 0x56, 11: 0x30, 12: 0x10, 13: 0x11, 14: 0x12, 15: 0x13,
	16: 0x15, 17: 0x14, 18: 0x02, 19: 0x03, 20: 0x04, 21: 0x05, 22: 0x07, 23: 0x06,
	24: 0x0d, 25: 0x0a, 26: 0x08, 27: 0x0c, 28: 0x09, 29: 0x0b, 30: 0x1b, 31: 0x18,
	32: 0x16, 33: 0x1a, 34: 0x17, 35: 0x19, 36: 0x1c, 37: 0x26, 38: 0x24, 39: 0x28,
	40: 0x25, 41: 0x27, 42: 0x2b, 43: 0x33, 44: 0x35, 45: 0x31, 46: 0x32, 47: 0x34,
	48: 0x0f, 49: 0x39, 50: 0x29, 51: 0x0e, 53: 0x01, 54: 0xe05c, 55: 0xe05b,
	56: 0x2a, 57: 0x3a, 58: 0x38, 59: 0x1d, 60: 0x36, 61: 0xe038, 62: 0xe01d,
	65: 0x53, 67: 0x37, 69: 0x4e, 71: 0x45, 75: 0xe035, 76: 0xe01c, 78: 0x4a,
	82: 0x52, 83: 0x4f, 84: 0x50, 85: 0x51, 86: 0x4b, 87: 0x4c, 88: 0x4d,
	89: 0x47, 91: 0x48, 92: 0x49, 96: 0x3f, 97: 0x40, 98: 0x41, 99: 0x3d,
	100: 0x42, 101: 0x43, 103: 0x57, 109: 0x44, 111: 0x58, 114: 0xe052,
	115: 0xe047, 116: 0xe049, 117: 0xe053, 118: 0x3e, 119: 0xe04f, 120: 0x3c,
	121: 0xe051, 122: 0x3b, 123: 0xe04b, 124: 0xe04d, 125: 0xe050, 126: 0xe048,
}

type darwinDesktopKeyboard struct {
	core                 *desktopKeyboardCapture
	invoke               func(func())
	window               objc.ID
	webview              objc.ID
	application          objc.ID
	tap, source          uintptr
	mode                 uintptr
	prompted             bool
	tapCreate            func(uint32, uint32, uint32, uint64, uintptr, uintptr) uintptr
	tapEnable            func(uintptr, bool)
	getInteger           func(uintptr, uint32) int64
	getFlags             func(uintptr) uint64
	keyState             func(int32, uint16) bool
	trusted              func(uintptr) bool
	dictionary           func(uintptr, *uintptr, *uintptr, int64, uintptr, uintptr) uintptr
	makeSource           func(uintptr, uintptr, int64) uintptr
	mainLoop             func() uintptr
	addSource            func(uintptr, uintptr, uintptr)
	removeSource         func(uintptr, uintptr, uintptr)
	invalidate           func(uintptr)
	cfRelease            func(uintptr)
	promptKey, trueValue uintptr
	callback             uintptr
	isActive             objc.SEL
	isKeyWindow          objc.SEL
	viewWindow           objc.SEL
}

func NewRDPKeyboardCapture(emit func(RDPKeyEvent), dispatch ...func(func())) (*RDPKeyboardCapture, error) {
	if len(dispatch) == 0 || dispatch[0] == nil {
		return nil, fmt.Errorf("desktop keyboard capture requires a UI dispatcher")
	}
	capture := newDesktopKeyboardCapture(emit)
	b := &darwinDesktopKeyboard{core: capture, invoke: dispatch[0]}
	libraries := []struct {
		path      string
		functions map[string]any
	}{
		{"/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", map[string]any{
			"CGEventTapCreate": &b.tapCreate, "CGEventTapEnable": &b.tapEnable,
			"CGEventGetIntegerValue": &b.getInteger, "CGEventGetFlags": &b.getFlags,
			"CGEventSourceKeyState": &b.keyState,
		}},
		{"/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", map[string]any{
			"AXIsProcessTrustedWithOptions": &b.trusted,
		}},
		{"/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", map[string]any{
			"CFDictionaryCreate": &b.dictionary, "CFMachPortCreateRunLoopSource": &b.makeSource,
			"CFRunLoopGetMain": &b.mainLoop, "CFRunLoopAddSource": &b.addSource,
			"CFRunLoopRemoveSource": &b.removeSource, "CFMachPortInvalidate": &b.invalidate,
			"CFRelease": &b.cfRelease,
		}},
	}
	for _, library := range libraries {
		handle, err := purego.Dlopen(library.path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return nil, err
		}
		if err := bindDesktopKeyboardFunctions(handle, library.functions); err != nil {
			return nil, err
		}
	}
	for name, target := range map[string]*uintptr{
		"kAXTrustedCheckOptionPrompt": &b.promptKey, "kCFBooleanTrue": &b.trueValue,
		"kCFRunLoopCommonModes": &b.mode,
	} {
		address, err := purego.Dlsym(purego.RTLD_DEFAULT, name)
		if err != nil {
			return nil, fmt.Errorf("desktop keyboard constant %s: %w", name, err)
		}
		*target = *(*uintptr)(unsafe.Pointer(address))
	}
	b.callback = purego.NewCallback(b.event)
	b.isActive = objc.RegisterName("isActive")
	b.isKeyWindow = objc.RegisterName("isKeyWindow")
	b.viewWindow = objc.RegisterName("window")
	capture.backend = b
	go capture.run()
	return &RDPKeyboardCapture{capture}, nil
}

func bindDesktopKeyboardFunctions(library uintptr, functions map[string]any) error {
	for name, target := range functions {
		symbol, err := purego.Dlsym(library, name)
		if err != nil {
			return fmt.Errorf("desktop keyboard API %s: %w", name, err)
		}
		purego.RegisterFunc(target, symbol)
	}
	return nil
}

func (b *darwinDesktopKeyboard) capture(window uintptr) bool {
	var enabled bool
	b.invoke(func() {
		options := uintptr(0)
		if !b.prompted {
			options = b.dictionary(0, &b.promptKey, &b.trueValue, 1, 0, 0)
			b.prompted = true
		}
		trusted := b.trusted(options)
		if options != 0 {
			b.cfRelease(options)
		}
		if !trusted {
			return
		}
		if b.window != objc.ID(window) {
			b.releaseViews()
			b.window = objc.ID(window).Send(objc.RegisterName("retain"))
		}
		b.application = objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
		if view := findDesktopWebview(b.window.Send(objc.RegisterName("contentView")), 0); view != 0 && view != b.webview {
			if b.webview != 0 {
				b.webview.Send(objc.RegisterName("release"))
			}
			b.webview = view.Send(objc.RegisterName("retain"))
		}
		if !b.focusedOnUI() {
			return
		}
		if b.tap == 0 {
			b.tap = b.tapCreate(1, 0, 0, 1<<10|1<<11|1<<12, b.callback, 0)
			if b.tap == 0 {
				return
			}
			b.source = b.makeSource(0, b.tap, 0)
			if b.source == 0 {
				b.invalidate(b.tap)
				b.cfRelease(b.tap)
				b.tap = 0
				return
			}
			b.addSource(b.mainLoop(), b.source, b.mode)
		}
		b.tapEnable(b.tap, true)
		enabled = true
	})
	return enabled
}

func findDesktopWebview(view objc.ID, depth int) objc.ID {
	if view == 0 || depth > 64 {
		return 0
	}
	if view.Send(objc.RegisterName("isKindOfClass:"), objc.GetClass("WKWebView")) != 0 {
		return view
	}
	children := view.Send(objc.RegisterName("subviews"))
	count := uintptr(children.Send(objc.RegisterName("count")))
	for i := uintptr(0); i < count; i++ {
		if result := findDesktopWebview(children.Send(objc.RegisterName("objectAtIndex:"), i), depth+1); result != 0 {
			return result
		}
	}
	return 0
}

func (b *darwinDesktopKeyboard) focusedOnUI() bool {
	if b.application == 0 || b.application.Send(b.isActive) == 0 {
		return false
	}
	window := b.window
	if b.webview != 0 {
		window = b.webview.Send(b.viewWindow)
	}
	return window != 0 && window.Send(b.isKeyWindow) != 0
}

func (b *darwinDesktopKeyboard) focused() bool {
	var focused bool
	b.invoke(func() { focused = b.focusedOnUI() })
	return focused
}

func (b *darwinDesktopKeyboard) event(_ uintptr, eventType uint32, event uintptr, _ uintptr) uintptr {
	if eventType == 0xfffffffe || eventType == 0xffffffff {
		b.core.mu.Lock()
		b.core.reset()
		b.core.mu.Unlock()
		if b.tap != 0 {
			b.tapEnable(b.tap, true)
		}
		return event
	}
	code := b.getInteger(event, 9)
	if code < 0 || code >= int64(len(darwinDesktopScans)) || darwinDesktopScans[code] == 0 {
		return event
	}
	flags := b.getFlags(event)
	ctrl, shift, meta, alt := flags&(1<<18) != 0, flags&(1<<17) != 0, flags&(1<<20) != 0, flags&(1<<19) != 0
	if eventType == 12 && code == 57 {
		swallow := b.core.key(0x3a, true, b.focusedOnUI(), ctrl, shift, meta, alt)
		b.core.key(0x3a, false, b.focusedOnUI(), ctrl, shift, meta, alt)
		if swallow {
			return 0
		}
		return event
	}
	down := eventType == 10
	if eventType == 12 {
		down = b.keyState(0, uint16(code))
	}
	if b.core.key(darwinDesktopScans[code], down, b.focusedOnUI(), ctrl, shift, meta, alt) {
		return 0
	}
	return event
}

func (b *darwinDesktopKeyboard) release() {}

func (b *darwinDesktopKeyboard) releaseViews() {
	for _, view := range []objc.ID{b.webview, b.window} {
		if view != 0 {
			view.Send(objc.RegisterName("release"))
		}
	}
	b.webview, b.window = 0, 0
}

func (b *darwinDesktopKeyboard) close() {
	b.invoke(func() {
		if b.tap != 0 {
			b.invalidate(b.tap)
			b.removeSource(b.mainLoop(), b.source, b.mode)
			b.cfRelease(b.source)
			b.cfRelease(b.tap)
			b.source, b.tap = 0, 0
		}
		b.releaseViews()
	})
}
