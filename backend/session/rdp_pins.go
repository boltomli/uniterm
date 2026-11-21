package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ys-ll/uniterm/backend/log"
)

// RDP server-certificate pinning (trust on first use).
//
// The proxy terminates TLS to the RDP server and hands the plaintext stream to
// the WASM client, so the certificate chain is never validated anywhere:
// without a pin, an active attacker between uniTerm and the RDP host can
// present their own certificate and, on servers without NLA/CredSSP pubkey
// binding, read the session (including credentials typed into it).
//
// Pins live next to the SSH known_hosts file. A first connection records the
// leaf fingerprint; a later connection that presents a different certificate
// for the same host:port is refused with an explicit error, and the operator
// has to delete the entry to accept the change (the standard TOFU model).
//
// Loopback targets are not pinned: they are reached either directly on this
// machine or through an already-authenticated SSH tunnel, so there is no
// network segment to attack.
const rdpPinFileName = "rdp_certs.json"

// rdpPinDisableEnv is an escape hatch for environments that cannot persist
// pins (read-only home in a container) or that re-provision RDP certificates
// on every boot. Setting it to "1" logs a warning on every connection.
const rdpPinDisableEnv = "UNITERM_RDP_PIN_DISABLE"

type rdpPinEntry struct {
	Fingerprint string `json:"fingerprint"` // sha256:<hex>
	FirstSeen   string `json:"firstSeen"`   // RFC3339
}

type rdpPinFile struct {
	Pins map[string]rdpPinEntry `json:"pins"`
}

var (
	rdpPinMu   sync.Mutex
	rdpPinPath = defaultRDPPinPath
)

func defaultRDPPinPath() (string, error) {
	// Reuse the SSH known_hosts location resolver: it already handles the
	// per-platform home directory (including Android's sandbox).
	if known, err := KnownHostsPath(); err == nil {
		return filepath.Join(filepath.Dir(known), rdpPinFileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".uniterm", rdpPinFileName), nil
}

// rdpPinFingerprint returns the SHA-256 fingerprint of a DER certificate.
func rdpPinFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// checkRDPCertPin enforces TOFU pinning for one RDP target. It returns an
// error when the presented certificate differs from the stored pin.
func checkRDPCertPin(target string, leafDER []byte) error {
	if os.Getenv(rdpPinDisableEnv) == "1" {
		log.Writef("[RDPProxy] certificate pinning disabled by %s for %s", rdpPinDisableEnv, target)
		return nil
	}
	if host, _, err := net.SplitHostPort(target); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		if strings.EqualFold(host, "localhost") {
			return nil
		}
	}
	if len(leafDER) == 0 {
		return fmt.Errorf("RDP server presented no certificate")
	}

	path, err := rdpPinPath()
	if err != nil {
		log.Writef("[RDPProxy] cannot locate the certificate pin file (%v); continuing without pinning", err)
		return nil
	}

	fingerprint := rdpPinFingerprint(leafDER)

	rdpPinMu.Lock()
	defer rdpPinMu.Unlock()

	pins := rdpPinFile{Pins: map[string]rdpPinEntry{}}
	raw, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		if uerr := json.Unmarshal(raw, &pins); uerr != nil {
			// A corrupted pin file must not silently reset trust: that would
			// turn a local write into a full MITM bypass.
			return fmt.Errorf("RDP certificate pin file %s is corrupt (%v); delete it to re-trust on first use", path, uerr)
		}
		if pins.Pins == nil {
			pins.Pins = map[string]rdpPinEntry{}
		}
	case os.IsNotExist(readErr):
		// first run: fall through and record
	default:
		log.Writef("[RDPProxy] cannot read %s (%v); continuing without pinning", path, readErr)
		return nil
	}

	entry, known := pins.Pins[target]
	if known {
		if strings.EqualFold(entry.Fingerprint, fingerprint) {
			return nil
		}
		log.Writef("[RDPProxy] certificate CHANGE for %s: pinned %s, presented %s", target, entry.Fingerprint, fingerprint)
		return fmt.Errorf(
			"RDP server certificate for %s does not match the pinned certificate (%s). "+
				"If the server certificate was legitimately replaced, delete its entry from %s and reconnect",
			target, entry.Fingerprint, path)
	}

	pins.Pins[target] = rdpPinEntry{Fingerprint: fingerprint, FirstSeen: time.Now().UTC().Format(time.RFC3339)}
	if err := writeRDPPins(path, pins); err != nil {
		log.Writef("[RDPProxy] cannot persist the certificate pin (%v); pinning is not enforced for %s", err, target)
		return nil
	}
	log.Writef("[RDPProxy] pinned certificate for %s (%s)", target, fingerprint)
	return nil
}

func writeRDPPins(path string, pins rdpPinFile) error {
	raw, err := json.MarshalIndent(pins, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
