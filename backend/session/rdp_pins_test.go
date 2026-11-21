package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// withTestRDPPins points the RDP certificate pin store at a temp file.
func withTestRDPPins(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rdp_certs.json")
	old := rdpPinPath
	rdpPinPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { rdpPinPath = old })
	t.Setenv(rdpPinDisableEnv, "")
	return path
}

// TestRDPCertPinTOFU covers first-use pinning and change detection.
func TestRDPCertPinTOFU(t *testing.T) {
	path := withTestRDPPins(t)

	certA := []byte("certificate-A")
	certB := []byte("certificate-B")
	const target = "rdp.example.com:3389"

	if err := checkRDPCertPin(target, certA); err != nil {
		t.Fatalf("first connection should pin: %v", err)
	}
	if err := checkRDPCertPin(target, certA); err != nil {
		t.Fatalf("unchanged certificate should be accepted: %v", err)
	}
	if err := checkRDPCertPin(target, certB); err == nil {
		t.Fatal("changed certificate was accepted")
	}
	// Another target pins independently.
	if err := checkRDPCertPin("other.example.com:3389", certB); err != nil {
		t.Fatalf("second target should pin independently: %v", err)
	}
	// Loopback is exempt (direct or through an authenticated SSH tunnel).
	if err := checkRDPCertPin("127.0.0.1:3389", certB); err != nil {
		t.Fatalf("loopback target must not be pinned: %v", err)
	}
	if err := checkRDPCertPin("localhost:3389", certA); err != nil {
		t.Fatalf("localhost target must not be pinned: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("pin file missing: %v", err)
	}
	// Windows has no POSIX permission bits (Go reports 0666 for any writable
	// file); the 0600 mode matters on Unix and is asserted there.
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("pin file mode = %o, want 600", perm)
		}
	}
}

// TestRDPCertPinCorruptFileFailsClosed makes sure a damaged pin file cannot be
// used to silently reset trust.
func TestRDPCertPinCorruptFileFailsClosed(t *testing.T) {
	path := withTestRDPPins(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkRDPCertPin("rdp.example.com:3389", []byte("cert")); err == nil {
		t.Fatal("corrupt pin file was ignored instead of failing closed")
	}
}

// TestRDPCertPinDisableEscalation documents the explicit escape hatch.
func TestRDPCertPinDisableEscalation(t *testing.T) {
	withTestRDPPins(t)
	t.Setenv(rdpPinDisableEnv, "1")
	if err := checkRDPCertPin("rdp.example.com:3389", []byte("cert-A")); err != nil {
		t.Fatalf("pin with disable env: %v", err)
	}
	if err := checkRDPCertPin("rdp.example.com:3389", []byte("cert-B")); err != nil {
		t.Fatalf("pin mismatch must be ignored when pinning is disabled: %v", err)
	}
}
