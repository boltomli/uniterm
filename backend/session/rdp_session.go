//go:build windows

package session

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/ys-ll/uniterm/backend/log"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	atlDll              = windows.NewLazySystemDLL("atl.dll")
	procAtlAxWinInit    = atlDll.NewProc("AtlAxWinInit")
	procAtlAxGetControl = atlDll.NewProc("AtlAxGetControl")

	user32Dll              = windows.NewLazySystemDLL("user32.dll")
	procCreateWindowExW    = user32Dll.NewProc("CreateWindowExW")
	procSetWindowPos       = user32Dll.NewProc("SetWindowPos")
	procShowWindow         = user32Dll.NewProc("ShowWindow")
	procDestroyWindow      = user32Dll.NewProc("DestroyWindow")
	procFindWindowW        = user32Dll.NewProc("FindWindowW")
	procPeekMessage        = user32Dll.NewProc("PeekMessageW")
	procTranslateMessage   = user32Dll.NewProc("TranslateMessage")
	procDispatchMessage    = user32Dll.NewProc("DispatchMessageW")
	procGetWindowRect      = user32Dll.NewProc("GetWindowRect")
	procGetClientRect      = user32Dll.NewProc("GetClientRect")
	procClientToScreen     = user32Dll.NewProc("ClientToScreen")
	procPostMessageW       = user32Dll.NewProc("PostMessageW")
	procFindWindowExW      = user32Dll.NewProc("FindWindowExW")
	procSendMessageW       = user32Dll.NewProc("SendMessageW")
	procGetSystemMetrics   = user32Dll.NewProc("GetSystemMetrics")
	procGetWindowThreadPID = user32Dll.NewProc("GetWindowThreadProcessId")
	procInvalidateRect     = user32Dll.NewProc("InvalidateRect")
	procRedrawWindow       = user32Dll.NewProc("RedrawWindow")
	procGetDC              = user32Dll.NewProc("GetDC")
	procReleaseDC          = user32Dll.NewProc("ReleaseDC")
	procPrintWindow        = user32Dll.NewProc("PrintWindow")

	gdi32Dll                   = windows.NewLazySystemDLL("gdi32.dll")
	procBitBlt                 = gdi32Dll.NewProc("BitBlt")
	procStretchBlt             = gdi32Dll.NewProc("StretchBlt")
	procCreateCompatibleDC     = gdi32Dll.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32Dll.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32Dll.NewProc("SelectObject")
	procDeleteObject           = gdi32Dll.NewProc("DeleteObject")
	procDeleteDC               = gdi32Dll.NewProc("DeleteDC")
	procGetDIBits              = gdi32Dll.NewProc("GetDIBits")

	// kernel32Dll is declared in local_session_windows.go (same package).
	procGetCurrentProcID = kernel32Dll.NewProc("GetCurrentProcessId")
)

const (
	WM_CLOSE           = 0x0010
	WM_SIZE            = 0x0005
	SWP_SHOWWINDOW     = 0x0040
	SWP_HIDEWINDOW     = 0x0080
	SWP_NOMOVE         = 0x0002
	SWP_NOSIZE         = 0x0001
	SWP_NOACTIVATE     = 0x0010
	SWP_NOZORDER       = 0x0004
	SWP_ASYNCWINDOWPOS = 0x4000 // non-blocking: avoids freezing RDP COM thread
	WS_EX_NOACTIVATE   = 0x08000000
	WS_CHILD           = 0x40000000
	WS_CLIPSIBLINGS    = 0x04000000
	PM_REMOVE          = 0x0001
	SW_HIDE            = 0
	SW_SHOWNOACTIVATE  = 4
	RDW_INVALIDATE     = 0x0001
	RDW_ALLCHILDREN    = 0x0080
	RDW_UPDATENOW      = 0x0100
	WM_COMMAND         = 0x0111
	BM_CLICK           = 0x00F5
	IDYES              = 6
	IDOK               = 1
	SM_CXSCREEN        = 0
	SM_CYSCREEN        = 1

	// Display/input messages used by the display watchdog. WM_DISPLAYCHANGE is
	// broadcast to top-level windows only, so the WS_CHILD-hosted control never
	// sees it natively — resync kicks replay it directly (see relayout).
	WM_DISPLAYCHANGE = 0x007E
	WM_KEYDOWN       = 0x0100
	WM_KEYUP         = 0x0101
	WM_SYSKEYDOWN    = 0x0104
	WM_SYSKEYUP      = 0x0105
	WM_MOUSEMOVE     = 0x0200
	WM_LBUTTONDOWN   = 0x0201
	WM_LBUTTONUP     = 0x0202
	WM_RBUTTONDOWN   = 0x0204
	WM_RBUTTONUP     = 0x0205
	WM_MBUTTONDOWN   = 0x0207
	WM_MBUTTONUP     = 0x0208
	WM_MOUSEWHEEL    = 0x020A
	WM_XBUTTONDOWN   = 0x020B
	WM_XBUTTONUP     = 0x020C

	// Thumbnail size for the watchdog's frame samples (see sampleFrame).
	sampleThumbW = 64
	sampleThumbH = 36
)

const (
	// Delay after the last window-size change before applying the new desktop
	// size (adaptive resolution). Avoids reconnect storms while the user drags
	// the window edge.
	adaptiveResizeDelay = 1200 * time.Millisecond
	// Smallest change (in either dimension) that is worth a reconnect. Below
	// this the existing desktop is kept and merely clipped/scaled by the
	// control window.
	adaptiveResizeThreshold = 16
	// RDP desktop size limits for adaptive mode.
	adaptiveMinWidth  = 640
	adaptiveMinHeight = 480
	adaptiveMaxWidth  = 7680
	adaptiveMaxHeight = 4320
	// Grace window after a Reconnect during which the pump's "connection lost"
	// detection is suppressed: the Connected property transiently reads 0
	// while the control renegotiates.
	adaptiveReconnectGrace = 15 * time.Second
	// Minimum spacing between two adaptive reconnects.
	adaptiveMinInterval = 3 * time.Second
	// An A→B→A request within this window after a reconnect completes is
	// treated as a machine-driven resize feedback loop: a human cannot act
	// within 2 seconds of a ~5s disconnect/reconnect cycle finishing, but a
	// layout feedback loop fires almost immediately. Legitimate maximize/
	// restore toggling (5s+ between actions) must NOT trip this.
	adaptiveLoopWindow = 2 * time.Second
	// When a suspected feedback loop is detected, adaptive resize pauses for
	// this long instead of being disabled for the session; a real loop keeps
	// re-triggering the cooldown (effectively suppressed), while occasional
	// false positives self-heal.
	adaptiveCooldown = 10 * time.Second
	// Timeouts for the adaptive resize phases: disconnect-wait and
	// reconnect-wait (the pump drives both, see stepAdaptiveResize).
	adaptivePhase1Timeout = 5 * time.Second
	adaptivePhase2Timeout = 30 * time.Second
	// Connected must stay 1 this long before the reconnect counts as
	// established: the property flips to 1 immediately after Connect() (it
	// reflects the requested state, not an established session), and acting
	// on it too early — e.g. toggling the deferred full screen — makes the
	// control bail back to windowed mode.
	adaptivePhase2Settle = 2 * time.Second
	// RdpFixedWidth/Height sentinels (see ConnectionConfig).
	rdpSizeFullscreen = -1 // follow the primary monitor on connect
	rdpSizeAdaptive   = -2 // desktop follows the .rdp-area window size
	// Layout reserves used both when placing the RDP child window and when
	// estimating the .rdp-area size for the initial adaptive desktop size.
	rdpTopReserve    = 80
	rdpBottomReserve = 32
	rdpSideMargin    = 4
)

type RDPSession struct {
	baseSession
	parentHwnd uintptr
	hwnd       uintptr
	rdp        *ole.IDispatch
	config     ConnectionConfig
	mu         sync.Mutex
	shown      bool

	// Last known position, used by Show() after Hide()
	trackX, trackY int
	trackW, trackH int

	// Full-screen toggle request, applied on the COM STA thread by the message
	// pump. COM (STA) calls must run on the thread that created the object;
	// calling PutProperty from the Wails binding thread deadlocks.
	fsRequested bool   // a toggle has been requested
	fsValue     bool   // desired FullScreen value
	fsActive    bool   // last observed FullScreen state (for exit detection)
	onFsExit    func() // called (on COM thread) when user leaves full screen via the connection bar

	// Remote desktop size (DesktopWidth/DesktopHeight) configured at Connect.
	// The display watchdog's renegotiation kick shrinks it by 2px and restores
	// it, forcing the server to resynchronize the graphics stream.
	deskW, deskH int
	// Manual refresh request (frontend "refresh" button), applied on the COM
	// STA thread by the message pump for the same reason as fsRequested.
	resyncRequested bool
	// Adaptive resize lifecycle notifications, so the frontend can freeze the
	// last frame over .rdp-area during the disconnect/reconnect cycle.
	// event is "start" (snapshot carries a frozen PNG, base64) or "end".
	onAdaptiveEvent func(event, snapshot string)
	// Adaptive mode: a FullScreen=true toggle deferred until the desktop has
	// been resized to the monitor resolution (see runMessagePump).
	fsAfterResize bool

	// Adaptive desktop resize: when config.RdpFixedWidth == rdpSizeAdaptive, SetPosition
	// tracks the .rdp-area size and, once it stays stable, the message pump
	// runs a disconnect → resize → reconnect cycle (IMsRdpClient8::Reconnect
	// on a live session never completes its renegotiation; the explicit
	// disconnect/reconnect cycle is what mstsc itself does when resizing).
	rsW, rsH                   int // pending request, normalized (even, clamped)
	rsAt                       time.Time
	rsReq                      bool
	rsPhase                    int       // 0 idle, 1 disconnect-wait, 2 reconnect-wait
	rsDeadline                 time.Time // phase timeout
	rsUpSince                  time.Time // phase 2: when Connected first read 1 (settle timer)
	desktopW, desktopH         int       // desktop size currently in effect
	prevDesktopW, prevDesktopH int       // desktop size before the last reconnect
	rsAppliedAt                time.Time // when the last reconnect was applied
	rsGraceUntil               time.Time // suppress disconnect detection until this time
	rsCooldownUntil            time.Time // adaptive resize paused (suspected feedback loop)
}

// SetOnFullScreenExit registers a callback fired when the user exits the
// ActiveX full screen via its built-in connection bar.
func (s *RDPSession) SetOnFullScreenExit(cb func()) {
	s.mu.Lock()
	s.onFsExit = cb
	s.mu.Unlock()
}

// SetOnAdaptiveEvent registers a callback fired at the start and end of each
// adaptive resize cycle ("start" carries a frozen frame; "end" none).
func (s *RDPSession) SetOnAdaptiveEvent(cb func(event, snapshot string)) {
	s.mu.Lock()
	s.onAdaptiveEvent = cb
	s.mu.Unlock()
}

// emitAdaptive delivers an adaptive resize lifecycle event to the host.
func (s *RDPSession) emitAdaptive(event, snapshot string) {
	s.mu.Lock()
	cb := s.onAdaptiveEvent
	s.mu.Unlock()
	if cb != nil {
		cb(event, snapshot)
	}
}

func NewRDPSession(id string) *RDPSession {
	return &RDPSession{
		baseSession: baseSession{
			id:          id,
			sessionType: "rdp",
			status:      StatusDisconnected,
		},
	}
}

// ClientAreaScreenRect returns the main window's client area in screen
// coordinates (physical pixels). Surfaced in the session:status payload; the
// frontend positions the RDP child window from the .rdp-area DOM rect, so these
// values are diagnostic only.
func (s *RDPSession) ClientAreaScreenRect() (x, y, w, h int) {
	if s.parentHwnd == 0 {
		return
	}
	var cr rect
	ret, _, _ := procGetClientRect.Call(s.parentHwnd, uintptr(unsafe.Pointer(&cr)))
	if ret == 0 {
		return
	}
	var origin point
	ret, _, _ = procClientToScreen.Call(s.parentHwnd, uintptr(unsafe.Pointer(&origin)))
	if ret == 0 {
		return
	}
	return int(origin.X), int(origin.Y), int(cr.Right), int(cr.Bottom)
}

func (s *RDPSession) SetParentHwnd(hwnd uintptr) {
	s.parentHwnd = hwnd
}

// autoDismissSecurityDialogs polls for RDP security warning dialogs (e.g.
// cert prompts or "do you want to connect" dialogs) and dismisses them.
func (s *RDPSession) autoDismissSecurityDialogs(stop <-chan struct{}) {
	dialogTitles := []string{
		"远程桌面连接",
		"远程桌面连接安全警告",
		"Remote Desktop Connection",
		"Remote Desktop Connection Security Warning",
		"Windows 安全",
		"Windows Security",
		"安全警告",
		"Security Warning",
	}
	clsName, _ := windows.UTF16PtrFromString("#32770")

	// Our own process ID, used to skip dialogs belonging to the system's mstsc.
	pid, _, _ := procGetCurrentProcID.Call()
	selfPID := uint32(pid)

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			for _, title := range dialogTitles {
				tPtr, _ := windows.UTF16PtrFromString(title)
				hwnd, _, _ := procFindWindowW.Call(
					uintptr(unsafe.Pointer(clsName)),
					uintptr(unsafe.Pointer(tPtr)),
				)
				if hwnd == 0 {
					continue
				}

				// Only dismiss dialogs owned by our OWN process. The dialog class
				// (#32770) and titles ("远程桌面连接" / "Windows 安全") are shared
				// with the system's mstsc; without this filter we would repeatedly
				// close mstsc's own password/security prompts, causing them to
				// flicker and re-open. (issue #348)
				var dlgPID uint32
				procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&dlgPID)))
				if dlgPID != selfPID {
					continue
				}

				// Dismiss via standard dialog button IDs
				procPostMessageW.Call(hwnd, WM_COMMAND, IDYES, 0)
				procPostMessageW.Call(hwnd, WM_COMMAND, IDOK, 0)
				procPostMessageW.Call(hwnd, WM_COMMAND, IDYES+1, 0) // IDNO sometimes maps to 7

				// Also try clicking buttons directly (Windows 11 may use different labels)
				for _, btnText := range []string{
					"是(&Y)", "是", "Yes", "&Yes",
					"连接(&C)", "连接(&N)", "连接", "Connect", "&Connect",
					"确认", "确认(&Y)", "确认(&O)",
					"确定", "确定(&O)", "OK", "&OK",
					"继续", "继续(&C)", "Continue", "&Continue",
				} {
					btnPtr, _ := windows.UTF16PtrFromString(btnText)
					btnHwnd, _, _ := procFindWindowExW.Call(hwnd, 0, 0, uintptr(unsafe.Pointer(btnPtr)))
					if btnHwnd != 0 {
						procSendMessageW.Call(btnHwnd, BM_CLICK, 0, 0)
						break
					}
				}
			}
		}
	}
}

func (s *RDPSession) Connect(config ConnectionConfig) error {
	defer func() {
		if r := recover(); r != nil {
			log.Writef("[RDP] PANIC in Connect: %v", r)
			s.setStatus(StatusError)
		}
	}()

	// Phase 1: quick state init (brief lock)
	s.mu.Lock()
	s.config = config
	s.title = fmt.Sprintf("%s@%s (RDP)", config.User, config.Host)
	s.setStatus(StatusConnecting)
	s.mu.Unlock()

	runtime.LockOSThread() // pin COM STA to a dedicated OS thread
	ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	defer func() {
		// Properly disconnect the RDP ActiveX control first.
		// This closes network sockets and stops internal threads —
		// skipping it leaks resources that cause progressive lag.
		s.mu.Lock()
		rdp := s.rdp
		s.rdp = nil
		s.mu.Unlock()
		if rdp != nil {
			rdp.CallMethod("Disconnect")
			rdp.Release()
		}

		s.mu.Lock()
		hwnd := s.hwnd
		s.hwnd = 0
		s.mu.Unlock()
		if hwnd != 0 {
			// Hide first to avoid visual flash during destruction
			procSetWindowPos.Call(hwnd, 0, 32000, 32000, 0, 0,
				SWP_NOSIZE|SWP_NOACTIVATE|SWP_NOZORDER|SWP_ASYNCWINDOWPOS)
			procDestroyWindow.Call(hwnd)
		}

		ole.CoUninitialize()
		runtime.UnlockOSThread()
	}()

	if s.parentHwnd == 0 {
		title, _ := windows.UTF16PtrFromString("uniTerm")
		hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
		if hwnd == 0 {
			log.Writef("[RDP] ERROR: cannot find main window")
			s.setStatus(StatusError)
			return fmt.Errorf("cannot find main window")
		}
		s.parentHwnd = hwnd
	}

	ret, _, _ := procAtlAxWinInit.Call()
	if ret == 0 {
		log.Writef("[RDP] ERROR: AtlAxWinInit failed")
		s.setStatus(StatusError)
		return fmt.Errorf("AtlAxWinInit failed")
	}

	width := config.RdpFixedWidth
	height := config.RdpFixedHeight
	// Sentinel -1 means "follow the display": use the primary monitor's
	// physical resolution as the remote desktop size. (issue: fullscreen option)
	if width == -1 || height == -1 {
		sw, _, _ := procGetSystemMetrics.Call(uintptr(SM_CXSCREEN))
		sh, _, _ := procGetSystemMetrics.Call(uintptr(SM_CYSCREEN))
		width = int(sw)
		height = int(sh)
	}
	if width <= 0 {
		width = 800
	}
	if height <= 0 {
		height = 600
	}

	// Adaptive resolution: seed the desktop with the current .rdp-area estimate
	// so the initial session roughly matches the window; the frontend position
	// sync keeps refining it from there (see requestAdaptiveResize).
	if config.RdpFixedWidth == rdpSizeAdaptive {
		if w, h, ok := s.overlaySizeEstimate(); ok {
			width, height = w, h
		}
	}

	s.mu.Lock()
	s.desktopW, s.desktopH = width, height
	s.mu.Unlock()
	log.Writef("[RDP] config: adaptive=%v fixed=%dx%d initial=%dx%d", config.RdpFixedWidth == rdpSizeAdaptive, config.RdpFixedWidth, config.RdpFixedHeight, width, height)

	dispatch, hwnd, err := hostRdpControl(s.parentHwnd, width, height)
	if err != nil {
		log.Writef("[RDP] ERROR: %v", err)
		s.setStatus(StatusError)
		return err
	}

	s.mu.Lock()
	s.hwnd = hwnd
	s.rdp = dispatch
	s.deskW = width
	s.deskH = height
	s.mu.Unlock()

	port := config.Port
	if port <= 0 {
		port = 3389
	}

	// NonScriptable.ClearTextPassword BEFORE AdvancedSettings.
	s.configureNonScriptable(config.Password)

	dispatch.PutProperty("Server", config.Host)
	// The explicit RdpDomain field wins. As a fallback, the username may
	// carry a Windows login name of the form "DOMAIN\user" (or
	// "MACHINE\user" for a local account) — the same syntax mstsc accepts in
	// its username box — in which case the part before the backslash reaches
	// the ActiveX control's Domain property; a plain name or a UPN
	// ("user@domain") is passed through untouched.
	domain, user := config.RdpDomain, config.User
	if domain == "" {
		domain, user = splitDomainUser(config.User)
	}
	dispatch.PutProperty("UserName", user)
	dispatch.PutProperty("Domain", domain)
	dispatch.PutProperty("DesktopWidth", width)
	dispatch.PutProperty("DesktopHeight", height)
	dispatch.PutProperty("FullScreen", false)

	// AdvancedSettings2
	advObj, _ := dispatch.GetProperty("AdvancedSettings2")
	if advObj != nil {
		adv := advObj.ToIDispatch()
		if adv != nil {
			adv.PutProperty("RDPPort", port)
			adv.PutProperty("RedirectClipboard", true)
			adv.PutProperty("RedirectDrives", true)
			// Let the ActiveX control handle full screen
			// itself and show its built-in connection bar (which carries the
			// restore/exit button). Requires ContainerHandledFullScreen=false.
			adv.PutProperty("DisplayConnectionBar", true)
			adv.PutProperty("EnableAutoReconnect", true)
			if config.RdpEnableNLA {
				adv.PutProperty("EnableCredSspSupport", true)
				adv.PutProperty("AuthenticationLevel", 2)
			} else {
				adv.PutProperty("EnableCredSspSupport", false)
				adv.PutProperty("AuthenticationLevel", 0)
			}
			adv.PutProperty("WarnOnDirectConnect", false)
			// false = ActiveX manages full screen itself
			// (renders its own connection bar with an exit button), instead of
			// delegating to the host container.
			adv.PutProperty("ContainerHandledFullScreen", false)
			if config.RdpSmartSizing {
				adv.PutProperty("SmartSizing", true)
			}
			if config.Password != "" {
				adv.PutProperty("ClearTextPassword", config.Password)
			}
			adv.Release()
		}
	}

	// Route Windows key and special key combos (Win+E, Win+D, Alt+Tab, etc.)
	// to the remote machine instead of the local one. KeyboardHookMode=1 means
	// "apply key combinations at the remote server". (issue #351)
	// Note: Ctrl+Alt+Del is a Secure Attention Sequence and can never be
	// forwarded by any RDP client; use Ctrl+Alt+End instead.
	secObj, _ := dispatch.GetProperty("SecuredSettings2")
	if secObj != nil {
		sec := secObj.ToIDispatch()
		if sec != nil {
			sec.PutProperty("KeyboardHookMode", 1)
			sec.Release()
		}
	}

	// Suppress security prompts on all available AdvancedSettings versions
	for _, ver := range []int{9, 8, 7, 6, 5, 4, 3} {
		propName := fmt.Sprintf("AdvancedSettings%d", ver)
		advHigh, _ := dispatch.GetProperty(propName)
		if advHigh != nil {
			a := advHigh.ToIDispatch()
			if a != nil {
				a.PutProperty("ContainerHandledFullScreen", false)
				a.PutProperty("WarnOnDirectConnect", false)
				// Hide the minimize button on the full-screen connection bar.
				// A minimized RDP window has no taskbar entry to restore from, so it
				// would become unrecoverable. Supported in AdvancedSettings6+.
				a.PutProperty("ConnectionBarShowMinimizeButton", false)
				// ConnectToAdministerServer (AdvancedSettings8+) is the ActiveX
				// equivalent of "mstsc /admin": attach to the remote console
				// (admin) session instead of a fresh virtual session.
				if config.RdpAdminSession {
					a.PutProperty("ConnectToAdministerServer", true)
				}
				a.Release()
			}
		}
	}

	// Suppress server certificate warning at OS level
	setAuthLevelOverride()

	// Auto-dismiss any security dialogs that appear during Connect (e.g.
	// "网站正在尝试启动远程连接"). The goroutine polls for dialog windows
	// and clicks "Yes" to dismiss them.
	// On Windows 11, the dialog may appear after Connect succeeds, so keep
	// polling for a few seconds after connection.
	stopDismiss := make(chan struct{})
	go s.autoDismissSecurityDialogs(stopDismiss)
	defer func() {
		// Keep polling for dialogs after Connect completes. On Windows 11
		// the security warning may appear slightly after connection.
		go func() {
			time.Sleep(5 * time.Second)
			close(stopDismiss)
		}()
	}()

	_, err = dispatch.CallMethod("Connect")
	if err != nil {
		log.Writef("[RDP] Connect failed: %v", err)
		s.mu.Lock()
		s.hwnd = 0
		s.rdp = nil
		s.mu.Unlock()
		dispatch.Release()
		procDestroyWindow.Call(hwnd)
		s.setStatus(StatusError)
		return fmt.Errorf("RDP Connect: %w", err)
	}

	// Immediate show-and-position to avoid white/black screen.
	// Frontend will refine via RDPSetPosition shortly after.
	s.positionFromMainWindow(width, height)

	// Keep WS_EX_NOACTIVATE permanently: the ActiveX control and its child
	// windows can steal foreground during/after Connect, pushing uniTerm
	// behind other windows. The flag prevents this. (issue #385, #470)

	s.setStatus(StatusConnected)

	s.runMessagePump()

	return nil
}

type rect struct {
	Left, Top, Right, Bottom int32
}

// bitmapInfoHeader is the BITMAPINFOHEADER layout for GetDIBits (32bpp BGRA,
// top-down when biHeight is negative).
type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type msg struct {
	HWND    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

func (s *RDPSession) runMessagePump() {
	var m msg
	noMsgCount := 0
	disconnectLogged := false
	// Display watchdog state (this thread only): samples the presented frame
	// and escalates resync kicks when it stops updating while the user is
	// interacting with the control — the "black screen after a long session,
	// clicks still work" failure. See rdp_watchdog.go for the decision logic.
	wd := newDisplayWatchdog()
	lastSampleAt := time.Time{}
	lastRenegotiated := time.Time{}
	renegRestoreAt := time.Time{}
	for {
		s.mu.Lock()
		done := s.hwnd == 0
		s.mu.Unlock()
		if done {
			break
		}

		now := time.Now()

		// Restore the remote desktop size after a renegotiation nudge. Must run
		// on THIS (COM STA) thread, like every other control call.
		if !renegRestoreAt.IsZero() && !now.Before(renegRestoreAt) {
			renegRestoreAt = time.Time{}
			s.restoreDesktopSize()
		}

		// Manual refresh (frontend button): the user asked for a full resync —
		// the COM half has to run here (see fsRequested).
		s.mu.Lock()
		resync := s.resyncRequested
		s.resyncRequested = false
		s.mu.Unlock()
		if resync {
			lastRenegotiated = now
			if s.renegotiateDesktop() {
				renegRestoreAt = now.Add(2 * time.Second)
			}
		}

		// Display watchdog: sample the presented frame at a fixed cadence and
		// run the resync ladder when it stalls.
		if now.Sub(lastSampleAt) >= displaySampleInterval {
			lastSampleAt = now
			s.mu.Lock()
			visible := s.shown
			fullScreen := s.fsActive
			s.mu.Unlock()
			if visible {
				if hash, dark, ok := s.sampleFrame(); ok {
					kick := wd.tick(now, hash, dark)
					// Full screen is the control's own top-level-like mode: only
					// the neutral repaint kick applies there.
					if fullScreen && kick != displayKickNone {
						kick = displayKickRepaint
					}
					switch kick {
					case displayKickRepaint:
						log.Writef("[RDP] display watchdog: frame stalled, forcing repaint")
						s.Invalidate()
					case displayKickRelayout:
						log.Writef("[RDP] display watchdog: frame still stalled, relayout + display-change replay")
						s.relayout()
					case displayKickRenegotiate:
						if now.Sub(lastRenegotiated) >= displayRenegotiateCooldown {
							lastRenegotiated = now
							log.Writef("[RDP] display watchdog: frame still stalled, renegotiating desktop size")
							if s.renegotiateDesktop() {
								renegRestoreAt = now.Add(2 * time.Second)
							}
						}
					}
				}
			}
		}

		// Apply any pending full-screen toggle on THIS (COM STA) thread.
		s.mu.Lock()
		if s.fsRequested {
			s.fsRequested = false
			full := s.fsValue
			rdp := s.rdp
			// Adaptive mode: entering full screen needs the desktop at the
			// monitor resolution FIRST — toggle FullScreen only after the
			// resize cycle completes (fsAfterResize), otherwise the desktop
			// stays at the windowed size and merely gets stretched.
			deferred := false
			if full && rdp != nil && s.config.RdpFixedWidth == rdpSizeAdaptive && time.Now().After(s.rsCooldownUntil) && s.rsPhase == 0 {
				sw, _, _ := procGetSystemMetrics.Call(uintptr(SM_CXSCREEN))
				sh, _, _ := procGetSystemMetrics.Call(uintptr(SM_CYSCREEN))
				mw, mh := clampDesktopWidth(int(sw)), clampDesktopHeight(int(sh))
				if mw != s.desktopW || mh != s.desktopH {
					deferred = true
					s.fsAfterResize = true
					s.rsW, s.rsH = mw, mh
					s.rsAt = time.Now()
					s.rsReq = true
				}
			}
			if !deferred {
				s.fsActive = full
			}
			if !full {
				// Exiting full screen: cancel any pending deferred enter.
				s.fsAfterResize = false
			}
			s.mu.Unlock()
			if rdp != nil && !deferred {
				rdp.PutProperty("FullScreen", full)
			}
		} else {
			s.mu.Unlock()
		}

		// Apply a settled adaptive desktop resize on THIS (COM STA) thread.
		// applyDesktopResize keeps rsReq set when rate-limited so the request
		// is retried on a later pump iteration.
		s.mu.Lock()
		rsReq := s.rsReq
		rsW, rsH, rsAt := s.rsW, s.rsH, s.rsAt
		s.mu.Unlock()
		if rsReq && time.Since(rsAt) >= adaptiveResizeDelay {
			s.applyDesktopResize(rsW, rsH)
		}

		// Drive an in-flight adaptive resize (disconnect → reconnect phases).
		s.mu.Lock()
		rsPhase := s.rsPhase
		s.mu.Unlock()
		if rsPhase != 0 {
			s.stepAdaptiveResize()
		}

		ret, _, _ := procPeekMessage.Call(
			uintptr(unsafe.Pointer(&m)),
			0, 0, 0,
			PM_REMOVE,
		)
		if ret != 0 {
			if m.Message == 0x0012 { // WM_QUIT
				return
			}
			// The watchdog only acts on stalls the user is waiting on, so track
			// input aimed at the RDP window as it flows through this pump.
			if isInputMessage(m.Message) {
				wd.observeInput(time.Now())
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
			noMsgCount = 0
		} else {
			// No message available; sleep briefly to avoid busy-wait.
			// Check hwnd every ~1 second via heartbeat counter.
			time.Sleep(50 * time.Millisecond)
			noMsgCount++

			// Detect full-screen exit via the built-in connection bar: the
			// control flips FullScreen back to false. Poll on THIS COM thread
			// every ~250ms while full screen is active.
			if noMsgCount%5 == 0 {
				s.mu.Lock()
				active := s.fsActive
				rdp := s.rdp
				s.mu.Unlock()
				if active && rdp != nil {
					fs, err := rdp.GetProperty("FullScreen")
					if err == nil && fs != nil {
						stillFull := false
						if b, ok := fs.Value().(bool); ok {
							stillFull = b
						}
						if !stillFull {
							s.mu.Lock()
							s.fsActive = false
							// Reset the oscillation baseline: the windowed-size
							// resize the frontend requests after exiting full
							// screen is a legitimate cycle, not a loop.
							s.prevDesktopW, s.prevDesktopH = 0, 0
							cb := s.onFsExit
							s.mu.Unlock()
							if cb != nil {
								cb()
							}
						}
					}
				}
			}

			if noMsgCount%20 == 0 {
				// Check if RDP connection is still alive via ActiveX Connected property.
				// When the remote side drops or the connection is lost, this transitions
				// to 0 while the ActiveX window is still alive.
				if !disconnectLogged {
					// Skip liveness polling during the adaptive-reconnect grace
					// window: Connected transiently reads 0 mid-renegotiation.
					s.mu.Lock()
					grace := time.Now().Before(s.rsGraceUntil)
					s.mu.Unlock()
					rdp := (*ole.IDispatch)(nil)
					if !grace {
						s.mu.Lock()
						rdp = s.rdp
						s.mu.Unlock()
					}
					if rdp != nil {
						connected, err := rdp.GetProperty("Connected")
						if err == nil && connected != nil {
							v := connected.Value()
							isDisconnected := false
							if b, ok := v.(bool); ok {
								isDisconnected = !b
							} else if v == nil || v == int16(0) || v == int32(0) || v == 0 {
								isDisconnected = true
							}
							if isDisconnected {
								discMsg := "RDP connection was lost"
								// Try to get the disconnect reason
								reason, reasonErr := rdp.GetProperty("DisconnectedReason")
								if reasonErr == nil && reason != nil {
									discMsg = fmt.Sprintf("RDP disconnected: %v", reason.Value())
								}
								log.Writef("[RDP-pump] connection lost: %s, signaling disconnected", discMsg)
								disconnectLogged = true
								s.setStatus(StatusDisconnected)
								// Post WM_QUIT to exit the pump and trigger cleanup
								s.mu.Lock()
								if s.hwnd != 0 {
									procPostMessageW.Call(s.hwnd, 0x0012, 0, 0)
								}
								s.mu.Unlock()
							}
						}
					}
				}
			}
		}
	}
}

// rdpControlCandidates lists the RDP ActiveX control identifiers to try,
// preferred first. The "NotSafeForScripting" ProgIDs are what the Remote
// Desktop client registers on stock Windows; the versioned MsRDP./MsTscAx.
// ProgIDs cover machines where only those are registered; literal CLSIDs are a
// last resort for the redistributable control.
func rdpControlCandidates() []string {
	ids := []string{
		"MsRdpClient13NotSafeForScripting",
		"MsRdpClient12NotSafeForScripting",
		"MsRdpClient11NotSafeForScripting",
		"MsRdpClient10NotSafeForScripting",
		"MsRdpClient9NotSafeForScripting",
		"MsRdpClient8NotSafeForScripting",
		"MsTscAxNotSafeForScripting",
		"MsTscAx",
	}
	for v := 13; v >= 2; v-- {
		ids = append(ids, fmt.Sprintf("MsRDP.MsRDP.%d", v), fmt.Sprintf("MsTscAx.MsTscAx.%d", v))
	}
	return append(ids,
		"{9059F30F-4EB1-4BD2-9FDC-36F43A218F4A}",
		"{54D38BF7-B1EF-4479-9674-1BD6EA465258}",
		"{C0EFA91A-EEB7-41C7-97FA-F0ED645EFB24}",
		"{301B94BA-5F25-4A12-9FFE-3B274E75C7DE}",
		"{5F681803-2900-4C43-A1CC-CF405404A676}",
		"{1FB464C8-09BB-4017-A2F5-EB742F04392F}",
	)
}

// hostRdpControl creates the RDP ActiveX container: a WS_CHILD AtlAxWin window
// at 32000,32000 (parent-client-relative, i.e. outside the client area =
// hidden; the actual show/position is done by positionFromMainWindow once
// Connect succeeds) hosting the first RDP control this machine can actually
// instantiate. A child window:
//   - follows the parent automatically when it moves (owned top-level windows do not),
//   - is clipped to the parent's client area (so it can never cover the header/tabs),
//   - cannot become the foreground window (so cannot push uniTerm behind other
//     windows during/after connect).
//
// The RDP overlay is driven purely by sibling z-order: HWND_TOP to show it above
// the webview, HWND_BOTTOM to tuck it under the webview while an HTML menu/dialog
// is open — leaving the ActiveX rendering surface untouched, so no black screen
// on restore. WS_EX_NOACTIVATE keeps the ActiveX + its child dialogs from
// stealing foreground/focus during Connect.
//
// Each candidate is tried until one yields a control that answers the RDP
// automation API (how Connect drives it). Class-name resolution alone is not
// enough: CLSIDFromString parses any well-formed GUID even when the class is
// unregistered, and a registered class can still fail to instantiate inside the
// AtlAxWin WM_CREATE handler — which used to surface as a bare
// "CreateWindowExW failed" with no indication of which control or why.
func hostRdpControl(parent uintptr, width, height int) (*ole.IDispatch, uintptr, error) {
	className, _ := windows.UTF16PtrFromString("AtlAxWin")
	var failures []string
	for _, id := range rdpControlCandidates() {
		name, _ := windows.UTF16PtrFromString(id)
		hwnd, _, callErr := procCreateWindowExW.Call(
			uintptr(WS_EX_NOACTIVATE),
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(name)),
			uintptr(WS_CHILD|WS_CLIPSIBLINGS),
			32000, 32000, // child-relative; outside the client area = hidden
			uintptr(width), uintptr(height),
			uintptr(parent), // parent window handle
			0,               // menu
			0,               // hInstance
			0,               // lParam
		)
		if hwnd == 0 {
			failures = append(failures, fmt.Sprintf("%s: CreateWindowExW: %s", id, win32Error(callErr)))
			continue
		}
		// No GWLP_HWNDPARENT owner call needed — the window is a genuine child now.

		var unk *ole.IUnknown
		procAtlAxGetControl.Call(hwnd, uintptr(unsafe.Pointer(&unk)))
		if unk == nil {
			procDestroyWindow.Call(hwnd)
			failures = append(failures, id+": AtlAxGetControl returned no control")
			continue
		}
		dispatch, err := unk.QueryInterface(ole.IID_IDispatch)
		unk.Release()
		if err != nil {
			procDestroyWindow.Call(hwnd)
			failures = append(failures, fmt.Sprintf("%s: control has no IDispatch: %v", id, err))
			continue
		}
		// Connect drives the control by name ("Server", "Connect", "Connected",
		// ...). An object that cannot resolve them is not a usable RDP control
		// even if it hosts and exposes IDispatch.
		if _, err := dispatch.GetProperty("Server"); err != nil {
			dispatch.Release()
			procDestroyWindow.Call(hwnd)
			failures = append(failures, fmt.Sprintf("%s: hosted object has no RDP API: %v", id, err))
			continue
		}
		log.Writef("[RDP] hosting ActiveX control %s", id)
		return dispatch, hwnd, nil
	}
	return nil, 0, fmt.Errorf("no usable RDP ActiveX control (is mstscax.dll installed?): %s", strings.Join(failures, " | "))
}

// win32Error renders a LazyProc.Call lastErr with its numeric code; the bare
// message is not diagnostic (it reads "The operation completed successfully"
// whenever the value is stale).
func win32Error(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("win32 error %d (%v)", errno, errno)
	}
	return fmt.Sprintf("%v", err)
}

// setAuthLevelOverride sets the system-wide RDP authentication level to 0,
// which suppresses the server certificate warning dialog.
func setAuthLevelOverride() {
	// AuthenticationLevelOverride = 0 disables server cert verification.
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Terminal Server Client`,
		registry.SET_VALUE)
	if err != nil {
		k, _, err = registry.CreateKey(registry.CURRENT_USER,
			`Software\Microsoft\Terminal Server Client`,
			registry.SET_VALUE)
		if err != nil {
			return
		}
	}
	defer k.Close()
	k.SetDWordValue("AuthenticationLevelOverride", 0)
	// Also disable the redirection warning dialog via the non-policy key
	k.SetDWordValue("ShowRedirectionWarningDialog", 0)

	// RedirectionWarningDialogVersion = 1 suppresses the "unknown remote
	// connection" security warning dialog. Check if already set first.
	if isRDWAlreadySet() {
		return
	}

	// Write to HKCU policy path (no elevation needed, works on Windows 11)
	rdwPath := `Software\Policies\Microsoft\Windows NT\Terminal Services\Client`
	if writeRegDWORD(registry.CURRENT_USER, rdwPath, "RedirectionWarningDialogVersion", 1) {
		return
	}

	// Fallback: try HKLM policy path (requires admin)
	rdwPathLM := `SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services\Client`
	if writeRegDWORD(registry.LOCAL_MACHINE, rdwPathLM, "RedirectionWarningDialogVersion", 1) {
		return
	}
	elevateRegWrite()
}

// isRDWAlreadySet returns true if RedirectionWarningDialogVersion is already 1.
func isRDWAlreadySet() bool {
	paths := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services\Client`},
		{registry.CURRENT_USER, `Software\Policies\Microsoft\Windows NT\Terminal Services\Client`},
	}
	for _, p := range paths {
		if readRegDWORD(p.root, p.path, "RedirectionWarningDialogVersion") == 1 {
			return true
		}
	}
	return false
}

// writeRegDWORD writes a DWORD value to the registry. Returns true on success.
func writeRegDWORD(root registry.Key, path, name string, value uint32) bool {
	k, err := registry.OpenKey(root, path, registry.SET_VALUE)
	if err != nil {
		k, _, err = registry.CreateKey(root, path, registry.SET_VALUE)
		if err != nil {
			return false
		}
	}
	defer k.Close()
	return k.SetDWordValue(name, value) == nil
}

// readRegDWORD reads a DWORD value from the registry. Returns 0 if not found.
func readRegDWORD(root registry.Key, path, name string) uint32 {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return 0
	}
	defer k.Close()
	val, _, err := k.GetIntegerValue(name)
	if err != nil {
		return 0
	}
	return uint32(val)
}

// elevateRegWrite launches reg.exe with the "runas" verb to write the
// RedirectionWarningDialogVersion machine-policy key with admin rights.
func elevateRegWrite() {
	shell32 := windows.NewLazySystemDLL("shell32.dll")
	procShellExecute := shell32.NewProc("ShellExecuteW")

	op, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString("reg.exe")
	params, _ := windows.UTF16PtrFromString(
		`add "HKLM\SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services\Client" /v RedirectionWarningDialogVersion /t REG_DWORD /d 1 /f`,
	)

	ret, _, _ := procShellExecute.Call(
		0,
		uintptr(unsafe.Pointer(op)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0,
		0, // SW_HIDE
	)
	if ret <= 32 {
		log.Writef("[RDP] ShellExecute runas failed: %d", ret)
	}
}

func (s *RDPSession) configureNonScriptable(password string) {
	if s.rdp == nil {
		return
	}
	nsGUIDs := []string{
		"{C1E6743A-41C1-4A74-832A-0DD06C0C9265}", // IMsTscNonScriptable (base)
		"{4F5331FB-42F5-48A2-9AFD-4743E3F6D3D7}", // IMsRdpClientNonScriptable5
		"{F50FA8AA-1C05-471B-9CB5-3BD7A6FD32BD}", // IMsRdpClientNonScriptable4
		"{B3378D90-0728-45C7-8ED7-B6159FB92219}", // IMsRdpClientNonScriptable3
	}
	unk, err := s.rdp.QueryInterface(ole.IID_IUnknown)
	if err != nil {
		log.Writef("[RDP] QI IUnknown for NonScriptable: %v", err)
		return
	}
	defer unk.Release()
	for _, guid := range nsGUIDs {
		nsGUID := ole.NewGUID(guid)
		nsUnk, err := unk.QueryInterface(nsGUID)
		if err != nil || nsUnk == nil {
			continue
		}
		if password != "" {
			nsUnk.PutProperty("ClearTextPassword", password)
		}
		nsUnk.Release()
		break
	}
}

type point struct{ X, Y int32 }

// splitDomainUser splits a Windows login name of the form "DOMAIN\user"
// (or "MACHINE\user" for a local account) into its domain and user parts.
// The backslash form is what mstsc accepts in its username box, so the same
// syntax is honored here. A name without a backslash — including a UPN such
// as "user@domain" — is returned unchanged with an empty domain.
// Windows limits the domain/computer part to 15 chars, but the split is
// kept lenient: the whole string is treated as the user when no backslash
// is present, and the last backslash wins when several appear (defensive).
func splitDomainUser(user string) (domain, name string) {
	if user == "" {
		return "", ""
	}
	if i := strings.LastIndex(user, `\`); i >= 0 {
		return user[:i], user[i+1:]
	}
	return "", user
}

// positionFromMainWindow calculates the RDP window position and initializes tracking.
func (s *RDPSession) positionFromMainWindow(width, height int) {
	if s.parentHwnd == 0 || s.hwnd == 0 {
		return
	}
	var cr rect
	ret, _, _ := procGetClientRect.Call(s.parentHwnd, uintptr(unsafe.Pointer(&cr)))
	if ret == 0 {
		log.Writef("[RDP] GetClientRect failed, fallback to GetWindowRect")
		var wr rect
		ret2, _, _ := procGetWindowRect.Call(s.parentHwnd, uintptr(unsafe.Pointer(&wr)))
		if ret2 == 0 {
			log.Writef("[RDP] GetWindowRect also failed")
			return
		}
		cr = rect{0, 0, wr.Right - wr.Left, wr.Bottom - wr.Top}
	}
	clientWidth := int(cr.Right - cr.Left)
	clientHeight := int(cr.Bottom - cr.Top)

	// trackX/trackY are PARENT-CLIENT-RELATIVE offsets into the main window. As
	// a WS_CHILD, the RDP window is positioned in the parent's client coordinate
	// space, so these are used directly by placeAtChild — no screen conversion
	// or owner-origin addition is needed, and the window follows the parent
	// automatically when it moves.
	x := rdpSideMargin
	y := rdpTopReserve
	w := clientWidth - rdpSideMargin*2
	h := clientHeight - rdpTopReserve - rdpBottomReserve

	s.shown = true
	s.placeAtChild(x, y, w, h, SWP_SHOWWINDOW|SWP_NOACTIVATE|SWP_ASYNCWINDOWPOS)

	s.trackX = x
	s.trackY = y
	s.trackW = w
	s.trackH = h
}

// overlaySizeEstimate estimates the .rdp-area size (physical pixels) the same
// way positionFromMainWindow computes the initial child window placement: the
// parent client area minus the layout reserves. Returns ok=false when the
// parent window is not available yet.
func (s *RDPSession) overlaySizeEstimate() (w, h int, ok bool) {
	if s.parentHwnd == 0 {
		return 0, 0, false
	}
	var cr rect
	if ret, _, _ := procGetClientRect.Call(s.parentHwnd, uintptr(unsafe.Pointer(&cr))); ret == 0 {
		return 0, 0, false
	}
	cw := int(cr.Right-cr.Left) - rdpSideMargin*2
	ch := int(cr.Bottom-cr.Top) - rdpTopReserve - rdpBottomReserve
	if cw < adaptiveMinWidth || ch < adaptiveMinHeight {
		// Degenerate layout (e.g. connect while minimized): fall back to the
		// monitor-based default chosen by the caller.
		return 0, 0, false
	}
	return cw, ch, true
}

// adaptiveMode reports whether the session was configured with the adaptive
// resolution option (rdpFixedWidth/Height == rdpSizeAdaptive).
func (s *RDPSession) adaptiveMode() bool {
	return s.config.RdpFixedWidth == rdpSizeAdaptive
}

// requestAdaptiveResize records a pending desktop-size request from a
// SetPosition sync. The message pump applies it (see applyDesktopResize) once
// the size has stayed stable for adaptiveResizeDelay.
func (s *RDPSession) requestAdaptiveResize(w, h int) {
	w = clampDesktopWidth(w)
	h = clampDesktopHeight(h)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.adaptiveMode() || time.Now().Before(s.rsCooldownUntil) {
		return
	}
	if w == s.desktopW && h == s.desktopH {
		// Back at the current desktop size: drop any stale pending request.
		s.rsReq = false
		return
	}
	// Ignore sub-threshold changes: a reconnect is not worth a few pixels.
	dw := w - s.desktopW
	if dw < 0 {
		dw = -dw
	}
	dh := h - s.desktopH
	if dh < 0 {
		dh = -dh
	}
	if dw < adaptiveResizeThreshold && dh < adaptiveResizeThreshold {
		// Same reasoning as above: the window converged back near the current
		// desktop size, so a stale pending request must not fire later.
		s.rsReq = false
		return
	}
	// Oscillation guard: requesting the size that was in effect BEFORE the last
	// reconnect (A→B→A) almost immediately after that reconnect means the resize
	// feedback is fighting itself (e.g. the control or layout reacting to the
	// reconnect). Pause adaptive resize instead of reconnect-storming the server
	// into dropping the session; the cooldown self-heals, a real loop keeps
	// re-triggering it.
	if !s.rsAppliedAt.IsZero() && time.Since(s.rsAppliedAt) < adaptiveLoopWindow &&
		w == s.prevDesktopW && h == s.prevDesktopH {
		s.rsCooldownUntil = time.Now().Add(adaptiveCooldown)
		s.rsReq = false
		log.Writef("[RDP] adaptive resize: oscillation detected (%dx%d↔%dx%d), pausing for %s", w, h, s.desktopW, s.desktopH, adaptiveCooldown)
		return
	}
	// Only restart the stability timer when the pending size actually changed;
	// repeated syncs with the same size must not postpone the reconnect.
	if w != s.rsW || h != s.rsH {
		s.rsW, s.rsH = w, h
		s.rsAt = time.Now()
		log.Writef("[RDP] adaptive resize requested: %dx%d (current %dx%d)", w, h, s.desktopW, s.desktopH)
	}
	s.rsReq = true
}

// clampDesktopWidth/Height normalize a requested desktop dimension: even
// number (protocol requirement), clamped to the adaptive size limits.
func clampDesktopWidth(w int) int {
	if w < adaptiveMinWidth {
		return adaptiveMinWidth
	}
	if w > adaptiveMaxWidth {
		return adaptiveMaxWidth
	}
	return w &^ 1
}

func clampDesktopHeight(h int) int {
	if h < adaptiveMinHeight {
		return adaptiveMinHeight
	}
	if h > adaptiveMaxHeight {
		return adaptiveMaxHeight
	}
	return h &^ 1
}

// applyDesktopResize starts the adaptive resize: a control-level Disconnect
// followed by a Connect with the new desktop dimensions — the same sequence
// mstsc runs when the desktop size changes. IMsRdpClient8::Reconnect was
// tried first, but on a live session its renegotiation never completes
// (Connected stays 0, screen stays blank), so the explicit cycle is used
// instead. The server keeps the session either way, so running programs are
// unaffected. The pump drives the phases (see stepAdaptiveResize); must run
// on the COM STA thread.
func (s *RDPSession) applyDesktopResize(w, h int) {
	// Even dimensions only (protocol requirement).
	w = clampDesktopWidth(w)
	h = clampDesktopHeight(h)
	s.mu.Lock()
	if w == s.desktopW && h == s.desktopH {
		// Already applied (e.g. a duplicate pending request): consume it.
		s.rsReq = false
		s.mu.Unlock()
		return
	}
	if time.Now().Before(s.rsCooldownUntil) {
		// Suspended (suspected feedback loop): discard stale pending requests;
		// the next SetPosition sync re-requests after the cooldown lifts.
		s.rsReq = false
		s.mu.Unlock()
		return
	}
	// Rate limit: at most one reconnect per adaptiveMinInterval. Keep the
	// request pending so the pump retries after the interval.
	if !s.rsAppliedAt.IsZero() && time.Since(s.rsAppliedAt) < adaptiveMinInterval {
		s.mu.Unlock()
		return
	}
	rdp := s.rdp
	full := s.fsActive
	phase := s.rsPhase
	s.mu.Unlock()
	if rdp == nil || full || phase != 0 {
		// A resize cycle is already in flight (or no control / full screen):
		// leave the request pending for a later pump iteration.
		return
	}
	s.mu.Lock()
	s.rsReq = false
	s.rsPhase = 1
	s.rsDeadline = time.Now().Add(adaptivePhase1Timeout)
	s.rsGraceUntil = time.Now().Add(adaptiveReconnectGrace)
	s.mu.Unlock()
	log.Writef("[RDP] adaptive resize: disconnecting to resize desktop %dx%d → %dx%d", s.desktopW, s.desktopH, w, h)

	// Hand the frontend a frozen frame of the current desktop so the resize
	// cycle shows a still image over .rdp-area instead of a blank/logon flash.
	// Captured here on the pump thread right before the disconnect — the last
	// good frame.
	snap, _ := s.Snapshot()
	s.emitAdaptive("start", snap)
	// Give the frontend a moment to decode and layer the frozen frame over
	// .rdp-area before the live window goes blank underneath.
	time.Sleep(250 * time.Millisecond)

	rdp.CallMethod("Disconnect")
}

// stepAdaptiveResize advances the adaptive resize state machine. Called on
// the COM STA thread (message pump) once per pump iteration while a resize
// cycle is in flight; never blocks, so window messages keep being pumped.
func (s *RDPSession) stepAdaptiveResize() {
	s.mu.Lock()
	phase := s.rsPhase
	deadline := s.rsDeadline
	w, h := s.rsW, s.rsH
	rdp := s.rdp
	s.mu.Unlock()
	if rdp == nil {
		s.mu.Lock()
		s.rsPhase = 0
		s.mu.Unlock()
		return
	}
	if time.Now().After(deadline) {
		log.Writef("[RDP] adaptive resize: phase %d timed out, aborting", phase)
		s.mu.Lock()
		s.rsPhase = 0
		s.rsAppliedAt = time.Now() // rate-limit the next attempt
		s.fsAfterResize = false    // a deferred full-screen enter is lost
		s.mu.Unlock()
		s.emitAdaptive("end", "")
		return
	}

	connected, err := rdp.GetProperty("Connected")
	if err != nil || connected == nil {
		return // transient; retry on the next pump iteration
	}
	isUp := !connectedPropZero(connected.Value())

	if phase == 1 && !isUp {
		// Control is fully disconnected: apply the new desktop size and
		// reconnect (the server keeps the existing session).
		rdp.PutProperty("DesktopWidth", w)
		rdp.PutProperty("DesktopHeight", h)
		if _, err := rdp.CallMethod("Connect"); err != nil {
			log.Writef("[RDP] adaptive resize: Connect(%d,%d) failed: %v", w, h, err)
			s.mu.Lock()
			s.rsPhase = 0
			s.rsAppliedAt = time.Now()
			s.fsAfterResize = false
			s.mu.Unlock()
			s.emitAdaptive("end", "")
			return
		}
		s.mu.Lock()
		s.rsPhase = 2
		s.rsDeadline = time.Now().Add(adaptivePhase2Timeout)
		s.rsGraceUntil = s.rsDeadline
		s.mu.Unlock()
		log.Writef("[RDP] adaptive resize: reconnecting at %dx%d", w, h)
		return
	}

	if phase == 2 && !isUp {
		// Dropped again during the settle window: restart the settle timer.
		s.mu.Lock()
		s.rsUpSince = time.Time{}
		s.mu.Unlock()
		return
	}

	if phase == 2 && isUp {
		// The Connected property reads 1 immediately after Connect() — wait
		// for it to hold steadily before treating the session as established
		// (a deferred full-screen toggle fires on this success path).
		s.mu.Lock()
		if s.rsUpSince.IsZero() {
			s.rsUpSince = time.Now()
			s.mu.Unlock()
			return
		}
		settled := time.Since(s.rsUpSince) >= adaptivePhase2Settle
		s.mu.Unlock()
		if !settled {
			return
		}

		s.mu.Lock()
		s.rsPhase = 0
		s.rsUpSince = time.Time{}
		s.prevDesktopW, s.prevDesktopH = s.desktopW, s.desktopH
		s.desktopW, s.desktopH = w, h
		s.rsAppliedAt = time.Now()
		enterFs := s.fsAfterResize
		s.fsAfterResize = false
		x, y, tw, th := s.trackX, s.trackY, s.trackW, s.trackH
		s.mu.Unlock()
		log.Writef("[RDP] adaptive resize: desktop now %dx%d", w, h)
		s.emitAdaptive("end", "")

		// The control may have resized its own window to the new desktop
		// size; snap it back to the tracked .rdp-area rect. Safe here:
		// SetWindowPos with ASYNCWINDOWPOS does not block the COM STA thread.
		s.placeAtChild(x, y, tw, th, SWP_NOACTIVATE|SWP_ASYNCWINDOWPOS)

		if enterFs {
			// Deferred full-screen toggle: the desktop now matches the
			// monitor, so let the control take over. Reset the oscillation
			// baseline first — the coming fullscreen-exit resize back to the
			// windowed size is a legitimate cycle, not a feedback loop.
			s.mu.Lock()
			s.prevDesktopW, s.prevDesktopH = 0, 0
			s.fsActive = true
			rdp := s.rdp
			s.mu.Unlock()
			if rdp != nil {
				rdp.PutProperty("FullScreen", true)
			}
		}
	}
}

// connectedPropZero interprets the ActiveX "Connected" property value:
// false, or a zero number, means disconnected.
func connectedPropZero(v any) bool {
	switch t := v.(type) {
	case bool:
		return !t
	case nil:
		return true
	default:
		return v == int16(0) || v == int32(0) || v == 0
	}
}

// placeAtChild positions the RDP CHILD window at the given parent-client-
// relative offset (offsetX, offsetY) and size, bringing it to the top of the
// main window's child z-order (above the webview) so the ActiveX is visible.
// For a WS_CHILD, position/size are already parent-client-relative — no screen
// origin conversion is needed (the window follows the parent automatically).
// insertAfter=0 is HWND_TOP.
func (s *RDPSession) placeAtChild(offsetX, offsetY, w, h int, flags uintptr) {
	s.mu.Lock()
	hwnd := s.hwnd
	s.mu.Unlock()
	if hwnd == 0 {
		return
	}
	procSetWindowPos.Call(hwnd, 0, // HWND_TOP
		uintptr(offsetX), uintptr(offsetY),
		uintptr(w), uintptr(h),
		flags)
}

func (s *RDPSession) SetPosition(x, y, w, h int) {
	s.mu.Lock()
	if s.hwnd == 0 {
		s.mu.Unlock()
		return
	}
	s.shown = true
	s.trackX = x
	s.trackY = y
	s.trackW = w
	s.trackH = h
	s.mu.Unlock()

	// x/y are PARENT-CLIENT-RELATIVE offsets; placeAtChild positions the child
	// directly in the parent's client coordinate space (no screen conversion).
	// The frontend computes these from the .rdp-area DOM rect, so the RDP child
	// is sized/placed to exactly cover the placeholder region.
	s.placeAtChild(x, y, w, h, SWP_NOACTIVATE|SWP_ASYNCWINDOWPOS)
	// Force ActiveX to recalculate layout and repaint via WM_SIZE.
	s.mu.Lock()
	hwnd := s.hwnd
	s.mu.Unlock()
	if hwnd != 0 {
		lparam := uintptr(h<<16 | w&0xFFFF)
		procPostMessageW.Call(hwnd, WM_SIZE, 0, lparam)
	}

	// Adaptive resolution: a stable .rdp-area size change triggers a reconnect
	// with the new desktop size (applied by the message pump on the STA thread).
	s.requestAdaptiveResize(w, h)
}

// SetFullScreen toggles the ActiveX control's built-in full-screen mode.
// With ContainerHandledFullScreen=false the control renders its own
// connection bar (with a restore button) to exit full screen.
//
// The actual COM PutProperty runs on the message-pump (STA) thread — see
// runMessagePump — because STA COM objects must be called on their owning
// thread; calling from the Wails binding thread deadlocks.
func (s *RDPSession) SetFullScreen(full bool) {
	s.mu.Lock()
	s.fsRequested = true
	s.fsValue = full
	s.mu.Unlock()
}

func (s *RDPSession) Show() {
	s.mu.Lock()
	if s.shown {
		s.mu.Unlock()
		return
	}
	tX := s.trackX
	tY := s.trackY
	tW := s.trackW
	tH := s.trackH
	s.shown = true
	s.mu.Unlock()

	// Bring the child back to HWND_TOP (above the webview) at its tracked
	// parent-client-relative position. placeAtChild passes insertAfter=0
	// (HWND_TOP). The window was merely occluded (Hide only lowered z-order,
	// never moved/resized it), so bringing it back reveals the current frame; a
	// redraw kick makes it repaint immediately without a size change (a 1px
	// resize nudge caused a visible re-scale flicker).
	s.placeAtChild(tX, tY, tW, tH, SWP_SHOWWINDOW|SWP_NOACTIVATE|SWP_ASYNCWINDOWPOS)
	s.mu.Lock()
	hwnd := s.hwnd
	s.mu.Unlock()
	if hwnd != 0 {
		procInvalidateRect.Call(hwnd, 0, 1)
		procRedrawWindow.Call(hwnd, 0, 0, RDW_INVALIDATE|RDW_UPDATENOW|RDW_ALLCHILDREN)
	}
}

func (s *RDPSession) Hide() {
	s.mu.Lock()
	if !s.shown {
		s.mu.Unlock()
		return
	}
	s.shown = false
	s.mu.Unlock()
	s.mu.Lock()
	hwnd := s.hwnd
	s.mu.Unlock()
	if hwnd != 0 {
		// Lower the child to HWND_BOTTOM (below the webview) so an HTML
		// menu/dialog can render over it. This does NOT move or resize the
		// window, so the ActiveX rendering surface stays intact — avoiding the
		// black screen that a move-offscreen hide produces (the webview, which
		// is opaque, covers the child).
		procSetWindowPos.Call(hwnd, 1, // HWND_BOTTOM
			0, 0, 0, 0,
			SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE|SWP_ASYNCWINDOWPOS)
	}
}

// Invalidate forces a repaint of the RDP ActiveX control.
func (s *RDPSession) Invalidate() {
	s.mu.Lock()
	hwnd := s.hwnd
	s.mu.Unlock()
	if hwnd != 0 {
		procInvalidateRect.Call(hwnd, 0, 1)
		// Force a synchronous repaint so the control presents its current frame
		// instead of a black one when re-shown after being occluded.
		procRedrawWindow.Call(hwnd, 0, 0, RDW_INVALIDATE|RDW_UPDATENOW|RDW_ALLCHILDREN)
	}
}

// isInputMessage reports whether a window message is user input aimed at the
// RDP window — the "the user is waiting for feedback" signal for the display
// watchdog (rdp_watchdog.go).
func isInputMessage(m uint32) bool {
	switch m {
	case WM_KEYDOWN, WM_KEYUP, WM_SYSKEYDOWN, WM_SYSKEYUP,
		WM_MOUSEMOVE, WM_LBUTTONDOWN, WM_LBUTTONUP,
		WM_RBUTTONDOWN, WM_RBUTTONUP, WM_MBUTTONDOWN, WM_MBUTTONUP,
		WM_MOUSEWHEEL, WM_XBUTTONDOWN, WM_XBUTTONUP:
		return true
	}
	return false
}

// sampleFrame returns an FNV-1a hash of a small thumbnail of the control's
// currently presented frame plus the fraction of near-black pixels. The
// thumbnail is ~9 KB, so polling every couple of seconds stays cheap. ok is
// false when the window cannot be sampled (e.g. mid-teardown), in which case
// the caller must keep its watchdog state untouched.
func (s *RDPSession) sampleFrame() (hash uint64, dark float64, ok bool) {
	s.mu.Lock()
	hwnd := s.hwnd
	s.mu.Unlock()
	if hwnd == 0 {
		return 0, 0, false
	}
	var cr rect
	if ret, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr))); ret == 0 {
		return 0, 0, false
	}
	srcW, srcH := int(cr.Right-cr.Left), int(cr.Bottom-cr.Top)
	if srcW <= 0 || srcH <= 0 {
		return 0, 0, false
	}

	winDC, _, _ := procGetDC.Call(hwnd)
	if winDC == 0 {
		return 0, 0, false
	}
	defer procReleaseDC.Call(hwnd, winDC)

	memDC, _, _ := procCreateCompatibleDC.Call(winDC)
	if memDC == 0 {
		return 0, 0, false
	}
	defer procDeleteDC.Call(memDC)

	hbm, _, _ := procCreateCompatibleBitmap.Call(winDC, sampleThumbW, sampleThumbH)
	if hbm == 0 {
		return 0, 0, false
	}
	defer procDeleteObject.Call(hbm)

	oldObj, _, _ := procSelectObject.Call(memDC, hbm)
	if oldObj == 0 {
		return 0, 0, false
	}
	defer procSelectObject.Call(memDC, oldObj)

	const srcCopy = 0x00CC0020
	procStretchBlt.Call(memDC, 0, 0, sampleThumbW, sampleThumbH,
		winDC, 0, 0, uintptr(srcW), uintptr(srcH), srcCopy)

	bi := bitmapInfoHeader{
		biSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:    sampleThumbW,
		biHeight:   -sampleThumbH, // negative = top-down rows
		biPlanes:   1,
		biBitCount: 32,
	}
	pix := make([]byte, sampleThumbW*sampleThumbH*4)
	if ret, _, _ := procGetDIBits.Call(memDC, hbm, 0, sampleThumbH,
		uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&bi)), 0); ret == 0 {
		return 0, 0, false
	}

	const (
		fnvOffset64 = 14695981039346656037
		fnvPrime64  = 1099511628211
	)
	h := uint64(fnvOffset64)
	darkCount := 0
	for i := 0; i < len(pix); i += 4 {
		b, g, r := pix[i], pix[i+1], pix[i+2]
		h ^= uint64(r)
		h *= fnvPrime64
		h ^= uint64(g)
		h *= fnvPrime64
		h ^= uint64(b)
		h *= fnvPrime64
		if int(r)+int(g)+int(b) <= 24 {
			darkCount++
		}
	}
	return h, float64(darkCount) / float64(sampleThumbW*sampleThumbH), true
}

// relayout re-places the control at its tracked rect and replays the display
// notifications a WS_CHILD never receives (WM_DISPLAYCHANGE broadcasts reach
// top-level windows only), then forces a repaint. Neutral when the tracked size
// is unchanged. Thread-safe: plain user32 calls.
func (s *RDPSession) relayout() {
	s.mu.Lock()
	hwnd := s.hwnd
	x, y, w, h := s.trackX, s.trackY, s.trackW, s.trackH
	s.mu.Unlock()
	if hwnd == 0 || w <= 0 || h <= 0 {
		return
	}
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		SWP_NOACTIVATE|SWP_ASYNCWINDOWPOS)
	lparam := uintptr(h<<16 | w&0xFFFF)
	procPostMessageW.Call(hwnd, WM_SIZE, 0, lparam)
	procPostMessageW.Call(hwnd, WM_DISPLAYCHANGE, 32, lparam)
	s.Invalidate()
}

// renegotiateDesktop briefly shrinks the remote desktop by 2px so the server
// resynchronizes the graphics stream — a full-frame redraw, the recovery a
// window resize gives mstsc users stuck on a frozen picture. The remote side
// sees a transient resize (restoreDesktopSize puts the size back), so callers
// gate it behind the watchdog's black-frame + cooldown rules. COM call: the
// message-pump (STA) thread only.
func (s *RDPSession) renegotiateDesktop() bool {
	s.mu.Lock()
	rdp := s.rdp
	w, h := s.deskW, s.deskH
	s.mu.Unlock()
	if rdp == nil || w <= 2 || h <= 2 {
		return false
	}
	log.Writef("[RDP] display watchdog: renegotiating desktop size %dx%d", w, h)
	rdp.PutProperty("DesktopWidth", w-2)
	rdp.PutProperty("DesktopHeight", h-2)
	return true
}

// restoreDesktopSize undoes renegotiateDesktop. COM call: the message-pump
// (STA) thread only.
func (s *RDPSession) restoreDesktopSize() {
	s.mu.Lock()
	rdp := s.rdp
	w, h := s.deskW, s.deskH
	s.mu.Unlock()
	if rdp == nil {
		return
	}
	log.Writef("[RDP] display watchdog: restoring desktop size %dx%d", w, h)
	rdp.PutProperty("DesktopWidth", w)
	rdp.PutProperty("DesktopHeight", h)
}

// Refresh forces a full resync of the RDP display: re-place the control,
// replay the display-change notifications and repaint immediately, then ask
// the COM thread to renegotiate the desktop size. Wired to the frontend's
// refresh button for stalls the watchdog cannot see (e.g. a frozen but
// non-black frame nobody is clicking on). Thread-safe.
func (s *RDPSession) Refresh() {
	s.relayout()
	s.mu.Lock()
	s.resyncRequested = true
	s.mu.Unlock()
}

// Snapshot captures the RDP window's current content as a base64-encoded PNG.
// The frontend uses it as a frozen background for .rdp-area while the RDP window
// is hidden under an overlay (menu/dialog), so the area shows a snapshot instead
// of a black placeholder.
func (s *RDPSession) Snapshot() (string, error) {
	s.mu.Lock()
	hwnd := s.hwnd
	s.mu.Unlock()
	if hwnd == 0 {
		return "", fmt.Errorf("RDP window not created")
	}
	var wr rect
	if ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr))); ret == 0 {
		return "", fmt.Errorf("GetWindowRect failed")
	}
	w := int(wr.Right - wr.Left)
	h := int(wr.Bottom - wr.Top)
	if w <= 0 || h <= 0 {
		return "", fmt.Errorf("bad window size %dx%d", w, h)
	}

	winDC, _, _ := procGetDC.Call(hwnd)
	if winDC == 0 {
		return "", fmt.Errorf("GetDC(hwnd) failed")
	}
	defer procReleaseDC.Call(hwnd, winDC)

	memDC, _, _ := procCreateCompatibleDC.Call(winDC)
	if memDC == 0 {
		return "", fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	hbm, _, _ := procCreateCompatibleBitmap.Call(winDC, uintptr(w), uintptr(h))
	if hbm == 0 {
		return "", fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(hbm)

	oldObj, _, _ := procSelectObject.Call(memDC, hbm)
	if oldObj == 0 {
		return "", fmt.Errorf("SelectObject failed")
	}
	defer procSelectObject.Call(memDC, oldObj)

	// PrintWindow with PW_RENDERFULLCONTENT renders the window AND its child
	// control into the target DC, which is what actually carries the remote
	// desktop frame (a plain BitBlt from the AtlAxWin DC would capture the empty
	// parent surface). Fall back to BitBlt if PrintWindow reports failure.
	const pwRenderFullContent = 0x00000002
	const srcCopy = 0x00CC0020
	if ret, _, _ := procPrintWindow.Call(hwnd, memDC, pwRenderFullContent); ret == 0 {
		if ret2, _, _ := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), winDC, 0, 0, srcCopy); ret2 == 0 {
			return "", fmt.Errorf("PrintWindow failed")
		}
	}

	// DIB header for GetDIBits (top-down 32bpp BGRA).
	bi := bitmapInfoHeader{
		biSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:    int32(w),
		biHeight:   -int32(h), // negative = top-down rows
		biPlanes:   1,
		biBitCount: 32,
	}
	pix := make([]byte, w*h*4)
	if ret, _, _ := procGetDIBits.Call(memDC, hbm, 0, uintptr(h), uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&bi)), 0); ret == 0 {
		return "", fmt.Errorf("GetDIBits failed")
	}

	// BGRA → RGBA, then encode PNG.
	img := &image.RGBA{Pix: make([]byte, w*h*4), Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	for i := range w * h {
		b := pix[i*4+0]
		g := pix[i*4+1]
		r := pix[i*4+2]
		a := pix[i*4+3]
		img.Pix[i*4+0] = r
		img.Pix[i*4+1] = g
		img.Pix[i*4+2] = b
		img.Pix[i*4+3] = a
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func (s *RDPSession) Disconnect() error {
	// Post WM_QUIT to the COM STA message pump so it exits cleanly.
	// Do NOT zero s.hwnd here — the defer in Connect() needs it
	// to call DestroyWindow for proper cleanup.
	s.mu.Lock()
	hwnd := s.hwnd
	s.mu.Unlock()

	if hwnd != 0 {
		procPostMessageW.Call(hwnd, 0x0012, 0, 0) // WM_QUIT
	}
	s.setStatus(StatusDisconnected)
	return nil
}

func (s *RDPSession) Resize(cols, rows int) error {
	s.mu.Lock()
	if s.rdp != nil {
		s.deskW = cols
		s.deskH = rows
		s.rdp.PutProperty("DesktopWidth", cols)
		s.rdp.PutProperty("DesktopHeight", rows)
	}
	s.mu.Unlock()
	return nil
}

func (s *RDPSession) Write(_ []byte) error {
	return nil
}

func (s *RDPSession) IsConnected() bool {
	return s.Status() == StatusConnected
}
