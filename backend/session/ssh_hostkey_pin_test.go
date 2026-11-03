package session

// Regression tests for the security review (security.md, SSH stack):
//
//   - P1: a jump-host tunnel rewrites the dial address to
//     127.0.0.1:<ephemeral> (app_terminal.setupJumpHostTunnel). The host key
//     must still be verified and recorded under the ORIGINAL host:port carried
//     in TunnelTargetHost/Port (hostKeyVerifyTarget), otherwise every
//     connection is a fresh first-trust — a compromised jump host can MITM the
//     target — and known_hosts grows one entry per ephemeral port, breaking
//     when the port is reused for another target.
//
// The P0 finding this file originally pinned (a cwd-hook shell probe dialing
// with InsecureIgnoreHostKey) no longer applies: SSH cwd discovery no longer
// dials a second connection or issues an exec — TestSSHConnectNeverExecutesCwdProbe
// pins that — so there is no probe to skip host-key verification.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// newTestPublicKey returns a throwaway ed25519 key no test server presents.
func newTestPublicKey(t *testing.T) ssh.PublicKey {
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

// writeKnownHostLine records one host key line in the known_hosts file at
// path (creating the directory when needed), the same format the verifier
// writes.
func writeKnownHostLine(t *testing.T, path, host, keyType, keyB64 string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := host + " " + keyType + " " + keyB64 + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("seed known_hosts: %v", err)
	}
}

// readKnownHosts returns the non-empty, non-comment lines of path.
func readKnownHosts(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// TestHostKeyPinVerifiedUnderOriginalHostPort is the P1 regression: a config
// whose dial address was rewritten to 127.0.0.1:<ephemeral> must still verify
// the key under the original host:port carried in TunnelTargetHost/Port.
func TestHostKeyPinVerifiedUnderOriginalHostPort(t *testing.T) {
	path := withTempKnownHosts(t)
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
		// Jump-host shape: dial 127.0.0.1:<ephemeral>, verify "the original".
		TunnelTargetHost: "jump-target.example.com",
		TunnelTargetPort: 22,
	}
	foreign := newTestPublicKey(t)
	writeKnownHostLine(t, path, "jump-target.example.com", foreign.Type(),
		base64.StdEncoding.EncodeToString(foreign.Marshal()))

	_, err = DialSSHClient(cfg)
	if err == nil {
		t.Fatalf("dial succeeded although jump-target.example.com has a different pinned key — the verifier keyed on the ephemeral dial address %s", addr)
	}
	if !strings.Contains(err.Error(), "host key for jump-target.example.com:22 changed") {
		t.Fatalf("dial error = %v, want host key changed for the pinned address", err)
	}
}

// TestHostKeyPinDoesNotGrowKnownHostsPerEphemeralPort: one jump-host session
// must pin the target once, not add a 127.0.0.1:<ephemeral> entry that breaks
// when the port is reused and bloats the file on every reconnect.
func TestHostKeyPinDoesNotGrowKnownHostsPerEphemeralPort(t *testing.T) {
	path := withTempKnownHosts(t)
	addr, _ := startProbeTrackingServer(t, false)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	cfg := ConnectionConfig{
		User:             "admin",
		Host:             host,
		Port:             port,
		AuthType:         "password",
		Password:         "pw",
		TunnelTargetHost: "jump-target.example.com",
		TunnelTargetPort: 22,
	}

	client, err := DialSSHClient(cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	lines := readKnownHosts(t, path)
	if len(lines) != 1 {
		t.Fatalf("known_hosts has %d entries (%v), want exactly 1", len(lines), lines)
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 3 || fields[0] != "jump-target.example.com" {
		t.Fatalf("known_hosts entry host = %q, want the original host:port", fields[0])
	}
	if strings.Contains(string(mustRead(t, path)), "127.0.0.1:"+portStr) {
		t.Fatalf("known_hosts grew an entry for the ephemeral dial address 127.0.0.1:%s", portStr)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
