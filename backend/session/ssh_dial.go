package session

import (
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ys-ll/uniterm/backend/log"
)

type sshConnDialer func() (net.Conn, error)
type sshClientConfigFactory func() (*ssh.ClientConfig, func(), error)

// sshHandshakeTimeout bounds the SSH key-exchange + authentication handshake.
// ClientConfig.Timeout only covers ssh.Dial's TCP connect, so without a conn
// deadline a server/middlebox that accepts TCP and then stalls leaves the
// session at "Connecting" forever. Overridable for tests.
var sshHandshakeTimeout = 30 * time.Second

// sshHandshakeDeadline returns the deadline applied to a raw conn around
// ssh.NewClientConn: the config's own Timeout when set (per-caller semantic,
// e.g. the 6s cwd probe), else sshHandshakeTimeout.
func sshHandshakeDeadline(c *ssh.ClientConfig) time.Duration {
	if c != nil && c.Timeout > 0 {
		return c.Timeout
	}
	return sshHandshakeTimeout
}

// dialSSHWithCipherFallback keeps modern AEAD preference, but retries a
// handshake EOF once with CTR first for servers that falsely advertise GCM.
// The algorithm sets are resolved per connection (see resolveSSHAlgorithms)
// and tried in order; only a first-attempt EOF advances to the next set.
func dialSSHWithCipherFallback(addr string, sets []sshAlgoSet, newConfig sshClientConfigFactory, dial sshConnDialer) (*ssh.Client, error) {
	var lastErr error
	for i, set := range sets {
		conn, err := dial()
		if err != nil {
			return nil, fmt.Errorf("tcp dial: %w", err)
		}
		config, cleanup, err := newConfig()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("ssh auth: %w", err)
		}
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			tcpConn.SetKeepAlive(true)
			tcpConn.SetKeepAlivePeriod(sshKeepAliveInterval)
		}
		attempt := *config
		attempt.Config = set.config()
		// Offer a key type that is already pinned for this address first
		// (OpenSSH behavior) so a server that added a second key type keeps
		// negotiating the pinned one — see preferPinnedHostKeyAlgorithms.
		// addr is the dial address; for a jump-host rewrite it holds no pin,
		// which leaves the order untouched (hostKeyPinAddr callers below
		// reorder against the original host:port themselves).
		attempt.HostKeyAlgorithms = preferPinnedHostKeyAlgorithms(set.HostKeys, addr)
		// Bound the handshake itself: ClientConfig.Timeout only covers
		// ssh.Dial's TCP connect, so a server or middlebox that accepts TCP
		// and then stalls would otherwise leave Connect at "Connecting"
		// forever. Best-effort — conns backed by an SSH channel (jump hops)
		// do not support deadlines and return an error we ignore.
		_ = conn.SetDeadline(time.Now().Add(sshHandshakeDeadline(config)))
		sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, &attempt)
		cleanup()
		if err == nil {
			// The connection now lives for the whole session: clear the
			// handshake deadline or every later read/write fails once it
			// lapses.
			_ = conn.SetDeadline(time.Time{})
			return ssh.NewClient(sshConn, chans, reqs), nil
		}
		conn.Close()
		lastErr = err
		if i == 0 && errors.Is(err, io.EOF) {
			continue
		}
		break
	}
	return nil, fmt.Errorf("ssh handshake: %w", lastErr)
}

// Markers within x/crypto's error strings. The library returns both as plain
// fmt.Errorf values with no typed sentinel, so the message is the only
// stable handle.
const (
	authExhaustedMarker  = "unable to authenticate"
	kbdIntColdFailMarker = "unexpected message type 51 (expected 60)"
)

// dialSSHWithAuthRetry offers keyboard-interactive alongside the primary
// methods (password/key) on the FIRST handshake, and retries once without it.
//
// Keeping keyboard-interactive in the first handshake means prompt-based
// logins (OTP/2FA, keyboard-interactive-only servers) and a password rejection
// followed by an interactive prompt complete in a single TCP handshake — with
// one handshake per failed attempt, every wrong password paid a full extra
// dial plus a second server-side fail delay before the user got to retype
// (issue #949).
//
// x/crypto surfaces a server that answers the keyboard-interactive initiation
// with USERAUTH_FAILURE before any info request as "ssh: unexpected message
// type 51 (expected 60)". RFC 4252 allows that reply and OpenSSH's own client
// treats it as a plain rejection, but the library aborts the handshake with
// that cryptic protocol error. Servers that fail keyboard-interactive cold are
// common (root login restricted to keys, dropbear without PAM), so the retry
// drops keyboard-interactive; its plain rejection is the honest auth failure
// the user should see.
func dialSSHWithAuthRetry(addr string, sets []sshAlgoSet, authConfig, fallbackConfig sshClientConfigFactory, dial sshConnDialer) (*ssh.Client, error) {
	if authConfig == nil {
		authConfig = fallbackConfig
	}
	client, err := dialSSHWithCipherFallback(addr, sets, authConfig, dial)
	if err == nil || !strings.Contains(err.Error(), kbdIntColdFailMarker) {
		return client, err
	}
	client, retryErr := dialSSHWithCipherFallback(addr, sets, fallbackConfig, dial)
	if retryErr == nil {
		return client, nil
	}
	return nil, retryErr
}

// dialSSHTCP dials addr (through the upstream proxy when non-nil), performs the
// SSH handshake, and returns a *ssh.Client. Used by the SFTP and monitor
// sessions, which previously dialed directly via ssh.Dial.
func dialSSHTCP(addr string, clientConfig *ssh.ClientConfig, upstream *SocksProxy) (*ssh.Client, error) {
	raw, err := dialFirstHop(addr, upstream)
	if err != nil {
		return nil, fmt.Errorf("tcp dial: %w", err)
	}
	// Bound the handshake (see dialSSHWithCipherFallback); cleared on success.
	_ = raw.SetDeadline(time.Now().Add(sshHandshakeDeadline(clientConfig)))
	// Offer an already-pinned key type first so a server that added a second
	// key type keeps negotiating the pinned one (see
	// preferPinnedHostKeyAlgorithms). The caller's config is left untouched.
	attempt := *clientConfig
	attempt.HostKeyAlgorithms = preferPinnedHostKeyAlgorithms(clientConfig.HostKeyAlgorithms, addr)
	conn, chans, reqs, err := ssh.NewClientConn(raw, addr, &attempt)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	_ = raw.SetDeadline(time.Time{})
	return ssh.NewClient(conn, chans, reqs), nil
}

// DialSSHClient 建立非交互 SSH 连接（容器 runner 用，无 PTY、无键盘交互回显）。
// 与 ssh_session 的交互式拨号共享认证与密钥交换配置。不提供 keyboard-interactive：
// 此上下文中无法应答 challenge，提供它只会让普通密码拒绝被回调自身的错误掩盖。
func DialSSHClient(config ConnectionConfig) (*ssh.Client, error) {
	addr := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	// Pin under the original host:port — see hostKeyPinAddr (jump-host
	// tunnels rewrite the dial address to 127.0.0.1:<ephemeral>).
	pinAddr := hostKeyPinAddr(config, addr)
	cb, trust := NewHostKeyVerifier(pinAddr)
	newConfig := func(challenge ssh.KeyboardInteractiveChallenge) sshClientConfigFactory {
		return func() (*ssh.ClientConfig, func(), error) {
			authMethods, cleanup, err := makeSSHAuthMethodsForAttempt(config, challenge)
			if err != nil {
				return nil, nil, err
			}
			return &ssh.ClientConfig{
				User:            config.User,
				Auth:            authMethods,
				Timeout:         30 * time.Second,
				HostKeyCallback: cb,
			}, cleanup, nil
		}
	}
	sets, err := resolveSSHDialAlgorithms(config.SSHAlgorithms)
	if err != nil {
		return nil, err
	}
	// Pin-aware host key order (the dial helper only sees the dial address,
	// which a jump-host rewrite replaced with 127.0.0.1:<ephemeral>).
	for i := range sets {
		sets[i].HostKeys = preferPinnedHostKeyAlgorithms(sets[i].HostKeys, pinAddr)
	}
	factory := newConfig(nil)
	client, err := dialSSHWithAuthRetry(addr, sets, factory, factory, func() (net.Conn, error) {
		// Route through the configured proxy like every other SSH dial path
		// (ssh_session, tunnels, sftp): dialing direct ignored the user's
		// proxy policy and simply failed on proxy-only networks.
		return dialFirstHop(addr, config.Proxy)
	})
	if err != nil {
		return nil, err
	}
	if trust != nil && trust.FirstTrust {
		log.Writef("[known_hosts] host key not seen before — trusted and saved: %s %s", pinAddr, trust.Fingerprint)
	}
	return client, nil
}

// sshAlgoModeOf reports the effective mode of a connection's algorithm
// preferences, used for error annotation.
func sshAlgoModeOf(p *SSHAlgoConfig) string {
	if p == nil || p.Mode == "" {
		return SSHAlgoModeCompatible
	}
	return p.Mode
}

// resolveSSHDialAlgorithms returns the algorithm sets tried in order on a
// dial path: the connection's resolved base set, followed by the EOF-retry
// set when it differs from the base.
func resolveSSHDialAlgorithms(p *SSHAlgoConfig) ([]sshAlgoSet, error) {
	base, retry, err := resolveSSHAlgorithms(p)
	if err != nil {
		return nil, err
	}
	if slices.Equal(base.Ciphers, retry.Ciphers) && slices.Equal(base.KeyExchanges, retry.KeyExchanges) &&
		slices.Equal(base.MACs, retry.MACs) && slices.Equal(base.HostKeys, retry.HostKeys) {
		return []sshAlgoSet{base}, nil
	}
	return []sshAlgoSet{base, retry}, nil
}
