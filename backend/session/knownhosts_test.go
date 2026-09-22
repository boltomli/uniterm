package session

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newTestPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}
	return key
}

// recomputedFingerprint independently re-derives the expected OpenSSH-style
// fingerprint so the test does not just mirror hostKeyFingerprint's code path.
func recomputedFingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// configureLegacy puts the store back into the unconfigured state after the
// test, so later tests in the package never see a stale TempDir path.
func configureLegacy(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { ConfigureKnownHosts("") })
}

func TestKnownHostsUnconfiguredAcceptsAnyKey(t *testing.T) {
	ConfigureKnownHosts("")
	configureLegacy(t)
	cb, trust := NewHostKeyVerifier("anywhere:22")
	if trust == nil {
		t.Fatal("trust event must not be nil")
	}
	if err := cb("anywhere:22", nil, newTestPublicKey(t)); err != nil {
		t.Fatalf("legacy mode must accept any key: %v", err)
	}
	// No path is configured, so nothing can be (or needs to be) written to disk.
}

func TestKnownHostsFirstTrustPersistsEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	ConfigureKnownHosts(path)
	configureLegacy(t)
	key := newTestPublicKey(t)
	const addr = "first.example:22"

	cb, trust := NewHostKeyVerifier(addr)
	if err := cb(addr, nil, key); err != nil {
		t.Fatalf("first connection must be trusted: %v", err)
	}
	if trust == nil || !trust.FirstTrust {
		t.Fatalf("FirstTrust = %v, want true", trust)
	}
	if want := recomputedFingerprint(key); trust.Fingerprint != want {
		t.Fatalf("Fingerprint = %q, want %q", trust.Fingerprint, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("known-hosts file not written: %v", err)
	}
	var entries []knownHostFileEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("file does not parse back: %v", err)
	}
	if len(entries) != 1 || entries[0].Host != addr {
		t.Fatalf("entries = %+v, want one entry for %s", entries, addr)
	}
	if len(entries[0].Keys) != 1 {
		t.Fatalf("keys = %+v, want exactly one", entries[0].Keys)
	}
	stored := entries[0].Keys[0]
	if stored.Type != key.Type() {
		t.Fatalf("stored type = %q, want %q", stored.Type, key.Type())
	}
	if want := base64.StdEncoding.EncodeToString(key.Marshal()); stored.Blob != want {
		t.Fatalf("stored blob = %q, want %q", stored.Blob, want)
	}
}

func TestKnownHostsKnownKeyAcceptedAndConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	ConfigureKnownHosts(path)
	configureLegacy(t)
	key := newTestPublicKey(t)
	const addr = "known.example:22"

	if cb, _ := NewHostKeyVerifier(addr); cb(addr, nil, key) != nil {
		t.Fatal("first connection must be trusted")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known-hosts file: %v", err)
	}

	cb, trust := NewHostKeyVerifier(addr)
	if err := cb(addr, nil, key); err != nil {
		t.Fatalf("known key must be accepted: %v", err)
	}
	if trust.FirstTrust {
		t.Fatal("FirstTrust must stay false for an already-stored key")
	}
	if want := recomputedFingerprint(key); trust.Fingerprint != want {
		t.Fatalf("Fingerprint = %q, want %q", trust.Fingerprint, want)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read known-hosts file: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("reconnecting a known key must not rewrite the file")
	}

	// Race scenario 3's callback across goroutines (own verifier each, so the
	// shared RWMutex-guarded store is what gets shaken, not one trust event).
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, tr := NewHostKeyVerifier(addr)
			if err := c(addr, nil, key); err != nil {
				errs[i] = err
				return
			}
			if tr.FirstTrust {
				errs[i] = fmt.Errorf("FirstTrust flipped true under concurrency")
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent callback %d: %v", i, err)
		}
	}
}

func TestKnownHostsChangedKeyRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	ConfigureKnownHosts(path)
	configureLegacy(t)
	oldKey := newTestPublicKey(t)
	const addr = "changed.example:22"

	if cb, _ := NewHostKeyVerifier(addr); cb(addr, nil, oldKey) != nil {
		t.Fatal("first connection must be trusted")
	}

	// Same key type, different bytes.
	newKey := newTestPublicKey(t)
	cb, trust := NewHostKeyVerifier(addr)
	err := cb(addr, nil, newKey)
	if err == nil {
		t.Fatal("changed key must be rejected")
	}
	if trust.FirstTrust {
		t.Fatal("rejected handshake must not report FirstTrust")
	}
	msg := err.Error()
	if want := addr; !strings.Contains(msg, want) {
		t.Errorf("error must contain addr %q: %s", want, msg)
	}
	if !strings.Contains(strings.ToLower(msg), "changed") {
		t.Errorf("error must contain \"changed\": %s", msg)
	}
	if want := recomputedFingerprint(newKey); !strings.Contains(msg, want) {
		t.Errorf("error must contain presented fingerprint %q: %s", want, msg)
	}
	if want := recomputedFingerprint(oldKey); !strings.Contains(msg, want) {
		t.Errorf("error must contain stored fingerprint %q: %s", want, msg)
	}
	if !strings.Contains(msg, path) {
		t.Errorf("error must contain the state file path %q: %s", path, msg)
	}

	// A different key type counts as changed too.
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	rsaKey, err := ssh.NewPublicKey(&rsaPriv.PublicKey)
	if err != nil {
		t.Fatalf("ssh rsa public key: %v", err)
	}
	cb2, _ := NewHostKeyVerifier(addr)
	err = cb2(addr, nil, rsaKey)
	if err == nil {
		t.Fatal("key of a different type must be rejected")
	}
	msg = err.Error()
	if want := recomputedFingerprint(rsaKey); !strings.Contains(msg, want) {
		t.Errorf("error must contain presented rsa fingerprint %q: %s", want, msg)
	}
	if want := recomputedFingerprint(oldKey); !strings.Contains(msg, want) {
		t.Errorf("error must contain stored fingerprint %q: %s", want, msg)
	}
}

func TestKnownHostsReloadsFromDiskOnReconfigure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	ConfigureKnownHosts(path)
	configureLegacy(t)
	key := newTestPublicKey(t)
	const addr = "reload.example:22"

	if cb, _ := NewHostKeyVerifier(addr); cb(addr, nil, key) != nil {
		t.Fatal("first connection must be trusted")
	}

	// Simulate an app restart: fresh in-memory state, same file on disk.
	ConfigureKnownHosts(path)
	cb, trust := NewHostKeyVerifier(addr)
	if err := cb(addr, nil, key); err != nil {
		t.Fatalf("key persisted before restart must still be trusted: %v", err)
	}
	if trust.FirstTrust {
		t.Fatal("reloaded key must not report FirstTrust")
	}

	other := newTestPublicKey(t)
	cb2, _ := NewHostKeyVerifier(addr)
	if err := cb2(addr, nil, other); err == nil {
		t.Fatal("different key must still be rejected after reload")
	}
}

func TestKnownHostsFileModeIs0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	ConfigureKnownHosts(path)
	configureLegacy(t)
	key := newTestPublicKey(t)

	if cb, _ := NewHostKeyVerifier("mode.example:22"); cb("mode.example:22", nil, key) != nil {
		t.Fatal("first connection must be trusted")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat known-hosts file: %v", err)
	}
	if got, want := fi.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("file mode = %o, want %o", got, want)
	}
}
