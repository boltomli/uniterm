package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"strings"
	"testing"
	"time"

	"github.com/ys-ll/uniterm/backend/mcp"
	"github.com/ys-ll/uniterm/backend/session"
	"github.com/ys-ll/uniterm/backend/store"
)

func TestMCPForegroundDiscoversAllTextTerminals(t *testing.T) {
	dir := t.TempDir()
	cs, err := store.NewConnectionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{sessionManager: session.NewSessionManager(), connectionStore: cs, localStateStore: store.NewLocalStateStore(dir)}
	profiles := []session.ConnectionConfig{}
	for _, kind := range []string{"ssh", "local", "wsl", "telnet", "mosh", "serial", "tcp", "sftp"} {
		cfg := session.ConnectionConfig{ID: kind, Type: kind, Name: kind}
		profiles = append(profiles, cfg)
		if _, err := a.sessionManager.Create(kind, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if err := cs.Save(session.ConnectionStoreData{Connections: profiles}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode string
		want int
	}{{"foreground", 7}, {"background", 1}} {
		if err := a.localStateStore.Save(store.LocalState{MCP: &store.MCPSettings{ExecMode: tc.mode}}); err != nil {
			t.Fatal(err)
		}
		listed, saved := a.mcpListSessions(), a.mcpListConnections()
		if len(listed) != tc.want || len(saved) != tc.want {
			t.Fatalf("%s: sessions=%d profiles=%d", tc.mode, len(listed), len(saved))
		}
		for _, row := range listed {
			if row.Type == "sftp" {
				t.Fatal("file session exposed as terminal")
			}
		}
	}
}

func TestMcpNotifyStrings(t *testing.T) {
	en := mcpNotifyStringsFor("en")
	zh := mcpNotifyStringsFor("zh-CN")
	if en.Title != "uniTerm · MCP approval request" {
		t.Errorf("en title = %q", en.Title)
	}
	if !strings.Contains(zh.BodyConnect, "请求建立新连接") {
		t.Errorf("zh connect template = %q", zh.BodyConnect)
	}
	if want := fmt.Sprintf(zh.BodyConnect, "cc"); mcpNotifyBody(zh, "cc", "") != want {
		t.Errorf("zh connect body = %q, want %q", mcpNotifyBody(zh, "cc", ""), want)
	}
	if got := mcpNotifyBody(en, "cc", "ls -la"); got != "cc: ls -la" {
		t.Errorf("short command preview = %q", got)
	}
	long := string(make([]byte, 100))
	if got := mcpNotifyBody(en, "cc", long); !strings.HasSuffix(got, "…") {
		t.Errorf("long command should end with ellipsis, got %q", got)
	}
	if mcpNotifyStringsFor("xx-unknown") != mcpNotifyLabels["en"] {
		t.Errorf("unknown language must fall back to English")
	}
}

// ── MCP file transfer waiters (see app_mcp.go) ──────────────────────────

func newMcpTransferTestApp() *App {
	return &App{
		mcpTransferWaiters: make(map[string]chan mcpTransferOutcome),
	}
}

func TestMcpRunTransferSuccess(t *testing.T) {
	a := newMcpTransferTestApp()
	go func() {
		time.Sleep(20 * time.Millisecond)
		a.mcpDispatchTransferEvent(map[string]any{"taskId": "t1", "event": "complete", "status": "done"})
	}()
	if err := a.mcpRunTransfer(func() (string, error) { return "t1", nil }, 2*time.Second); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestMcpRunTransferErrorCarriesMessage(t *testing.T) {
	a := newMcpTransferTestApp()
	go func() {
		time.Sleep(20 * time.Millisecond)
		a.mcpDispatchTransferEvent(map[string]any{"taskId": "t2", "event": "complete", "status": "error", "error": "permission denied"})
	}()
	err := a.mcpRunTransfer(func() (string, error) { return "t2", nil }, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected error carrying event message, got %v", err)
	}
}

// The completion event may fire between start() returning and the waiter
// registering — the run-transfer mutex must make dispatch wait for the
// registration instead of dropping the event. Simulated deterministically:
// the worker goroutine dispatches (and blocks on the held mutex) while
// start() is still running.
func TestMcpRunTransferCompletionDuringStart(t *testing.T) {
	a := newMcpTransferTestApp()
	err := a.mcpRunTransfer(func() (string, error) {
		go a.mcpDispatchTransferEvent(map[string]any{"taskId": "t3", "event": "complete", "status": "done"})
		time.Sleep(50 * time.Millisecond) // let the dispatch block on the mutex
		return "t3", nil
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("expected success despite completion during start, got %v", err)
	}
}

func TestMcpRunTransferStartError(t *testing.T) {
	a := newMcpTransferTestApp()
	err := a.mcpRunTransfer(func() (string, error) { return "", fmt.Errorf("start failed") }, 2*time.Second)
	if err == nil || err.Error() != "start failed" {
		t.Fatalf("expected start error passthrough, got %v", err)
	}
}

func TestMcpRunTransferTimeout(t *testing.T) {
	a := newMcpTransferTestApp()
	err := a.mcpRunTransfer(func() (string, error) { return "t4", nil }, 30*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

// Non-complete events must not resolve a waiter.
func TestMcpDispatchIgnoresProgressEvents(t *testing.T) {
	a := newMcpTransferTestApp()
	a.mcpDispatchTransferEvent(map[string]any{"taskId": "t5", "event": "start", "status": "done"})
	if len(a.mcpTransferWaiters) != 0 {
		t.Fatalf("waiter map should stay empty without registered waiters, got %d", len(a.mcpTransferWaiters))
	}
}

// ── Token cache (mcpResolveToken) ──────────────────────────────────────────

// Regression (security.md low): mcpResolveToken called fi.ModTime() after
// os.Stat failed, so with no mcp.json every MCP request panicked (nil
// dereference) and the client saw a dropped connection instead of a 401.
func TestMcpResolveTokenWithoutFile(t *testing.T) {
	a := &App{dataDir: t.TempDir()} // no mcp.json yet
	if name, scope := a.mcpResolveToken("deadbeef"); name != "" || scope != "" {
		t.Fatalf("resolve without mcp.json = %q/%q, want empty", name, scope)
	}
}

func TestMcpResolveTokenHotReload(t *testing.T) {
	dir := t.TempDir()
	a := &App{dataDir: dir}
	if name, _ := a.mcpResolveToken("h1"); name != "" {
		t.Fatalf("unknown hash = %q, want empty", name)
	}
	buf := []byte(`{"tokens":[{"name":"claude","hash":"h1","scope":"files"}]}`)
	if err := os.WriteFile(filepath.Join(dir, mcpTokensFileName), buf, 0600); err != nil {
		t.Fatal(err)
	}
	if name, scope := a.mcpResolveToken("h1"); name != "claude" || scope != "files" {
		t.Fatalf("after writing mcp.json: resolve = %q/%q, want claude/files", name, scope)
	}

	// F-06: an external rewrite is picked up on the next resolve even when
	// the mtime cannot move (same-second write) — the size participates in
	// the cache key.
	buf2 := []byte(`{"tokens":[{"name":"other","hash":"h2"}]}`)
	if err := os.WriteFile(filepath.Join(dir, mcpTokensFileName), buf2, 0600); err != nil {
		t.Fatal(err)
	}
	if name, _ := a.mcpResolveToken("h1"); name != "" {
		t.Fatalf("revoked h1 still resolves to %q", name)
	}
}

// F-06: revocation must take effect on the very next resolve — no sleep, no
// mtime luck — because mcpWriteTokens (the UI revocation path) resets the
// cache directly after the file write.
func TestMcpResolveTokenRevocationIsImmediate(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	write := func(recs ...MCPTokenRecord) {
		t.Helper()
		if err := a.mcpWriteTokens(MCPTokensFile{Tokens: recs}); err != nil {
			t.Fatalf("write tokens: %v", err)
		}
	}
	write(MCPTokenRecord{Name: "agent", Hash: "hx", Scope: "exec"})
	if name, scope := a.mcpResolveToken("hx"); name != "agent" || scope != "exec" {
		t.Fatalf("resolve = %q/%q, want agent/exec", name, scope)
	}
	write() // revoke
	if name, _ := a.mcpResolveToken("hx"); name != "" {
		t.Fatalf("revoked token still resolves to %q", name)
	}
	// Same-size rewrite (rename only) must also invalidate: mcpWriteTokens
	// re-stamps the cache from the written file.
	write(MCPTokenRecord{Name: "agent2", Hash: "hx"})
	if name, _ := a.mcpResolveToken("hx"); name != "agent2" {
		t.Fatalf("re-minted token resolves to %q, want agent2", name)
	}
}

// ── Approval dialog serialization ─────────────────────────────────────────

// Regression (security.md high, P1): the backend fired mcp:approval-request
// for every tool call independently, so a second request replaced the dialog
// the user was already reading. Requests must be shown one at a time; a
// queued request is not even registered until the dialog in front of the
// user has been answered.
func TestMcpApproveSerializesDialogs(t *testing.T) {
	a := &App{mcpApprovals: make(map[string]chan mcpApprovalVerdict)}
	pending := func(id string) bool {
		a.mcpApprovalsMu.Lock()
		defer a.mcpApprovalsMu.Unlock()
		_, ok := a.mcpApprovals[id]
		return ok
	}
	waitFor := func(id string, want bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if pending(id) == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("request %s pending=%v never happened", id, want)
	}

	errA := make(chan error, 1)
	go func() { errA <- a.mcpApprove(mcp.ApprovalRequest{ID: "req-a"}) }()
	waitFor("req-a", true)

	// Second request arrives while A's dialog is open.
	errB := make(chan error, 1)
	go func() { errB <- a.mcpApprove(mcp.ApprovalRequest{ID: "req-b"}) }()
	time.Sleep(50 * time.Millisecond)
	if pending("req-b") {
		t.Fatal("second request must stay queued while the first dialog is open")
	}
	if err := a.ResolveMCPApproval("req-b", true, ""); err == nil {
		t.Fatal("a queued request must not be resolvable before it is shown")
	}

	if err := a.ResolveMCPApproval("req-a", true, ""); err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	if err := <-errA; err != nil {
		t.Fatalf("A must be approved, got %v", err)
	}

	// B reaches the front of the queue with its own id.
	waitFor("req-b", true)
	if err := a.ResolveMCPApproval("req-b", true, ""); err != nil {
		t.Fatalf("resolve B: %v", err)
	}
	if err := <-errB; err != nil {
		t.Fatalf("B must be approved, got %v", err)
	}
}

// ── SaveLocalState → MCP endpoint stability ───────────────────────────────

// freeLoopbackPort reserves a loopback port with :0 and releases it, so a
// test can hand a real (currently free) port to the MCP listener.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	return port
}

// tcpConnAlive reports whether c is still connected. A read that hits a
// freshly set deadline means "nothing yet, link intact"; EOF or a reset
// means the server closed it. The deadline is reset on every probe so an
// earlier timeout can never mask a later close.
func tcpConnAlive(t *testing.T, c net.Conn) bool {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 1)
	_, err := c.Read(buf)
	if err == nil {
		return false // unexpected payload on a raw connection
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	return false
}

func mcpTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	a := NewApp(t.TempDir())
	a.dataDir = dir
	a.localStateStore = store.NewLocalStateStore(dir)
	t.Cleanup(func() {
		if a.mcpServer != nil {
			a.mcpServer.Stop()
		}
	})
	return a
}

// Regression (security.md medium): every SaveLocalState called StartMCP,
// which stopped and re-bound the listener — dropping every connected MCP
// client and its in-flight tool calls. localStateStore.update fires on each
// tab change, sidebar toggle and slider drag, so this happened constantly.
// Saving the same endpoint repeatedly must leave client connections alone.
func TestSaveLocalStateKeepsMCPClientWhenEndpointUnchanged(t *testing.T) {
	a := mcpTestApp(t)
	port := freeLoopbackPort(t)
	state := store.LocalState{MCP: &store.MCPSettings{Enabled: true, Port: port}}

	if err := a.SaveLocalState(state); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if !a.GetMCPStatus().Running {
		t.Fatal("MCP endpoint must be running after the first save")
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial MCP endpoint: %v", err)
	}
	defer conn.Close()

	for i := range 3 {
		if err := a.SaveLocalState(state); err != nil {
			t.Fatalf("save %d: %v", i+1, err)
		}
	}
	if !tcpConnAlive(t, conn) {
		t.Fatal("SaveLocalState with an unchanged MCP endpoint dropped a live client connection")
	}
}

// Changing the configured port still has to restart the endpoint: the old
// listener must go away and the new port must accept connections.
func TestSaveLocalStateRestartsMCPWhenPortChanges(t *testing.T) {
	a := mcpTestApp(t)
	oldPort := freeLoopbackPort(t)
	state := store.LocalState{MCP: &store.MCPSettings{Enabled: true, Port: oldPort}}

	if err := a.SaveLocalState(state); err != nil {
		t.Fatalf("first save: %v", err)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(oldPort)))
	if err != nil {
		t.Fatalf("dial MCP endpoint: %v", err)
	}
	defer conn.Close()

	newPort := freeLoopbackPort(t)
	state.MCP.Port = newPort
	if err := a.SaveLocalState(state); err != nil {
		t.Fatalf("save with new port: %v", err)
	}
	if tcpConnAlive(t, conn) {
		t.Fatal("old listener must be stopped when the configured port changes")
	}
	if !a.GetMCPStatus().Running || a.GetMCPStatus().Port != newPort {
		t.Fatalf("status after port change = %+v, want running on %d", a.GetMCPStatus(), newPort)
	}
	if c2, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(newPort))); err != nil {
		t.Fatalf("dial new MCP endpoint: %v", err)
	} else {
		c2.Close()
	}
}

// Disabling the endpoint stops it — and a later enable must bring it back.
func TestSaveLocalStateStopsMCPWhenDisabled(t *testing.T) {
	a := mcpTestApp(t)
	port := freeLoopbackPort(t)
	if err := a.SaveLocalState(store.LocalState{MCP: &store.MCPSettings{Enabled: true, Port: port}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := a.SaveLocalState(store.LocalState{MCP: &store.MCPSettings{Enabled: false, Port: port}}); err != nil {
		t.Fatalf("save disabled: %v", err)
	}
	if a.GetMCPStatus().Running {
		t.Fatal("endpoint must stop when MCP is disabled")
	}
	if err := a.SaveLocalState(store.LocalState{MCP: &store.MCPSettings{Enabled: true, Port: port}}); err != nil {
		t.Fatalf("save enabled: %v", err)
	}
	if !a.GetMCPStatus().Running {
		t.Fatal("endpoint must start again when MCP is re-enabled")
	}
}

// F-06 / F-12.1: mcp-audit.log must not grow without bound — it records full
// command lines and lives for as long as the data dir. Size-based rotation
// keeps the current file under the cap and preserves one generation.
func TestMCPAuditRotatesBySize(t *testing.T) {
	old := maxAuditBytes
	maxAuditBytes = 2048
	t.Cleanup(func() { maxAuditBytes = old })

	a := &App{dataDir: t.TempDir()}
	entry := mcp.AuditEntry{
		Time:    time.Now().Format(time.RFC3339),
		Token:   "agent",
		Tool:    "exec_command",
		Command: strings.Repeat("x", 128),
	}
	for i := 0; i < 200; i++ {
		a.mcpAudit(entry)
	}

	cur := filepath.Join(a.dataDir, mcpAuditFileName)
	fi, err := os.Stat(cur)
	if err != nil {
		t.Fatalf("stat current log: %v", err)
	}
	if fi.Size() >= maxAuditBytes+512 {
		t.Fatalf("current log grew to %d without rotating (cap %d)", fi.Size(), maxAuditBytes)
	}
	if _, err := os.Stat(cur + ".1"); err != nil {
		t.Fatalf("rotated generation missing: %v", err)
	}
}

// Opt in because this starts a real ConPTY shell. This checks the exact path
// used by the built-in assistant, including terminal control-sequence handling.
func TestForegroundCompletionOnRealWindowsTerminal(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows ConPTY")
	}
	if os.Getenv("UNITERM_TEST_PTY") != "1" {
		t.Skip("set UNITERM_TEST_PTY=1 to test real local terminals")
	}
	for _, tc := range []struct{ name, path, command, check string }{
		{"git-bash", `C:\Program Files\Git\bin\bash.exe`, `__ut_keep=kept; printf 'first\n'; sleep 1.1; printf 'last\n'; (exit 7)`, `printf '%s\n' "$__ut_keep"`},
		{"powershell", "powershell.exe", `$__ut_keep='kept'; Write-Output 'first'; Start-Sleep -Milliseconds 1100; Get-Item .; Write-Output 'last'; cmd /c exit 7`, `Write-Output $__ut_keep`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := session.NewLocalSession("test-" + tc.name)
			if err := s.Connect(session.ConnectionConfig{ShellPath: tc.path}); err != nil {
				t.Fatal(err)
			}
			defer s.Disconnect()
			var rawMu sync.Mutex
			var raw strings.Builder
			// A headless test has no xterm.js to answer cursor-position probes.
			off := s.AddDataTap(func(data []byte) {
				rawMu.Lock()
				raw.Write(data)
				if strings.Contains(string(data), "\x1b[6n") {
					_ = s.Write([]byte("\x1b[1;1R"))
				}
				rawMu.Unlock()
			})
			defer off()
			pty := ptyAdapter{s: s, taps: s}
			r := mcp.NewRegistry()
			started := time.Now()
			job, err := r.StartWithScreen(pty, s.ID(), tc.command, newlineFor(s), 8*time.Second, 0, screenCols, screenRows)
			if err != nil {
				t.Fatal(err)
			}
			wait := func(job *mcp.Job, wanted int, text string) {
				t.Helper()
				defer r.Interrupt(job.ID)
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					out, _, code, state, err := job.Snapshot()
					if state != mcp.StateRunning {
						found := false
						for _, line := range strings.Split(string(out), "\n") {
							found = found || strings.TrimSpace(line) == text
						}
						if state != mcp.StateExited || code != wanted || !found {
							rawMu.Lock()
							rawText := raw.String()
							if len(rawText) > 2000 {
								rawText = rawText[len(rawText)-2000:]
							}
							t.Logf("raw tail=%q", rawText)
							rawMu.Unlock()
							t.Fatalf("state=%s code=%d err=%v output=%q", state, code, err, out)
						}
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Fatal("completion not received")
			}
			wait(job, 7, "last")
			if time.Since(started) < time.Second {
				t.Fatal("returned during command pause")
			}
			// Dot/eval must preserve variables in the user's current shell.
			next, err := r.StartWithScreen(pty, s.ID(), tc.check, newlineFor(s), 8*time.Second, 0, screenCols, screenRows)
			if err != nil {
				t.Fatal(err)
			}
			wait(next, 0, "kept")
		})
	}
}
