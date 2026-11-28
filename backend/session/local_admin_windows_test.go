//go:build windows
// +build windows

package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UserExistsError/conpty"
)

func TestParseAdminShellPath(t *testing.T) {
	const cmdPath = `C:\Windows\System32\cmd.exe`
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"prefixed", AdminShellPathPrefix + cmdPath, cmdPath, true},
		{"prefixed uppercase", "ADMIN://" + cmdPath, cmdPath, true},
		{"plain path", cmdPath, "", false},
		{"wsl scheme", "wsl://Ubuntu", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseAdminShellPath(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("ParseAdminShellPath(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestShellNameAdminSuffix(t *testing.T) {
	if got := shellName(AdminShellPathPrefix + `C:\Windows\System32\cmd.exe`); got != "cmd (Admin)" {
		t.Fatalf("shellName(admin cmd) = %q, want %q", got, "cmd (Admin)")
	}
	if got := shellName(`C:\Windows\System32\cmd.exe`); got == "cmd (Admin)" {
		t.Fatalf("shellName(plain cmd) = %q, must not carry the Admin suffix", got)
	}
}

// TestElevatedShellBaseAndAllowlist pins the broker's executable allowlist:
// parse the first token of the spawn command line, then allow only known
// shell executables to run elevated.
func TestElevatedShellBaseAndAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name, cmdline string
		wantBase      string
		wantAllowed   bool
	}{
		{"quoted system32 cmd", `"C:\Windows\System32\cmd.exe" /k echo hi`, "cmd.exe", true},
		{"unquoted wsl", "wsl.exe -d Ubuntu --cd ~", "wsl.exe", true},
		{"unquoted powershell", "powershell.exe -NoExit", "powershell.exe", true},
		{"forward slash path", "C:/Tools/pwsh.exe -NoLogo", "pwsh.exe", true},
		{"disallowed executable", `C:\evil\bad.exe`, "bad.exe", false},
		{"empty", "", "", false},
		{"quotes only", `""`, "", false},
		{"unclosed quote", `"C:\Windows\System32\cmd.exe`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := elevatedShellBase(tc.cmdline)
			if got != tc.wantBase {
				t.Fatalf("elevatedShellBase(%q) = %q, want %q", tc.cmdline, got, tc.wantBase)
			}
			if allowed := isAllowedElevatedShell(got); allowed != tc.wantAllowed {
				t.Fatalf("isAllowedElevatedShell(%q) = %v, want %v", got, allowed, tc.wantAllowed)
			}
		})
	}
}

// TestIsAllowedElevatedShell covers set membership directly, including the
// case-insensitive comparison (Windows file names are case-insensitive).
func TestIsAllowedElevatedShell(t *testing.T) {
	for _, tc := range []struct {
		base string
		want bool
	}{
		{"cmd.exe", true},
		{"netcat.exe", false},
		{"CMD.EXE", true},
		{"", false},
	} {
		if got := isAllowedElevatedShell(tc.base); got != tc.want {
			t.Errorf("isAllowedElevatedShell(%q) = %v, want %v", tc.base, got, tc.want)
		}
	}
}

// TestElevatedSpawnRefusal pins the full spawn validation behind the
// elevated broker (F3): the executable must be an allowlisted shell
// basename whose canonical path sits under a trusted Windows root, and the
// argument tail must match a shape local_session_windows.go generates.
// Everything is checked against the validation function directly — no
// elevation, no ConPTY, no process is ever spawned.
func TestElevatedSpawnRefusal(t *testing.T) {
	sysroot := os.Getenv("SystemRoot")
	if sysroot == "" {
		t.Skip("SystemRoot not set")
	}
	cmdExe := filepath.Join(sysroot, "System32", "cmd.exe")
	psExe := filepath.Join(sysroot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	wslExe := filepath.Join(sysroot, "System32", "wsl.exe")
	t.Setenv("ComSpec", cmdExe)

	// A copy of cmd.exe in the temp dir: same allowlisted basename, wrong
	// location — the F3 privilege-escalation case.
	tmpDir := t.TempDir()
	tmpCmd := filepath.Join(tmpDir, "cmd.exe")
	src, err := os.ReadFile(cmdExe)
	if err != nil {
		t.Fatalf("read %s: %v", cmdExe, err)
	}
	if err := os.WriteFile(tmpCmd, src, 0o755); err != nil {
		t.Fatalf("write copy: %v", err)
	}

	for _, tc := range []struct {
		name    string
		cmdline string
		refused bool
	}{
		{"canonical system32 cmd accepted", `"` + cmdExe + `" /k`, false},
		{"canonical powershell accepted", `"` + psExe + `"`, false},
		{"bare name resolves through PATH", `cmd.exe /k`, false},
		{"wsl launch shape accepted", `"` + wslExe + `" -d Ubuntu --cd ~`, false},
		{"wsl explicit cwd shape accepted", `"` + wslExe + `" -d Ubuntu`, false},
		{"wsl integration shape refused", `"` + wslExe + `" -d Ubuntu --cd ~ -e bash --rcfile /tmp/uniterm-x`, true},
		{"wsl zsh env injection refused", `"` + wslExe + `" -d Ubuntu -e env ZDOTDIR=/tmp/x zsh`, true},
		{"temp-dir copy of cmd.exe refused", `"` + tmpCmd + `" /k`, true},
		{"cmd foreign subcommand refused", `"` + cmdExe + `" /c calc`, true},
		{"cmd /k operand outside clink shape refused", `"` + cmdExe + `" /k & calc`, true},
		{"wsl --exec refused", `"` + wslExe + `" -d Ubuntu --exec calc.exe`, true},
		{"wsl unknown flag refused", `"` + wslExe + `" -d Ubuntu --cd C:\\Users`, true},
		{"disallowed basename refused", `"` + filepath.Join(tmpDir, "bad.exe") + `"`, true},
		{"empty refused", "", true},
		{"unclosed quote refused", `"` + cmdExe, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := elevatedSpawnRefusal(tc.cmdline)
			if (got != "") != tc.refused {
				t.Fatalf("elevatedSpawnRefusal(%q) = %q, want refused=%v", tc.cmdline, got, tc.refused)
			}
		})
	}

	// The clink composition goes through buildClinkCommandLine so the test
	// pins the launcher↔broker contract: the exact line the launcher
	// generates for a trusted clink path passes, the same line pointing at
	// an untrusted clink.exe does not.
	programFiles := os.Getenv("ProgramFiles")
	if programFiles == "" {
		t.Skip("ProgramFiles not set")
	}
	clinkGood := buildClinkCommandLine(filepath.Join(programFiles, "clink", "clink.exe"), filepath.Join(tmpDir, "profile"))
	if got := elevatedSpawnRefusal(clinkGood); got != "" {
		t.Errorf("trusted clink line refused: %s\nline: %s", got, clinkGood)
	}
	clinkBad := buildClinkCommandLine(filepath.Join(tmpDir, "clink.exe"), "")
	if got := elevatedSpawnRefusal(clinkBad); got == "" {
		t.Errorf("untrusted clink line accepted\nline: %s", clinkBad)
	}
}

// TestAdminPtyBrokerRefusesUntrustedShellCopy drives a full spawn frame
// whose CommandLine points at a copy of cmd.exe in the test temp dir — the
// F3 case — through the real broker protocol, without elevation: the
// broker must refuse it before any ConPTY capability check, deliver the
// reason as PTY output, and exit with code 1.
func TestAdminPtyBrokerRefusesUntrustedShellCopy(t *testing.T) {
	sysroot := os.Getenv("SystemRoot")
	if sysroot == "" {
		t.Skip("SystemRoot not set")
	}
	tmpCmd := filepath.Join(t.TempDir(), "cmd.exe")
	src, err := os.ReadFile(filepath.Join(sysroot, "System32", "cmd.exe"))
	if err != nil {
		t.Fatalf("read cmd.exe: %v", err)
	}
	if err := os.WriteFile(tmpCmd, src, 0o755); err != nil {
		t.Fatalf("write copy: %v", err)
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	pipeName := `\\.\pipe\uniterm-pty-test-` + hex.EncodeToString(suffix)

	handle, err := createPtyPipeServer(pipeName)
	if err != nil {
		t.Fatalf("pipe server: %v", err)
	}
	brokerDone := make(chan int, 1)

	p := &adminPty{f: os.NewFile(uintptr(handle), pipeName)}
	defer p.Close()

	go func() { brokerDone <- serveLocalPtyBroker(pipeName) }()

	brokerGone := make(chan struct{})
	if err := p.waitBrokerHello(brokerGone, 10*time.Second); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	specBytes, err := json.Marshal(localPtySpawn{
		CommandLine: `"` + tmpCmd + `" /k`,
		Cols:        80,
		Rows:        25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ptyFrameWriteTo(p.f, ptyFrameSpawn, specBytes); err != nil {
		t.Fatalf("send spawn: %v", err)
	}

	waitForMarker(t, p, "refused", 10*time.Second)

	select {
	case code := <-brokerDone:
		if code != 1 {
			t.Fatalf("broker exit code = %d, want 1", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("broker did not exit after refusing the spawn")
	}
}

// TestAdminPtyBrokerRelay drives the full broker protocol in-process, without
// elevation: the test plays the app side (pipe server, spawn frame, adminPty)
// while serveLocalPtyBroker runs in a goroutine playing the broker.
func TestAdminPtyBrokerRelay(t *testing.T) {
	if !conpty.IsConPtyAvailable() {
		t.Skip("ConPTY not available on this Windows version")
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	pipeName := `\\.\pipe\uniterm-pty-test-` + hex.EncodeToString(suffix)

	handle, err := createPtyPipeServer(pipeName)
	if err != nil {
		t.Fatalf("pipe server: %v", err)
	}
	brokerDone := make(chan int, 1)

	p := &adminPty{f: os.NewFile(uintptr(handle), pipeName)}
	defer p.Close()

	go func() { brokerDone <- serveLocalPtyBroker(pipeName) }()

	// Same handshake as production: broker hello, then the spawn request.
	brokerGone := make(chan struct{})
	if err := p.waitBrokerHello(brokerGone, 10*time.Second); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = `C:\Windows\System32\cmd.exe`
	}
	specBytes, err := json.Marshal(localPtySpawn{
		CommandLine: fmt.Sprintf(`"%s" /k`, comspec),
		Cols:        80,
		Rows:        25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ptyFrameWriteTo(p.f, ptyFrameSpawn, specBytes); err != nil {
		t.Fatalf("send spawn: %v", err)
	}

	// ConPTY suppresses cmd's classic banner, so drive the round trip
	// directly: the echoed command text plus its output must come back
	// through the relay.
	const marker = "UNITERM_ADMIN_RELAY_OK"
	if _, err := p.Write([]byte("echo " + marker + "\r\n")); err != nil {
		t.Fatalf("write input: %v", err)
	}
	waitForMarker(t, p, marker, 20*time.Second)

	if err := p.Resize(100, 30); err != nil {
		t.Fatalf("resize: %v", err)
	}

	// Closing the pipe must make the broker kill the shell and exit — an
	// elevated process must never linger without its owner.
	p.Close()
	select {
	case code := <-brokerDone:
		t.Logf("broker exited with code %d", code)
	case <-time.After(10 * time.Second):
		t.Fatal("broker did not exit after the control pipe closed")
	}
}

// waitForMarker accumulates relayed PTY output until substr appears. The
// watchdog closes the pipe on timeout so a blocked read unwinds and the
// failure path actually fires instead of hanging to the test timeout.
func waitForMarker(t *testing.T, p *adminPty, substr string, timeout time.Duration) {
	t.Helper()
	type outcome struct {
		acc []byte
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		acc := make([]byte, 0, 8192)
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			acc = append(acc, buf[:n]...)
			if bytes.Contains(acc, []byte(substr)) {
				done <- outcome{acc, nil}
				return
			}
			if err != nil {
				done <- outcome{acc, err}
				return
			}
		}
	}()
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("relay closed before %q appeared (saw %q): %v", substr, truncateTestOutput(o.acc), o.err)
		}
	case <-time.After(timeout):
		p.Close()
		t.Fatalf("timed out waiting for %q", substr)
	}
}

func truncateTestOutput(b []byte) string {
	const max = 512
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}

// TestFrameRoundTrip pins the wire format: type/length framing with u16
// little-endian payload lengths.
func TestFrameRoundTrip(t *testing.T) {
	payloads := [][]byte{nil, []byte("x"), bytes.Repeat([]byte{0xAB}, ptyFrameMaxPayload)}
	for _, want := range payloads {
		var buf bytes.Buffer
		if err := ptyFrameWriteTo(&buf, ptyFrameData, want); err != nil {
			t.Fatalf("write frame (len %d): %v", len(want), err)
		}
		gotType, gotPayload, err := ptyFrameReadFrom(&buf)
		if err != nil {
			t.Fatalf("read frame (len %d): %v", len(want), err)
		}
		if gotType != ptyFrameData || !bytes.Equal(gotPayload, want) {
			t.Fatalf("round trip mismatch: type=%d len=%d want len=%d", gotType, len(gotPayload), len(want))
		}
	}
}

// ── F-10: spawn-frame Env / WorkDir / dims ──────────────────────────────────

// The spawn frame arrives from same-user code, but the shell it starts is
// elevated: environment injection (BASH_ENV, PSModulePath, ...) would turn
// the broker into an arbitrary elevated code launcher. Only a tight
// allowlist survives.
func TestFilterElevatedEnv(t *testing.T) {
	keep := []string{
		"WT_SESSION=0",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"LANG=zh_CN.UTF-8",
		"LC_ALL=C",
		"LC_MESSAGES=en_US.UTF-8",
	}
	drop := []string{
		"BASH_ENV=/tmp/pwn.sh",
		"ENV=/tmp/pwn.sh",
		"PSModulePath=C:\\evil\\modules",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.fsmonitor",
		"WSLENV=pwn/u",
		"LD_PRELOAD=/tmp/evil.so",
		"PATH=/evil/bin",
		"PROMPT=$pwned",
		"NOEQUALS",
		"=empty-name",
		"BROKEN=value\x00with-nul",
	}
	in := append(append([]string{}, keep...), drop...)
	// Windows environment names are case-insensitive: the lowercase variant
	// must survive too.
	in = append(in, "wt_session=0")
	out := filterElevatedEnv(in)

	if len(out) != len(keep)+1 {
		t.Fatalf("filterElevatedEnv kept %d entries, want %d: %v", len(out), len(keep)+1, out)
	}
	for _, kv := range keep {
		found := false
		for _, got := range out {
			if strings.EqualFold(got, kv) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("allowlisted entry %q was dropped", kv)
		}
	}
	for _, kv := range out {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		upper := strings.ToUpper(name)
		if !elevatedEnvAllowlist[upper] && !strings.HasPrefix(upper, "LC_") {
			t.Errorf("non-allowlisted entry survived the filter: %q", kv)
		}
	}
	if got := filterElevatedEnv(nil); got != nil {
		t.Fatalf("nil input should stay nil, got %v", got)
	}
}

// The elevated working directory must be a plain local path: no UNC/device
// namespace, no "..", no relative or drive-relative forms.
func TestElevatedWorkDirRefusal(t *testing.T) {
	ok := []string{
		"",
		"C:\\Users\\me\\project",
		"D:/work",
		"C:\\",
	}
	for _, dir := range ok {
		if refusal := elevatedWorkDirRefusal(dir); refusal != "" {
			t.Errorf("elevatedWorkDirRefusal(%q) = %q, want accepted", dir, refusal)
		}
	}
	bad := []struct {
		dir  string
		want string
	}{
		{"C:\\Windows\\System32\\..\\evil", ".."},
		{"C:\\a\\..\\..\\evil", ".."},
		{"..\\evil", "absolute"},
		{"evil", "absolute"},
		{"C:foo", "absolute"},
		{"\\foo", "absolute"},
		{"\\\\server\\share\\dir", "UNC"},
		{"\\\\.\\pipe\\x", "UNC"},
		{"\\\\?\\C:\\evil", "UNC"},
		{"//server/share", "UNC"},
		{"C:\\x\x00y", "NUL"},
	}
	for _, c := range bad {
		refusal := elevatedWorkDirRefusal(c.dir)
		if refusal == "" {
			t.Errorf("elevatedWorkDirRefusal(%q) accepted, want refusal", c.dir)
			continue
		}
		if !strings.Contains(refusal, c.want) {
			t.Errorf("elevatedWorkDirRefusal(%q) = %q, want mention of %q", c.dir, refusal, c.want)
		}
	}
}

func TestClampPtyDims(t *testing.T) {
	cases := []struct{ cols, rows, wantCols, wantRows int }{
		{80, 25, 80, 25},
		{0, 0, 80, 24},
		{-5, -1, 80, 24},
		{1, 1, 1, 1},
		{100000, 100000, maxPtyDim, maxPtyDim},
	}
	for _, c := range cases {
		gotCols, gotRows := clampPtyDims(c.cols, c.rows)
		if gotCols != c.wantCols || gotRows != c.wantRows {
			t.Errorf("clampPtyDims(%d,%d) = %d,%d want %d,%d", c.cols, c.rows, gotCols, gotRows, c.wantCols, c.wantRows)
		}
	}
}

// Full protocol case (F-10): a valid administrator shell command line with an
// unsafe WorkDir must be refused before any ConPTY capability check, with the
// reason delivered as PTY output and broker exit code 1.
func TestAdminPtyBrokerRefusesUnsafeWorkDir(t *testing.T) {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "C:\\Windows\\System32\\cmd.exe"
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	pipeName := `\\.\pipe\uniterm-pty-test-` + hex.EncodeToString(suffix)

	handle, err := createPtyPipeServer(pipeName)
	if err != nil {
		t.Fatalf("pipe server: %v", err)
	}
	brokerDone := make(chan int, 1)

	p := &adminPty{f: os.NewFile(uintptr(handle), pipeName)}
	defer p.Close()

	go func() { brokerDone <- serveLocalPtyBroker(pipeName) }()

	brokerGone := make(chan struct{})
	if err := p.waitBrokerHello(brokerGone, 10*time.Second); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	specBytes, err := json.Marshal(localPtySpawn{
		CommandLine: fmt.Sprintf(`"%s" /k`, comspec),
		WorkDir:     `\\server\share\wd`,
		Cols:        80,
		Rows:        25,
		Env:         []string{"WT_SESSION=0", "BASH_ENV=/tmp/pwn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ptyFrameWriteTo(p.f, ptyFrameSpawn, specBytes); err != nil {
		t.Fatalf("send spawn: %v", err)
	}

	waitForMarker(t, p, "refused", 10*time.Second)

	select {
	case code := <-brokerDone:
		if code != 1 {
			t.Fatalf("broker exit code = %d, want 1", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("broker did not exit after refusing the spawn")
	}
}
