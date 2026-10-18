// Package mcp exposes uniTerm as an MCP (Model Context Protocol) server so
// external AI agents (Claude Code, Codex CLI, Kimi CLI, ...) can run commands
// on the user's saved / live SSH connections without ever seeing a password
// or private key: the app holds the credentials, executes through its own
// authenticated clients, and gates every command behind an approval dialog
// surfaced in the UI.
//
// Transport: Streamable HTTP on 127.0.0.1 only, authenticated with a
// per-client bearer token (stored as SHA-256, shown once at creation).
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ys-ll/uniterm/backend/log"
)

// DefaultPort is the MCP endpoint's listen port. Uncommon on purpose; the
// user can override it in settings.
const DefaultPort = 61207

// DefaultApprovalTimeout bounds how long a tool call waits for the user to
// answer the approval dialog. Kept under Codex's 120s tools/call limit so a
// stale approval can never fire after the client already gave up.
const DefaultApprovalTimeout = 110 * time.Second

// DefaultSyncTimeout is how long exec_command waits for completion before
// returning an async handle (commandId) the agent can poll.
const DefaultSyncTimeout = 20 * time.Second

// DefaultCommandTimeout is the hard runtime cap for one exec command.
const DefaultCommandTimeout = 5 * time.Minute

// DefaultMaxOutputBytes caps the output carried back in one tool result;
// larger output is truncated with a marker.
const DefaultMaxOutputBytes = 64 * 1024

// maxMCPRequestBody caps one request body (F-06): the SDK handler does not
// limit reads, so an unbounded body from any local process could park
// gigabytes in memory.
const maxMCPRequestBody = 8 << 20 // 8 MiB

// Approval-dialog rate limiting (F-06): a malicious client could otherwise
// spam dialogs until the user approves out of fatigue (social engineering).
// Sliding window per resolved client (token name / client label).
const (
	approvalRateMax    = 10
	approvalRateWindow = time.Minute
)

// SessionFunc resolves a live session id to its SSH executor. Injected by the
// App layer (backend/session imports are kept out of this package's API so
// the mcp package stays testable in isolation).
type SessionFunc func(sessionID string) (SSHExecutor, bool)

// FileEntry is one remote listing row for list_remote_dir (kept free of
// session types so the mcp package stays isolated).
type FileEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"isDir"`
	ModTime int64  `json:"modTime,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

// CommandFunc resolves a commandId to its owning session's executor and the
// session id. Injected by the App layer.
type CommandFunc func(commandID string) (sessionID string, exec SSHExecutor, ok bool)

// Env is everything the MCP server needs from the app. All fields are
// read-only after Start.
type Env struct {
	Sessions SessionFunc
	// Commands resolves a commandId to its owning session + executor.
	Commands CommandFunc
	// ListSessions returns live session metadata (id, type, title, status).
	ListSessions func() []SessionSummary
	// ListConnections returns saved SSH connection profiles with safe
	// metadata only — never credentials.
	ListConnections func() []ConnectionSummary
	// Connect opens a new SSH session for a saved connection id and returns
	// the session id. The approval gate runs before dialing.
	Connect func(connectionID string) (sessionID string, err error)
	// OpenTerminal opens a saved terminal profile, or an ad hoc local/WSL terminal.
	OpenTerminal func(connectionID, terminalType, shellPath string) (string, error)
	// Approve surfaces a confirmation request to the user and waits for the
	// verdict. err is non-nil on timeout/no-window.
	Approve func(req ApprovalRequest) error
	// Audit appends one JSONL entry.
	Audit func(entry AuditEntry)
	// Policy returns the active approval policy.
	Policy func() Policy
	// ResolveToken resolves a token hash to its client name and scope
	// (comma-separated capability list; "" = full access), re-reading
	// persisted tokens when mcp.json changed (so tokens written elsewhere
	// take effect without a restart). "" name = unknown token. When nil, the
	// in-memory map from SetTokens is authoritative.
	ResolveToken func(hash string) (name string, scope string)
	// FileSession resolves a live session id to its companion file-transfer
	// executor (created lazily per the connection's file protocol); an
	// error when the session is gone, not SSH, or the transfer session
	// cannot be opened — the text reaches the AI caller.
	FileSession func(sessionID string) (FileExecutor, error)
	// ResolveLocalPath validates a local path against the user-configured
	// allowed directories (SFTP bookmarks) and returns the resolved path.
	ResolveLocalPath func(path string) (resolved string, err error)
	// Foreground runs a command in the live terminal PTY as an async job and
	// completes on idle-settle / EOF rather than an end marker. When nil the
	// unified uniterm_exec tool reports foreground mode unsupported; background
	// mode still works over Sessions/Commands.
	Foreground ForegroundJobs
	// ExecMode returns the user-selected execution mode: "background" (SSH
	// non-PTY exec tools) or "foreground" (live-terminal tools plus capture /
	// send-key). Read once per server Start to pick the registered toolset;
	// the user stops the endpoint before changing it in settings.
	// "" / unknown falls back to background.
	ExecMode func() string
	// Capture snapshots a live session's terminal screen (redraw-resolved,
	// like what the user sees). Used by the foreground toolset's
	// uniterm_capture; nil disables the tool.
	Capture func(sessionID string, tailLines int) (string, error)
	// SendKey types text or a control key into a live session's terminal.
	// text is typed verbatim (sendEnter appends the shell newline); control
	// is "ctrl_c" | "ctrl_d" | "enter". Used by uniterm_send_key; nil
	// disables the tool.
	SendKey func(sessionID, text, control string, sendEnter bool) error
	// Collect starts read-only output observation using the foreground job engine.
	Collect func(sessionID string, idleMs, hardLimitMs int) (string, error)
}

// ForegroundJobs is the unified async-job surface for the live-PTY (foreground)
// mode. Implemented by the App layer over the foreground job registry; kept as an
// interface so the mcp package stays testable without a real session.
type ForegroundJobs interface {
	// StartForeground types command into sessionID's live terminal and returns
	// a jobId. idleMs bounds the silence that counts as completion; hardLimitMs
	// is the hard runtime cap (the job moves to "timeout" but stays pollable).
	StartForeground(sessionID, command string, idleMs, hardLimitMs int) (jobID string, err error)
	// PollForeground returns output-so-far, exit code and state for a jobId.
	PollForeground(jobID string, offset, max int) (stdout, stderr []byte, exitCode int, state string, err error)
	// InterruptForeground stops a running foreground job.
	InterruptForeground(jobID, signal string) error
}

// SessionSummary is one live session row for list_sessions.
type SessionSummary struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// Cwd is the last OSC-7 reported working directory, if known.
	Cwd string `json:"cwd,omitempty"`
}

// ConnectionSummary is one saved connection for list_connections. Deliberately
// free of credential fields.
type ConnectionSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
}

// SSHExecutor runs one command on an authenticated SSH connection and
// returns stdout, stderr, the exit code and an async handle when the sync
// wait budget is exceeded. Implemented by session.SSHSession.
type SSHExecutor interface {
	// MCPExec runs cmd. If the command is still running after syncTimeout it
	// keeps running and commandID is set; poll with MCPLatestOutput.
	MCPExec(cmd string, syncTimeout, hardTimeout, maxOutput int) (stdout, stderr []byte, exitCode int, commandID string, err error)
	// MCPLatestOutput returns the captured output so far for a commandID.
	MCPLatestOutput(commandID string, offset int, max int) (stdout, stderr []byte, exitCode int, state string, err error)
	// MCPInterrupt kills a command started via MCPExec.
	MCPInterrupt(commandID string, signal string) error
}

// FileExecutor carries the SFTP-backed file tools. Split from SSHExecutor so
// the exec group can be implemented/tested without SFTP.
type FileExecutor interface {
	MCPListDir(remotePath string) (entries []FileEntry, err error)
	MCPWriteFile(localPath, remotePath string) (bytes int64, err error)
	MCPReadRemoteToFile(remotePath, localPath string) (bytes int64, err error)
}

// Policy is the approval policy for exec calls.
//   - "confirm_write": only risk≥write commands (default)
//   - "confirm_all": every command needs approval
//   - "confirm_dangerous": only dangerous commands
//   - "bypass": no dialogs at all, including dangerous commands — the same
//     "全部免确认" semantics as the built-in AI panel (see gateExec). Users
//     picking bypass are explicitly opting out of every confirmation.
type Policy string

const (
	PolicyConfirmAll       Policy = "confirm_all"
	PolicyConfirmWrite     Policy = "confirm_write"
	PolicyConfirmDangerous Policy = "confirm_dangerous"
	PolicyBypass           Policy = "bypass"
)

// RiskClass is the coarse command classification used by the policy engine.
type RiskClass int

const (
	RiskRead RiskClass = iota
	RiskWrite
	RiskDangerous
)

// ApprovalRequest is one confirmation dialog.
type ApprovalRequest struct {
	ID         string `json:"id"`
	Client     string `json:"client"` // token name of the calling agent
	Connection string `json:"connection"`
	Command    string `json:"command"`
	Cwd        string `json:"cwd,omitempty"`
	Timeout    int    `json:"timeout"`
	CreatedAt  int64  `json:"createdAt"`
}

// AuditEntry is one JSONL line in mcp-audit.log.
type AuditEntry struct {
	Time     string `json:"time"`
	Token    string `json:"token"`
	Tool     string `json:"tool"`
	Session  string `json:"session,omitempty"`
	Command  string `json:"command,omitempty"`
	ExitCode int    `json:"exitCode,omitempty"`
	Approved *bool  `json:"approved,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Server is the uniTerm MCP server: HTTP listener + SDK server + token auth.
type Server struct {
	env Env

	mu      sync.Mutex
	tokens  map[string]TokenInfo // keyed by sha256(token)
	handler *mcp.StreamableHTTPHandler
	ln      net.Listener
	httpSrv *http.Server
	port    int
	started bool
	// finishedBg caches sync-completed background results for the unified
	// uniterm_exec_result poll path (keyed by bg- jobId).
	finishedBg       map[string]finishedBackground
	foregroundOutput map[string]terminalOutputOptions

	// approvalSeen is the per-client sliding window of approval prompts (F-06).
	approvalMu   sync.Mutex
	approvalSeen map[string][]time.Time
}

// TokenInfo is the stored form of one client token.
type TokenInfo struct {
	Name string `json:"name"`
	// Scope is the token's capability list (F-06): comma-separated subset of
	// "exec" (connect/exec/interrupt/send-key/capture tools), "files" (the SFTP
	// file tools) and "readonly" (discovery + output polling only).
	// "" / "full" keeps every tool — tokens minted before scopes existed stay
	// fully usable.
	Scope string `json:"scope,omitempty"`
}

// NewServer builds a server (not yet listening).
func NewServer(env Env) *Server {
	return &Server{env: env, tokens: make(map[string]TokenInfo)}
}

// Port reports the listening port (0 when not running).
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// Running reports whether the HTTP listener is up.
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// SetTokens replaces the token set (called when settings change).
func (s *Server) SetTokens(tokens map[string]TokenInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = tokens
}

// Start brings up the listener on 127.0.0.1:port. Starting the port the
// server already listens on is a no-op: the App layer calls Start from
// SaveLocalState, which fires on every UI toggle, and a restart would drop
// every connected MCP client and its in-flight tool calls. Requesting a
// different port (or 0 = "pick one") stops the running server first.
func (s *Server) Start(port int) error {
	s.mu.Lock()
	running, current := s.started, s.port
	s.mu.Unlock()
	if running && (port == 0 || port == current) {
		return nil
	}

	s.Stop()

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "uniterm",
		Version: "1.0.0",
	}, nil)
	s.registerTools(srv)

	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return srv
	}, nil)

	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("mcp: listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", s.guardMiddleware(s.authMiddleware(handler)))
	httpSrv := &http.Server{
		Handler: mux,
		// F-06: without timeouts any local process could Slowloris the
		// listener or park a connection forever. WriteTimeout stays 0 on
		// purpose — streamable-HTTP/SSE responses are long-lived; the read
		// side is bounded above and idle keep-alives are reaped.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	s.mu.Lock()
	s.handler = handler
	s.ln = ln
	s.httpSrv = httpSrv
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.started = true
	s.mu.Unlock()

	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Writef("mcp: serve: %v", err)
		}
	}()
	log.Writef("mcp: server listening on 127.0.0.1:%d", s.port)
	return nil
}

// Stop tears the listener down.
func (s *Server) Stop() {
	s.mu.Lock()
	httpSrv := s.httpSrv
	s.httpSrv = nil
	s.ln = nil
	s.handler = nil
	s.port = 0
	s.started = false
	s.mu.Unlock()
	if httpSrv != nil {
		_ = httpSrv.Close()
	}
}

// tokenContextKey carries the resolved token identity from the auth
// middleware into tool handlers (the SDK passes req.Context() through).
type tokenContextKey struct{}

type tokenContext struct {
	Name  string
	Scope string
}

// tokenFromContext returns the identity stashed by authMiddleware (zero value
// when absent, e.g. handlers invoked directly in tests → full access).
func tokenFromContext(ctx context.Context) tokenContext {
	tk, _ := ctx.Value(tokenContextKey{}).(tokenContext)
	return tk
}

// requireScope rejects a tool call whose token lacks the needed capability
// class (F-06). Empty/full scope keeps every tool (backward compatible);
// otherwise the scope must list the class or "full".
func requireScope(ctx context.Context, class Scope) error {
	tk := tokenFromContext(ctx)
	if scopeAllows(tk.Scope, class) {
		return nil
	}
	return fmt.Errorf("forbidden: token %q has scope %q and may not use %s tools", tk.Name, tk.Scope, class)
}

func scopeAllows(scope string, class Scope) bool {
	s := strings.TrimSpace(scope)
	if s == "" || strings.EqualFold(s, "full") {
		return true
	}
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if strings.EqualFold(p, "full") || strings.EqualFold(p, string(class)) {
			return true
		}
	}
	return false
}

// Scope is a token capability class (F-06).
type Scope string

const (
	// ScopeExec covers the execution side: connect, exec_command,
	// interrupt_command and the foreground uniterm_* action tools.
	ScopeExec Scope = "exec"
	// ScopeFiles covers the SFTP file tools (list/upload/download).
	ScopeFiles Scope = "files"
)

// scoped wraps a tool handler with its capability check (F-06). The error
// surfaces as an MCP tool error, so a files-scoped token calling exec_command
// gets a clear refusal instead of execution.
func scoped[I any, O any](class Scope, h func(context.Context, *mcp.CallToolRequest, I) (*mcp.CallToolResult, O, error)) func(context.Context, *mcp.CallToolRequest, I) (*mcp.CallToolResult, O, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in I) (*mcp.CallToolResult, O, error) {
		if err := requireScope(ctx, class); err != nil {
			var zero O
			return nil, zero, err
		}
		return h(ctx, req, in)
	}
}

// guardMiddleware is the pre-auth depth-of-defense layer (F-06):
//   - browser Origins are rejected — a web page must never reach the endpoint
//     even if it learned the port (DNS-rebinding / CSRF style abuse);
//   - the Host header must name a loopback address (DNS-rebinding defense);
//   - request bodies are capped so no local process can park unbounded
//     memory in the SDK handler.
func (s *Server) guardMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !isLoopbackOrigin(origin) {
			http.Error(w, "cross-origin requests rejected", http.StatusForbidden)
			return
		}
		if !isLoopbackHost(r.Host) {
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxMCPRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackOrigin reports whether Origin is an http(s) origin on a loopback
// host (127.0.0.1 / localhost / ::1). Anything else — including "null" and
// file: origins — is refused.
func isLoopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return isLoopbackHostname(u.Hostname())
}

// isLoopbackHost validates a request Host header (host[:port]).
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	} else {
		host = strings.Trim(hostport, "[]")
	}
	return isLoopbackHostname(host)
}

func isLoopbackHostname(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// authMiddleware rejects requests without a valid bearer token. Loopback
// binding alone is not trust: browser pages and local processes can reach
// 127.0.0.1 unauthenticated. Token resolution prefers Env.ResolveToken
// (file-backed, hot-reloaded); the in-memory map is the fallback. The
// resolved token name + scope are stashed in the request context so tool
// handlers can label approvals/audit entries and enforce their scope.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		key := HashToken(token)
		name, scope := "", ""
		if s.env.ResolveToken != nil {
			name, scope = s.env.ResolveToken(key)
		}
		if name == "" {
			s.mu.Lock()
			info, ok := s.tokens[key]
			s.mu.Unlock()
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="uniterm"`)
				http.Error(w, "invalid or missing token", http.StatusUnauthorized)
				return
			}
			name, scope = info.Name, info.Scope
		}
		ctx := context.WithValue(r.Context(), tokenContextKey{}, tokenContext{Name: name, Scope: scope})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts the Authorization: Bearer value.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) > len(prefix) && (auth[:len(prefix)] == prefix || auth[:len(prefix)] == "bearer ") {
		return auth[len(prefix):]
	}
	return ""
}

// GenerateToken mints a new random 32-byte hex token and returns it together
// with its stored hash key.
func GenerateToken() (token, hashKey string) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	token = hex.EncodeToString(buf)
	hash := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(hash[:])
}

// HashToken derives the storage key for a plaintext token.
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// TokenEntry is one persisted token record (hash only — the plaintext is
// shown once and then forgotten).
type TokenEntry struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}
