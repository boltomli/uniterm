package mcpclient

import (
	"context"
	"errors"
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
