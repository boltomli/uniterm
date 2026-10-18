package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ── F-06: HTTP listener hardening ───────────────────────────────────────────

// The listener used to be a bare http.Server{Handler: mux}: any local process
// could Slowloris it (no ReadHeaderTimeout) or park an unbounded body in
// memory (no body cap, no timeouts at all).
func TestHTTPServerConfiguresTimeouts(t *testing.T) {
	srv := NewServer(Env{
		ListSessions:    func() []SessionSummary { return nil },
		ListConnections: func() []ConnectionSummary { return nil },
		Policy:          func() Policy { return PolicyConfirmWrite },
		Approve:         func(req ApprovalRequest) error { return nil },
		Audit:           func(entry AuditEntry) {},
	})
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	srv.mu.Lock()
	httpSrv := srv.httpSrv
	srv.mu.Unlock()
	if httpSrv == nil {
		t.Fatal("http server not stored")
	}
	if httpSrv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be set (Slowloris)")
	}
	if httpSrv.ReadTimeout <= 0 {
		t.Error("ReadTimeout must be set (unbounded body read)")
	}
	if httpSrv.IdleTimeout <= 0 {
		t.Error("IdleTimeout must be set (idle keep-alive reaping)")
	}
	// WriteTimeout intentionally stays 0: streamable-HTTP/SSE responses are
	// long-lived and a write deadline would cut them off.
	if httpSrv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (SSE/streaming)", httpSrv.WriteTimeout)
	}
}

func TestGuardRejectsForeignOrigin(t *testing.T) {
	s := NewServer(Env{})
	nextHit := false
	h := s.guardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextHit = true
		w.WriteHeader(http.StatusNoContent)
	}))

	cases := []struct {
		name   string
		origin string
		host   string
		want   int
	}{
		{"browser origin is refused", "http://evil.example", "127.0.0.1:61207", http.StatusForbidden},
		{"https browser origin is refused", "https://evil.example", "127.0.0.1:61207", http.StatusForbidden},
		{"opaque origin is refused", "null", "127.0.0.1:61207", http.StatusForbidden},
		{"loopback origin is allowed", "http://127.0.0.1:61207", "127.0.0.1:61207", http.StatusNoContent},
		{"localhost origin is allowed", "http://localhost:61207", "localhost:61207", http.StatusNoContent},
		{"rebound host is refused", "", "attacker.example", http.StatusForbidden},
		{"plain loopback host is allowed", "", "127.0.0.1:61207", http.StatusNoContent},
		{"ipv6 loopback host is allowed", "", "[::1]:61207", http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nextHit = false
			r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(nil))
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if reached := nextHit; reached != (tc.want == http.StatusNoContent) {
				t.Fatalf("next handler reached = %v", reached)
			}
		})
	}
}

func TestGuardCapsRequestBody(t *testing.T) {
	s := NewServer(Env{})
	var readErr error
	h := s.guardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))

	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(bytes.Repeat([]byte("x"), maxMCPRequestBody+1024)))
	r.Host = "127.0.0.1:61207"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if readErr == nil {
		t.Fatal("reading past the body cap must fail (no unbounded buffering)")
	}
	if !strings.Contains(readErr.Error(), "too large") && !strings.Contains(readErr.Error(), "MaxBytes") {
		t.Fatalf("unexpected error: %v", readErr)
	}
}

// ── F-06: token scope ───────────────────────────────────────────────────────

func TestScopeAllowsMatrix(t *testing.T) {
	cases := []struct {
		scope string
		class Scope
		want  bool
	}{
		{"", ScopeExec, true},          // legacy token: full access
		{"", ScopeFiles, true},         //
		{"full", ScopeExec, true},      //
		{"exec", ScopeExec, true},      //
		{"exec", ScopeFiles, false},    //
		{"files", ScopeFiles, true},    //
		{"files", ScopeExec, false},    //
		{"readonly", ScopeExec, false}, // no exec, no files
		{"readonly", ScopeFiles, false},
		{"exec,files", ScopeExec, true},
		{"files, exec", ScopeFiles, true}, // whitespace + order agnostic
		{"EXEC", ScopeExec, true},         // case-insensitive
	}
	for _, tc := range cases {
		if got := scopeAllows(tc.scope, tc.class); got != tc.want {
			t.Errorf("scopeAllows(%q, %s) = %v, want %v", tc.scope, tc.class, got, tc.want)
		}
	}
}

// requireScope must read the identity stashed by authMiddleware and refuse
// with a readable error (surfaced as an MCP tool error).
func TestRequireScopeFromContext(t *testing.T) {
	filesCtx := context.WithValue(context.Background(), tokenContextKey{}, tokenContext{Name: "file-agent", Scope: "files"})
	if err := requireScope(filesCtx, ScopeFiles); err != nil {
		t.Fatalf("files scope on files class: %v", err)
	}
	if err := requireScope(filesCtx, ScopeExec); err == nil {
		t.Fatal("files scope must not unlock exec tools")
	} else if !strings.Contains(err.Error(), "scope") {
		t.Fatalf("error should mention the scope: %v", err)
	}
	// Missing context (handler invoked directly in tests) → full access.
	if err := requireScope(context.Background(), ScopeExec); err != nil {
		t.Fatalf("identity-less context must default to full access: %v", err)
	}
}

// End-to-end: a files-scoped token reaches discovery but is refused on the
// exec group; an exec-scoped token is refused on the file group.
func TestTokenScopeGatesToolsOverHTTP(t *testing.T) {
	env := Env{
		Sessions: func(id string) (SSHExecutor, bool) { return fakeExecutor{}, id == "s1" },
		Commands: func(id string) (string, SSHExecutor, bool) { return "", nil, false },
		ListSessions: func() []SessionSummary {
			return []SessionSummary{{ID: "s1", Type: "ssh", Status: "connected"}}
		},
		ListConnections: func() []ConnectionSummary { return nil },
		Approve:         func(req ApprovalRequest) error { return nil },
		Audit:           func(entry AuditEntry) {},
		Policy:          func() Policy { return PolicyConfirmAll },
	}
	start := func(t *testing.T, scope string) (*mcp.ClientSession, string) {
		t.Helper()
		srv := NewServer(env)
		token, hash := GenerateToken()
		srv.SetTokens(map[string]TokenInfo{hash: {Name: "scoped-agent", Scope: scope}})
		if err := srv.Start(0); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(srv.Stop)
		url := "http://127.0.0.1:" + strconv.Itoa(srv.Port()) + "/mcp"
		client := mcp.NewClient(&mcp.Implementation{Name: "scope-test", Version: "1.0"}, nil)
		transport := &mcp.StreamableClientTransport{Endpoint: url}
		transport.HTTPClient = &http.Client{Transport: tokenTransport{rt: http.DefaultTransport, token: token}}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cs, err := client.Connect(ctx, transport, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		return cs, token
	}
	call := func(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("call %s: %v", tool, err)
		}
		b, _ := json.Marshal(res)
		return string(b)
	}

	// files scope: discovery works, exec_command refused.
	csFiles, _ := start(t, "files")
	if text := call(t, csFiles, "list_sessions", nil); strings.Contains(text, "scope") {
		t.Fatalf("files token must still reach discovery: %s", text)
	}
	if text := call(t, csFiles, "exec_command", map[string]any{"sessionId": "s1", "command": "echo hi"}); !strings.Contains(text, "scope") {
		t.Fatalf("files token calling exec_command must be scope-refused, got: %s", text)
	}

	// exec scope: file tools refused, exec allowed.
	csExec, _ := start(t, "exec")
	if text := call(t, csExec, "list_remote_dir", map[string]any{"sessionId": "s1", "remotePath": "/tmp"}); !strings.Contains(text, "scope") {
		t.Fatalf("exec token calling list_remote_dir must be scope-refused, got: %s", text)
	}
	if text := call(t, csExec, "exec_command", map[string]any{"sessionId": "s1", "command": "echo hi"}); strings.Contains(text, "scope") {
		t.Fatalf("exec token must be allowed to exec: %s", text)
	}
}

// ── F-06: approval prompt rate limit ────────────────────────────────────────

// A malicious client must not be able to spam dialogs until the user
// approves out of fatigue.
func TestApprovalRateLimitPerClient(t *testing.T) {
	s := NewServer(Env{})
	for i := 0; i < approvalRateMax; i++ {
		if err := s.allowApproval("spam-agent"); err != nil {
			t.Fatalf("prompt %d must be allowed: %v", i+1, err)
		}
	}
	if err := s.allowApproval("spam-agent"); err == nil {
		t.Fatal("prompt over the window must be refused")
	} else if !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("error should mention the rate limit: %v", err)
	}
	// Other clients are unaffected.
	if err := s.allowApproval("other-agent"); err != nil {
		t.Fatalf("other client must be unaffected: %v", err)
	}
}
