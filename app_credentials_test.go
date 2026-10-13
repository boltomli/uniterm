package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/credentials"
	"github.com/ys-ll/uniterm/backend/session"
	"github.com/ys-ll/uniterm/backend/store"
)

// credTestKeychain is an in-memory credentials.Keychain for the root package.
type credTestKeychain struct{ entries map[string]string }

func newCredTestKeychain() *credTestKeychain {
	return &credTestKeychain{entries: map[string]string{}}
}
func (k *credTestKeychain) Get(key string) (string, error) {
	if v, ok := k.entries[key]; ok {
		return v, nil
	}
	return "", credentials.ErrKeychainNotFound
}
func (k *credTestKeychain) Set(key, value string) error { k.entries[key] = value; return nil }
func (k *credTestKeychain) Delete(key string) error     { delete(k.entries, key); return nil }

// newCredentialTestApp wires every store the credential lifecycle touches to
// one credentials.Store holding keyA.
func newCredentialTestApp(t *testing.T) (*App, *credentials.Store, string) {
	t.Helper()
	dir := t.TempDir()

	cs := credentials.New(dir, newCredTestKeychain())
	key, err := randomKey32()
	if err != nil {
		t.Fatalf("randomKey32: %v", err)
	}
	cs.SetKey(key)

	conn, err := store.NewConnectionStore(dir)
	if err != nil {
		t.Fatalf("NewConnectionStore: %v", err)
	}
	settings, err := store.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}
	idents, err := store.NewIdentityStore(dir)
	if err != nil {
		t.Fatalf("NewIdentityStore: %v", err)
	}
	proxies, err := store.NewProxyStore(dir)
	if err != nil {
		t.Fatalf("NewProxyStore: %v", err)
	}
	ai, err := store.NewAIConfigStore(dir)
	if err != nil {
		t.Fatalf("NewAIConfigStore: %v", err)
	}
	tunnels := store.NewTunnelStore(dir)

	conn.SetPasswordStore(cs)
	settings.SetPasswordStore(cs)
	idents.SetPasswordStore(cs)
	proxies.SetPasswordStore(cs)
	ai.SetPasswordStore(cs)
	tunnels.SetPasswordStore(cs)

	a := &App{
		dataDir:         dir,
		credentialStore: cs,
		connectionStore: conn,
		settingsStore:   settings,
		identityStore:   idents,
		proxyStore:      proxies,
		aiConfigStore:   ai,
		tunnelStore:     tunnels,
	}
	return a, cs, dir
}

// seedCredentialTestApp writes one secret into every store that encrypts.
func seedCredentialTestApp(t *testing.T, a *App) {
	t.Helper()
	if err := a.connectionStore.Save(session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{{
			ID: "c1", Type: "ssh", AuthType: "password", Password: "conn-pass",
			SentinelPassword: "sentinel-pass", TunnelSSHPassword: "tunnel-ssh-pass",
			K8sConfigInline: "apiVersion: v1", KeyContent: "pem-key-material",
		}},
	}); err != nil {
		t.Fatalf("seed connections: %v", err)
	}

	settings, err := a.settingsStore.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	settings.AI.Models = []store.AIModelConfig{{ID: "m1", Name: "prod", APIKey: "sk-settings"}}
	if err := a.settingsStore.Save(settings); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := a.identityStore.Save(session.IdentityStoreData{
		Identities: []session.Identity{{
			ID: "i1", Name: "prod", AuthType: "password", Password: "identity-pass",
		}},
	}); err != nil {
		t.Fatalf("seed identities: %v", err)
	}

	if err := a.proxyStore.Save(session.ProxyStoreData{
		Proxies: []session.Proxy{{
			ID: "p1", Name: "vpn", Kind: "socks5", Host: "127.0.0.1", Port: 1080, Pass: "proxy-pass",
		}},
	}); err != nil {
		t.Fatalf("seed proxies: %v", err)
	}

	if err := a.aiConfigStore.Save(store.AIStoreData{
		Models: []store.AIModelConfig{{ID: "m2", Name: "agent", APIKey: "sk-ai"}},
	}); err != nil {
		t.Fatalf("seed ai.json: %v", err)
	}

	if err := a.tunnelStore.Save(session.TunnelStoreData{
		Version: 1,
		Tunnels: []session.Tunnel{{
			ID: "t1", Name: "jump",
			Upstream: &session.SocksProxy{Kind: "socks5", Host: "127.0.0.1", Port: 1081, Pass: "tunnel-pass"},
		}},
	}); err != nil {
		t.Fatalf("seed tunnels.json: %v", err)
	}
}

// TestReencryptAllCoversAIAndTunnels locks in the re-key fix: after a mode
// switch or master-password change, ai.json and tunnels.json must be readable
// under the new key. The old chain skipped both files, so Load() failed with a
// decrypt error forever — including tunnel auto-start.
func TestReencryptAllCoversAIAndTunnels(t *testing.T) {
	a, cs, _ := newCredentialTestApp(t)
	seedCredentialTestApp(t, a)

	oldKey := append([]byte(nil), cs.Key()...)
	newKey, err := randomKey32()
	if err != nil {
		t.Fatalf("randomKey32: %v", err)
	}

	if err := a.reencryptAll(cs, oldKey, newKey); err != nil {
		t.Fatalf("reencryptAll: %v", err)
	}
	if !bytes.Equal(cs.Key(), newKey) {
		t.Fatal("credential store key was not swapped to the new key")
	}

	aiCfg, err := a.aiConfigStore.Load()
	if err != nil {
		t.Fatalf("ai.json unreadable after re-key (skipped by reencryptAll?): %v", err)
	}
	if len(aiCfg.Models) != 1 || aiCfg.Models[0].APIKey != "sk-ai" {
		t.Fatalf("ai.json api key mismatch after re-key: %+v", aiCfg.Models)
	}

	tun, err := a.tunnelStore.Load()
	if err != nil {
		t.Fatalf("tunnels.json unreadable after re-key (skipped by reencryptAll?): %v", err)
	}
	if len(tun.Tunnels) != 1 || tun.Tunnels[0].Upstream == nil || tun.Tunnels[0].Upstream.Pass != "tunnel-pass" {
		t.Fatalf("tunnels.json upstream password mismatch after re-key: %+v", tun.Tunnels)
	}

	// The remaining stores must survive the swap too.
	conns, err := a.connectionStore.Load()
	if err != nil {
		t.Fatalf("connections.json unreadable after re-key: %v", err)
	}
	if len(conns.Connections) != 1 || conns.Connections[0].Password != "conn-pass" {
		t.Fatalf("connection password mismatch after re-key: %+v", conns.Connections)
	}
	st, err := a.settingsStore.Load()
	if err != nil {
		t.Fatalf("settings.json unreadable after re-key: %v", err)
	}
	if len(st.AI.Models) != 1 || st.AI.Models[0].APIKey != "sk-settings" {
		t.Fatalf("settings apiKey mismatch after re-key: %+v", st.AI.Models)
	}
	idents, err := a.identityStore.Load()
	if err != nil {
		t.Fatalf("identities.json unreadable after re-key: %v", err)
	}
	if len(idents.Identities) != 1 || idents.Identities[0].Password != "identity-pass" {
		t.Fatalf("identity password mismatch after re-key: %+v", idents.Identities)
	}
	proxies, err := a.proxyStore.Load()
	if err != nil {
		t.Fatalf("proxies.json unreadable after re-key: %v", err)
	}
	if len(proxies.Proxies) != 1 || proxies.Proxies[0].Pass != "proxy-pass" {
		t.Fatalf("proxy password mismatch after re-key: %+v", proxies.Proxies)
	}

	// Everything stays encrypted at rest under the new key.
	for _, name := range []string{"ai.json", "tunnels.json", "connections.json"} {
		raw, err := os.ReadFile(filepath.Join(a.dataDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Contains(raw, []byte(credentials.Prefix)) {
			t.Fatalf("%s lost its ciphertext after re-key", name)
		}
		if bytes.Contains(raw, []byte("sk-ai")) || bytes.Contains(raw, []byte("conn-pass")) ||
			bytes.Contains(raw, []byte("tunnel-pass")) {
			t.Fatalf("%s written in plaintext after re-key", name)
		}
	}
}

// resetFixtureFiles is the independent checklist ResetCredentials must cover:
// every file the stores encrypt, and every field inside them.
var resetFixtureFiles = map[string]string{
	"connections.json": `{"connections":[
		{"id":"c1","authType":"password","password":"enc:v1:nonce:blob","keyContent":"enc:v1:nonce:blob",
		 "sentinelPassword":"enc:v1:nonce:blob","tunnelSSHPassword":"enc:v1:nonce:blob","k8sConfigInline":"enc:v1:nonce:blob"},
		{"id":"c2","authType":"password","password":"plaintext-password"}]}`,
	"identities.json": `{"identities":[{"id":"i1","password":"enc:v1:nonce:blob","keyContent":"enc:v1:nonce:blob"}]}`,
	"proxies.json":    `{"proxies":[{"id":"p1","pass":"enc:v1:nonce:blob"}]}`,
	"settings.json":   `{"ai":{"models":[{"id":"m1","apiKey":"enc:v1:nonce:blob"}]}}`,
	"ai.json":         `{"models":[{"id":"m2","apiKey":"enc:v1:nonce:blob"}]}`,
	"tunnels.json":    `{"tunnels":[{"id":"t1","upstream":{"pass":"enc:v1:nonce:blob"}}]}`,
}

// TestResetCredentialsLeavesNoCiphertext: after a reset the vault must be
// clean — otherwise Setup refuses in both modes (the UI is stuck behind
// "existing encrypted data") and ConnectionStore.Load still chokes on the
// leftovers.
func TestResetCredentialsLeavesNoCiphertext(t *testing.T) {
	dir := t.TempDir()
	for name, body := range resetFixtureFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	cs := credentials.New(dir, newCredTestKeychain())
	key, err := randomKey32()
	if err != nil {
		t.Fatalf("randomKey32: %v", err)
	}
	cs.SetKey(key)
	a := &App{dataDir: dir, credentialStore: cs}

	if err := a.ResetCredentials(); err != nil {
		t.Fatalf("ResetCredentials: %v", err)
	}

	for _, name := range []string{"connections.json", "settings.json", "identities.json", "proxies.json", "ai.json", "tunnels.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(raw), credentials.Prefix) {
			t.Errorf("%s still contains ciphertext after reset", name)
		}
	}

	// Named plaintext secrets are blanked too, not just enc:v1: values.
	raw, err := os.ReadFile(filepath.Join(dir, "connections.json"))
	if err != nil {
		t.Fatalf("read connections.json: %v", err)
	}
	for _, leftover := range []string{"plaintext-password"} {
		if strings.Contains(string(raw), leftover) {
			t.Errorf("connections.json still holds %q after reset", leftover)
		}
	}
	for _, field := range []string{"keyContent", "sentinelPassword", "tunnelSSHPassword", "k8sConfigInline"} {
		if !strings.Contains(string(raw), `"`+field+`": ""`) {
			t.Errorf("connections.json field %q was not cleared by reset", field)
		}
	}

	// The whole point: Setup must no longer be blocked by leftovers.
	if err := cs.Setup(credentials.ModeMasterPassword, "fresh-password"); err != nil {
		t.Fatalf("Setup after reset must not be blocked by leftover secrets: %v", err)
	}
}
