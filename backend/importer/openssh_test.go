package importer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseOpenSSH(t *testing.T) {
	data := []byte("Host web\n  HostName 10.0.0.7\n  User root\n  Port 2222\n  IdentityFile ~/.ssh/id_ed25519\n")
	res, err := parseOpenSSH(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := res.Connections[0]
	if c.Type != "ssh" || c.Host != "10.0.0.7" || c.User != "root" || c.Port != 2222 || c.AuthType != "key" {
		t.Fatalf("mapping wrong: %+v", c)
	}
}

// TestParseOpenSSHTildeKeyPath locks in that IdentityFile paths written as
// "~/.ssh/..." (the common OpenSSH config form) are expanded to the user's
// home directory at import time, so the stored connection connects without
// failing to locate the private key (#981).
func TestParseOpenSSHTildeKeyPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no user home dir available")
	}
	data := []byte("Host web\n  IdentityFile ~/.ssh/ed25519_ubuntu\n")
	res, err := parseOpenSSH(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := res.Connections[0]
	if c.KeyPath != filepath.Join(home, ".ssh", "ed25519_ubuntu") {
		t.Fatalf("KeyPath = %q, want %q", c.KeyPath, filepath.Join(home, ".ssh", "ed25519_ubuntu"))
	}
}
