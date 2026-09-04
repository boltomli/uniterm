//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/ys-ll/uniterm/backend/log"
	"golang.org/x/sys/windows"
)

func (a *App) GetAvailableShells() []string {
	var shells []string
	var seen = make(map[string]bool)

	add := func(path string) {
		if path == "" {
			return
		}
		abs, err := exec.LookPath(path)
		if err != nil {
			return
		}
		key := strings.ToLower(strings.ReplaceAll(abs, `\`, `/`))
		if seen[key] {
			return
		}
		seen[key] = true
		shells = append(shells, abs)
	}

	hasShell := func(name string) bool {
		for _, sh := range shells {
			if strings.EqualFold(filepath.Base(sh), name) {
				return true
			}
		}
		return false
	}

	add("pwsh.exe")
	add("powershell.exe")
	add("cmd.exe")
	for _, p := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
		`C:\ProgramData\chocolatey\bin\bash.exe`,
	} {
		add(p)
	}
	if !hasShell("bash.exe") {
		add("bash.exe")
	}
	if distros, _ := listWSLDistros(); len(distros) > 0 {
		for _, d := range distros {
			shells = append(shells, "wsl://"+d)
		}
	}
	return shells
}

func listWSLDistros() ([]string, error) {
	cmd := exec.Command("wsl.exe", "-l", "-q")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	return parseWSLDistros(out), nil
}

func parseWSLDistros(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}

	content := string(raw)
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		u16 := make([]uint16, 0, len(raw)/2)
		for i := 2; i+1 < len(raw); i += 2 {
			u16 = append(u16, uint16(raw[i])|uint16(raw[i+1])<<8)
		}
		content = string(utf16.Decode(u16))
	}

	var distros []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		line = strings.ReplaceAll(line, "\x00", "")
		line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "docker-desktop") {
			continue
		}
		if !seen[line] {
			seen[line] = true
			distros = append(distros, line)
		}
	}
	return distros
}

const (
	GWLP_WNDPROC     = ^uintptr(3)
	WM_ENTERSIZEMOVE = 0x0231
	WM_EXITSIZEMOVE  = 0x0232
	WM_SYSCOMMAND    = 0x0112
	WM_SIZE          = 0x0005
	SC_MAXIMIZE      = 0xF030
	SC_MINIMIZE      = 0xF020
	SC_RESTORE       = 0xF120
)

func (a *App) findMainWindow() uintptr {
	pid := windows.GetCurrentProcessId()
	var result uintptr

	user32 := windows.NewLazySystemDLL("user32.dll")
	procEnumWindows := user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId := user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW := user32.NewProc("GetWindowTextW")

	cb := windows.NewCallback(func(hwnd windows.HWND, lParam uintptr) uintptr {
		var wndPid uint32
		procGetWindowThreadProcessId.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&wndPid)))
		if wndPid != pid {
			return 1 // continue
		}
		// Verify it has our window title so we don't pick up invisible helper windows.
		buf := make([]uint16, 256)
		procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), 255)
		if windows.UTF16ToString(buf) == "uniTerm" {
			result = uintptr(hwnd)
			return 0 // stop
		}
		return 1 // continue
	})
	procEnumWindows.Call(cb, 0)
	return result
}

// bringMainWindowToFront raises the main window to the foreground once. After a
// relaunch the fresh instance can otherwise open behind other windows on Windows:
// SetForegroundWindow is normally restricted, but a process spawned by the
// currently-foreground process is one of the documented exceptions, so a single
// raise works while the old instance (the foreground process) is still alive.
// No-op when no main window has been created yet.
func (a *App) bringMainWindowToFront() {
	hwnd := a.mainHwnd
	if hwnd == 0 {
		hwnd = a.findMainWindow()
	}
	if hwnd == 0 {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	procSetForegroundWindow := user32.NewProc("SetForegroundWindow")
	procBringWindowToTop := user32.NewProc("BringWindowToTop")
	procSetForegroundWindow.Call(hwnd)
	procBringWindowToTop.Call(hwnd)
}

// emitMoveResize sends a move/resize event to the frontend without blocking.
// It must never be called from within the WndProc modal resize/move loop.
func (a *App) emitMoveResize(event string) {
	if a.moveResizeCh == nil {
		return
	}
	select {
	case a.moveResizeCh <- event:
	default:
	}
}

func (a *App) subclassMainWindow() {
	if a.mainHwnd == 0 {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	procSetWindowLongPtrW := user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW := user32.NewProc("CallWindowProcW")

	cb := windows.NewCallback(func(hwnd windows.HWND, msg uint32, wparam, lparam uintptr) uintptr {
		switch msg {
		case WM_ENTERSIZEMOVE:
			a.inSizeMove = true
			a.emitMoveResize("rdp:move-resize-start")
		case WM_EXITSIZEMOVE:
			a.inSizeMove = false
			a.emitMoveResize("rdp:move-resize-end")
		case WM_SYSCOMMAND:
			switch wparam {
			case SC_MAXIMIZE, SC_MINIMIZE, SC_RESTORE:
				a.emitMoveResize("rdp:move-resize-start")
			}
		case WM_SIZE:
			if !a.inSizeMove {
				a.emitMoveResize("rdp:move-resize-end")
			}
		}
		ret, _, _ := procCallWindowProcW.Call(a.originalWndProc, uintptr(hwnd), uintptr(msg), wparam, lparam)
		return ret
	})
	a.wndProcCb = cb

	orig, _, _ := procSetWindowLongPtrW.Call(a.mainHwnd, GWLP_WNDPROC, cb)
	a.originalWndProc = orig
}

func (a *App) unsubclassMainWindow() {
	if a.originalWndProc == 0 || a.mainHwnd == 0 {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	procSetWindowLongPtrW := user32.NewProc("SetWindowLongPtrW")
	procSetWindowLongPtrW.Call(a.mainHwnd, GWLP_WNDPROC, a.originalWndProc)
	a.originalWndProc = 0
}

// CANDIDATEFORM and IME constants for ImmSetCandidateWindow.
const (
	cfsCandidatePos = 0x0008
)

// point and candidateForm mirror the Win32 POINT and CANDIDATEFORM structs
// for passing to ImmSetCandidateWindow via unsafe.Pointer.
type point struct {
	x, y int32
}

type candidateForm struct {
	dwIndex       uint32
	dwStyle       uint32
	ptCurrentPos  point
	rcArea        [4]int32 // left, top, right, bottom — unused for CFS_CANDIDATEPOS
}

// SetIMECandidatePosition repositions the IME candidate window to follow the
// textarea. Called from the frontend after resize, focus, and activation — the
// only moments where the textarea moves but the IME doesn't follow.
//
// The frontend supplies the textarea's screen-space bounding rect; this method
// uses ImmSetCandidateWindow(CFS_CANDIDATEPOS) to tell the IME where the text
// insertion point is, preventing the candidate window from drifting to the
// screen origin (0,0) or going off-screen.
//
// No-op when imm32.dll is unavailable (non-Windows builds via cross-compile).
func (a *App) SetIMECandidatePosition(x, y, width, height float64) error {
	user32 := windows.NewLazySystemDLL("user32.dll")
	imm32 := windows.NewLazySystemDLL("imm32.dll")
	procGetFocus := user32.NewProc("GetFocus")
	procImmGetContext := imm32.NewProc("ImmGetContext")
	procImmSetCandidateWindow := imm32.NewProc("ImmSetCandidateWindow")
	procImmReleaseContext := imm32.NewProc("ImmReleaseContext")

	// Get the HWND that currently has keyboard focus on this thread.
	// HWND=0 does NOT work with WebView2 — the IME context lives on the
	// child WebView2 control window, not the main Wails window.
	focusedHWND, _, _ := procGetFocus.Call()
	if focusedHWND == 0 {
		log.Writef("[IME] GetFocus returned 0 — no focused HWND (x=%.0f y=%.0f)", x, y)
		return nil
	}
	himc, _, _ := procImmGetContext.Call(focusedHWND)
	if himc == 0 {
		log.Writef("[IME] ImmGetContext returned 0 for hwnd=%v (x=%.0f y=%.0f)", focusedHWND, x, y)
		return nil
	}
	defer procImmReleaseContext.Call(focusedHWND, himc)

	// Position candidate window at the caret location (left edge + a pixel
	// for the cursor, top of the textarea). Width/height are unused for
	// CFS_CANDIDATEPOS but the struct must be properly sized.
	cf := candidateForm{
		dwIndex: 0,
		dwStyle: cfsCandidatePos,
		ptCurrentPos: point{
			x: int32(x + 1), // +1 avoids degenerate zero-width caret edge
			y: int32(y),
		},
	}
	procImmSetCandidateWindow.Call(
		himc,
		uintptr(unsafe.Pointer(&cf)),
	)
	log.Writef("[IME] ImmSetCandidateWindow pos=(%.0f, %.0f) size=(%.0f, %.0f)", x+1, y, width, height)
	return nil
}

// configureMacKeyRepeat is a no-op on Windows; the press-and-hold accent
// picker only exists on macOS. See app_darwin.go for details.
func (a *App) configureMacKeyRepeat() {}

// hideProcWindow prevents batch-file shims (e.g. VS Code's code.cmd) from
// flashing a console window: shims can only run through cmd.exe, which
// allocates a console of its own when launched from a GUI process that has
// none. HideWindow (STARTF_USESHOWWINDOW, SW_HIDE) suppresses that console.
// It must only be applied to shims: GUI exes honor STARTF_USESHOWWINDOW for
// their first window too, so hiding them would leave the editor invisible.
// Editors needing a CLI shim for wait semantics (VS Code's -w lives in the
// node cli.js, not in Code.exe) are launched via their shim + HideWindow.
func hideProcWindow(cmd *exec.Cmd) {
	ext := strings.ToLower(filepath.Ext(cmd.Path))
	if ext == ".cmd" || ext == ".bat" {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
}

// detectExternalEditors scans for text editors installed on this Windows host
// and returns only those actually found. Console-based editors (Vim, Neovim,
// nano, Micro) are excluded: spawned from a GUI app without a console they
// exit immediately, so they aren't offered. Every GUI editor is probed on PATH
// first, then falls back to common install dirs (globbing versioned folders),
// because default Windows installs often leave these off PATH.
func detectExternalEditors() []ExternalEditorOption {
	type fixed struct{ glob, suffix string }
	type editor struct {
		name, prog, pathCmd string
		fixed               []fixed
	}
	// VS Code family: the -w (wait) flag is implemented by the node cli.js
	// invoked from the bin\*.cmd shim, NOT by the GUI exe — launching Code.exe
	// directly exits immediately and breaks auto-upload. So even when the exe
	// is found at a fixed path, the command must point at the bin shim.
	editors := []editor{
		{"VS Code", "code", "code -w", []fixed{
			{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Microsoft VS Code", "bin", "code.cmd"), " -w"},
			{filepath.Join(os.Getenv("ProgramFiles"), "Microsoft VS Code", "bin", "code.cmd"), " -w"},
		}},
		{"VS Code Insiders", "code-insiders", "code-insiders -w", []fixed{
			{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Microsoft VS Code Insiders", "bin", "code-insiders.cmd"), " -w"},
			{filepath.Join(os.Getenv("ProgramFiles"), "Microsoft VS Code Insiders", "bin", "code-insiders.cmd"), " -w"},
		}},
		{"VSCodium", "codium", "codium -w", []fixed{
			{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "VSCodium", "bin", "codium.cmd"), " -w"},
			{filepath.Join(os.Getenv("ProgramFiles"), "VSCodium", "bin", "codium.cmd"), " -w"},
		}},
		{"Sublime Text", "subl", "subl -w", []fixed{
			{filepath.Join(os.Getenv("ProgramFiles"), "Sublime Text", "subl.exe"), " -w"},
			{filepath.Join(os.Getenv("ProgramFiles(x86)"), "Sublime Text", "subl.exe"), " -w"},
			{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Sublime Text", "subl.exe"), " -w"},
		}},
		{"Atom", "atom", "atom", []fixed{
			{filepath.Join(os.Getenv("LOCALAPPDATA"), "atom", "bin", "atom.exe"), ""},
			{filepath.Join(os.Getenv("ProgramFiles"), "Atom", "bin", "atom.exe"), ""},
		}},
		{"gVim", "gvim", "gvim", []fixed{
			{filepath.Join(os.Getenv("ProgramFiles"), "Vim", "vim*", "gvim.exe"), ""},
			{filepath.Join(os.Getenv("ProgramFiles(x86)"), "Vim", "vim*", "gvim.exe"), ""},
		}},
		{"Emacs", "emacs", "emacs", []fixed{
			{filepath.Join(os.Getenv("ProgramFiles"), "Emacs", "emacs-*", "bin", "emacs.exe"), ""},
			{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Emacs", "emacs-*", "bin", "emacs.exe"), ""},
		}},
		{"Notepad++", "notepad++", "notepad++", []fixed{
			{filepath.Join(os.Getenv("ProgramFiles"), "Notepad++", "notepad++.exe"), ""},
			{filepath.Join(os.Getenv("ProgramFiles(x86)"), "Notepad++", "notepad++.exe"), ""},
			{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Notepad++", "notepad++.exe"), ""},
		}},
		{"EmEditor", "emeditor", "emeditor", []fixed{
			{filepath.Join(os.Getenv("ProgramFiles"), "EmEditor", "emeditor.exe"), ""},
			{filepath.Join(os.Getenv("ProgramFiles(x86)"), "EmEditor", "emeditor.exe"), ""},
			{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "EmEditor", "emeditor.exe"), ""},
		}},
		{"Notepad--", "notepad--", "notepad--", []fixed{
			{filepath.Join(os.Getenv("ProgramFiles"), "Notepad--", "Notepad--.exe"), ""},
			{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Notepad--", "Notepad--.exe"), ""},
		}},
		// Notepad lives in System32, which is always on PATH.
		{"Notepad", "notepad", "notepad", nil},
	}

	var out []ExternalEditorOption
	seen := map[string]bool{} // by canonical display name
	add := func(name, command string) {
		if seen[name] {
			return
		}
		seen[name] = true
		out = append(out, ExternalEditorOption{Label: name + " (" + command + ")", Value: command})
	}

	for _, e := range editors {
		// Prefer the real GUI exe in known install dirs: launching it directly
		// avoids the batch shim (code.cmd) and the console window it flashes.
		for _, f := range e.fixed {
			matches, _ := filepath.Glob(f.glob)
			if len(matches) > 0 {
				add(e.name, "\""+matches[0]+"\""+f.suffix)
				break
			}
		}
		if seen[e.name] {
			continue
		}
		// Fall back to PATH (may resolve to a shim — covered by hideProcWindow).
		if _, err := exec.LookPath(e.prog); err == nil {
			add(e.name, e.pathCmd)
		}
	}

	return out
}
