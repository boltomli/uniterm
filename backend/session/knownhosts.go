package session

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/ys-ll/uniterm/backend/log"
)

// knownKey is one stored host key: the SSH key type plus the wire encoding of
// the key, base64.StdEncoded (JSON-safe). Multiple keys per host are allowed
// so a host can carry rsa+ed25519 side by side (manually seeded files, and
// hosts whose negotiated key type changed between connections).
type knownKey struct {
	Type string `json:"type"`
	Blob string `json:"blob"`
}

// knownHostFileEntry is one host in the on-disk JSON array.
type knownHostFileEntry struct {
	Host string     `json:"host"`
	Keys []knownKey `json:"keys"`
}

// TrustEvent reports what the callback from NewHostKeyVerifier decided.
// It is filled in AFTER the callback runs: FirstTrust is true only when this
// callback persisted the host's very first entry; Fingerprint is the
// OpenSSH-style SHA256 fingerprint of the verified key.
type TrustEvent struct {
	FirstTrust  bool
	Fingerprint string
}

// HostKeyChangedError is returned by the callback from NewHostKeyVerifier
// when a known host presented a different key — the signature of a MITM or of
// a legitimate-but-unrecorded key rotation. Either way the dial aborts; the
// remediation is to verify the new key out of band, then drop the stored entry.
type HostKeyChangedError struct {
	Addr                 string
	PresentedFingerprint string
	StoredFingerprint    string
	Path                 string
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf(
		"host key CHANGED for %s — possible MITM; verify out-of-band, then remove the entry from %s to trust the new key (presented key %s, stored key %s)",
		e.Addr, e.Path, e.PresentedFingerprint, e.StoredFingerprint)
}

// knownHostsStore holds the trust-on-first-use state. path == "" selects
// legacy mode (accept every key, mirroring the old no-verification callback)
// so unit tests and dev contexts work without app-startup wiring.
type knownHostsStore struct {
	mu      sync.RWMutex
	path    string
	loaded  bool
	entries map[string][]knownKey
}

var knownHosts knownHostsStore

// ConfigureKnownHosts points the trust-on-first-use store at path (the app
// passes <dataDir>/known_hosts.json) and resets in-memory state; the file is
// re-read lazily on the next handshake. Safe to call repeatedly — tests
// reconfigure it. An empty path selects legacy mode: every host key is
// accepted and a single warning is logged.
func ConfigureKnownHosts(path string) {
	knownHosts.mu.Lock()
	knownHosts.path = path
	knownHosts.loaded = false
	knownHosts.entries = make(map[string][]knownKey)
	knownHosts.mu.Unlock()
	if path == "" {
		log.Writef("[known_hosts] no known-hosts file configured — accepting ALL host keys (MITM possible)")
	}
}

// NewHostKeyVerifier returns a trust-on-first-use host key callback for
// hostport plus the trust event the callback fills in when it runs.
//
// Callback behavior:
//  1. legacy/unconfigured (empty path) → accept unconditionally;
//  2. unknown hostport → accept and persist the entry (FirstTrust=true);
//  3. known hostport, key matches a stored entry → accept (FirstTrust stays false);
//  4. known hostport, presented key differs from every stored entry (a
//     different key type counts as changed) → abort with HostKeyChangedError.
func NewHostKeyVerifier(hostport string) (cb ssh.HostKeyCallback, trust *TrustEvent) {
	trust = &TrustEvent{}
	cb = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		blob := base64.StdEncoding.EncodeToString(key.Marshal())
		fp := hostKeyFingerprint(key)

		// Read fast path: legacy mode, or a key already on file.
		knownHosts.mu.RLock()
		legacy := knownHosts.path == ""
		onFile := knownHosts.entries[hostport]
		knownHosts.mu.RUnlock()
		if legacy {
			return nil
		}
		if hasHostKey(onFile, key, blob) {
			trust.Fingerprint = fp
			return nil
		}

		// Slow path: first contact with this host (persist) or a changed key
		// (reject) — both need the write lock.
		knownHosts.mu.Lock()
		defer knownHosts.mu.Unlock()
		if knownHosts.path == "" {
			return nil // reconfigured to legacy mode meanwhile
		}
		knownHosts.loadLocked()
		keys := knownHosts.entries[hostport]
		trust.Fingerprint = fp
		if hasHostKey(keys, key, blob) {
			return nil
		}
		if len(keys) == 0 {
			// Trust on first use: persist before accepting, so a failure to
			// record the key never degrades to an unverifiable connection.
			knownHosts.entries[hostport] = []knownKey{{Type: key.Type(), Blob: blob}}
			if err := knownHosts.persistLocked(); err != nil {
				delete(knownHosts.entries, hostport)
				return fmt.Errorf("known hosts: cannot save key for %s: %w", hostport, err)
			}
			trust.FirstTrust = true
			return nil
		}
		// Changed: report the stored key of the same type when there is one
		// (rotation setups keep several), else the first stored key.
		worst := keys[0]
		for _, k := range keys {
			if k.Type == key.Type() {
				worst = k
				break
			}
		}
		return &HostKeyChangedError{
			Addr:                 hostport,
			PresentedFingerprint: fp,
			StoredFingerprint:    storedFingerprint(worst),
			Path:                 knownHosts.path,
		}
	}
	return cb, trust
}

// hasHostKey reports whether keys contains exactly this key (type AND wire
// bytes — a same-type key with different bytes, or any key of another type,
// counts as changed).
func hasHostKey(keys []knownKey, key ssh.PublicKey, blob string) bool {
	for _, k := range keys {
		if k.Type == key.Type() && k.Blob == blob {
			return true
		}
	}
	return false
}

// hostKeyFingerprint returns the OpenSSH-style fingerprint: "SHA256:" plus the
// unpadded standard-base64 of the sha256 over the key's wire encoding — the
// same format as ssh.FingerprintSHA256 / ssh-keygen -lf.
func hostKeyFingerprint(key ssh.PublicKey) string {
	return fingerprintOfBlob(key.Marshal())
}

// storedFingerprint recomputes a stored entry's fingerprint from its base64
// blob so changed-key errors can show both sides in the same format.
func storedFingerprint(k knownKey) string {
	blob, err := base64.StdEncoding.DecodeString(k.Blob)
	if err != nil {
		return ""
	}
	return fingerprintOfBlob(blob)
}

func fingerprintOfBlob(blob []byte) string {
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// loadLocked reads the state file once per ConfigureKnownHosts (called under
// mu). A missing file starts empty; an unreadable/unparsable file logs and
// starts empty rather than blocking connections — changed-key detection still
// guards every key that gets stored afterwards.
func (kh *knownHostsStore) loadLocked() {
	if kh.loaded {
		return
	}
	kh.loaded = true
	if kh.path == "" {
		return
	}
	if kh.entries == nil {
		kh.entries = make(map[string][]knownKey)
	}
	data, err := os.ReadFile(kh.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Writef("[known_hosts] cannot read %s: %v — starting empty", kh.path, err)
		}
		return
	}
	var file []knownHostFileEntry
	if err := json.Unmarshal(data, &file); err != nil {
		log.Writef("[known_hosts] cannot parse %s: %v — starting empty", kh.path, err)
		return
	}
	for _, e := range file {
		kh.entries[e.Host] = e.Keys
	}
}

// persistLocked rewrites the whole state file atomically (temp file in the
// same directory + rename) with mode 0600. Chmod is best-effort on Windows.
func (kh *knownHostsStore) persistLocked() error {
	if kh.path == "" {
		return nil
	}
	hosts := make([]string, 0, len(kh.entries))
	for h := range kh.entries {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts) // deterministic output
	out := make([]knownHostFileEntry, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, knownHostFileEntry{Host: h, Keys: kh.entries[h]})
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(kh.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "known_hosts-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil && runtime.GOOS != "windows" {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, kh.path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
