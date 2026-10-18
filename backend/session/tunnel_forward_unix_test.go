//go:build !windows

package session

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestUserTunnelStopCancelsStalledAgent(t *testing.T) {
	cfg, _ := tunnelTestServerConfig(t, &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, nil
		},
	})
	cfg.AuthType = "agent"
	// Keep below the Unix socket path limit even with macOS's long TMPDIR.
	dir, err := os.MkdirTemp("/tmp", "ut-agent-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("SSH_AUTH_SOCK", path)
	requested := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Read the agent's identities request, but never send a response.
		if _, err := io.ReadFull(conn, make([]byte, 5)); err != nil {
			conn.Close()
			return
		}
		requested <- conn
	}()
	ts := NewTunnelService()
	defer ts.Shutdown()
	started := make(chan TunnelState, 1)
	go func() {
		started <- ts.StartTunnel(Tunnel{ID: "agent", SSHConnID: cfg.ID, Mode: TunnelLocal, AutoReconnect: true}, func(string) (ConnectionConfig, bool) { return cfg, true })
	}()
	conn := tunnelTestReceive(t, requested)
	defer conn.Close()
	stopped := make(chan bool, 1)
	go func() { ts.StopTunnel("agent"); stopped <- true }()
	tunnelTestReceive(t, stopped)
	if st := tunnelTestReceive(t, started); st.Status != TunnelStopped {
		t.Fatalf("cancelled agent authentication = %+v", st)
	}
}
