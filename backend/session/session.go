package session

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ys-ll/uniterm/backend/log"
)

type SessionStatus string

const (
	StatusConnecting   SessionStatus = "connecting"
	StatusConnected    SessionStatus = "connected"
	StatusDisconnected SessionStatus = "disconnected"
	StatusError        SessionStatus = "error"
)

// disconnectNotice formats a red disconnect prompt with the local time it
// happened, so users can tell when a remote host dropped them. See issue #367.
func disconnectNotice(msg string) []byte {
	ts := time.Now().Format("2006-01-02 15:04:05")
	return []byte(fmt.Sprintf("\r\n\x1b[31m%s (%s) Press Enter to reconnect.\x1b[0m\r\n", msg, ts))
}

type ConnectionGroup struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	ParentId *string `json:"parentId,omitempty"`
}

// SSHAlgoConfig holds per-connection SSH algorithm preferences. See
// ssh_config.go for the mode semantics. nil (or an empty Mode) on
// ConnectionConfig.SSHAlgorithms selects the compatible mode.
type SSHAlgoConfig struct {
	Mode string `json:"mode,omitempty"`
	// Only consulted in custom mode; each list is the negotiation preference
	// order and must be non-empty with supported algorithm names only.
	KeyExchanges []string `json:"keyExchanges,omitempty"`
	Ciphers      []string `json:"ciphers,omitempty"`
	MACs         []string `json:"macs,omitempty"`
	HostKeys     []string `json:"hostKeys,omitempty"`
}

type ConnectionConfig struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Remark string `json:"remark,omitempty"`
	Type   string `json:"type"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	User   string `json:"user"`
	// SSHAlgorithms customizes the negotiated algorithms for SSH-based
	// sessions. nil keeps the compatible default.
	SSHAlgorithms *SSHAlgoConfig `json:"sshAlgorithms,omitempty"`
	AuthType      string         `json:"authType"`
	// AuthType "agent" uses SSH_AUTH_SOCK on Unix. On Windows it uses Pageant,
	// falling back to the Windows OpenSSH agent named pipe.
	// AuthType "kerberos" uses the local Kerberos credential cache and
	// SSH gssapi-with-mic; no password or private key is persisted.
	// KerberosRealm is appended to the host service principal when Host is an
	// IP address, producing host/<ip>@<REALM>. Domain-name targets keep their
	// existing canonicalization behavior.
	KerberosRealm string `json:"kerberosRealm,omitempty"`
	// IdentityId references a saved Identity (see identity.go). When set with
	// AuthType "identity", MaterializeIdentity resolves and injects the
	// credentials at connect time.
	IdentityId string `json:"identityId,omitempty"`
	// Password is encrypted at rest (enc:v1:) for every authType when a
	// password store is wired; authType "identity" clears it instead (unused).
	Password string `json:"password,omitempty"`
	KeyPath  string `json:"keyPath,omitempty"`
	// KeyContent holds the inline private-key text (PEM) for authType "keyText".
	// When set, the connection authenticates from the text directly instead of
	// reading KeyPath from disk. Encrypted at rest and normalized across cloud
	// sync exactly like passwords so it stays portable across devices (#720).
	KeyContent string  `json:"keyContent,omitempty"`
	GroupId    *string `json:"groupId,omitempty"`
	// RDP-specific fields
	// RdpFixedWidth/Height carry the requested desktop size, or a sentinel:
	// -1 = follow the primary monitor on connect (the "full screen" option),
	// -2 = adaptive: the desktop size follows the .rdp-area window size — when
	// the window size stays stable for a short delay, the session reconnects
	// with the new size so the server renders at the window's resolution.
	RdpFixedWidth  int  `json:"rdpFixedWidth,omitempty"`
	RdpFixedHeight int  `json:"rdpFixedHeight,omitempty"`
	RdpEnableNLA   bool `json:"rdpEnableNLA"`
	// RdpDomain is the Windows domain (or computer name for local accounts)
	// sent to the RDP ActiveX control's Domain property. When empty, a
	// "DOMAIN\user" prefix in User is used as a fallback, mirroring the
	// username syntax mstsc accepts.
	RdpDomain string `json:"rdpDomain,omitempty"`
	// RdpAdminSession connects to the remote console (admin) session, the
	// equivalent of "mstsc /admin". Mapped to the ActiveX
	// IMsRdpClientAdvancedSettings8::ConnectToAdministerServer property.
	RdpAdminSession bool `json:"rdpAdminSession"`
	// Local terminal shell path
	ShellPath string `json:"shellPath,omitempty"`
	// Working directory for local terminal (defaults to user home directory if empty)
	Cwd string `json:"cwd,omitempty"`
	// Serial port configuration
	SerialPort     string  `json:"serialPort,omitempty"`
	SerialBaudRate int     `json:"serialBaudRate,omitempty"`
	SerialDataBits int     `json:"serialDataBits,omitempty"`
	SerialStopBits float64 `json:"serialStopBits,omitempty"`
	SerialParity   string  `json:"serialParity,omitempty"`
	// Database-specific fields
	DBType   string `json:"dbType,omitempty"`   // "mysql", "postgres", "rqlite", "oracle", "sqlserver", "redis", "mongodb", "elasticsearch"
	DBName   string `json:"dbName,omitempty"`   // default database name
	DBParams string `json:"dbParams,omitempty"` // extra DSN query parameters, e.g. "sslmode=require&connect_timeout=30"
	// Elasticsearch / OpenSearch fields
	EsUseSSL     bool   `json:"esUseSsl,omitempty"`     // use HTTPS
	EsPathPrefix string `json:"esPathPrefix,omitempty"` // optional path prefix, e.g. "/es"
	EsSkipVerify bool   `json:"esSkipVerify,omitempty"` // insecureSkipVerify for self-signed certs
	// Redis Sentinel fields (only used when RedisMode == "sentinel")
	RedisMode        string `json:"redisMode,omitempty"`        // ""/"standalone"(default) | "sentinel"
	RedisMasterName  string `json:"redisMasterName,omitempty"`  // Sentinel primary group name, e.g. "mymaster"
	RedisSentinels   string `json:"redisSentinels,omitempty"`   // comma-separated sentinel host:port list
	SentinelUser     string `json:"sentinelUser,omitempty"`     // Sentinel ACL user (optional)
	SentinelPassword string `json:"sentinelPassword,omitempty"` // Sentinel requirepass (optional)
	// RedisKeySeparator splits keys into a folder tree in the Redis browser.
	// Defaults to ":" when empty; set to a character that never appears in
	// keys to disable grouping (flat list).
	RedisKeySeparator string `json:"redisKeySeparator,omitempty"`
	// SSH post-login script: commands to execute after successful login
	PostLoginScript string `json:"postLoginScript,omitempty"`
	// Post-login expect/send automation: interactive steps executed after login.
	PostLoginExpectSteps []PostLoginExpectStep `json:"postLoginExpectSteps,omitempty"`
	// SSH tunnel: reference to an existing SSH connection used as a jump host.
	// When set, the connection goes through local port forwarding:
	//   127.0.0.1:auto-port → tunnel SSH → target Host:Port
	TunnelSSHConnID string `json:"tunnelSSHConnId,omitempty"`
	// Proxy: reference to a saved Proxy (see proxy.go). When set, the
	// first-hop TCP dial goes through this SOCKS5/HTTP proxy instead of
	// connecting directly.
	ProxyId string `json:"proxyId,omitempty"`
	// Proxy is the resolved runtime proxy for this connect attempt. Populated
	// by materializeProxy at connect time; never persisted.
	Proxy             *SocksProxy `json:"-"`
	TunnelSSHUser     string      `json:"tunnelSSHUser,omitempty"`
	TunnelSSHPassword string      `json:"tunnelSSHPassword,omitempty"`
	// HostKeyPinHost/HostKeyPinPort carry the connection's ORIGINAL host:port
	// for known-hosts pinning when the dial address is rewritten at connect
	// time. Jump-host tunnels (setupJumpHostTunnel) replace Host/Port with
	// 127.0.0.1:<ephemeral local port>; pinning under that address would
	// re-trust the target on every connection — a compromised jump host could
	// MITM it — and grow known_hosts.json by one entry per ephemeral port
	// (breaking when a port is reused for another target). Runtime-only,
	// never persisted; see hostKeyPinAddr.
	HostKeyPinHost string `json:"-"`
	HostKeyPinPort int    `json:"-"`
	// SFTP max concurrent transfers (0 = unlimited)
	SftpMaxConcurrency int `json:"sftpMaxConcurrency,omitempty"`
	// FileTransferProto selects the file-transfer protocol used by the SSH
	// connection's companion file panel and "connect SFTP/SCP" actions:
	// "" / "sftp" (default) | "scp" — for hosts without an SFTP subsystem.
	FileTransferProto string `json:"fileTransferProto,omitempty"`
	// Initial terminal size reported by the frontend BEFORE the
	// SSH/local PTY is created. Without this the backend starts the
	// remote shell with the default 80x24 and Claude Code draws tables
	// at that width; by the time the frontend's fitAddon measures the
	// actual xterm cols and sends SessionResize, several lines are
	// already wrapped at 80 cols and the rest at the real cols — table
	// borders drift apart. 0/0 means "use default"; otherwise fed into
	// SetPendingSize before Connect so getInitialSize picks them up.
	InitialCols int `json:"initialCols,omitempty"`
	InitialRows int `json:"initialRows,omitempty"`
	// FTP-specific fields
	FtpEncryption string `json:"ftpEncryption,omitempty"` // "none"(default) | "auto" | "required"
	FtpPassive    bool   `json:"ftpPassive"`              // passive mode (default true)
	// FtpSkipVerify opts in to tls.Config.InsecureSkipVerify for FTPS connections.
	// Defaults to false (verify enabled). Off by default preserves backwards
	// compatibility for users today who rely on it for self-signed certs —
	// but the toggle now exists so the choice is explicit, and a one-shot
	// session-log warning fires on connect when it is enabled.
	FtpSkipVerify bool `json:"ftpSkipVerify,omitempty"`
	// VNC-specific fields (issue #95)
	// VncShared is forwarded to noVNC's RFB constructor as `shared`.
	// true (default) — the new client may connect alongside other clients.
	// false — the server will typically disconnect other clients on connect.
	VncShared bool `json:"vncShared,omitempty"`
	// VncRepeaterID is forwarded to noVNC's RFB constructor as `repeaterID`.
	// Empty (default) — direct connection to the VNC server.
	// Non-empty — connect via an UltraVNC-compatible repeater using the given ID.
	VncRepeaterID string `json:"vncRepeaterID,omitempty"`

	VncClipboardEncoding string `json:"vncClipboardEncoding,omitempty"`
	// SMB-specific fields
	SmbDomain string `json:"smbDomain,omitempty"`
	SmbShare  string `json:"smbShare,omitempty"`
	// S3-specific fields
	S3Region string `json:"s3Region,omitempty"`
	S3Bucket string `json:"s3Bucket,omitempty"`
	// S3URLStyle chooses URL addressing for S3 requests against a custom
	// endpoint. "" / "virtual" (default) uses virtual-hosted style
	// (https://bucket.endpoint/key) — required by Alibaba Cloud OSS,
	// Tencent COS and Huawei OBS; they reject path-style requests with
	// "SecondLevelDomainForbidden" (issue #452). "path" uses path-style
	// (https://endpoint/bucket/key) for AWS S3 and MinIO.
	S3URLStyle string `json:"s3UrlStyle,omitempty"`
	// Terminal character encoding for ssh/telnet, filename charset for ftp:
	// "" / "utf-8"(default) | "gbk" | "gb2312" | "gb18030" | "big5" | "shift-jis" | "euc-jp" | "euc-kr" | "latin-1"
	Encoding string `json:"encoding,omitempty"`
	// TermType is the terminal type reported to the remote host (SSH
	// pty-req / telnet TTYPE / local TERM env). "" falls back to
	// DefaultTermType; see EffectiveTermType for the whitelist.
	TermType string `json:"termType,omitempty"`
	// X11Forwarding enables SSH X11 forwarding (ssh -X semantics). Sends an
	// "x11-req" global request after RequestPty; accepts "x11" channels from
	// the server and bridges them to the local X server at $DISPLAY. If
	// $DISPLAY is unset, xauth is missing, or the local X server is
	// unreachable the connection still succeeds and a yellow warning is
	// emitted in the terminal (silent degradation). Trusted mode is used as
	// fallback when MIT-MAGIC-COOKIE-1 cannot be read from $XAUTHORITY.
	X11Forwarding bool `json:"x11Forwarding,omitempty"`
	// AgentForwarding exposes the local SSH agent to the remote SSH session
	// (the equivalent of OpenSSH's -A option). The private keys remain in the
	// local agent; only signing requests are forwarded.
	AgentForwarding bool `json:"agentForwarding,omitempty"`
	// Backspace key byte sequence for terminal-stream types (ssh/telnet/serial).
	// The translation happens on the frontend in applyBackspaceKey before the
	// byte hits SessionWrite, so the backend does not read this field — it is
	// kept here so the Wails binding reflects the full connection contract.
	// ""(default) | "del"(0x7F) | "bs"(0x08) | "vt220"(ESC[3~)
	BackspaceKey string `json:"backspaceKey,omitempty"`
	// Telnet-specific fields
	// TelnetNegotiationMode controls who initiates option negotiation.
	// "active" (default) — client sends WILL/DO after connect.
	// "passive" — client only responds to server negotiation.
	TelnetNegotiationMode string `json:"telnetNegotiationMode,omitempty"`
	// LocalEcho echoes typed characters locally when the remote side doesn't.
	LocalEcho bool `json:"localEcho,omitempty"`
	// TelnetSendMode: "character" (default) — each keystroke sent immediately.
	// "line" — buffer until Enter, send the whole line.
	TelnetSendMode string `json:"telnetSendMode,omitempty"`
	// NewlineMode: "cr" (default) — Enter sends \r. "crlf" — Enter sends \r\n.
	NewlineMode string `json:"newlineMode,omitempty"`
	// K8s-specific fields
	K8sConfigPath   string `json:"k8sConfigPath,omitempty"`   // File 模式：kubeconfig 文件路径
	K8sConfigInline string `json:"k8sConfigInline,omitempty"` // Inline 模式：kubeconfig YAML 全文（同连接密码策略：落盘以 enc:v1: 加密，密码保护的导出中同样加密）
	K8sContext      string `json:"k8sContext,omitempty"`      // 选中的 context 名，为空则用 current-context
	K8sNamespace    string `json:"k8sNamespace,omitempty"`    // 默认 namespace，"" = all
	K8sInsecureTls  bool   `json:"k8sInsecureTls,omitempty"`  // 覆盖 kubeconfig 中的 insecure-skip-tls-verify
	// Container-specific fields
	ContainerTransport string `json:"containerTransport,omitempty"` // "ssh" | "local"
	ContainerSSHConnID string `json:"containerSSHConnId,omitempty"` // 引用的 SSH 连接（transport=ssh）
	ContainerRuntime   string `json:"containerRuntime,omitempty"`   // "docker" | "podman" | "nerdctl"
	// 容器文件会话（type=container-file）的目标容器 ID；容器连接走 config.ID
	// ContainerNamespace 仅 k8s 连接使用（目标 Pod 的资源 namespace）
	ContainerID        string `json:"containerId,omitempty"`
	ContainerNamespace string `json:"containerNamespace,omitempty"`
	// LogOnConnect, when true, tells the App layer to enable the
	// session output log automatically the first time this panel binds
	// a session. It has no effect on later reconnects — a manually
	// stopped log stays stopped for the life of the panel.
	LogOnConnect bool `json:"logOnConnect,omitempty"`
	// X11Desktop fields. Used when Type == "x11-desktop".
	// This type carries its own SSH credentials (Host, Port, User,
	// AuthType, Password, KeyPath) and connects directly — it no
	// longer references a separate SSH connection. X11 forward is
	// forced on automatically (this type is meaningless without it).
	//   DesktopType — "gnome" | "kde" | "xfce" | "mate" | "cinnamon" |
	//                 "openbox" | "custom". Mapped to a built-in command or
	//                 to CustomCmd verbatim.
	//   CustomCmd   — only used when DesktopType == "custom". The string is
	//                 passed to sshd verbatim (so it goes through
	//                 /bin/sh -c on the remote).
	X11DesktopDesktopType string `json:"x11DesktopDesktopType,omitempty"`
	X11DesktopCustomCmd   string `json:"x11DesktopCustomCmd,omitempty"`
	// Saved-workspace fields. Used when Type == "workspace": the config is a
	// named, persisted workspace snapshot living in the connection tree.
	// Members hold only a ConnectionID reference — host credentials re-resolve
	// through the normal connection decryption path at connect time, so no
	// secret is ever duplicated here.
	WorkspaceMembers []SavedWorkspaceMember    `json:"workspaceMembers,omitempty"`
	WorkspaceLayout  *SavedWorkspaceLayoutNode `json:"workspaceLayout,omitempty"`
}

// DefaultTermType is the terminal type reported to remote hosts when the
// connection does not configure one (the pty-req terminal name for SSH, the
// TTYPE reply for telnet, and the TERM environment entry for local shells).
const DefaultTermType = "xterm-256color"

// supportedTermTypes whitelists the terminal names a connection may choose
// from. The value is sent verbatim to remote hosts (SSH pty-req, telnet
// TTYPE), so anything outside this set falls back to DefaultTermType instead
// of reaching the server's terminfo lookup.
var supportedTermTypes = map[string]bool{
	"xterm-256color": true,
	"xterm":          true,
	"vt100":          true,
	"vt220":          true,
	"vt320":          true,
	"ansi":           true,
	"linux":          true,
}

// EffectiveTermType maps the connection's terminal type to the value sent to
// the remote host: the configured value when supported, DefaultTermType
// otherwise (including for the empty/unset case).
func EffectiveTermType(cfgTermType string) string {
	if supportedTermTypes[cfgTermType] {
		return cfgTermType
	}
	return DefaultTermType
}

// ExplicitTermType returns the connection's terminal type only when the user
// explicitly chose a supported one, and "" otherwise. Local-shell sessions
// use this to keep their existing "never override inherited environment"
// behavior unless the user made an explicit per-connection choice.
func ExplicitTermType(cfgTermType string) string {
	if supportedTermTypes[cfgTermType] {
		return cfgTermType
	}
	return ""
}

// SavedWorkspaceMember is one terminal slot inside a saved workspace.
type SavedWorkspaceMember struct {
	// ID is the stable member identity; saved-layout leaves reference it.
	// The frontend reuses the source panel's id, so a re-save of the same
	// live workspace keeps ids stable.
	ID string `json:"id"`
	// ConnectionID references the saved host the member connects to; empty
	// for self-contained members (local/wsl terminals).
	ConnectionID string `json:"connectionId,omitempty"`
	// Type is the terminal panel kind (ssh/local/wsl/telnet/serial/...).
	Type string `json:"type"`
	// Title is the display title saved from the live panel.
	Title string `json:"title"`
	// ShellPath recreates local/wsl members (wsl://<distro> carries the distro).
	ShellPath string `json:"shellPath,omitempty"`
}

// SavedWorkspaceLayoutNode mirrors the frontend LayoutNode with the leaf
// panelId replaced by the stable member ID (runtime panel ids are ephemeral).
type SavedWorkspaceLayoutNode struct {
	Type      string                     `json:"type"` // "leaf" | "split"
	MemberID  string                     `json:"memberId,omitempty"`
	Direction string                     `json:"direction,omitempty"` // "horizontal" | "vertical"
	Children  []SavedWorkspaceLayoutNode `json:"children,omitempty"`
	Sizes     []float64                  `json:"sizes,omitempty"`
}

// ConnectionStoreData is the top-level structure persisted to connections.json.
type ConnectionStoreData struct {
	Groups      []ConnectionGroup  `json:"groups"`
	Connections []ConnectionConfig `json:"connections"`
}

type SessionInfo struct {
	ID     string        `json:"id"`
	Type   string        `json:"type"`
	Title  string        `json:"title"`
	Status SessionStatus `json:"status"`
	// ProxyAddr is the local WebSocket URL for VNC/SPICE sessions whose
	// client (noVNC / spice-html5) connects through the WebSocket↔TCP proxy.
	// Returned synchronously from CreateSession so the frontend can start
	// the RFB/SPICE client without racing the session:status event.
	ProxyAddr string `json:"proxyAddr,omitempty"`
}

type Session interface {
	ID() string
	Type() string
	Title() string
	Status() SessionStatus

	Connect(config ConnectionConfig) error
	Disconnect() error
	IsConnected() bool
	Resize(cols, rows int) error
	// SetPendingSize stashes cols/rows to be applied at Connect time, for
	// the deferred-start flow where the frontend mounts the xterm and
	// measures the real size AFTER CreateSession has already created the
	// session object but BEFORE SessionStart runs Connect.
	SetPendingSize(cols, rows int)

	Write(data []byte) error
	SetOnDataCallback(cb func([]byte))
	SetOnBinaryCallback(cb func([]byte))
	SetOnStatusChangeCallback(cb func(SessionStatus))
	SetZmodemMode(bool)
	EndZmodem([]byte)
	IsZmodemMode() bool
	// SetOutputPaused holds (true) or releases (false) the text-output
	// stream — the frontend's end-to-end flow control (P-02).
	SetOutputPaused(bool)
}

type baseSession struct {
	id               string
	sessionType      string
	title            string
	status           SessionStatus
	onDataCallback   func([]byte)
	onBinaryCallback func([]byte)
	onStatusCallback func(SessionStatus)
	// statusListeners are ADDITIONAL status observers (AddStatusListener),
	// fired after onStatusCallback. Cross-cutting concerns (the Android
	// foreground-keepalive counter) register here so they survive the App
	// layer re-registering onStatusCallback via SetOnStatusChangeCallback.
	statusListeners []func(SessionStatus)
	// dataTaps are ADDITIONAL data observers (AddDataTap), fired alongside the
	// single-slot onDataCallback without replacing it. Used by the MCP
	// foreground job tap to collect a typed command's output without disturbing
	// the frontend's own stream subscription.
	dataTaps []func([]byte)
	// statusTaps are REMOVABLE status observers (AddStatusTap); a short-lived
	// watcher (MCP foreground job EOF) detaches when its job ends, unlike
	// session-lifetime statusListeners. Guarded by s.mu.
	statusTaps       []func(SessionStatus)
	mu               sync.RWMutex
	pendingCols      int
	pendingRows      int
	zmodemMode       bool
	zmodemTimer      *time.Timer
	zmodemDeadline   time.Time
	zmodemGeneration uint64
	lastReadTime     atomic.Int64
	// outputLogWriter, if non-nil, receives a copy of every byte emitted
	// via emitData. It is set by the App layer and lives longer than any
	// single session — a reconnect re-uses the same underlying logger by
	// installing the same writer on the new session.
	outputLogWriter func([]byte)
	// logOnConnect mirrors ConnectionConfig.LogOnConnect so the App
	// layer can query it via AutoLogOnConnect() and decide whether to
	// enable the log the first time this session binds to a panel.
	logOnConnect   bool
	logSessionName string
	logHost        string
	// idleSignal is sent-to (non-blocking) every time RecordReadActivity
	// runs. waitIdle subscribes to this channel to avoid the busy-loop
	// that previously woke every 50ms; see F-017. idleSignalOnce makes
	// the channel lazy-allocated so we don't have to touch every
	// baseSession constructor.
	idleSignal     chan struct{}
	idleSignalOnce sync.Once
	// Async emit machinery (issue #1053). Terminal sessions call
	// startEmitLoop so emitData hands chunks to the emitLoop goroutine
	// instead of invoking the (synchronous) Wails event callback inline —
	// a cat of a binary file used to back-pressure the PTY readLoop
	// through the frontend and froze the whole app. Sessions that never
	// call startEmitLoop (zero-value test sessions, monitor JSON
	// consumers) keep the synchronous path. Guarded by emitMu.
	emitMu      sync.Mutex
	emitCond    *sync.Cond
	emitQueue   [][]byte
	emitDropped int
	emitClosing bool
	// emitOverflow marks degraded drop-oldest mode: set when the producer
	// blocked on a full queue past emitBlockWatchdog, cleared once the
	// queue fully drains. Guarded by emitMu.
	emitOverflow bool
	// emitQuit is closed (and emitCond broadcast) once by stopEmitLoop;
	// emitDone is closed by emitLoop when it has fully exited. nil until
	// startEmitLoop runs.
	emitQuit chan struct{}
	emitDone chan struct{}
	emitOnce sync.Once
	// Output flow control (P-02): the frontend pauses the stream while its
	// xterm write backlog is above the high-water mark. outputPaused is
	// atomic so zmodem entry (under s.mu) can drop a hold without emitting
	// an emitMu→s.mu lock cycle; the cond, timer and generation live under
	// emitMu. Guarded by emitMu: outputPauseTimer, pauseGen.
	outputPaused     atomic.Bool
	outputPauseTimer *time.Timer
	pauseGen         uint64
	// utf8Tail holds ≤3 bytes of an incomplete trailing UTF-8 rune between
	// reads so every emitted chunk ends on a rune boundary (see
	// utf8_carry.go). Guarded by utf8Mu, which is a leaf lock: no path
	// holds it while acquiring another mutex.
	utf8Mu   sync.Mutex
	utf8Tail []byte
}

const (
	// emitQueueCapacity bounds the pending chunk queue. When the consumer
	// (frontend) falls behind, the producer blocks first (flow control —
	// see queueEmit); the OLDEST chunks are dropped only after the
	// emitBlockWatchdog expires, mirroring Alacritty/WezTerm where fresher
	// output matters more than stale bytes. Losing stale `cat` output is
	// cosmetic, but losing bytes from a full-screen TUI (menuconfig, vim,
	// ...) desyncs the screen — so dropping is a last resort, not the norm.
	emitQueueCapacity = 64
)

var (
	// emitFlushInterval coalesces high-frequency chunks into one IPC
	// event per window, cutting Wails emit count 10-100x under load.
	// Package-level var so tests can shorten it.
	emitFlushInterval = 16 * time.Millisecond
	// emitBlockWatchdog bounds how long queueEmit blocks the producer while
	// the queue is full. Blocking backpressures the PTY/SSH channel and
	// pauses the remote writer — the way a physical terminal applies flow
	// control — keeping the byte stream lossless for full-screen TUIs. If
	// the consumer stays wedged past the watchdog, the producer degrades
	// to drop-oldest so the readLoop and terminal input stay alive.
	// Package-level var so tests can shorten it.
	emitBlockWatchdog = 3 * time.Second
	// outputPauseTimeout bounds a frontend-requested output pause (P-02):
	// the session auto-resumes if no SessionResumeOutput arrives — a
	// crashed or suspended frontend must never wedge a session with a
	// silently paused stream. Shorter than the zmodem safety timeout: a
	// stalled readLoop just delays binary-mode detection by at most this.
	// Package-level var so tests can shorten it.
	outputPauseTimeout = 5 * time.Second
	// emitChunkMaxBytes caps one emit callback (P-08). Wails routes events
	// larger than its inline payload limit (~8KiB) through a fetch whose
	// promise chain serializes EVERY later event behind it — one merged
	// ~1MB flush used to park key echo and other small events behind a
	// huge JSON parse. 64KiB is the compromise: small enough that the
	// queueing delay stays imperceptible, large enough not to explode
	// ExecJS call counts (Wails' own comment notes fetch throughput wins
	// on WebView2 below ~8KiB, so never go that low). Splits happen only
	// at complete UTF-8 rune boundaries.
	emitChunkMaxBytes = 64 * 1024
)

const (
	// emitStopWait bounds how long stopEmitLoop waits for the final
	// flush. If the consumer is wedged, disconnect must not hang on it.
	emitStopWait = 2 * time.Second
)

func (s *baseSession) ID() string            { return s.id }
func (s *baseSession) Type() string          { return s.sessionType }
func (s *baseSession) Title() string         { return s.title }
func (s *baseSession) Status() SessionStatus { s.mu.RLock(); defer s.mu.RUnlock(); return s.status }

func (s *baseSession) SetOnDataCallback(cb func([]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDataCallback = cb
}

func (s *baseSession) SetOnStatusChangeCallback(cb func(SessionStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onStatusCallback = cb
}

// AddStatusListener registers an ADDITIONAL status observer. Unlike
// SetOnStatusChangeCallback (single slot, owned by the App layer and
// overwritten on re-register), listeners accumulate — used by cross-cutting
// concerns like the Android foreground-keepalive tracker that must survive
// the app layer re-registering its own callback.
func (s *baseSession) AddStatusListener(cb func(SessionStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusListeners = append(s.statusListeners, cb)
}

// AddDataTap registers an additive data observer. Unlike the single-slot
// SetOnDataCallback (owned by the App layer and overwritten on re-register),
// taps accumulate and are fired alongside it, so a job tap never replaces the
// frontend's stream subscription. Returns an unsubscribe function.
func (s *baseSession) AddDataTap(cb func([]byte)) func() {
	s.mu.Lock()
	idx := len(s.dataTaps)
	for i, tap := range s.dataTaps {
		if tap == nil {
			idx = i
			break
		}
	}
	if idx == len(s.dataTaps) {
		s.dataTaps = append(s.dataTaps, cb)
	} else {
		s.dataTaps[idx] = cb
	}
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.dataTaps[idx] = nil
			s.mu.Unlock()
		})
	}
}

// AddStatusTap registers a removable observer with a stable slot. Removing
// one observer must never change the identity of another observer's slot.
func (s *baseSession) AddStatusTap(cb func(SessionStatus)) func() {
	s.mu.Lock()
	idx := len(s.statusTaps)
	for i, tap := range s.statusTaps {
		if tap == nil {
			idx = i
			break
		}
	}
	if idx == len(s.statusTaps) {
		s.statusTaps = append(s.statusTaps, cb)
	} else {
		s.statusTaps[idx] = cb
	}
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.statusTaps[idx] = nil
			s.mu.Unlock()
		})
	}
}

func (s *baseSession) setStatus(st SessionStatus) {
	s.mu.Lock()
	s.status = st
	cb := s.onStatusCallback
	listeners := make([]func(SessionStatus), len(s.statusListeners))
	copy(listeners, s.statusListeners)
	taps := make([]func(SessionStatus), len(s.statusTaps))
	copy(taps, s.statusTaps)
	s.mu.Unlock()
	if st == StatusDisconnected || st == StatusError {
		// Terminal output is over; flush anything still queued (e.g. the
		// disconnect notice emitted right before Disconnect) and release
		// the emitLoop goroutine. emitData falls back to the synchronous
		// path afterwards.
		s.stopEmitLoop()
	}
	if cb != nil {
		cb(st)
	}
	for _, l := range listeners {
		l(st)
	}
	for _, tap := range taps {
		if tap != nil {
			tap(st)
		}
	}
}

// startEmitLoop enables the async, bounded emit path for this session.
// Called by terminal session types from Connect. Idempotent.
func (s *baseSession) startEmitLoop() {
	s.emitOnce.Do(func() {
		// A (re)connect starts a fresh byte stream; a stale tail from the
		// previous connection must not prefix the new output.
		s.resetUTF8Carry()
		s.emitQuit = make(chan struct{})
		s.emitDone = make(chan struct{})
		s.emitCond = sync.NewCond(&s.emitMu)
		go s.emitLoop()
	})
}

// stopEmitLoop shuts the emit loop down, flushing everything still queued
// so the tail of the output (e.g. a disconnect notice queued right before
// Disconnect) is never lost. After it returns, emitData falls back to the
// synchronous path. Safe to call multiple times and when never started.
func (s *baseSession) stopEmitLoop() {
	s.emitMu.Lock()
	if s.emitQuit == nil || s.emitClosing {
		s.emitMu.Unlock()
		return
	}
	s.emitClosing = true
	s.emitMu.Unlock()
	close(s.emitQuit)
	s.emitCond.Broadcast()
	select {
	case <-s.emitDone:
	case <-time.After(emitStopWait):
		log.Writef("session %s: emitLoop did not exit within %s during stop", s.id, emitStopWait)
	}
}

func (s *baseSession) emitData(data []byte) {
	s.mu.RLock()
	w := s.outputLogWriter
	cb := s.onDataCallback
	taps := make([]func([]byte), len(s.dataTaps))
	copy(taps, s.dataTaps)
	s.mu.RUnlock()
	if w != nil {
		w(data)
	}
	for _, tap := range taps {
		if tap != nil {
			tap(data)
		}
	}
	if cb == nil {
		return
	}
	// Hold back an incomplete trailing UTF-8 rune and prepend it to the next
	// read, so json.Marshal never sees (and replaces) a split rune. ZMODEM/
	// binary bytes are routed to emitBinary and never reach here; skip the
	// carry anyway while binary mode is on so a stray text chunk during a
	// transfer cannot queue bytes across the mode switch.
	if !s.IsZmodemMode() {
		data = s.carryUTF8(data)
		if len(data) == 0 {
			return
		}
	}
	if s.queueEmit(data) {
		return
	}
	// Synchronous path: emit loop never started (zero-value sessions and
	// tests), already stopped, or a zmodem transfer is in progress — file
	// transfer bytes must never be dropped or delayed.
	cb(data)
}

// queueEmit hands data to the emit loop. Returns false when the caller must
// emit synchronously instead. Copies the bytes: readLoops reuse their read
// buffer across iterations.
//
// When the queue is full the producer blocks first (flow control): the PTY/
// SSH channel backpressures and the remote writer pauses, so no bytes are
// lost and full-screen TUIs stay in sync. Only when the block outlives
// emitBlockWatchdog — the consumer is wedged, not merely slow — does it
// degrade to drop-oldest, keeping the readLoop and terminal input alive.
// Degradation is sticky until the queue fully drains, so a long wedged
// period drops steadily instead of re-waiting the watchdog on every chunk.
func (s *baseSession) queueEmit(data []byte) bool {
	if s.IsZmodemMode() {
		return false
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if s.emitQuit == nil || s.emitClosing {
		return false
	}
	// Frontend flow control (P-02): while output is paused the frontend's
	// write backlog is above its high-water mark. Hold here (cond.Wait
	// releases emitMu) so backpressure reaches the SSH/PTY channel and no
	// further chunks pile up in the queue. Bounded by the pause watchdog;
	// SetOutputPaused(false) and zmodem entry both broadcast. zmodem's own
	// data never reaches this point — it routes to emitBinary.
	for s.outputPaused.Load() && !s.emitClosing {
		s.emitCond.Wait()
	}
	if s.emitClosing {
		return false
	}
	if len(s.emitQueue) >= emitQueueCapacity && !s.emitOverflow {
		watchdog := time.AfterFunc(emitBlockWatchdog, s.emitCond.Broadcast)
		defer watchdog.Stop()
		deadline := time.Now().Add(emitBlockWatchdog)
		for len(s.emitQueue) >= emitQueueCapacity && !s.emitClosing {
			if !time.Now().Before(deadline) {
				// Consumer wedged past the watchdog: degrade to
				// drop-oldest until the queue drains again.
				s.emitOverflow = true
				break
			}
			s.emitCond.Wait()
		}
	}
	for len(s.emitQueue) >= emitQueueCapacity {
		// Drop-oldest keeps the readLoop unblocked and the terminal
		// showing the freshest output. Reached only in degraded mode.
		s.emitQueue = s.emitQueue[1:]
		s.emitDropped++
	}
	if s.emitClosing {
		// stopEmitLoop broadcast while we waited: the queue has been
		// force-flushed, so hand this chunk to the synchronous path.
		return false
	}
	s.emitQueue = append(s.emitQueue, append([]byte(nil), data...))
	s.emitCond.Signal()
	return true
}

// drainEmitQueue returns all currently queued chunks under one lock.
func (s *baseSession) drainEmitQueue() [][]byte {
	s.emitMu.Lock()
	chunks := s.emitQueue
	s.emitQueue = nil
	// Queue fully drained: the consumer caught up, so return to lossless
	// flow-control mode on the next full queue.
	s.emitOverflow = false
	dropped := s.emitDropped
	s.emitDropped = 0
	s.emitMu.Unlock()
	if dropped > 0 {
		log.Writef("session %s: emit queue overflow, dropped %d stale chunk(s)", s.id, dropped)
	}
	return chunks
}

// flushEmitChunks coalesces chunks into a single callback invocation so a
// flood of output costs one IPC event per flush window, not one per chunk.
// The coalesced payload is then capped at emitChunkMaxBytes per callback —
// split only on complete UTF-8 rune boundaries (P-08; the chunks already
// end on boundaries thanks to emitData's carry, so only the cap cuts).
func (s *baseSession) flushEmitChunks(chunks [][]byte) {
	s.mu.RLock()
	cb := s.onDataCallback
	s.mu.RUnlock()
	if cb == nil || len(chunks) == 0 {
		return
	}
	if len(chunks) == 1 {
		emitChunked(cb, chunks[0])
		return
	}
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	joined := make([]byte, 0, total)
	for _, c := range chunks {
		joined = append(joined, c...)
	}
	emitChunked(cb, joined)
}

// emitChunked invokes cb with data split into pieces of at most
// emitChunkMaxBytes each, cut only at complete UTF-8 rune boundaries — a
// piece must never end mid-rune (P-03's guarantee survives P-08's split:
// json.Marshal would turn a split rune into U+FFFD again).
func emitChunked(cb func([]byte), data []byte) {
	for len(data) > emitChunkMaxBytes {
		complete, _ := splitIncompleteUTF8(data[:emitChunkMaxBytes])
		if len(complete) == 0 {
			// UTF-8 runes are at most 4 bytes, so complete is always ≥
			// cap-3 here; guard anyway so pathological input cannot
			// loop forever.
			complete = data[:emitChunkMaxBytes]
		}
		cb(complete)
		data = data[len(complete):]
	}
	if len(data) > 0 {
		cb(data)
	}
}

func (s *baseSession) emitLoop() {
	defer close(s.emitDone)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	for {
		// Sleep until data arrives or shutdown is requested.
		s.emitMu.Lock()
		for len(s.emitQueue) == 0 && !s.emitClosing {
			s.emitCond.Wait()
		}
		s.emitMu.Unlock()

		// Coalesce the FIRST flush of a burst into one IPC event: that is
		// what keeps a trickle (shell echo, one log line at a time) from
		// costing one Wails event per chunk. Every following pass in the
		// same burst skips the window when the producer already refilled
		// the queue during the previous flush — i.e. when output is
		// sustained rather than trickling.
		//
		// The unconditional window used to cap throughput at
		// emitQueueCapacity chunks per emitFlushInterval no matter how fast
		// the consumer was. With small chunks (ConPTY/TUI redraws emit many
		// short writes) that ceiling sat below the frontend's parse rate,
		// so the queue stayed full and the readLoop blocked on a producer
		// that was merely waiting for the timer.
		coalesce := true
		for {
			select {
			case <-s.emitQuit:
				s.flushEmitChunks(s.drainEmitQueue())
				return
			default:
			}
			if coalesce {
				// Batch window: give the producer a beat to accumulate more
				// chunks before flushing.
				timer.Reset(emitFlushInterval)
				select {
				case <-s.emitQuit:
					s.flushEmitChunks(s.drainEmitQueue())
					return
				case <-timer.C:
				}
			}
			chunks := s.drainEmitQueue()
			if len(chunks) == 0 {
				break
			}
			s.flushEmitChunks(chunks)
			// Chunks arrived while we were emitting: flush them immediately
			// instead of adding another coalescing delay.
			coalesce = !s.emitQueuePending()
		}
	}
}

// emitQueuePending reports whether chunks are already waiting for the emit
// loop. Lets the loop decide whether another coalescing window is worth
// waiting for: when the producer is outpacing the consumer, flushing
// immediately is what keeps the readLoop from blocking on the queue.
func (s *baseSession) emitQueuePending() bool {
	s.emitMu.Lock()
	pending := len(s.emitQueue) > 0
	s.emitMu.Unlock()
	return pending
}

// SetOutputLogWriter installs (or clears with nil) the sink that
// receives each byte emitted via emitData. Ownership of the underlying
// logger lives at the App layer so it can outlive any single session
// and survive reconnects.
func (s *baseSession) SetOutputLogWriter(w func([]byte)) {
	s.mu.Lock()
	s.outputLogWriter = w
	s.mu.Unlock()
}

// SetLogOnConnect records the per-connection auto-log preference so
// the App layer can read it via AutoLogOnConnect(). Each session type
// calls this from Connect based on ConnectionConfig.LogOnConnect.
func (s *baseSession) SetLogOnConnect(v bool) { s.logOnConnect = v }

// AutoLogOnConnect reports whether this session was created from a
// connection configured to start logging automatically.
func (s *baseSession) AutoLogOnConnect() bool { return s.logOnConnect }

// SetLogIdentity records the connection fields used by the session-log
// filename template. It is called before Connect, so auto-logging can use the
// original configured name and host even while the connection is starting.
func (s *baseSession) SetLogIdentity(name, host string) {
	s.mu.Lock()
	s.logSessionName = name
	s.logHost = host
	s.mu.Unlock()
}

func (s *baseSession) LogIdentity() (name, host string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logSessionName, s.logHost
}

func (s *baseSession) SetPendingSize(cols, rows int) {
	s.mu.Lock()
	s.pendingCols = cols
	s.pendingRows = rows
	s.mu.Unlock()
}

func (s *baseSession) GetPendingSize() (cols, rows int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pendingCols, s.pendingRows
}

func (s *baseSession) getInitialSize(defCols, defRows int) (int, int) {
	cols, rows := s.GetPendingSize()
	if cols <= 0 {
		cols = defCols
	}
	if rows <= 0 {
		rows = defRows
	}
	return cols, rows
}

func (s *baseSession) SetZmodemMode(v bool) {
	s.mu.Lock()
	s.setZmodemModeLocked(v)
	s.mu.Unlock()
}

// SetOutputPaused holds (v=true) or releases (v=false) the text-output
// stream — the frontend's end-to-end flow control (P-02). While paused the
// readLoop blocks in queueEmit, so backpressure reaches the SSH/PTY channel
// instead of the frontend's write buffer piling up seconds of output (the
// cause of laggy Ctrl+C and jumpy full-screen redraws).
//
// Every pause is bounded by outputPauseTimeout (auto-resume), and ZMODEM
// ignores pause entirely — entering binary mode releases an active hold so
// a transfer can never be stalled by the frontend.
func (s *baseSession) SetOutputPaused(v bool) {
	if v {
		if s.IsZmodemMode() {
			return
		}
		if !s.outputPaused.CompareAndSwap(false, true) {
			return // already paused; the existing watchdog still bounds it
		}
		timeout := outputPauseTimeout
		s.emitMu.Lock()
		s.pauseGen++
		gen := s.pauseGen
		s.outputPauseTimer = time.AfterFunc(timeout, func() {
			s.emitMu.Lock()
			stale := s.pauseGen != gen
			s.emitMu.Unlock()
			if stale {
				return // paused again meanwhile; that pause owns the watchdog
			}
			log.Writef("session %s: output pause watchdog expired after %s, auto-resuming", s.id, timeout)
			s.SetOutputPaused(false)
		})
		s.emitMu.Unlock()
		return
	}
	if !s.outputPaused.CompareAndSwap(true, false) {
		return
	}
	s.emitMu.Lock()
	s.pauseGen++ // invalidate a watchdog callback that may already be queued
	if s.outputPauseTimer != nil {
		s.outputPauseTimer.Stop()
		s.outputPauseTimer = nil
	}
	cond := s.emitCond
	s.emitMu.Unlock()
	if cond != nil {
		cond.Broadcast() // wake readLoops parked in queueEmit's pause wait
	}
}

// releasePauseForZmodem drops an active output pause when the session enters
// binary mode: the frontend's hold must never stall a transfer. Callers hold
// s.mu; emitMu is a leaf lock from here (queueEmit never takes s.mu while
// holding emitMu), and emitCond may be nil for sessions that never started
// the emit loop — nobody waits on it then.
func (s *baseSession) releasePauseForZmodem() {
	if !s.outputPaused.CompareAndSwap(true, false) {
		return
	}
	s.emitMu.Lock()
	s.pauseGen++
	if s.outputPauseTimer != nil {
		s.outputPauseTimer.Stop()
		s.outputPauseTimer = nil
	}
	cond := s.emitCond
	s.emitMu.Unlock()
	if cond != nil {
		cond.Broadcast()
	}
}

// EndZmodem leaves binary mode and emits terminal bytes that followed the
// protocol's final handshake through the normal text-output path. Session
// types with an output decoder can override this method.
func (s *baseSession) EndZmodem(trailing []byte) {
	s.mu.Lock()
	s.setZmodemModeLocked(false)
	w := s.outputLogWriter
	cb := s.onDataCallback
	s.mu.Unlock()
	// Drop the pre-transfer tail (already incomplete), then give the
	// trailing bytes the same rune-boundary protection as normal output.
	s.resetUTF8Carry()
	trailing = s.carryUTF8(trailing)
	if w != nil && len(trailing) > 0 {
		w(trailing)
	}
	if cb != nil && len(trailing) > 0 {
		cb(trailing)
	}
}

// This is only a final safety net for an abandoned frontend transfer. Keep it
// longer than the frontend protocol watchdog so file-picker interaction is not
// mistaken for a stalled transfer.
var zmodemModeTimeout = 2 * time.Minute

func (s *baseSession) setZmodemModeLocked(v bool) {
	s.zmodemGeneration++
	s.zmodemMode = v
	if v {
		// Entering binary mode: drop any held incomplete-rune tail so it
		// cannot be prepended to post-transfer output.
		s.resetUTF8Carry()
		// Binary mode owns the stream: release any frontend output pause so
		// the transfer's synchronous path can never stall behind it.
		s.releasePauseForZmodem()
	}
	if s.zmodemTimer != nil {
		s.zmodemTimer.Stop()
		s.zmodemTimer = nil
	}
	if !v {
		s.zmodemDeadline = time.Time{}
		return
	}
	generation := s.zmodemGeneration
	s.zmodemDeadline = time.Now().Add(zmodemModeTimeout)
	s.zmodemTimer = time.AfterFunc(zmodemModeTimeout, func() {
		s.mu.Lock()
		if s.zmodemMode && s.zmodemGeneration == generation {
			s.zmodemMode = false
			s.zmodemTimer = nil
			s.zmodemDeadline = time.Time{}
		}
		s.mu.Unlock()
	})
}

func (s *baseSession) IsZmodemMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.zmodemMode
}

func (s *baseSession) SetOnBinaryCallback(cb func([]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onBinaryCallback = cb
}

func (s *baseSession) emitBinary(data []byte) {
	s.mu.Lock()
	cb := s.onBinaryCallback
	if s.zmodemMode {
		s.setZmodemModeLocked(true)
	}
	s.mu.Unlock()
	if cb != nil {
		cb(data)
	}
}

// looksLikeZmodemHeader reports whether data contains a ZMODEM frame header.
// A real header is ZPAD ZPAD ZDLE frame-type: `**` `\x18` `[A-C]`. The ZDLE
// (0x18) control byte is mandatory and effectively never appears in ordinary
// terminal output, so requiring it avoids false positives — e.g. vim rendering
// a file whose content contains `**` followed by a long hex string, which used
// to trip a looser heuristic and flip the session into binary mode, crashing
// the remote shell (issue #242).
func looksLikeZmodemHeader(data []byte) bool {
	for i := 0; i+3 < len(data); i++ {
		if data[i] == '*' && data[i+1] == '*' && data[i+2] == 0x18 &&
			data[i+3] >= 'A' && data[i+3] <= 'C' {
			return true
		}
	}
	return false
}

// RecordReadActivity updates the last-read timestamp for idle detection.
// Each session's readLoop should call this whenever data is received.
func (s *baseSession) RecordReadActivity() {
	s.lastReadTime.Store(time.Now().UnixNano())
	// Non-blocking signal to any waitIdle goroutine. A 1-slot buffer is
	// enough: waitIdle only cares that "something happened since the last
	// tick"; coalescing repeated signals is fine.
	ch, _ := s.idleSignalCh()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// idleSignalCh returns the lazy-allocated idle signal channel.
func (s *baseSession) idleSignalCh() (chan struct{}, bool) {
	s.idleSignalOnce.Do(func() {
		s.idleSignal = make(chan struct{}, 1)
	})
	return s.idleSignal, true
}

// idleSince reports how long since the last read activity. Returns -1 if no
// read has ever been recorded. Used for disconnect diagnostics.
func (s *baseSession) idleSince() time.Duration {
	ns := s.lastReadTime.Load()
	if ns == 0 {
		return -1
	}
	return time.Since(time.Unix(0, ns))
}

// waitIdle blocks until no read activity has occurred for the given idle
// duration, or the overall timeout expires. It returns true on idle detection.
// Implemented with a 1-slot signal channel + idle timer instead of a 50ms
// busy loop; RecordReadActivity pushes to the channel so we only wake when
// there is something to check (F-017).
func (s *baseSession) waitIdle(timeout, idle time.Duration) bool {
	deadline := time.Now().Add(timeout)
	ch, _ := s.idleSignalCh()
	for {
		last := time.Unix(0, s.lastReadTime.Load())
		if !last.IsZero() && time.Since(last) >= idle {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		// Cap the inner wait so a stream of activity can't starve the
		// deadline check; idle granularity is the dominant signal.
		wait := idle
		if wait > remaining {
			wait = remaining
		}
		select {
		case <-ch:
			// Activity happened — re-check the timestamp on next loop.
		case <-time.After(wait):
			// Either we sat idle past `idle`, or the deadline is close;
			// fall through and let the timestamp check decide.
		}
	}
}

// RunPostLoginScript sends each non-empty line of script after the terminal
// output goes idle, and waits for idle between commands. Stops early if ctx
// is cancelled or isConnected returns false.
func (s *baseSession) RunPostLoginScript(ctx context.Context, script string, send func([]byte), isConnected func() bool) {
	if strings.TrimSpace(script) == "" {
		return
	}
	// Wait for shell to finish initialization.
	if !s.waitIdle(5*time.Second, 300*time.Millisecond) {
		return
	}
	lines := strings.Split(script, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !isConnected() {
			return
		}
		send([]byte(line + "\r"))
		// Wait for command output to settle.
		if !s.waitIdle(3*time.Second, 300*time.Millisecond) {
			return
		}
	}
}
