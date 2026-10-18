package main

// Regression coverage for the tunnel credential prompt path (issue #844): a
// tunnel whose exit SSH connection has no saved password must be startable with
// inline credentials supplied by the frontend's credential dialog.

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/ys-ll/uniterm/backend/session"
	"github.com/ys-ll/uniterm/backend/store"
)

// TestWithExitCredOverride locks the override semantics: inline user/password
// fill only the exit connection's EMPTY fields, never overwrite saved values,
// and are not applied to non-exit hops. Empty inline credentials return the
// resolver untouched.
func TestWithExitCredOverride(t *testing.T) {
	conns := map[string]session.ConnectionConfig{
		"exit": {ID: "exit", Host: "h1", User: "exit-user", Password: ""},
		"mid":  {ID: "mid", Host: "h2", User: "", Password: ""},
	}
	base := func(id string) (session.ConnectionConfig, bool) {
		c, ok := conns[id]
		return c, ok
	}

	// Empty inline credentials: resolver returned as-is.
	if got := withExitCredOverride(base, "exit", "", ""); got == nil {
		t.Fatal("empty override: expected the original resolver back")
	}

	resolve := withExitCredOverride(base, "exit", "inline-user", "inline-pw")

	// Exit hop: empty password filled, saved user kept.
	c, ok := resolve("exit")
	if !ok || c.User != "exit-user" || c.Password != "inline-pw" {
		t.Fatalf("exit hop: got user=%q password=%q ok=%v", c.User, c.Password, ok)
	}

	// Non-exit hop: untouched.
	c, ok = resolve("mid")
	if !ok || c.User != "" || c.Password != "" {
		t.Fatalf("non-exit hop: got user=%q password=%q ok=%v", c.User, c.Password, ok)
	}

	// Unknown id still reports not-found.
	if _, ok = resolve("nope"); ok {
		t.Fatal("unknown id: expected not found")
	}
}

// TestApp_StartTunnel_InlineCredentials drives App.StartTunnel end to end: the
// exit connection is saved without a password, so starting the tunnel fails on
// ssh auth; passing the password inline (as the credential dialog would) makes
// it run.
func TestApp_StartTunnel_InlineCredentials(t *testing.T) {
	targetPort := unreachableLocalPort(t)
	sshPort := startFakeJumpSSHD(t)

	cs, err := store.NewConnectionStore(t.TempDir())
	if err != nil {
		t.Fatalf("connection store: %v", err)
	}
	cs.SetPasswordStore(stubPasswordStore{})
	// No saved password — the case the credential prompt exists for.
	if err := cs.Save(session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{{
			ID:       "jump-1",
			Type:     "ssh",
			Host:     "127.0.0.1",
			Port:     sshPort,
			User:     "jump",
			AuthType: "password",
		}},
	}); err != nil {
		t.Fatalf("save jump connection: %v", err)
	}

	ts := store.NewTunnelStore(t.TempDir())
	tunnel := session.Tunnel{
		ID:         "tun-1",
		Name:       "regression",
		Mode:       session.TunnelLocal,
		SSHConnID:  "jump-1",
		ListenPort: freeLocalPort(t),
		TargetHost: "127.0.0.1",
		TargetPort: targetPort,
	}
	if err := ts.Save(session.TunnelStoreData{Version: 1, Tunnels: []session.Tunnel{tunnel}}); err != nil {
		t.Fatalf("save tunnel: %v", err)
	}

	a := NewApp("")
	a.connectionStore = cs
	a.tunnelService = session.NewTunnelService()
	a.tunnelStore = ts

	// Guard: without inline credentials the ssh handshake fails.
	st, err := a.StartTunnel("tun-1", "", "")
	if err != nil {
		t.Fatalf("no inline creds: unexpected error: %v", err)
	}
	if st.Status != session.TunnelError {
		t.Fatalf("no inline creds: expected error state, got %q (%s)", st.Status, st.Error)
	}
	a.StopTunnel("tun-1")

	// With the password supplied inline the tunnel comes up.
	st, err = a.StartTunnel("tun-1", "", "jump-pw")
	if err != nil {
		t.Fatalf("inline creds: unexpected error: %v", err)
	}
	if st.Status != session.TunnelRunning {
		t.Fatalf("inline creds: expected running state, got %q (%s)", st.Status, st.Error)
	}
	a.StopTunnel("tun-1")
}

// freeLocalPort reserves an ephemeral loopback port for the tunnel's local
// listener and releases it immediately.
func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestApp_SaveTunnels_CancelsReconnectForDeletedTunnel(t *testing.T) {
	a := NewApp("")
	a.tunnelStore = store.NewTunnelStore(t.TempDir())
	a.tunnelService = session.NewTunnelService()
	t.Cleanup(a.tunnelService.Shutdown)
	tunnel := session.Tunnel{ID: "deleted", SSHConnID: "missing", AutoReconnect: true}
	if err := a.SaveTunnels(session.TunnelStoreData{Version: 1, Tunnels: []session.Tunnel{tunnel}}); err != nil {
		t.Fatal(err)
	}
	state := a.tunnelService.StartTunnel(tunnel, func(string) (session.ConnectionConfig, bool) {
		return session.ConnectionConfig{}, false
	})
	if state.Status != session.TunnelReconnecting {
		t.Fatalf("expected pending reconnect, got %+v", state)
	}
	if err := a.SaveTunnels(session.TunnelStoreData{Version: 1}); err != nil {
		t.Fatal(err)
	}
	for _, state := range a.ListTunnelStates() {
		if state.ID == tunnel.ID && state.Status != session.TunnelStopped {
			t.Fatalf("deleted tunnel still active: %+v", state)
		}
	}
}

func TestApp_CancelsTunnelWhilePreparing(t *testing.T) {
	actions := map[string]func(*App) error{
		"stop": func(a *App) error { return a.StopTunnel("pending") },
		"delete": func(a *App) error {
			return a.SaveTunnels(session.TunnelStoreData{Version: 1})
		},
		"shutdown": func(a *App) error { a.shutdown(); return nil },
	}
	for name, cancel := range actions {
		t.Run(name, func(t *testing.T) {
			a := NewApp(t.TempDir())
			a.tunnelStore = store.NewTunnelStore(t.TempDir())
			a.tunnelService = session.NewTunnelService()
			t.Cleanup(a.tunnelService.Shutdown)
			preparing, done := make(chan struct{}), make(chan struct{})
			resume, release := context.WithCancel(context.Background())
			t.Cleanup(release)
			var state session.TunnelState
			var err error
			go func() {
				defer close(done)
				state, err = a.runTunnelStart("pending", func(ctx context.Context) (session.TunnelState, error) {
					close(preparing)
					<-resume.Done()
					return a.tunnelService.StartTunnelContext(ctx, session.Tunnel{
						ID: "pending", SSHConnID: "missing", AutoReconnect: true,
					}, func(string) (session.ConnectionConfig, bool) {
						return session.ConnectionConfig{}, false
					}), nil
				})
			}()
			waitTunnelTestSignal(t, preparing)
			if err := cancel(a); err != nil {
				t.Fatal(err)
			}
			release()
			waitTunnelTestSignal(t, done)
			if err != nil || state.Status != session.TunnelStopped {
				t.Fatalf("cancelled preparation: state=%+v error=%v", state, err)
			}
			if states := a.ListTunnelStates(); len(states) != 0 {
				t.Fatalf("cancelled preparation reached the service: %+v", states)
			}
			if name == "shutdown" {
				state, _ := a.runTunnelStart("late", func(context.Context) (session.TunnelState, error) {
					t.Fatal("started preparing a tunnel after shutdown")
					return session.TunnelState{}, nil
				})
				if state.Status != session.TunnelStopped {
					t.Fatalf("start after shutdown: %+v", state)
				}
			}
		})
	}
}

func TestApp_SuccessfulTunnelStartKeepsLifecycleContext(t *testing.T) {
	a := NewApp("")
	var lifecycle context.Context
	a.runTunnelStart("running", func(ctx context.Context) (session.TunnelState, error) {
		lifecycle = ctx
		return session.TunnelState{ID: "running", Status: session.TunnelRunning}, nil
	})
	if lifecycle.Err() != nil {
		t.Fatal("successful start cancelled its lifecycle when returning")
	}
}

func TestApp_OldTunnelPreparationCannotRemoveReplacement(t *testing.T) {
	a := NewApp("")
	start := func() (context.CancelFunc, <-chan struct{}, <-chan context.Context) {
		resume, release := context.WithCancel(context.Background())
		t.Cleanup(release)
		done := make(chan struct{})
		prepared := make(chan context.Context, 1)
		go func() {
			defer close(done)
			a.runTunnelStart("same", func(ctx context.Context) (session.TunnelState, error) {
				prepared <- ctx
				<-resume.Done()
				return session.TunnelState{ID: "same", Status: session.TunnelRunning}, nil
			})
		}()
		return release, done, prepared
	}
	oldRelease, oldDone, oldPrepared := start()
	var oldCtx context.Context
	select {
	case oldCtx = <-oldPrepared:
	case <-time.After(3 * time.Second):
		t.Fatal("old preparation did not start")
	}
	a.StopTunnel("same")
	newRelease, newDone, newPrepared := start()
	var newCtx context.Context
	select {
	case newCtx = <-newPrepared:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement preparation did not start")
	}
	oldRelease()
	waitTunnelTestSignal(t, oldDone)
	if oldCtx.Err() == nil || newCtx.Err() != nil {
		t.Fatal("old cancellation affected the replacement")
	}
	a.StopTunnel("same")
	if newCtx.Err() == nil {
		t.Fatal("old completion removed the replacement cancellation handle")
	}
	newRelease()
	waitTunnelTestSignal(t, newDone)
}

func TestApp_AutoStartSkipsTunnelDeletedWhileEarlierStartIsPending(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		t.Cleanup(func() { conn.Close() })
		// Leave the SSH handshake pending until the test stops this tunnel.
		close(accepted)
	}()
	cs, err := store.NewConnectionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cs.SetPasswordStore(stubPasswordStore{})
	if err := cs.Save(session.ConnectionStoreData{Connections: []session.ConnectionConfig{{
		ID: "slow", Type: "ssh", Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port,
		AuthType: "password", User: "test",
	}}}); err != nil {
		t.Fatal(err)
	}
	a := NewApp("")
	a.connectionStore = cs
	a.tunnelStore = store.NewTunnelStore(t.TempDir())
	a.tunnelService = session.NewTunnelService()
	t.Cleanup(a.tunnelService.Shutdown)
	first := session.Tunnel{ID: "first", SSHConnID: "slow", AutoStart: true}
	second := session.Tunnel{ID: "deleted", SSHConnID: "missing", AutoStart: true, AutoReconnect: true}
	if err := a.SaveTunnels(session.TunnelStoreData{Version: 1, Tunnels: []session.Tunnel{first, second}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); a.autoStartTunnels() }()
	waitTunnelTestSignal(t, accepted)
	if err := a.SaveTunnels(session.TunnelStoreData{Version: 1, Tunnels: []session.Tunnel{first}}); err != nil {
		t.Fatal(err)
	}
	a.StopTunnel(first.ID)
	waitTunnelTestSignal(t, done)
	for _, state := range a.ListTunnelStates() {
		if state.ID == second.ID {
			t.Fatalf("deleted tunnel was started from the old snapshot: %+v", state)
		}
	}
}

func waitTunnelTestSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for tunnel operation")
	}
}
