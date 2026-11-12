package session

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ys-ll/uniterm/backend/log"
	"golang.org/x/crypto/ssh"
)

// HostKeyChangedError describes one host key mismatch. The exported fields
// are serialized into the frontend event payload.
type HostKeyChangedError struct {
	Host      string `json:"host"`
	Port      string `json:"port"`
	SessionID string `json:"sessionId"`
	KeyType   string `json:"keyType"`
	OldFp     string `json:"oldFp"` // SHA256 fingerprint (ssh.FingerprintSHA256 format)
	NewFp     string `json:"newFp"` // SHA256 fingerprint
	OldKeyB64 string `json:"oldKeyB64"`
	NewKeyB64 string `json:"newKeyB64"`
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("host key for %s:%s changed (%s): old=%s new=%s — possible MITM; verify the new fingerprint before accepting",
		e.Host, e.Port, e.KeyType, e.OldFp, e.NewFp)
}

// Is lets errors.Is/As recognize host key change errors.
func (e *HostKeyChangedError) Is(target error) bool {
	_, ok := target.(*HostKeyChangedError)
	return ok
}

// hostKeyEmitterFunc is the event emitter signature injected by the App
// layer; it fires when a key change should surface a frontend dialog.
type hostKeyEmitterFunc = func(name string, payload any)

var (
	// hostKeyMu serializes known_hosts file access so concurrent
	// connections cannot clobber each other's entries.
	hostKeyMu sync.Mutex

	hostKeyEmitterMu sync.RWMutex
	hostKeyEmitter   hostKeyEmitterFunc
)

// onceGrant is a one-shot bypass recorded by the change dialog ("connect
// once, don't save"): the exact key shown to the user is allowed through a
// single time without touching known_hosts.
type onceGrant struct {
	keyB64  string
	granted time.Time
}

const (
	onceGrantTTL = 10 * time.Minute
)

var (
	// onceGrants is guarded by hostKeyMu, same as the known_hosts file.
	onceGrants = map[string]onceGrant{}
)

// SetHostKeyEmitter injects the Wails event emitter. Called once by the App
// layer at startup. Passing nil clears it (mainly for tests).
func SetHostKeyEmitter(emitter hostKeyEmitterFunc) {
	hostKeyEmitterMu.Lock()
	defer hostKeyEmitterMu.Unlock()
	hostKeyEmitter = emitter
}

func getHostKeyEmitter() hostKeyEmitterFunc {
	hostKeyEmitterMu.RLock()
	defer hostKeyEmitterMu.RUnlock()
	return hostKeyEmitter
}

// knownHostsKey builds the host column of a known_hosts line, OpenSSH
// style: non-22 ports use the [host]:port form.
func knownHostsKey(host, port string) string {
	if port == "" || port == "22" {
		return host
	}
	return fmt.Sprintf("[%s]:%s", host, port)
}

// normalizeKnownHostsKey maps any host:port spelling (JoinHostPort's
// "host:2222" / "[v6]:2222", or knownHostsKey's "[host]:port") onto the
// canonical line format above, so lookups written against a dial address
// still hit the recorded entry.
func normalizeKnownHostsKey(s string) string {
	if strings.HasPrefix(s, "[") {
		if i := strings.LastIndex(s, "]"); i > 0 && i+1 < len(s) && s[i+1] == ':' {
			return knownHostsKey(s[1:i], s[i+2:])
		}
		return s
	}
	if h, p, ok := strings.Cut(s, ":"); ok {
		return knownHostsKey(h, p)
	}
	return knownHostsKey(s, "")
}

// hostKeyVerifyTarget returns the host/port a connection's host key must be
// verified and recorded under: the preserved pre-tunnel target when a
// jump-host tunnel rewrote Host/Port to 127.0.0.1:<ephemeral local port>
// (setupJumpHostTunnel fills TunnelTargetHost/Port), else the dial address
// itself. Verifying under the rewritten address would record a fresh
// first-use entry for every ephemeral port and let a compromised jump host
// present any key it likes for the real target.
func hostKeyVerifyTarget(config ConnectionConfig) (string, int) {
	if config.TunnelTargetHost != "" {
		port := config.TunnelTargetPort
		if port <= 0 {
			port = 22
		}
		return config.TunnelTargetHost, port
	}
	return config.Host, config.Port
}

// HostKeyCallback returns the TOFU host key verification callback. host/port
// locate the known_hosts entry; sessionID (optional) is carried in the change
// event payload so the frontend can reconnect the failed session after the
// user accepts the new key.
func HostKeyCallback(host, port, sessionID string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		return verifyHostKey(host, port, sessionID, key)
	}
}

// verifyHostKey implements the TOFU verification core.
func verifyHostKey(host, port, sessionID string, newKey ssh.PublicKey) error {
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()

	path, err := KnownHostsPath()
	if err != nil {
		// Failing to resolve the home directory is an environment problem;
		// refusing the connection is safer than letting any key through.
		return fmt.Errorf("hostkey: resolve known_hosts path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("hostkey: mkdir %s: %w", filepath.Dir(path), err)
	}

	keyType := newKey.Type()
	newKeyB64 := base64.StdEncoding.EncodeToString(newKey.Marshal())
	newFp := ssh.FingerprintSHA256(newKey)
	lookupKey := knownHostsKey(host, port)

	// One-shot bypass: the user accepted this exact key in the change dialog
	// for a single connection without recording it ("connect once"). The
	// grant is consumed on use and expires after a while.
	if g, ok := onceGrants[lookupKey]; ok {
		if time.Since(g.granted) > onceGrantTTL {
			delete(onceGrants, lookupKey)
		} else if g.keyB64 == newKeyB64 {
			delete(onceGrants, lookupKey)
			return nil
		}
	}

	entries, err := lookupKnownHostEntries(path, lookupKey)
	if err != nil {
		// Fail closed: the store exists but cannot be read or decoded, so
		// nothing trustworthy is known about this host. Treating it as
		// "first connect" would append to a file we could not parse and
		// silently drop every pin it still held — a corrupt known_hosts must
		// never degrade to accept-anything. The message names the file so the
		// user can repair or remove it.
		log.Writef("hostkey: %v — refusing connections until it is fixed or removed", err)
		return err
	}

	// Exact match (same type and wire bytes): allow.
	for _, e := range entries {
		if e.keyType == keyType && e.keyB64 == newKeyB64 {
			return nil
		}
	}

	if len(entries) == 0 {
		// First connection: record and allow (TOFU).
		if err := appendKnownHostKey(path, lookupKey, keyType, newKeyB64); err != nil {
			return fmt.Errorf("hostkey: write %s: %w", path, err)
		}
		log.Writef("[known_hosts] new host key recorded for %s (%s %s)", lookupKey, keyType, newFp)
		return nil
	}

	// The host is pinned but presented no pinned key: reject. A DIFFERENT key
	// type counts as changed too — accepting it would mint a fresh first-use
	// entry for whatever type a MITM chooses to offer on the very first
	// handshake, which is exactly the bypass the strict store exists to close.
	// (preferPinnedHostKeyAlgorithms keeps an ordinary server that merely ADDED
	// a second key type negotiating the pinned one, so this rarely fires for
	// legitimate hosts; a real rotation goes through the change dialog.)
	pick := entries[0]
	for _, e := range entries {
		if e.keyType == keyType {
			pick = e
			break
		}
	}
	changed := &HostKeyChangedError{
		Host:      host,
		Port:      port,
		SessionID: sessionID,
		KeyType:   keyType,
		OldFp:     ssh.FingerprintSHA256(pick.key),
		NewFp:     newFp,
		OldKeyB64: pick.keyB64,
		NewKeyB64: newKeyB64,
	}
	if emitter := getHostKeyEmitter(); emitter != nil {
		emitter("session:hostkey-changed", map[string]any{
			"host":      changed.Host,
			"port":      changed.Port,
			"sessionId": changed.SessionID,
			"keyType":   changed.KeyType,
			"oldFp":     changed.OldFp,
			"newFp":     changed.NewFp,
			"oldKeyB64": changed.OldKeyB64,
			"newKeyB64": changed.NewKeyB64,
		})
	}
	return changed
}

// storedHostKeyTypes returns the distinct key types recorded for hostport in
// file order (nil when the store is unavailable or the host has no entry).
// It feeds preferPinnedHostKeyAlgorithms: offering an already-pinned type
// first keeps a server that ADDS a second key type from tripping a false
// "host key CHANGED" alert. hostport may be in any host:port spelling.
func storedHostKeyTypes(hostport string) []string {
	path, err := KnownHostsPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // missing/unreadable → leave the caller's order untouched
	}
	want := normalizeKnownHostsKey(hostport)
	seen := make(map[string]bool, 4)
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || normalizeKnownHostsKey(fields[0]) != want {
			continue
		}
		if fields[1] == "" || seen[fields[1]] {
			continue
		}
		seen[fields[1]] = true
		out = append(out, fields[1])
	}
	return out
}

// appendKnownHostKey appends one entry to the known_hosts file.
func appendKnownHostKey(path, host, keyType, keyB64 string) error {
	line := fmt.Sprintf("%s %s %s\n", host, keyType, keyB64)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

// knownHostEntry is one key recorded for a host in the known_hosts file.
type knownHostEntry struct {
	keyType string
	keyB64  string
	key     ssh.PublicKey
}

// lookupKnownHostEntries returns every key recorded for host in path (nil
// when the file or the host is unknown). An entry that NAMES this host but
// cannot be decoded fails the lookup: a corrupt pin must never read as "no
// pin", which would let any key through as a first connect.
func lookupKnownHostEntries(path, host string) ([]knownHostEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []knownHostEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != host {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(fields[2])
		if err != nil {
			return nil, fmt.Errorf("hostkey: corrupt key for %s in %s: %w", host, path, err)
		}
		parsed, err := ssh.ParsePublicKey(raw)
		if err != nil {
			return nil, fmt.Errorf("hostkey: corrupt key for %s in %s: %w", host, path, err)
		}
		out = append(out, knownHostEntry{keyType: fields[1], keyB64: fields[2], key: parsed})
	}
	return out, nil
}

// AcceptHostKeyChange replaces the known_hosts entry for host with the new
// public key. Called from the App layer after the user confirms a host key
// change in the frontend dialog. newKeyB64 is the base64-encoded SSH wire
// format public key delivered with the change event.
func AcceptHostKeyChange(host, port, keyType, newKeyB64 string) error {
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()

	if _, _, err := parseHostKeyB64(newKeyB64); err != nil {
		return fmt.Errorf("hostkey: invalid key: %w", err)
	}

	path, err := KnownHostsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lookupKey := knownHostsKey(host, port)
	return replaceKnownHostKey(path, lookupKey, keyType, newKeyB64)
}

// AcceptHostKeyOnce records a one-shot bypass for host:port, allowing the
// given key through a single connection without writing it to known_hosts.
// Called from the App layer when the user picks "connect once, don't save"
// in the change dialog. The grant is bound to the exact key displayed in the
// dialog and expires after onceGrantTTL.
func AcceptHostKeyOnce(host, port, keyB64 string) error {
	if _, _, err := parseHostKeyB64(keyB64); err != nil {
		return fmt.Errorf("hostkey: invalid key: %w", err)
	}
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()
	// Drop grants that already expired so the map cannot grow without bound
	// if the dialog is opened repeatedly without connecting.
	now := time.Now()
	for k, g := range onceGrants {
		if now.Sub(g.granted) > onceGrantTTL {
			delete(onceGrants, k)
		}
	}
	onceGrants[knownHostsKey(host, port)] = onceGrant{keyB64: keyB64, granted: now}
	return nil
}

// parseHostKeyB64 validates a base64-encoded SSH wire format public key.
func parseHostKeyB64(keyB64 string) (ssh.PublicKey, []byte, error) {
	raw, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := ssh.ParsePublicKey(raw)
	if err != nil {
		return nil, nil, err
	}
	return parsed, raw, nil
}

// replaceKnownHostKey swaps the host+keyType entry in the known_hosts file;
// appends one when absent. The rewrite goes through a temp file + rename in
// the same directory so a crash mid-write cannot truncate (and thereby
// corrupt) the store — under fail-closed verification a half-written file
// would lock the user out of every host it held.
func replaceKnownHostKey(path, host, keyType, newKeyB64 string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 3 && fields[0] == host && fields[1] == keyType {
			continue // drop the stale entry
		}
		kept = append(kept, trimmed)
	}
	kept = append(kept, fmt.Sprintf("%s %s %s", host, keyType, newKeyB64))
	content := strings.Join(kept, "\n") + "\n"
	return writeFileAtomic(path, []byte(content), 0o600)
}

// writeFileAtomic writes data to path via a same-directory temp file and
// rename, with mode perm. On Windows rename replaces an existing file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+"-*.tmp")
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
	if err := os.Chmod(tmpName, perm); err != nil && runtime.GOOS != "windows" {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// legacyKnownHostEntry mirrors one entry of the pre-migration JSON store
// (backend/session/knownhosts.go, <dataDir>/known_hosts.json).
type legacyKnownHostEntry struct {
	Host string `json:"host"`
	Keys []struct {
		Type string `json:"type"`
		Blob string `json:"blob"`
	} `json:"keys"`
}

// MigrateLegacyKnownHosts folds pins from the older JSON store into the
// OpenSSH-format file KnownHostsPath points at. It is idempotent: entries
// whose (host, type, wire bytes) are already recorded are skipped, the
// legacy file is left in place for inspection, and a missing legacy file is
// a no-op. Called once at startup — without it, every host the user had
// pinned would be re-trusted on first connect after the upgrade, which is
// exactly the silent re-TOFU the fail-closed store exists to prevent.
func MigrateLegacyKnownHosts(jsonPath string) error {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var legacy []legacyKnownHostEntry
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("parse legacy %s: %w", jsonPath, err)
	}
	path, err := KnownHostsPath()
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	type pin struct{ host, keyType, blob string }
	seen := make(map[pin]bool)
	for _, line := range strings.Split(string(current), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		seen[pin{normalizeKnownHostsKey(fields[0]), fields[1], fields[2]}] = true
	}
	var add []string
	added := 0
	for _, e := range legacy {
		host := normalizeKnownHostsKey(e.Host)
		if host == "" {
			continue
		}
		for _, k := range e.Keys {
			if k.Type == "" || k.Blob == "" {
				continue
			}
			p := pin{host, k.Type, k.Blob}
			if seen[p] {
				continue
			}
			seen[p] = true
			add = append(add, fmt.Sprintf("%s %s %s", host, k.Type, k.Blob))
			added++
		}
	}
	if added == 0 {
		return nil
	}
	content := string(current)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += strings.Join(add, "\n") + "\n"
	if err := writeFileAtomic(path, []byte(content), 0o600); err != nil {
		return err
	}
	log.Writef("[known_hosts] migrated %d pin(s) from %s into %s", added, jsonPath, path)
	return nil
}

// AsHostKeyChangedError returns the *HostKeyChangedError inside err and
// true when err carries one, for the App layer to render friendly terminal
// messages.
func AsHostKeyChangedError(err error) (*HostKeyChangedError, bool) {
	var changed *HostKeyChangedError
	if errors.As(err, &changed) {
		return changed, true
	}
	return nil, false
}
