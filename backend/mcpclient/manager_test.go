package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ys-ll/uniterm/backend/store"
)

// fakeSession stands in for *mcp.ClientSession in tests; it returns a fixed tool
// list and echoes the called tool name/args.
type fakeSession struct {
	tools []*mcp.Tool
	calls []string
}

func (f *fakeSession) ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: f.tools}, nil
}

func (f *fakeSession) CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	f.calls = append(f.calls, params.Name)
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "echo:" + params.Name},
	}}, nil
}

func (f *fakeSession) Close() error { return nil }

func TestQualifiedName(t *testing.T) {
	cases := map[string]string{
		"fs":          "mcp__fs",
		"File Server": "mcp__file_server",
		"a/b:c":       "mcp__a_b_c",
	}
	for in, want := range cases {
		if got := QualifiedName(in, "read_file"); got != want+"__read_file" {
			t.Errorf("QualifiedName(%q,read_file)=%q want %q", in, got, want+"__read_file")
		}
	}
	if _, _, ok := splitQualified("plain_tool"); ok {
		t.Error("plain tool must not parse as qualified")
	}
	server, tool, ok := splitQualified("mcp__fs__read_file")
	if !ok || server != "fs" || tool != "read_file" {
		t.Errorf("splitQualified = %q,%q,%v", server, tool, ok)
	}
}

func TestManagerListAndCall(t *testing.T) {
	fs := &fakeSession{tools: []*mcp.Tool{
		{Name: "read_file", Description: "read a file", InputSchema: map[string]any{"type": "object"}},
		{Name: "write_file", Description: "write a file"},
	}}
	m := NewManager(store.NewMCPClientStore(t.TempDir()))
	m.dial = func(ctx context.Context, cfg store.MCPClientConfig) (Session, error) { return fs, nil }

	ctx := context.Background()
	m.ensureConn(ctx, store.MCPClientConfig{Name: "fs", Transport: store.MCPTransportStdio, Command: "npx", Enabled: true})

	tools := m.ListTools()
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
	if tools[0].QualifiedName != "mcp__fs__read_file" {
		t.Errorf("qualified = %q", tools[0].QualifiedName)
	}

	out, err := m.CallTool(ctx, "mcp__fs__read_file", map[string]any{"path": "/tmp/a"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if out != "echo:read_file" {
		t.Errorf("out=%q", out)
	}
	if fs.calls[0] != "read_file" {
		t.Errorf("remote tool called = %q", fs.calls[0])
	}

	// A non-qualified name must be rejected.
	if _, err := m.CallTool(ctx, "read_file", nil); err == nil {
		t.Error("plain name should error")
	}
}

func TestManagerRecordsDialFailure(t *testing.T) {
	wantErr := errors.New("boom")
	m := NewManager(store.NewMCPClientStore(t.TempDir()))
	m.dial = func(ctx context.Context, cfg store.MCPClientConfig) (Session, error) { return nil, wantErr }
	m.ensureConn(context.Background(), store.MCPClientConfig{Name: "bad", Transport: store.MCPTransportStdio, Command: "x", Enabled: true})

	if tools := m.ListTools(); len(tools) != 0 {
		t.Errorf("failed conn must not list tools, got %d", len(tools))
	}
	statuses := m.Statuses([]store.MCPClientConfig{{Name: "bad", Enabled: true}})
	if len(statuses) != 1 || statuses[0].Connected || statuses[0].Error == "" {
		t.Errorf("statuses=%+v", statuses)
	}
}

// TestInMemoryRoundTrip connects the manager's dialer to a real SDK server over
// in-memory transports, lists tools, and calls one — proving the SDK wiring
// (ListTools/CallTool) works, not just the fake session.
func TestInMemoryRoundTrip(t *testing.T) {
	ctx := context.Background()

	echo := func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:hi"}}}, nil
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "ping",
		Description: "echo back",
		InputSchema: map[string]any{"type": "object"},
	}, echo)

	client := mcp.NewClient(&mcp.Implementation{Name: "uniterm-test", Version: "1.0.0"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	m := NewManager(store.NewMCPClientStore(t.TempDir()))
	m.dial = func(context.Context, store.MCPClientConfig) (Session, error) { return cs, nil }

	m.ensureConn(ctx, store.MCPClientConfig{Name: "echo", Transport: store.MCPTransportStdio, Enabled: true})

	tools := m.ListTools()
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("tools=%+v", tools)
	}
	if tools[0].QualifiedName != "mcp__echo__ping" {
		t.Fatalf("qualified=%q", tools[0].QualifiedName)
	}

	out, err := m.CallTool(ctx, "mcp__echo__ping", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out != "echo:hi" {
		t.Fatalf("out=%q", out)
	}
}

// F-03: DiscoveredTool.Trusted mirrors the owning server's config so the
// built-in agent can gate its per-call confirmation on it. Untrusted (the
// default) must stay false — every external tool call then prompts.
func TestManagerPropagatesTrustedFlag(t *testing.T) {
	fs := &fakeSession{tools: []*mcp.Tool{{Name: "write_file"}}}
	m := NewManager(store.NewMCPClientStore(t.TempDir()))
	m.dial = func(ctx context.Context, cfg store.MCPClientConfig) (Session, error) { return fs, nil }

	ctx := context.Background()
	m.ensureConn(ctx, store.MCPClientConfig{
		Name: "plain", Transport: store.MCPTransportStdio, Command: "x", Enabled: true,
	})
	m.ensureConn(ctx, store.MCPClientConfig{
		Name: "trusted", Transport: store.MCPTransportStdio, Command: "x", Enabled: true, Trusted: true,
	})

	byServer := map[string]bool{}
	for _, tl := range m.ListTools() {
		byServer[tl.Server] = tl.Trusted
	}
	if len(byServer) != 2 {
		t.Fatalf("want tools from 2 servers, got %v", byServer)
	}
	if byServer["plain"] {
		t.Error("untrusted server must not surface trusted tools (F-03)")
	}
	if !byServer["trusted"] {
		t.Error("trusted server must surface trusted tools (F-03)")
	}
}

// dyingSession simulates issue #1174: the session works at first, then the
// transport drops (EOF on every call), mimicking a broken SSE long-poll.
type dyingSession struct {
	tools   []*mcp.Tool
	dead    atomic.Bool
	dials   atomic.Int32 // incremented by the dialer
	calls   []string
	callsMu sync.Mutex
}

func (f *dyingSession) ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: f.tools}, nil
}

func (f *dyingSession) CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	f.callsMu.Lock()
	f.calls = append(f.calls, params.Name)
	f.callsMu.Unlock()
	if f.dead.Load() {
		return nil, fmt.Errorf("connection closed: calling %q: client is closing: EOF", params.Name)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "echo:" + params.Name},
	}}, nil
}

func (f *dyingSession) Close() error { return nil }

func TestCallToolReconnectsAfterConnDrop(t *testing.T) {
	s1 := &dyingSession{tools: []*mcp.Tool{{Name: "vault_list", Description: "list"}}}
	m := NewManager(store.NewMCPClientStore(t.TempDir()))
	var dials atomic.Int32
	m.dial = func(ctx context.Context, cfg store.MCPClientConfig) (Session, error) {
		dials.Add(1)
		// First dial returns the original session; the reconnect returns a
		// FRESH live session, like a real SDK redial would.
		if dials.Load() == 1 {
			return s1, nil
		}
		return &dyingSession{tools: s1.tools}, nil
	}
	ctx := context.Background()
	m.ensureConn(ctx, store.MCPClientConfig{Name: "notes", Transport: store.MCPTransportSSE, URL: "http://x/sse", Enabled: true})

	// Before the drop: works.
	if out, err := m.CallTool(ctx, "mcp__notes__vault_list", nil); err != nil || out != "echo:vault_list" {
		t.Fatalf("pre-drop call: out=%q err=%v", out, err)
	}

	// The SSE stream drops. Pre-fix behavior: EOF forever. Fixed: one redial
	// and the call retries transparently.
	s1.dead.Store(true)
	out, err := m.CallTool(ctx, "mcp__notes__vault_list", nil)
	if err != nil {
		t.Fatalf("post-drop call must reconnect+retry, got: %v", err)
	}
	if out != "echo:vault_list" {
		t.Fatalf("retry out=%q", out)
	}
	// The dialer ran exactly once more (the reconnect), not once per call.
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials = %d, want 2 (initial + one reconnect)", got)
	}
}

func TestCallToolReconnectFailureIsActionable(t *testing.T) {
	s1 := &dyingSession{tools: []*mcp.Tool{{Name: "vault_list"}}}
	dialErr := errors.New("dial tcp 1.2.3.4: connection refused")
	m := NewManager(store.NewMCPClientStore(t.TempDir()))
	var dials atomic.Int32
	m.dial = func(ctx context.Context, cfg store.MCPClientConfig) (Session, error) {
		if dials.Add(1) == 1 {
			return s1, nil
		}
		return nil, dialErr
	}
	ctx := context.Background()
	m.ensureConn(ctx, store.MCPClientConfig{Name: "notes", Transport: store.MCPTransportSSE, URL: "http://x/sse", Enabled: true})

	s1.dead.Store(true)
	_, err := m.CallTool(ctx, "mcp__notes__vault_list", nil)
	if err == nil {
		t.Fatal("want error when reconnect also fails")
	}
	// Actionable: names the server, URL, transport, both errors, and a hint.
	msg := err.Error()
	for _, want := range []string{"notes", "http://x/sse", "sse", "reconnect failed", "EOF", "settings"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
	// The conn is marked failed so Statuses surfaces it and ListTools
	// stops reporting the stale tool list.
	if tools := m.ListTools(); len(tools) != 0 {
		t.Errorf("failed conn must stop listing tools, got %d", len(tools))
	}
	st := m.Statuses([]store.MCPClientConfig{{Name: "notes", Enabled: true}})
	if len(st) != 1 || st[0].Connected || st[0].Error == "" {
		t.Errorf("statuses=%+v", st)
	}
}

func TestConcurrentCallsTriggerSingleReconnect(t *testing.T) {
	s1 := &dyingSession{tools: []*mcp.Tool{{Name: "vault_list"}}}
	m := NewManager(store.NewMCPClientStore(t.TempDir()))
	var dials atomic.Int32
	m.dial = func(ctx context.Context, cfg store.MCPClientConfig) (Session, error) {
		if dials.Add(1) == 1 {
			return s1, nil
		}
		// Fresh live session for the reconnect; concurrent riders share it.
		return &dyingSession{tools: s1.tools}, nil
	}
	ctx := context.Background()
	m.ensureConn(ctx, store.MCPClientConfig{Name: "notes", Transport: store.MCPTransportSSE, URL: "http://x/sse", Enabled: true})

	// 10 concurrent calls hit the dead session at once; the reconnect lock
	// must collapse them to a single extra dial.
	s1.dead.Store(true)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.CallTool(ctx, "mcp__notes__vault_list", nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent post-drop call failed: %v", err)
		}
	}
	// The reconnect lock collapses the stampede: exactly one redial total.
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials = %d, want 2 (initial + single reconnect)", got)
	}
}

func TestIsConnError(t *testing.T) {
	if !isConnError(mcp.ErrConnectionClosed) {
		t.Error("ErrConnectionClosed must be a conn error")
	}
	if !isConnError(io.EOF) {
		t.Error("EOF must be a conn error")
	}
	if !isConnError(fmt.Errorf("connection closed: calling %q: client is closing: EOF", "tools/call")) {
		t.Error("wrapped SDK close phrasing must match")
	}
	if !isConnError(fmt.Errorf("write tcp: broken pipe")) {
		t.Error("broken pipe must match")
	}
	if isConnError(errors.New("vault not found")) {
		t.Error("tool-level error must NOT trigger reconnect")
	}
	if isConnError(nil) {
		t.Error("nil is not a conn error")
	}
}
