package session

// Regression tests for the security review (security.md, SSH stack):
//
//   - P0: the cwd-hook shell probe dials with the same credentials as the
//     main connection, so it must verify the host key too — an
//     InsecureIgnoreHostKey probe hands the password to a MITM even when the
//     main dial correctly aborts on a changed key.
//   - P1: a jump-host tunnel rewrites the dial address to
//     127.0.0.1:<ephemeral> (app_terminal.setupJumpHostTunnel). The host key
//     must still be pinned and verified under the ORIGINAL host:port,
//     otherwise every connection is a fresh first-trust (a compromised jump
//     host can MITM the target) and known_hosts grows one entry per
//     ephemeral port.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// configureKnownHostsAt points the global TOFU store at a fresh file holding
// entries (nil = start empty) and restores legacy mode (empty path) afterwards
// so every other test keeps accepting all host keys.
func configureKnownHostsAt(t *testing.T, entries []knownHostFileEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	if entries != nil {
		data, err := json.Marshal(entries)
		if err != nil {
			t.Fatalf("marshal known_hosts: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write known_hosts: %v", err)
		}
	}
	t.Cleanup(func() { ConfigureKnownHosts("") })
	ConfigureKnownHosts(path)
	return path
}

// knownHostEntry pins keys under hostport in the known_hosts file format.
func knownHostEntry(hostport string, keys ...ssh.PublicKey) knownHostFileEntry {
	e := knownHostFileEntry{Host: hostport}
	for _, k := range keys {
		e.Keys = append(e.Keys, knownKey{
			Type: k.Type(),
			Blob: base64.StdEncoding.EncodeToString(k.Marshal()),
		})
	}
	return e
}

// foreignPublicKey returns a throwaway ed25519 key no test server presents.
func foreignPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, pub, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(pub)
	if err != nil {
		t.Fatalf("signer from key: %v", err)
	}
	return signer.PublicKey()
}

// TestCwdProbeRejectsChangedHostKey is the P0 regression: with the target's
// pinned key differing from the server's, the shell probe must abort during
// key exchange. Before the fix the probe used InsecureIgnoreHostKey, so it
// authenticated with the stored password and issued the exec anyway.
func TestCwdProbeRejectsChangedHostKey(t *testing.T) {
	addr, conns := startProbeTrackingServer(t, false)
	configureKnownHostsAt(t, []knownHostFileEntry{knownHostEntry(addr, foreignPublicKey(t))})

	if got := probeCwdShellSeparately(probeTestConfig(addr)); got != "" {
		t.Errorf("probe returned shell %q although the pinned host key differs — the probe must verify the host key", got)
	}
	// The probe must have died in key exchange: no connection may have
	// reached the exec request (pre-fix it did, with the password sent).
	time.Sleep(100 * time.Millisecond)
	for _, act := range collectProbeConns(conns) {
		if atomic.LoadInt32(&act.exec) != 0 {
			t.Fatalf("probe issued exec despite a changed host key — verification skipped")
		}
	}
}

// TestCwdProbeAcceptsPinnedHostKey is the control: with the server's own key
// pinned the probe still runs, so the fix rejects changed keys rather than
// disabling the probe.
func TestCwdProbeAcceptsPinnedHostKey(t *testing.T) {
	addr, conns := startProbeTrackingServer(t, false)
	signer, err := ssh.ParsePrivateKey([]byte(testHostKeyPEM))
	if err != nil {
		t.Fatalf("parse test host key: %v", err)
	}
	configureKnownHostsAt(t, []knownHostFileEntry{knownHostEntry(addr, signer.PublicKey())})

	probeCwdShellSeparately(probeTestConfig(addr)) // no stty on the test server → ""

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, act := range collectProbeConns(conns) {
			if atomic.LoadInt32(&act.exec) > 0 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("probe never issued exec although the pinned host key matches")
}

// TestHostKeyPinVerifiedUnderOriginalHostPort is the P1 regression: a config
// whose dial address was rewritten to 127.0.0.1:<ephemeral> must still verify
// the key under the original host:port carried in HostKeyPinHost/Port.
func TestHostKeyPinVerifiedUnderOriginalHostPort(t *testing.T) {
	addr, _ := startProbeTrackingServer(t, false)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	cfg := ConnectionConfig{
		User:     "admin",
		Host:     host,
		Port:     port,
		AuthType: "password",
		Password: "pw",
		// Jump-host shape: dial 127.0.0.1:<ephemeral>, pin "the original".
		HostKeyPinHost: "jump-target.example.com",
		HostKeyPinPort: 22,
	}
	configureKnownHostsAt(t, []knownHostFileEntry{
		knownHostEntry("jump-target.example.com:22", foreignPublicKey(t)),
	})

	_, err = DialSSHClient(cfg)
	if err == nil {
		t.Fatalf("dial succeeded although jump-target.example.com:22 has a different pinned key — the verifier keyed on the ephemeral dial address %s", addr)
	}
	if !strings.Contains(err.Error(), "host key CHANGED") {
		t.Fatalf("dial error = %v, want host key CHANGED for the pinned address", err)
	}
}

// TestHostKeyPinDoesNotGrowKnownHostsPerEphemeralPort: one jump-host session
// must pin the target once, not add a 127.0.0.1:<ephemeral> entry that breaks
// when the port is reused and bloats the file on every reconnect.
func TestHostKeyPinDoesNotGrowKnownHostsPerEphemeralPort(t *testing.T) {
	addr, _ := startProbeTrackingServer(t, false)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	cfg := ConnectionConfig{
		User:           "admin",
		Host:           host,
		Port:           port,
		AuthType:       "password",
		Password:       "pw",
		HostKeyPinHost: "jump-target.example.com",
		HostKeyPinPort: 22,
	}
	path := configureKnownHostsAt(t, nil)

	client, err := DialSSHClient(cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	var entries []knownHostFileEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parse known_hosts: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("known_hosts has %d entries (%+v), want exactly 1", len(entries), entries)
	}
	if entries[0].Host != "jump-target.example.com:22" {
		t.Fatalf("known_hosts entry host = %q, want the original host:port", entries[0].Host)
	}
	for _, e := range entries {
		if e.Host == "127.0.0.1:"+portStr {
			t.Fatalf("known_hosts grew an entry for the ephemeral dial address %s", e.Host)
		}
	}
}
