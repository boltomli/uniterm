package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePS is a PasswordStore that encodes "enc:v1:fake-"+pt. Its output is
// recognized by isEncryptedField, so round-trips through the sync boundary
// exercise the real normalization path.
type fakePS struct{}

func (fakePS) Encrypt(plaintext string) (string, error) {
	return "enc:v1:fake-" + plaintext, nil
}
func (fakePS) Decrypt(encoded string) (string, error) {
	return strings.TrimPrefix(encoded, "enc:v1:fake-"), nil
}
func (fakePS) Unlocked() bool { return true }

// lockedPS is a PasswordStore that reports itself locked; used to assert that
// sync refuses to run when the credential store holds no usable key.
type lockedPS struct{}

func (lockedPS) Encrypt(plaintext string) (string, error) { return "", nil }
func (lockedPS) Decrypt(encoded string) (string, error)   { return "", nil }
func (lockedPS) Unlocked() bool                           { return false }

// TestSyncBoundaryNormalizesEncryptedFields is the regression test for the
// enc:v1: opacity bug: in-place encrypted secret fields must be normalized to
// plaintext on push and back to enc:v1: on pull, so the whole file is
// protected solely by the sync key at rest in the repo.
func TestSyncBoundaryNormalizesEncryptedFields(t *testing.T) {
	key := make([]byte, 32)
	srcDir := t.TempDir()
	repoDir := t.TempDir()
	dstDir := t.TempDir()

	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	write(srcDir, "connections.json", `{"connections":[{"id":"c1","authType":"password","password":"enc:v1:fake-secret","k8sConfigInline":"enc:v1:fake-kube"}]}`)
	write(srcDir, "identities.json", `{"identities":[{"id":"i1","password":"enc:v1:fake-idpass"}]}`)
	write(srcDir, "proxies.json", `{"proxies":[{"id":"p1","pass":"enc:v1:fake-proxypass"}]}`)
	write(srcDir, "tunnels.json", `{"version":1,"groups":[],"tunnels":[{"id":"t1","name":"jump","upstream":{"kind":"socks5","host":"h","port":1080,"pass":"enc:v1:fake-tunnelpass"}}]}`)
	write(srcDir, "settings.json", `{"ai":{"models":[{"id":"m1","apiKey":"enc:v1:fake-apikey"}]}}`)

	if err := EncryptConfigFiles(srcDir, repoDir, key, nil, fakePS{}); err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// The repo copy must be plaintext (protected only by the sync key).
	repoData, err := os.ReadFile(filepath.Join(repoDir, "connections.json"))
	if err != nil {
		t.Fatalf("read repo connections: %v", err)
	}
	repoPlain, err := decryptBytes(string(repoData), key, "connections.json")
	if err != nil {
		t.Fatalf("decrypt repo connections: %v", err)
	}
	var repoConn struct {
		Connections []map[string]interface{} `json:"connections"`
	}
	if err := json.Unmarshal(repoPlain, &repoConn); err != nil {
		t.Fatalf("parse repo connections: %v", err)
	}
	if got := repoConn.Connections[0]["password"]; got != "secret" {
		t.Fatalf("repo password = %q, want plaintext %q", got, "secret")
	}
	if got := repoConn.Connections[0]["k8sConfigInline"]; got != "kube" {
		t.Fatalf("repo k8sConfigInline = %q, want plaintext %q", got, "kube")
	}

	// Device-bound enc:v1: must not escape into the repo copy of tunnels.json
	// either, and the version/groups wrapper must survive normalization.
	repoTunData, err := os.ReadFile(filepath.Join(repoDir, "tunnels.json"))
	if err != nil {
		t.Fatalf("read repo tunnels: %v", err)
	}
	repoTunPlain, err := decryptBytes(string(repoTunData), key, "tunnels.json")
	if err != nil {
		t.Fatalf("decrypt repo tunnels: %v", err)
	}
	var repoTun struct {
		Version int `json:"version"`
		Tunnels []struct {
			Upstream map[string]interface{} `json:"upstream"`
		} `json:"tunnels"`
	}
	if err := json.Unmarshal(repoTunPlain, &repoTun); err != nil {
		t.Fatalf("parse repo tunnels: %v", err)
	}
	if repoTun.Version != 1 {
		t.Fatalf("repo tunnels version = %d, want 1 (wrapper keys dropped?)", repoTun.Version)
	}
	if got := repoTun.Tunnels[0].Upstream["pass"]; got != "tunnelpass" {
		t.Fatalf("repo tunnel pass = %q, want plaintext %q", got, "tunnelpass")
	}

	// Pull must re-encrypt back to enc:v1: under the local credential key.
	if err := DecryptConfigFiles(repoDir, dstDir, key, fakePS{}); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	dstData, err := os.ReadFile(filepath.Join(dstDir, "connections.json"))
	if err != nil {
		t.Fatalf("read dst connections: %v", err)
	}
	var dstConn struct {
		Connections []map[string]interface{} `json:"connections"`
	}
	if err := json.Unmarshal(dstData, &dstConn); err != nil {
		t.Fatalf("parse dst connections: %v", err)
	}
	if got := dstConn.Connections[0]["password"]; got != "enc:v1:fake-secret" {
		t.Fatalf("dst password = %q, want %q", got, "enc:v1:fake-secret")
	}
	if got := dstConn.Connections[0]["k8sConfigInline"]; got != "enc:v1:fake-kube" {
		t.Fatalf("dst k8sConfigInline = %q, want %q", got, "enc:v1:fake-kube")
	}

	dstTunData, err := os.ReadFile(filepath.Join(dstDir, "tunnels.json"))
	if err != nil {
		t.Fatalf("read dst tunnels: %v", err)
	}
	var dstTun struct {
		Version int `json:"version"`
		Tunnels []struct {
			Upstream map[string]interface{} `json:"upstream"`
		} `json:"tunnels"`
	}
	if err := json.Unmarshal(dstTunData, &dstTun); err != nil {
		t.Fatalf("parse dst tunnels: %v", err)
	}
	if dstTun.Version != 1 {
		t.Fatalf("dst tunnels version = %d, want 1", dstTun.Version)
	}
	if got := dstTun.Tunnels[0].Upstream["pass"]; got != "enc:v1:fake-tunnelpass" {
		t.Fatalf("dst tunnel pass = %q, want %q", got, "enc:v1:fake-tunnelpass")
	}
}

// TestCompareTunnelsNormalizesUpstreamPass: the local file carries a
// device-bound enc:v1: pass while the decrypted remote copy is plaintext —
// the comparison must normalize so the difference does not read as a conflict.
func TestCompareTunnelsNormalizesUpstreamPass(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "local")
	remote := filepath.Join(dir, "remote")
	if err := os.MkdirAll(local, 0755); err != nil {
		t.Fatalf("mkdir local: %v", err)
	}
	if err := os.MkdirAll(remote, 0755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "tunnels.json"),
		[]byte(`{"version":1,"tunnels":[{"id":"t1","upstream":{"kind":"socks5","host":"h","port":1080,"pass":"enc:v1:fake-pw"}}]}`), 0600); err != nil {
		t.Fatalf("write local: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "tunnels.json"),
		[]byte(`{"version":1,"tunnels":[{"id":"t1","upstream":{"kind":"socks5","host":"h","port":1080,"pass":"pw"}}]}`), 0600); err != nil {
		t.Fatalf("write remote: %v", err)
	}

	same, err := compareConfigFiles(filepath.Join(local, "tunnels.json"), filepath.Join(remote, "tunnels.json"), nil, fakePS{})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if !same {
		t.Fatalf("compareConfigFiles reported a difference when local enc:v1: decrypts to the same plaintext")
	}
}

// TestCompareConfigFilesNormalizesEncryptedFields verifies that the comparison
// path decrypts local enc:v1: fields so a push whose only change is the local
// encryption mode does not report a spurious difference (which would loop).
func TestCompareConfigFilesNormalizesEncryptedFields(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "local")
	remote := filepath.Join(dir, "remote")
	if err := os.MkdirAll(local, 0755); err != nil {
		t.Fatalf("mkdir local: %v", err)
	}
	if err := os.MkdirAll(remote, 0755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "connections.json"),
		[]byte(`{"connections":[{"id":"c1","authType":"password","password":"enc:v1:fake-secret"}]}`), 0600); err != nil {
		t.Fatalf("write local: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "connections.json"),
		[]byte(`{"connections":[{"id":"c1","authType":"password","password":"secret"}]}`), 0600); err != nil {
		t.Fatalf("write remote: %v", err)
	}

	same, err := compareConfigFiles(filepath.Join(local, "connections.json"), filepath.Join(remote, "connections.json"), nil, fakePS{})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if !same {
		t.Fatalf("compareConfigFiles reported a difference when local enc:v1: decrypts to the same plaintext")
	}
}

// TestRequireUnlocked verifies sync refuses to run when the credential store is
// locked, but still allows a nil (unwired) or unlocked store.
func TestRequireUnlocked(t *testing.T) {
	s := &SyncService{}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.requireUnlocked(); err != nil {
		t.Fatalf("nil store should be allowed, got %v", err)
	}

	s.passwordStore = fakePS{}
	if err := s.requireUnlocked(); err != nil {
		t.Fatalf("unlocked store should be allowed, got %v", err)
	}

	s.passwordStore = lockedPS{}
	if err := s.requireUnlocked(); err == nil {
		t.Fatal("locked store should be refused")
	}
}
