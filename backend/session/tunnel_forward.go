package session

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"

	"github.com/ys-ll/uniterm/backend/log"
)

// ConnResolver resolves a saved SSH connection by its ID. app.go supplies one
// backed by the connection store so the tunnel layer can look up the exit
// connection and recurse its jump hosts without importing the store package.
type ConnResolver func(id string) (ConnectionConfig, bool)

// userTunnelEntry is one running user-configured tunnel: the local (or remote)
// listener plus every ssh.Client in its chain, so Stop can tear the whole thing
// down.
type userTunnelEntry struct {
	listener net.Listener
	clients  []*ssh.Client // chain order; last is the exit
	quit     chan struct{}
}

// StartTunnel brings a user-configured tunnel up: it dials the proxy chain
// (recursing the exit connection's own jump hosts, optionally entering through
// an upstream SOCKS5/HTTP proxy) and starts the listener for the tunnel's mode.
// It returns the running state; on failure the returned state carries the error
// and nothing is left running.
func (ts *TunnelService) StartTunnel(t Tunnel, resolve ConnResolver) TunnelState {
	// A dynamic tunnel that would expose an unauthenticated SOCKS proxy to
	// the network is rejected before anything is reserved or dialed.
	if err := validateSOCKSExposure(t); err != nil {
		return ts.setState(t.ID, TunnelState{ID: t.ID, Status: TunnelError, Error: err.Error()})
	}

	// Reserve the ID under the lock BEFORE dialing. Everything below is
	// network I/O: without the reservation an auto-start and a manual start
	// of the same ID both dialed, and the loser either overwrote the winner's
	// entry — orphaning a listener and SSH chain nothing could stop — or
	// flipped a running tunnel to error.
	quit := make(chan struct{})
	ts.mu.Lock()
	if _, exists := ts.userTunnels[t.ID]; exists {
		ts.mu.Unlock()
		return ts.setState(t.ID, TunnelState{ID: t.ID, Status: TunnelRunning})
	}
	// Placeholder entry; the finished listener/clients replace it on publish.
	ts.userTunnels[t.ID] = &userTunnelEntry{quit: quit}
	ts.mu.Unlock()

	// release drops the reservation after a failure. StopTunnel may already
	// have removed it (and closed quit) — never delete someone else's entry.
	release := func() {
		ts.mu.Lock()
		if cur, ok := ts.userTunnels[t.ID]; ok && cur.quit == quit {
			delete(ts.userTunnels, t.ID)
		}
		ts.mu.Unlock()
	}

	chain, err := buildHopChain(t.SSHConnID, resolve)
	if err != nil {
		release()
		return ts.setState(t.ID, TunnelState{ID: t.ID, Status: TunnelError, Error: err.Error()})
	}

	exit, clients, err := ts.dialChain(chain, t.Upstream)
	if err != nil {
		release()
		return ts.setState(t.ID, TunnelState{ID: t.ID, Status: TunnelError, Error: err.Error()})
	}

	listener, err := ts.startListener(t, exit, quit)
	if err != nil {
		closeClients(clients)
		release()
		return ts.setState(t.ID, TunnelState{ID: t.ID, Status: TunnelError, Error: err.Error()})
	}

	// Publish over the reservation: if StopTunnel removed it while we were
	// dialing, tear down instead of orphaning a listener that nothing can
	// stop any more.
	ts.mu.Lock()
	cur, ok := ts.userTunnels[t.ID]
	if !ok || cur.quit != quit {
		ts.mu.Unlock()
		listener.Close()
		closeClients(clients)
		return ts.setState(t.ID, TunnelState{ID: t.ID, Status: TunnelStopped})
	}
	ts.userTunnels[t.ID] = &userTunnelEntry{listener: listener, clients: clients, quit: quit}
	ts.mu.Unlock()

	// Watch the exit client: keepalive actively closes a dead chain, and Wait
	// unblocks when the connection drops, so we can flip the tunnel to error.
	go tunnelKeepAlive(exit, quit, "tunnel="+t.ID)
	go func() {
		exit.Wait()
		select {
		case <-quit:
			// Stopped by the user; state already handled by StopTunnel.
		default:
			ts.StopTunnel(t.ID)
			ts.setState(t.ID, TunnelState{ID: t.ID, Status: TunnelError, Error: "ssh chain disconnected"})
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	return ts.setState(t.ID, TunnelState{ID: t.ID, Status: TunnelRunning, LocalPort: port})
}

// startListener opens the listener appropriate to the tunnel's mode and starts
// its accept loop. For local/dynamic the listener is local (net.Listen); for
// remote it lives on the exit host (exit.Listen).
func (ts *TunnelService) startListener(t Tunnel, exit *ssh.Client, quit chan struct{}) (net.Listener, error) {
	bindHost := t.ListenHost
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	bindAddr := net.JoinHostPort(bindHost, strconv.Itoa(t.ListenPort))

	switch t.Mode {
	case TunnelLocal:
		ln, err := net.Listen("tcp", bindAddr)
		if err != nil {
			return nil, fmt.Errorf("tunnel listen: %w", err)
		}
		target := net.JoinHostPort(t.TargetHost, strconv.Itoa(t.TargetPort))
		go acceptLoop(ln, quit, func(c net.Conn) {
			remote, err := exit.Dial("tcp", target)
			if err != nil {
				c.Close()
				return
			}
			pipe(c, remote)
		})
		return ln, nil

	case TunnelDynamic:
		ln, err := net.Listen("tcp", bindAddr)
		if err != nil {
			return nil, fmt.Errorf("tunnel listen: %w", err)
		}
		go acceptLoop(ln, quit, func(c net.Conn) {
			// Bounded, optionally authenticated negotiation (see
			// socks5Negotiate / validateSOCKSExposure).
			target, err := socks5Negotiate(c, t.SOCKSUser, t.SOCKSPass)
			if err != nil {
				c.Close()
				return
			}
			remote, err := exit.Dial("tcp", target)
			if err != nil {
				socks5Reply(c, socks5HostUnreachable)
				c.Close()
				return
			}
			if err := socks5Reply(c, socks5Success); err != nil {
				remote.Close()
				c.Close()
				return
			}
			pipe(c, remote)
		})
		return ln, nil

	case TunnelRemote:
		ln, err := exit.Listen("tcp", bindAddr)
		if err != nil {
			// The common failure is the remote port already being bound — a
			// stale sshd session that never tore its forward down, or another
			// process on the server — so point the user there.
			return nil, fmt.Errorf("tunnel remote listen: %w (check whether the port is already in use on the server by a stale session or another process)", err)
		}
		target := net.JoinHostPort(t.TargetHost, strconv.Itoa(t.TargetPort))
		go acceptLoop(ln, quit, func(c net.Conn) {
			local, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				return
			}
			pipe(c, local)
		})
		return ln, nil

	default:
		return nil, fmt.Errorf("unknown tunnel mode %q", t.Mode)
	}
}

// TestTunnel validates a tunnel configuration without persisting or
// registering it: the tunnel is brought up through the same path as StartTunnel
// (SSH chain dial + auth, then listener bind — for remote mode that also proves
// the port is free on the server) under a throwaway ID, and torn down
// immediately once the listener is bound. Running means everything bound;
// Error carries the reason. Throwaway-ID states never reach the UI (see
// setState), so failures are reported only through the return value.
func (ts *TunnelService) TestTunnel(t Tunnel, resolve ConnResolver) TunnelState {
	t.ID = fmt.Sprintf("__test__%d", time.Now().UnixNano())
	st := ts.StartTunnel(t, resolve)
	ts.StopTunnel(t.ID)
	return st
}

// StopTunnel tears down a running user tunnel (listener + whole ssh.Client
// chain). No-op if the tunnel isn't running. Safe on a reservation entry (a
// dial still in flight): closing quit cancels it, and the dialing StartTunnel
// tears down whatever it produced when it finds the reservation gone.
func (ts *TunnelService) StopTunnel(id string) {
	ts.mu.Lock()
	entry, ok := ts.userTunnels[id]
	if ok {
		delete(ts.userTunnels, id)
	}
	ts.mu.Unlock()
	if !ok {
		return
	}
	close(entry.quit)
	if entry.listener != nil {
		entry.listener.Close()
	}
	if entry.clients != nil {
		closeClients(entry.clients)
	}
	ts.setState(id, TunnelState{ID: id, Status: TunnelStopped})
}

// buildHopChain resolves the exit connection and recurses its jump host
// (ConnectionConfig.TunnelSSHConnID) into an ordered chain, outermost first:
// [firstHop, …, exit]. A connection with no jump host yields a single-element
// chain (direct to the exit). Loops are rejected.
func buildHopChain(exitID string, resolve ConnResolver) ([]ConnectionConfig, error) {
	if exitID == "" {
		return nil, fmt.Errorf("no SSH connection selected")
	}
	var chain []ConnectionConfig
	seen := make(map[string]bool)
	for id := exitID; id != ""; {
		if seen[id] {
			return nil, fmt.Errorf("proxy chain has a loop at connection %s", id)
		}
		seen[id] = true
		cfg, ok := resolve(id)
		if !ok {
			return nil, fmt.Errorf("SSH connection %s not found", id)
		}
		chain = append(chain, cfg)
		id = cfg.TunnelSSHConnID
	}
	// chain is exit-first; reverse to outermost-first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// dialChain establishes the SSH clients along the chain and returns the exit
// client (chain tail) plus every client for teardown. The first hop is dialed
// directly (or through the upstream proxy); each subsequent hop is dialed
// through the previous client — the standard ProxyJump chaining.
func (ts *TunnelService) dialChain(chain []ConnectionConfig, upstream *SocksProxy) (*ssh.Client, []*ssh.Client, error) {
	dialHop := ts.dialHop
	if dialHop == nil {
		dialHop = dialFirstHop
	}
	var clients []*ssh.Client
	var prev *ssh.Client

	for i, cfg := range chain {
		addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
		authMethods, cleanup, err := makeSSHAuthMethodsForAttempt(cfg, nil)
		if err != nil {
			closeClients(clients)
			return nil, nil, fmt.Errorf("ssh auth %s: %w", addr, err)
		}

		var raw net.Conn
		if i == 0 {
			raw, err = dialHop(addr, upstream)
		} else {
			raw, err = prev.Dial("tcp", addr)
		}
		if err != nil {
			cleanup()
			closeClients(clients)
			return nil, nil, fmt.Errorf("dial %s: %w", addr, err)
		}

		pinAddr := hostKeyPinAddr(cfg, addr)
		cb, trust := NewHostKeyVerifier(pinAddr)
		algoSet, _, err := resolveSSHAlgorithms(cfg.SSHAlgorithms)
		if err != nil {
			cleanup()
			raw.Close()
			closeClients(clients)
			return nil, nil, err
		}
		clientConfig := &ssh.ClientConfig{
			User:              cfg.User,
			Auth:              authMethods,
			Timeout:           sshHandshakeTimeout,
			HostKeyCallback:   cb,
			Config:            algoSet.config(),
			HostKeyAlgorithms: preferPinnedHostKeyAlgorithms(algoSet.HostKeys, pinAddr),
		}
		// Bound the handshake like the other SSH dial paths (ClientConfig.
		// Timeout alone only covers ssh.Dial's TCP connect). Best-effort:
		// hops behind another SSH client are channel-backed and don't
		// support deadlines. Cleared on success so the tunnel lives on.
		_ = raw.SetDeadline(time.Now().Add(sshHandshakeDeadline(clientConfig)))
		sshConn, chans, reqs, err := ssh.NewClientConn(raw, addr, clientConfig)
		cleanup()
		if err != nil {
			raw.Close()
			closeClients(clients)
			return nil, nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
		}
		_ = raw.SetDeadline(time.Time{})
		client := ssh.NewClient(sshConn, chans, reqs)
		if trust != nil && trust.FirstTrust {
			log.Writef("[known_hosts] host key not seen before — trusted and saved: %s %s", pinAddr, trust.Fingerprint)
		}
		clients = append(clients, client)
		prev = client
	}

	return prev, clients, nil
}

// dialFirstHop dials the first hop directly, or through an upstream SOCKS5/HTTP
// proxy when configured (the chain's entry point).
func dialFirstHop(addr string, upstream *SocksProxy) (net.Conn, error) {
	if upstream == nil {
		conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
		if err == nil {
			if tcp, ok := conn.(*net.TCPConn); ok {
				tcp.SetKeepAlive(true)
				tcp.SetKeepAlivePeriod(sshKeepAliveInterval)
			}
		}
		return conn, err
	}

	proxyAddr := net.JoinHostPort(upstream.Host, strconv.Itoa(upstream.Port))
	switch upstream.Kind {
	case "", "socks5":
		var auth *proxy.Auth
		if upstream.User != "" {
			auth = &proxy.Auth{User: upstream.User, Password: upstream.Pass}
		}
		d, err := proxy.SOCKS5("tcp", proxyAddr, auth, &net.Dialer{Timeout: 30 * time.Second})
		if err != nil {
			return nil, err
		}
		return d.Dial("tcp", addr)
	case "http":
		return dialHTTPConnect(proxyAddr, upstream, addr)
	default:
		return nil, fmt.Errorf("unsupported upstream proxy kind %q", upstream.Kind)
	}
}

// dialHTTPConnect opens a CONNECT tunnel through an HTTP proxy to addr.
func dialHTTPConnect(proxyAddr string, up *SocksProxy, addr string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, 30*time.Second)
	if err != nil {
		return nil, err
	}
	req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
	if up.User != "" {
		cred := basicAuth(up.User, up.Pass)
		req += "Proxy-Authorization: Basic " + cred + "\r\n"
	}
	req += "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}
	// Read status line up to the header terminator, bounded by the connect timeout.
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		conn.Close()
		return nil, err
	}
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(conn, one); err != nil {
			conn.Close()
			return nil, err
		}
		buf = append(buf, one[0])
		if len(buf) >= 4 && string(buf[len(buf)-4:]) == "\r\n\r\n" {
			break
		}
		if len(buf) > 4096 {
			conn.Close()
			return nil, fmt.Errorf("http proxy response too large")
		}
	}
	if len(buf) < 12 || string(buf[9:12]) != "200" {
		conn.Close()
		return nil, fmt.Errorf("http proxy CONNECT failed")
	}
	// Clear the read deadline so the connection is usable for the SSH session.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// --- accept / copy helpers ---

func acceptLoop(ln net.Listener, quit chan struct{}, handle func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-quit:
			default:
			}
			return
		}
		go handle(conn)
	}
}

// pipe copies bidirectionally between a and b and closes both when either side
// ends.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
}

func closeClients(clients []*ssh.Client) {
	for _, c := range clients {
		c.Close()
	}
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// --- runtime state ---

// setState records a tunnel's runtime state and notifies the callback (app.go
// wires it to a Wails event). Returns the state for convenience.
func (ts *TunnelService) setState(id string, st TunnelState) TunnelState {
	// Throwaway IDs from TestTunnel never surface in the UI: no recording, no
	// event — a failed test reports through its return value, not a toast.
	if strings.HasPrefix(id, "__test__") {
		return st
	}
	ts.mu.Lock()
	ts.states[id] = st
	cb := ts.onState
	ts.mu.Unlock()
	if cb != nil {
		cb(st)
	}
	return st
}

// SetStateCallback registers a callback invoked whenever a tunnel's state
// changes.
func (ts *TunnelService) SetStateCallback(cb func(TunnelState)) {
	ts.mu.Lock()
	ts.onState = cb
	ts.mu.Unlock()
}

// TunnelStates returns a snapshot of all known tunnel runtime states.
func (ts *TunnelService) TunnelStates() []TunnelState {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]TunnelState, 0, len(ts.states))
	for _, st := range ts.states {
		out = append(out, st)
	}
	return out
}

// --- minimal SOCKS5 server (CONNECT, optional username/password) ---

const (
	socks5Success         = 0x00
	socks5HostUnreachable = 0x04

	// SOCKS5 authentication methods (RFC 1929).
	socks5MethodNoAuth   = 0x00
	socks5MethodUserPass = 0x02
	socks5MethodNone     = 0xFF
)

// socks5HandshakeTimeout bounds the SOCKS5 negotiation. Without it an idle
// (or hostile) client that opens a connection and never speaks pins an
// accept-loop goroutine forever. Overridable for tests.
var socks5HandshakeTimeout = 10 * time.Second

// socks5Negotiate runs the SOCKS5 handshake under socks5HandshakeTimeout and
// clears the deadline on success — the connection then carries long-lived
// forwarded traffic, which a lingering deadline would kill.
func socks5Negotiate(conn net.Conn, user, pass string) (string, error) {
	if err := conn.SetDeadline(time.Now().Add(socks5HandshakeTimeout)); err != nil {
		return "", err
	}
	target, err := socks5Handshake(conn, user, pass)
	if err != nil {
		return "", err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return "", err
	}
	return target, nil
}

// socks5Handshake performs the server side of a SOCKS5 CONNECT negotiation on
// conn and returns the requested "host:port". The CONNECT command is the only
// one supported — enough for a dynamic forward. When user is non-empty the
// RFC 1929 username/password sub-negotiation is required, which is what keeps
// a non-loopback bind from being an open proxy (see validateSOCKSExposure).
func socks5Handshake(conn net.Conn, user, pass string) (string, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", err
	}
	if head[0] != 0x05 {
		return "", fmt.Errorf("socks: bad version %d", head[0])
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", err
	}
	// Select username/password when credentials are configured, no-auth
	// otherwise; decline when the client did not offer the selected method.
	selected := byte(socks5MethodNoAuth)
	requirement := "no-authentication"
	if user != "" {
		selected = socks5MethodUserPass
		requirement = "username/password"
	}
	if !slices.Contains(methods, selected) {
		if _, err := conn.Write([]byte{0x05, socks5MethodNone}); err != nil {
			return "", err
		}
		return "", fmt.Errorf("socks: client does not offer %s authentication", requirement)
	}
	// Reply: version 5 plus the selected method.
	if _, err := conn.Write([]byte{0x05, selected}); err != nil {
		return "", err
	}
	if selected == socks5MethodUserPass {
		if err := socks5Authenticate(conn, user, pass); err != nil {
			return "", err
		}
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return "", err
	}
	if req[0] != 0x05 {
		return "", fmt.Errorf("socks: bad request version %d", req[0])
	}
	if req[1] != 0x01 { // CONNECT only
		socks5Reply(conn, 0x07)
		return "", fmt.Errorf("socks: unsupported command %d", req[1])
	}

	var host string
	switch req[3] {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 0x03: // domain
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return "", err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		host = string(b)
	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	default:
		socks5Reply(conn, 0x08)
		return "", fmt.Errorf("socks: unknown address type %d", req[3])
	}

	pb := make([]byte, 2)
	if _, err := io.ReadFull(conn, pb); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(pb)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// socks5Authenticate runs the RFC 1929 username/password sub-negotiation and
// compares both fields in constant time.
func socks5Authenticate(conn net.Conn, user, pass string) error {
	if len(user) > 255 || len(pass) > 255 {
		return fmt.Errorf("socks: credentials longer than 255 bytes")
	}
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return err
	}
	if head[0] != 0x01 {
		return fmt.Errorf("socks: bad auth version %d", head[0])
	}
	uname := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, uname); err != nil {
		return err
	}
	var plen [1]byte
	if _, err := io.ReadFull(conn, plen[:]); err != nil {
		return err
	}
	pword := make([]byte, int(plen[0]))
	if _, err := io.ReadFull(conn, pword); err != nil {
		return err
	}
	ok := subtle.ConstantTimeCompare(uname, []byte(user)) & subtle.ConstantTimeCompare(pword, []byte(pass))
	if ok != 1 {
		// Status 1 = failure; the client is expected to close.
		_, _ = conn.Write([]byte{0x01, 0x01})
		return fmt.Errorf("socks: authentication failed")
	}
	_, err := conn.Write([]byte{0x01, 0x00})
	return err
}

// socksListenExposed reports whether a listen host reaches beyond this
// machine. An empty host means the default bind (127.0.0.1); anything else
// that is not provably loopback counts as exposed — a hostname may resolve
// off-box, so it fails closed.
func socksListenExposed(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") || strings.EqualFold(h, "ip6-localhost") {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return !ip.IsLoopback()
	}
	return true
}

// validateSOCKSExposure rejects a dynamic tunnel whose SOCKS5 listener would
// be reachable from other machines without authentication: a non-loopback bind
// must carry credentials, or explicitly acknowledge the open proxy. Everything
// else (loopback binds, non-SOCKS modes) is unaffected.
func validateSOCKSExposure(t Tunnel) error {
	if t.Mode != TunnelDynamic || !socksListenExposed(t.ListenHost) {
		return nil
	}
	if t.SOCKSUser != "" && t.SOCKSPass != "" {
		return nil
	}
	if t.SOCKSAllowUnauthenticated {
		return nil
	}
	bind := t.ListenHost
	if bind == "" {
		bind = "127.0.0.1"
	}
	return fmt.Errorf(
		"dynamic tunnel listening on %s would be an open SOCKS5 proxy for anyone who can reach it: set a SOCKS username AND password on the tunnel, bind it to 127.0.0.1 instead, or set socksAllowUnauthenticated to explicitly allow an unauthenticated proxy",
		net.JoinHostPort(bind, strconv.Itoa(t.ListenPort)))
}

// socks5Reply writes a SOCKS5 reply with the given status and a zero
// bound-address (which clients ignore for CONNECT).
func socks5Reply(conn net.Conn, status byte) error {
	_, err := conn.Write([]byte{0x05, status, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
}
