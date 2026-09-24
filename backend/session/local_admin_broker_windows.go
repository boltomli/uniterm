//go:build windows
// +build windows

package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

// RunLocalPtyBroker implements uniTerm's administrator-shell broker mode. The
// unelevated app launches a copy of itself with the "runas" verb and the
// --local-pty-broker argument; that elevated copy (this code) never touches
// the webview or app stores — it spawns the requested shell inside a ConPTY
// and relays bytes between the pseudo console and the control pipe until
// either side goes away. It returns true when args selected broker mode; main
// exits without initializing the app in that case.
func RunLocalPtyBroker(args []string) bool {
	if len(args) != 3 || args[1] != "--local-pty-broker" {
		return false
	}
	os.Exit(serveLocalPtyBroker(args[2]))
	return true
}

// elevatedShellToken splits a spawn command line into its first token (the
// executable: the whole quoted string when it starts with a quote, otherwise
// the text up to the first space) and the remaining argument text. Quotes
// still surrounding the token are stripped (a quoted first token arrives
// bare already; this guards double-wrapped input like ""cmd.exe""). ok is
// false when the command line is empty or unparseable (e.g. an opening quote
// with no closing quote).
func elevatedShellToken(cmdline string) (tok, rest string, ok bool) {
	s := strings.TrimSpace(cmdline)
	if s == "" {
		return "", "", false
	}
	var end int
	if s[0] == '"' {
		closing := strings.IndexByte(s[1:], '"')
		if closing < 0 {
			return "", "", false
		}
		tok = s[1 : 1+closing]
		end = closing + 2
	} else if i := strings.IndexAny(s, " \t"); i >= 0 {
		tok = s[:i]
		end = i
	} else {
		tok = s
		end = len(s)
	}
	tok = strings.Trim(tok, `"`)
	if tok == "" {
		return "", "", false
	}
	return tok, strings.TrimSpace(s[end:]), true
}

// elevatedShellBase extracts the normalized executable base name from a shell
// command line: the first token (see elevatedShellToken), reduced with
// filepath.Base (which handles both '\' and '/' separators on Windows) and
// lowercased. Returns "" when the command line is empty or unparseable.
func elevatedShellBase(cmdline string) string {
	tok, _, ok := elevatedShellToken(cmdline)
	if !ok {
		return ""
	}
	return strings.ToLower(filepath.Base(tok))
}

// allowedElevatedShells is the allowlist of executables the elevated broker
// may spawn. This process runs with an administrator token, so only known
// shell executables may be started here — any other command line would turn
// a single UAC consent into arbitrary elevated execution for any same-user
// code that can reach the broker's spawn frame. admin://clink://... produces
// a cmd.exe-leading command line ("cmd.exe /k <clink> inject ...") and
// admin://wsl://... produces a wsl.exe-leading one ("wsl.exe -d <distro>
// ..."), so both admin compositions still pass. The allowlist is only the
// first gate: elevatedSpawnRefusal additionally resolves the executable to
// its canonical path under a trusted Windows root (so any path *named*
// cmd.exe outside System32 is refused, F3) and restricts the argument tail
// to the shapes local_session_windows.go generates; quoteWindowsArg there
// keeps a hostile shell path inside the first token so the split below
// cannot be broken out of.
var allowedElevatedShells = map[string]bool{
	"cmd.exe":        true,
	"powershell.exe": true,
	"pwsh.exe":       true,
	"wsl.exe":        true,
	"bash.exe":       true,
	"sh.exe":         true,
	"zsh.exe":        true,
	"fish.exe":       true,
	"nu.exe":         true,
	"elvish.exe":     true,
}

// isAllowedElevatedShell reports whether base (see elevatedShellBase) is a
// shell the elevated broker may spawn. Case-insensitive — Windows file
// names are.
func isAllowedElevatedShell(base string) bool {
	return allowedElevatedShells[strings.ToLower(base)]
}

// elevatedTrustedRoots is the set of lowercase directory prefixes an
// elevated shell executable must live under, built from the process
// environment (never hardcoded drive letters): %SystemRoot%\System32 (covers
// cmd.exe, wsl.exe's system shim and, beneath it,
// WindowsPowerShell\v1.0\powershell.exe), %ProgramFiles%,
// %ProgramFiles(x86)% and %ProgramW6432% (pwsh.exe and Git Bash installs;
// each also covers its \WindowsApps subtree, where the Store wsl lives).
// Entries whose environment variable is unset are skipped; if no root
// resolves at all the broker refuses every spawn (fail closed).
func elevatedTrustedRoots() []string {
	var roots []string
	for _, dir := range []string{
		filepath.Join(os.Getenv("SystemRoot"), "System32"),
		filepath.Join(os.Getenv("ProgramFiles")),
		filepath.Join(os.Getenv("ProgramFiles(x86)")),
		filepath.Join(os.Getenv("ProgramW6432")),
	} {
		// filepath.Join of an empty element list yields a relative (or
		// empty) path — exactly the unset-variable case, which must not
		// become a root (every absolute path has that prefix).
		if filepath.IsAbs(dir) {
			roots = append(roots, strings.ToLower(filepath.Clean(dir)))
		}
	}
	return roots
}

// underElevatedTrustedRoot reports whether exe (an absolute, cleaned path)
// equals or sits beneath one of elevatedTrustedRoots. Comparison is
// case-insensitive and separator-bounded, so C:\Windowsevil\System32 does
// not pass as C:\Windows\System32.
func underElevatedTrustedRoot(exe string) bool {
	p := strings.ToLower(filepath.Clean(exe))
	for _, root := range elevatedTrustedRoots() {
		if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolveElevatedExe canonicalizes the first command-line token to an
// absolute path: explicit paths via filepath.Abs (which Cleans "..", "."
// and mixed separators, so C:\Windows\System32\..\..\evil\cmd.exe collapses
// out of the root before the check), bare names like "wsl.exe" via a PATH
// lookup first so the resolution lands where Windows would load the
// executable from. Returns "" when the token cannot be resolved.
func resolveElevatedExe(tok string) string {
	p := tok
	if !strings.ContainsAny(tok, `/\:`) {
		lp, err := exec.LookPath(tok)
		if err != nil {
			return ""
		}
		p = lp
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}

// elevatedArgsAllowed reports whether rest (the argument text after the
// executable) matches one of the exact shapes local_session_windows.go
// generates for base:
//
//   - buildCommandLine: bare, or the fixed ` /k` (cmd) / ` --login -i`
//     (bash) suffix;
//   - buildClinkCommandLine: the /k clink-inject composition;
//   - Connect + buildWSLStartArgs: the wsl.exe launch line.
//
// Nothing else can legitimately arrive here: the post-login script is never
// part of the command line — it is written to the PTY after Connect, see
// runPostLoginScript — so every argument the launcher sends is either one
// of those fixed flags or a config-derived operand re-validated by the
// sub-shape below. That is why the tail is pinned rather than free-form.
func elevatedArgsAllowed(base, rest string) bool {
	switch base {
	case "cmd.exe":
		return rest == "/k" || elevatedClinkArgsAllowed(rest)
	case "wsl.exe":
		return elevatedWslArgsAllowed(rest)
	case "bash.exe":
		return rest == "" || rest == "--login -i"
	default:
		// buildCommandLine returns the bare quoted executable for every
		// other allowlisted shell (powershell/pwsh/sh/zsh/fish/nu/elvish).
		return rest == ""
	}
}

// elevatedClinkArgsAllowed matches buildClinkCommandLine's exact output:
// `/k ""<clinkPath>" inject"` with an optional ` --profile "<dir>"`, wrapped
// in the outer quote pair that cmd's /k parser strips. The clink executable
// — the only program this shape makes cmd run — is pinned exactly like the
// first token: allowlisted basename, canonical path under a trusted root.
// Without that pin the shape would hand arbitrary elevated execution to
// anyone who can point <clinkPath> anywhere.
func elevatedClinkArgsAllowed(rest string) bool {
	const prefix = `/k "`
	if !strings.HasPrefix(rest, prefix) || !strings.HasSuffix(rest, `"`) || len(rest) < len(prefix)+2 {
		return false
	}
	body := rest[len(prefix) : len(rest)-1]
	if body[0] != '"' {
		return false
	}
	// body = `"<clinkPath>" inject[ --profile "<dir>"]`.
	end := strings.IndexByte(body[1:], '"')
	if end < 0 {
		return false
	}
	clinkPath := body[1 : 1+end]
	tail := body[1+end:]
	switch {
	case tail == `" inject`:
	case strings.HasPrefix(tail, `" inject --profile "`):
		// The prefix literal consumes the dir's opening quote; what is
		// left must be `<dir>` — non-empty content plus its closing quote.
		dir := tail[len(`" inject --profile "`):]
		if len(dir) < 2 || dir[len(dir)-1] != '"' || strings.Contains(dir[:len(dir)-1], `"`) {
			return false
		}
	default:
		return false
	}
	if !strings.EqualFold(filepath.Base(clinkPath), "clink.exe") {
		return false
	}
	exe := resolveElevatedExe(clinkPath)
	return exe != "" && underElevatedTrustedRoot(exe)
}

// elevatedWslArgsAllowed matches the wsl.exe launch line Connect builds:
//
//	wsl.exe -d <distro> [--cd ~] [-e bash --rcfile <path> | -e env ZDOTDIR=<dir> zsh]
//
// The distro name is re-validated with validWSLDistroName (the tail reaches
// wsl.exe argv, which re-quotes it for the distro's login shell), and the
// -e target is pinned to bash/env so the tail can never name an arbitrary
// executable for wsl to run — WSL interop would otherwise pass a Windows
// command through an elevated wsl.exe.
func elevatedWslArgsAllowed(rest string) bool {
	f := strings.Fields(rest)
	if len(f) < 2 || f[0] != "-d" || !validWSLDistroName(f[1]) {
		return false
	}
	f = f[2:]
	// The launcher only ever emits `--cd ~` (no cwd case emits nothing).
	if len(f) >= 2 && f[0] == "--cd" {
		if f[1] != "~" {
			return false
		}
		f = f[2:]
	}
	switch {
	case len(f) == 0:
		return true
	case len(f) == 4 && f[0] == "-e" && f[1] == "bash" && f[2] == "--rcfile":
		// wslShellIntegration asserts no space/tab/quote in start args;
		// Fields already removed whitespace, reject what remains of it.
		return !strings.Contains(f[3], `"`)
	case len(f) == 4 && f[0] == "-e" && f[1] == "env" && strings.HasPrefix(f[2], "ZDOTDIR=") && f[3] == "zsh":
		dir := strings.TrimPrefix(f[2], "ZDOTDIR=")
		return dir != "" && !strings.Contains(dir, `"`)
	}
	return false
}

// elevatedSpawnRefusal returns "" when cmdline is a spawn request the
// elevated broker may execute, otherwise the reason shown in the terminal.
// Four gates, all must pass: the command line parses, the executable base
// name is on the allowlist, its canonical path sits under a trusted Windows
// root (F3 — same allowlisted basename anywhere else is refused), and the
// argument tail matches a launcher-generated shape.
func elevatedSpawnRefusal(cmdline string) string {
	tok, rest, ok := elevatedShellToken(cmdline)
	if !ok {
		return fmt.Sprintf("not an allowed administrator shell (%q)", "")
	}
	base := strings.ToLower(filepath.Base(tok))
	if !isAllowedElevatedShell(base) {
		return fmt.Sprintf("not an allowed administrator shell (%q)", base)
	}
	exe := resolveElevatedExe(tok)
	if exe == "" {
		return fmt.Sprintf("cannot resolve shell path (%q)", tok)
	}
	if !underElevatedTrustedRoot(exe) {
		return fmt.Sprintf("shell outside trusted Windows directories (%q)", exe)
	}
	if !elevatedArgsAllowed(base, rest) {
		return fmt.Sprintf("unsupported arguments for %q (%q)", base, rest)
	}
	return ""
}

// serveLocalPtyBroker serves one elevated shell and returns the process exit
// code, so tests can run it in-process.
func serveLocalPtyBroker(pipeName string) int {
	pipe, err := dialPtyBrokerPipe(pipeName)
	if err != nil {
		return 1
	}
	// Announce the connection; the app waits for this frame instead of
	// ConnectNamedPipe (see startElevatedPty).
	if err := ptyFrameWriteTo(pipe, ptyFrameHello, nil); err != nil {
		pipe.Close()
		return 1
	}

	frameType, payload, err := ptyFrameReadFrom(pipe)
	if err != nil {
		pipe.Close()
		return 1
	}
	if frameType != ptyFrameSpawn {
		pipe.Close()
		return 1
	}
	var spec localPtySpawn
	if err := json.Unmarshal(payload, &spec); err != nil {
		pipe.Close()
		return 1
	}

	// Fail fast on anything but a sanctioned administrator shell command
	// line: this process is elevated, so the refusal reason must reach the
	// terminal feed before any ConPTY capability check.
	if refusal := elevatedSpawnRefusal(spec.CommandLine); refusal != "" {
		noteAndClose(pipe, "refused: "+refusal)
		return 1
	}

	if !conpty.IsConPtyAvailable() {
		noteAndClose(pipe, "ConPTY is not available on this Windows version")
		return 1
	}

	cols, rows := spec.Cols, spec.Rows
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	env := os.Environ()
	if len(spec.Env) > 0 {
		env = append(env, spec.Env...)
	}
	cpty, err := conpty.Start(spec.CommandLine,
		conpty.ConPtyDimensions(cols, rows),
		conpty.ConPtyWorkDir(spec.WorkDir),
		conpty.ConPtyEnv(env))
	if err != nil {
		noteAndClose(pipe, fmt.Sprintf("start shell: %v", err))
		return 1
	}

	// The app side vanished (tab closed, app quit, pipe broke): kill the
	// shell so it cannot linger elevated with no one attached.
	pipeBroken := make(chan struct{})
	shellExited := make(chan struct{})
	var brokenOnce, killOnce sync.Once
	signalPipeBroken := func() { brokenOnce.Do(func() { close(pipeBroken) }) }
	terminate := func() {
		killOnce.Do(func() {
			cpty.Close()
			// ClosePseudoConsole ends the console session; make sure the
			// client itself is gone even if it somehow survived.
			if proc, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(cpty.Pid())); err == nil {
				windows.TerminateProcess(proc, 1)
				windows.CloseHandle(proc)
			}
		})
	}

	// ConPTY output → app.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := cpty.Read(buf)
			if n > 0 {
				if ptyFrameWriteTo(pipe, ptyFrameData, buf[:n]) != nil {
					signalPipeBroken()
					return
				}
			}
			if err != nil {
				close(shellExited)
				return
			}
		}
	}()

	// App input / resize → ConPTY.
	go func() {
		for {
			frameType, payload, err := ptyFrameReadFrom(pipe)
			if err != nil {
				signalPipeBroken()
				return
			}
			switch frameType {
			case ptyFrameInput:
				if len(payload) > 0 {
					cpty.Write(payload)
				}
			case ptyFrameResize:
				if len(payload) >= 4 {
					cols := int(payload[0]) | int(payload[1])<<8
					rows := int(payload[2]) | int(payload[3])<<8
					cpty.Resize(cols, rows)
				}
			}
		}
	}()

	select {
	case <-pipeBroken:
		terminate()
		pipe.Close()
		return 1
	case <-shellExited:
		ptyFrameWriteTo(pipe, ptyFrameExited, nil)
		pipe.Close()
		return 0
	}
}

// noteAndClose tells the app why no shell could be started: the note travels
// as PTY output (the terminal shows it), the exited frame ends the session.
func noteAndClose(pipe *os.File, note string) {
	ptyFrameWriteTo(pipe, ptyFrameData, []byte("\r\n[administrator shell: "+note+"]\r\n"))
	ptyFrameWriteTo(pipe, ptyFrameExited, nil)
	pipe.Close()
}

// dialPtyBrokerPipe connects to the app's control pipe. The pipe exists
// before the broker is launched, but retry briefly anyway to absorb
// scheduling jitter between ShellExecuteEx returning and the broker starting.
func dialPtyBrokerPipe(pipeName string) (*os.File, error) {
	deadline := time.Now().Add(10 * time.Second)
	namePtr, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return nil, err
	}
	for {
		// FILE_FLAG_OVERLAPPED matches the server side: os.File routes the
		// handle through Go's async I/O machinery, which is what lets the
		// relay's blocked Read coexist with blocked Writes on this handle.
		handle, err := windows.CreateFile(
			namePtr,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			return os.NewFile(uintptr(handle), pipeName), nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
