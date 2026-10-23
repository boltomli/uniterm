package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// jobFakeExec implements SSHExecutor for the background path with scriptable
// output / async handle.
type jobFakeExec struct {
	stdout   []byte
	stderr   []byte
	exitCode int
	cmdID    string
}

func TestForegroundToolSchemaUsesUnifiedParameters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wiring := newJobTestServer(&fakeForeground{}, &jobFakeExec{}, func() string { return ModeForeground })
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	wiring.registerTools(server)
	st, ct := sdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{
		"connect":                {"connection_id", "type", "shell_path"},
		"uniterm_exec":           {"session_id", "command", "timeout_ms", "head_lines", "tail_lines"},
		"uniterm_collect_output": {"session_id", "timeout_ms", "head_lines", "tail_lines"},
		"uniterm_capture":        {"session_id", "tail_lines"},
		"uniterm_send_key":       {"session_id", "input", "control", "send_enter"},
		"uniterm_exec_result":    {"job_id", "offset", "max_bytes", "head_lines", "tail_lines"},
		"uniterm_interrupt":      {"job_id", "signal"},
	}
	for _, tool := range listed.Tools {
		if tool.Name == "exec_command" || tool.Name == "get_command_output" || tool.Name == "interrupt_command" {
			t.Fatalf("legacy tool exposed in foreground: %s", tool.Name)
		}
		keys, ok := expected[tool.Name]
		if !ok {
			continue
		}
		b, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
		if len(schema.Properties) != len(keys) {
			t.Fatalf("%s schema: %s", tool.Name, b)
		}
		for _, key := range keys {
			if _, ok := schema.Properties[key]; !ok {
				t.Fatalf("%s missing %s", tool.Name, key)
			}
		}
		delete(expected, tool.Name)
	}
	if len(expected) != 0 {
		t.Fatalf("missing tools: %v", expected)
	}
	result, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "uniterm_exec", Arguments: map[string]any{
		"sessionId": "s1", "command": "ls", "timeoutMs": 10,
	}})
	if err == nil && !result.IsError {
		t.Fatal("old parameter spellings were accepted")
	}
}

func TestForegroundOutputLimitsKeepRawByteOffset(t *testing.T) {
	srv := newJobTestServer(&fakeForeground{}, &jobFakeExec{}, nil)
	zero := 0
	srv.rememberOutputOptions("fg-test-1", &zero, &zero, 50)
	_, out, err := srv.toolUnitermResult(context.Background(), nil, unitermResultIn{JobID: "fg-test-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || !strings.Contains(out.Stdout, "TRUNCATED") || out.NextOffset != len("hello world\n") {
		t.Fatalf("wrong shaped result or byte offset: %+v", out)
	}
	negative := -1
	if _, _, err := srv.toolUnitermExecFg(context.Background(), nil, fgExecIn{SessionID: "s1", Command: "ls", HeadLines: &negative}); err == nil {
		t.Fatal("negative line limits accepted")
	}
}

func TestForegroundConnectOpensLocalOrSavedTerminal(t *testing.T) {
	s := newJobTestServer(nil, nil, func() string { return ModeForeground })
	var got connectTerminalIn
	s.env.OpenTerminal = func(id, kind, shell string) (string, error) {
		got = connectTerminalIn{ConnectionID: id, Type: kind, ShellPath: shell}
		return "new-terminal", nil
	}
	for _, in := range []connectTerminalIn{{Type: "local"}, {ConnectionID: "serial-profile"}, {Type: "wsl", ShellPath: "wsl://Ubuntu"}} {
		_, out, err := s.toolConnectTerminal(context.Background(), nil, in)
		if err != nil || out.SessionID != "new-terminal" || got != in {
			t.Fatalf("connect %+v: %+v %v", in, out, err)
		}
	}
	for _, in := range []connectTerminalIn{{}, {Type: "rdp"}, {Type: "wsl"}, {ConnectionID: "saved", Type: "local"}} {
		if _, _, err := s.toolConnectTerminal(context.Background(), nil, in); err == nil {
			t.Fatalf("accepted invalid connect input: %+v", in)
		}
	}
}

func (f *jobFakeExec) MCPExec(cmd string, syncTimeoutMs, hardTimeoutMs, maxOutput int) ([]byte, []byte, int, string, error) {
	return f.stdout, f.stderr, f.exitCode, f.cmdID, nil
}
func (f *jobFakeExec) MCPLatestOutput(commandID string, offset, max int) ([]byte, []byte, int, string, error) {
	out := f.stdout
	if offset > 0 && offset < len(out) {
		out = out[offset:]
	}
	return out, f.stderr, f.exitCode, "exited", nil
}
func (f *jobFakeExec) MCPInterrupt(commandID string, signal string) error { return nil }

// fakeForeground implements ForegroundJobs.
type fakeForeground struct {
	started     string
	hardLimitMs int
	polled      string
	interrupted string
}

func (f *fakeForeground) StartForeground(sessionID, command string, hardLimitMs int) (string, error) {
	f.started = command
	f.hardLimitMs = hardLimitMs
	return "fg-test-1", nil
}
func (f *fakeForeground) PollForeground(jobID string, offset, max int) ([]byte, []byte, int, string, error) {
	f.polled = jobID
	return []byte("hello world\n"), nil, 0, "exited", nil
}
func (f *fakeForeground) InterruptForeground(jobID, signal string) error {
	f.interrupted = jobID
	return nil
}

func newJobTestServer(fg ForegroundJobs, exec SSHExecutor, execMode func() string) *Server {
	return &Server{env: Env{
		Sessions:   func(string) (SSHExecutor, bool) { return exec, true },
		Commands:   func(string) (string, SSHExecutor, bool) { return "sid", exec, true },
		Policy:     func() Policy { return PolicyBypass },
		Foreground: fg,
		ExecMode:   execMode,
	}, finishedBg: map[string]finishedBackground{}}
}

func TestUnitermExecBackgroundSyncCached(t *testing.T) {
	exec := &jobFakeExec{stdout: []byte("output line\n"), exitCode: 0}
	srv := newJobTestServer(nil, exec, nil)

	_, out, err := srv.toolUnitermExecBg(context.Background(), nil, bgExecIn{
		SessionID: "s1", Command: "ls",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != ModeBackground || out.State != "exited" {
		t.Fatalf("out=%+v", out)
	}
	if out.JobID == "" || out.Stdout != "output line\n" {
		t.Fatalf("out=%+v", out)
	}

	// Poll through the unified result tool: the sync result must be cached.
	_, result, err := srv.toolUnitermResult(context.Background(), nil, unitermResultIn{JobID: out.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "exited" || result.Stdout != "output line\n" {
		t.Fatalf("result=%+v", result)
	}
}

func TestUnitermExecBackgroundAsyncCommandID(t *testing.T) {
	exec := &jobFakeExec{cmdID: "ssh-cmd-9", exitCode: -1}
	srv := newJobTestServer(nil, exec, nil)
	_, out, err := srv.toolUnitermExecBg(context.Background(), nil, bgExecIn{SessionID: "s1", Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Async || out.JobID != "ssh-cmd-9" || out.State != "running" {
		t.Fatalf("out=%+v", out)
	}
	_, result, err := srv.toolUnitermResult(context.Background(), nil, unitermResultIn{JobID: "ssh-cmd-9"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != ModeBackground || result.State != "exited" {
		t.Fatalf("result=%+v", result)
	}
}

func TestUnitermExecForegroundRoutesToTap(t *testing.T) {
	fg := &fakeForeground{}
	srv := newJobTestServer(fg, &jobFakeExec{}, func() string { return ModeForeground })

	_, out, err := srv.toolUnitermExecFg(context.Background(), nil, fgExecIn{
		SessionID: "s1", Command: "top",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.JobID != "fg-test-1" || !out.Async || out.Mode != ModeForeground {
		t.Fatalf("out=%+v", out)
	}
	if fg.started != "top" {
		t.Fatalf("foreground not started: %q", fg.started)
	}
	// timeoutMs defaults to the built-in-panel-aligned 60s.
	if fg.hardLimitMs != int(DefaultFgTimeout/time.Millisecond) {
		t.Fatalf("timeout mapping wrong: hardLimitMs=%d", fg.hardLimitMs)
	}

	_, result, err := srv.toolUnitermResult(context.Background(), nil, unitermResultIn{JobID: "fg-test-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != ModeForeground || result.Stdout != "hello world\n" {
		t.Fatalf("result=%+v", result)
	}
	if fg.polled != "fg-test-1" {
		t.Fatalf("not routed to foreground poller")
	}

	_, _, err = srv.toolUnitermInterrupt(context.Background(), nil, unitermInterruptIn{JobID: "fg-test-1"})
	if err != nil {
		t.Fatal(err)
	}
	if fg.interrupted != "fg-test-1" {
		t.Fatal("foreground interrupt not routed")
	}
}

func TestUnitermExecForegroundUnsupported(t *testing.T) {
	srv := newJobTestServer(nil, &jobFakeExec{}, func() string { return ModeForeground })
	_, _, err := srv.toolUnitermExecFg(context.Background(), nil, fgExecIn{SessionID: "s1", Command: "x"})
	if err == nil {
		t.Fatal("expected error for unsupported foreground")
	}
}

func TestExecModeDefaultsToBackground(t *testing.T) {
	srv := newJobTestServer(nil, &jobFakeExec{}, nil)
	if srv.execMode() != ModeBackground {
		t.Fatal("nil ExecMode must fall back to background")
	}
	srv2 := newJobTestServer(nil, &jobFakeExec{}, func() string { return "weird" })
	if srv2.execMode() != ModeBackground {
		t.Fatal("unknown ExecMode value must fall back to background")
	}
}

func TestUnitermCapture(t *testing.T) {
	srv := newJobTestServer(nil, &jobFakeExec{}, func() string { return ModeForeground })
	captured := ""
	srv.env.Capture = func(sessionID string, tailLines int) (string, error) {
		captured = sessionID + ":" + string(rune(tailLines))
		return "screen text", nil
	}
	_, out, err := srv.toolUnitermCapture(context.Background(), nil, unitermCaptureIn{SessionID: "s1", TailLines: 30})
	if err != nil {
		t.Fatal(err)
	}
	if out.Screen != "screen text" || captured == "" {
		t.Fatalf("out=%+v captured=%q", out, captured)
	}
}

func TestUnitermCollectUsesSharedObserver(t *testing.T) {
	srv := newJobTestServer(nil, &jobFakeExec{}, func() string { return ModeForeground })
	called := false
	srv.env.Collect = func(sid string, timeout int) (string, error) {
		called = true
		if sid != "s1" || timeout != 30000 {
			t.Fatalf("observer arguments: %s %d", sid, timeout)
		}
		return "fg-observer", nil
	}
	_, out, err := srv.toolUnitermCollect(context.Background(), nil, unitermCollectIn{SessionID: "s1"})
	if err != nil || !called || out.JobID != "fg-observer" || !out.Async {
		t.Fatalf("collection: %+v %v called=%v", out, err, called)
	}
	if _, _, err := srv.toolUnitermCollect(context.Background(), nil, unitermCollectIn{}); err == nil {
		t.Fatal("missing session should fail")
	}
}

func TestUnitermSendKey(t *testing.T) {
	srv := newJobTestServer(nil, &jobFakeExec{}, func() string { return ModeForeground })
	var gotText, gotControl string
	gotEnter := false
	srv.env.SendKey = func(sessionID, text, control string, sendEnter bool) error {
		gotText, gotControl, gotEnter = text, control, sendEnter
		return nil
	}
	_, out, err := srv.toolUnitermSendKey(context.Background(), nil, unitermSendKeyIn{SessionID: "s1", Input: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Sent || gotText != "y" || !gotEnter {
		t.Fatalf("out=%+v text=%q enter=%v", out, gotText, gotEnter)
	}
	_, _, err = srv.toolUnitermSendKey(context.Background(), nil, unitermSendKeyIn{SessionID: "s1", Control: "ctrl_c"})
	if err != nil {
		t.Fatal(err)
	}
	if gotControl != "ctrl_c" {
		t.Fatalf("control=%q", gotControl)
	}
	// Neither text nor control → error.
	if _, _, err := srv.toolUnitermSendKey(context.Background(), nil, unitermSendKeyIn{SessionID: "s1"}); err == nil {
		t.Fatal("expected error for empty send-key input")
	}
	// Unknown control → error.
	if _, _, err := srv.toolUnitermSendKey(context.Background(), nil, unitermSendKeyIn{SessionID: "s1", Control: "ctrl_x"}); err == nil {
		t.Fatal("expected error for unknown control")
	}
}
