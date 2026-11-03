package session

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// withTempKnownHosts redirects KnownHostsPath at a fresh temp dir by pointing
// the home-directory environment at it. os.UserHomeDir reads HOME on Unix and
// USERPROFILE on Windows, so both are set (t.Setenv restores them on cleanup).
func withTempKnownHosts(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return filepath.Join(dir, ".uniterm", "known_hosts")
}

// generateTestHostKey generates a fresh ed25519 SSH key pair for testing
// host-key verification. Returns the PublicKey (for the callback) and its
// base64 wire-format encoding (for known_hosts comparison).
func generateTestHostKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	sshPub := signer.PublicKey()
	return sshPub, base64.StdEncoding.EncodeToString(sshPub.Marshal())
}

func TestHostKeyCallback_TOFUFirstConnectRecords(t *testing.T) {
	path := withTempKnownHosts(t)

	key, keyB64 := generateTestHostKey(t)
	cb := HostKeyCallback("example.com", "22", "")
	if err := cb("example.com", nil, key); err != nil {
		t.Fatalf("first connect should pass (TOFU), got: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	wantLine := "example.com " + key.Type() + " " + keyB64
	if !strings.Contains(string(data), wantLine) {
		t.Errorf("known_hosts missing expected line\nwant: %s\nhave: %s", wantLine, data)
	}
}

func TestHostKeyCallback_NonStandardPortUsesBracketForm(t *testing.T) {
	path := withTempKnownHosts(t)

	key, keyB64 := generateTestHostKey(t)
	cb := HostKeyCallback("example.com", "2222", "")
	if err := cb("example.com", nil, key); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	wantLine := "[example.com]:2222 " + key.Type() + " " + keyB64
	if !strings.Contains(string(data), wantLine) {
		t.Errorf("known_hosts missing bracketed host:port entry\nwant: %s\nhave: %s", wantLine, data)
	}
}

func TestHostKeyCallback_MatchingKeyPasses(t *testing.T) {
	withTempKnownHosts(t)

	key, _ := generateTestHostKey(t)
	cb := HostKeyCallback("example.com", "22", "")
	if err := cb("example.com", nil, key); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	// Second connect with the same key must pass.
	if err := cb("example.com", nil, key); err != nil {
		t.Errorf("second connect with same key should pass, got: %v", err)
	}
}

func TestHostKeyCallback_ChangedKeyReturnsError(t *testing.T) {
	withTempKnownHosts(t)

	key1, _ := generateTestHostKey(t)
	cb := HostKeyCallback("example.com", "22", "")
	if err := cb("example.com", nil, key1); err != nil {
		t.Fatalf("first connect: %v", err)
	}

	key2, _ := generateTestHostKey(t)
	err := cb("example.com", nil, key2)
	if err == nil {
		t.Fatal("changed key should return an error, got nil")
	}
	changed, ok := AsHostKeyChangedError(err)
	if !ok {
		t.Fatalf("error should be *HostKeyChangedError, got %T: %v", err, err)
	}
	if changed.Host != "example.com" {
		t.Errorf("changed.Host = %q, want example.com", changed.Host)
	}
	if changed.NewFp == "" || changed.OldFp == "" {
		t.Errorf("fingerprints should be non-empty: old=%q new=%q", changed.OldFp, changed.NewFp)
	}
	if changed.OldFp == changed.NewFp {
		t.Error("old and new fingerprints should differ")
	}
}

func TestHostKeyCallback_ChangeEmitsEvent(t *testing.T) {
	withTempKnownHosts(t)

	var emittedName string
	var payload map[string]any
	SetHostKeyEmitter(func(name string, p any) {
		emittedName = name
		if m, ok := p.(map[string]any); ok {
			payload = m
		}
	})
	defer SetHostKeyEmitter(nil)

	key1, _ := generateTestHostKey(t)
	cb := HostKeyCallback("example.com", "2222", "sess-1")
	if err := cb("example.com", nil, key1); err != nil {
		t.Fatalf("first connect: %v", err)
	}

	key2, _ := generateTestHostKey(t)
	_ = cb("example.com", nil, key2) // returns an error; the event is what we assert
	if emittedName != "session:hostkey-changed" {
		t.Fatalf("emitted event name = %q, want session:hostkey-changed", emittedName)
	}
	if payload["sessionId"] != "sess-1" {
		t.Errorf("payload sessionId = %v, want sess-1", payload["sessionId"])
	}
	if payload["oldFp"] == "" || payload["newFp"] == "" {
		t.Errorf("payload fingerprints should be non-empty: %v", payload)
	}
	if payload["oldFp"] == payload["newFp"] {
		t.Error("payload old and new fingerprints should differ")
	}
}

func TestAcceptHostKeyChange_UpdatesKnownHosts(t *testing.T) {
	withTempKnownHosts(t)

	key1, _ := generateTestHostKey(t)
	cb := HostKeyCallback("example.com", "22", "")
	if err := cb("example.com", nil, key1); err != nil {
		t.Fatalf("first connect: %v", err)
	}

	key2, key2B64 := generateTestHostKey(t)
	// User accepts the new key via the frontend dialog.
	if err := AcceptHostKeyChange("example.com", "22", key2.Type(), key2B64); err != nil {
		t.Fatalf("AcceptHostKeyChange: %v", err)
	}
	// Connecting with key2 should now pass.
	if err := cb("example.com", nil, key2); err != nil {
		t.Errorf("after accept, connect with new key should pass, got: %v", err)
	}
	// And key1 should now be the one that triggers a change.
	if err := cb("example.com", nil, key1); err == nil {
		t.Error("after accept, the old key should now be rejected")
	}
}

func TestAcceptHostKeyOnce_AllowsOneConnectionWithoutRecording(t *testing.T) {
	path := withTempKnownHosts(t)

	key1, _ := generateTestHostKey(t)
	cb := HostKeyCallback("example.com", "22", "")
	if err := cb("example.com", nil, key1); err != nil {
		t.Fatalf("first connect: %v", err)
	}

	key2, key2B64 := generateTestHostKey(t)
	if err := cb("example.com", nil, key2); err == nil {
		t.Fatal("changed key should be rejected before the one-shot grant")
	}

	// User picks "connect once" in the dialog: grant a one-shot bypass for
	// exactly this key, then reconnect.
	if err := AcceptHostKeyOnce("example.com", "22", key2B64); err != nil {
		t.Fatalf("AcceptHostKeyOnce: %v", err)
	}
	if err := cb("example.com", nil, key2); err != nil {
		t.Errorf("one-shot bypass should let the granted key through, got: %v", err)
	}
	// The bypass must not have written anything to known_hosts.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	if strings.Contains(string(data), key2B64) {
		t.Error("one-shot bypass must not update known_hosts")
	}
	// It is one-shot: the next connection with the same key is rejected again,
	// and the original key still verifies cleanly.
	if err := cb("example.com", nil, key2); err == nil {
		t.Error("bypass should be consumed after a single use")
	}
	if err := cb("example.com", nil, key1); err != nil {
		t.Errorf("original key should keep passing, got: %v", err)
	}
}

func TestAcceptHostKeyOnce_RejectsUngrantedKey(t *testing.T) {
	withTempKnownHosts(t)

	key1, _ := generateTestHostKey(t)
	cb := HostKeyCallback("example.com", "22", "")
	if err := cb("example.com", nil, key1); err != nil {
		t.Fatalf("first connect: %v", err)
	}

	// Grant a bypass for key2, then present key3: must still be rejected.
	_, key2B64 := generateTestHostKey(t)
	if err := AcceptHostKeyOnce("example.com", "22", key2B64); err != nil {
		t.Fatalf("AcceptHostKeyOnce: %v", err)
	}
	key3, _ := generateTestHostKey(t)
	if err := cb("example.com", nil, key3); err == nil {
		t.Error("a key other than the granted one must stay rejected")
	}
}

func TestAcceptHostKeyChange_RejectsInvalidKey(t *testing.T) {
	withTempKnownHosts(t)

	if err := AcceptHostKeyChange("example.com", "22", "ssh-ed25519", "not-a-valid-key"); err == nil {
		t.Fatal("AcceptHostKeyChange should reject a malformed key")
	}
	// Nothing should have been written.
	path, err := KnownHostsPath()
	if err != nil {
		t.Fatalf("KnownHostsPath: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("known_hosts should not exist after a rejected accept, stat err: %v", err)
	}
}

func TestHostKeyCallback_CorruptKnownHostsRecovers(t *testing.T) {
	path := withTempKnownHosts(t)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("garbage line\n\xff\xfe broken base64 !!!\n"), 0600); err != nil {
		t.Fatalf("seed corrupt known_hosts: %v", err)
	}

	// A corrupt file must not wedge the connection: first connect re-records.
	key, keyB64 := generateTestHostKey(t)
	cb := HostKeyCallback("example.com", "22", "")
	if err := cb("example.com", nil, key); err != nil {
		t.Fatalf("first connect over corrupt known_hosts should pass, got: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), keyB64) {
		t.Errorf("known_hosts should contain the new key after recovery: %q (%v)", data, err)
	}
}
