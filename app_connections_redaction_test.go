package main

// Regression tests for the LoadConnections secret redaction (security.md,
// "LoadConnections sends every decrypted secret to the webview"):
//   - the bulk list ships no plaintext and no enc:v1 ciphertext, only flags;
//   - a load→edit→save round-trip keeps stored secrets ("leave blank to
//     keep");
//   - secrets still resolve Go-side by connection ID (fillStoredSecrets for
//     CreateSession/TestConnection, ResolveConnectionSecret for the few
//     renderer features that need the plaintext).

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/credentials"
	"github.com/ys-ll/uniterm/backend/session"
	"github.com/ys-ll/uniterm/backend/store"
)

// newRedactionTestApp wires an App with an encrypted connection store seeded
// with one connection per secret-bearing auth flavour.
func newRedactionTestApp(t *testing.T) *App {
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
	conn.SetPasswordStore(cs)
	return &App{dataDir: dir, credentialStore: cs, connectionStore: conn}
}

func seedConnections(t *testing.T, a *App, conns []session.ConnectionConfig) {
	t.Helper()
	if err := a.connectionStore.Save(session.ConnectionStoreData{
		Groups:      []session.ConnectionGroup{{ID: "g1", Name: "prod"}},
		Connections: conns,
	}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
}

func TestLoadConnectionsRedactsSecrets(t *testing.T) {
	a := newRedactionTestApp(t)
	seedConnections(t, a, []session.ConnectionConfig{
		{ID: "c-pw", Name: "pw", Type: "ssh", Host: "h1", Port: 22, User: "u", AuthType: "password", Password: "pw-secret"},
		{ID: "c-key", Name: "key", Type: "ssh", Host: "h2", Port: 22, User: "u", AuthType: "keyText", KeyContent: "-----BEGIN key-----", Password: "passphrase-secret"},
		{ID: "c-tun", Name: "tun", Type: "ssh", Host: "h3", Port: 22, User: "u", AuthType: "password", Password: "x", TunnelSSHPassword: "tunnel-secret"},
		{ID: "c-k8s", Name: "k8s", Type: "k8s", AuthType: "password", K8sConfigInline: "apiVersion: v1\nkind: Secret", K8sNamespace: "default"},
		{ID: "c-sen", Name: "redis", Type: "database", DBType: "redis", AuthType: "password", SentinelPassword: "sentinel-secret"},
	})

	out, err := a.LoadConnections()
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}

	// The wire payload must carry neither plaintext secrets nor ciphertext.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{
		"pw-secret", "passphrase-secret", "tunnel-secret", "sentinel-secret",
		"apiVersion: v1", "enc:v1:", "BEGIN key",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("LoadConnections payload leaked %q: %s", secret, raw)
		}
	}

	byID := map[string]RedactedConnection{}
	for _, c := range out.Connections {
		byID[c.ID] = c
	}
	if len(out.Groups) != 1 || out.Groups[0].ID != "g1" {
		t.Fatalf("groups not preserved: %+v", out.Groups)
	}

	cases := []struct {
		id                                      string
		hasPassword, hasKey, hasTunnel, hasSent bool
		hasSentinel, hasK8s                     bool
	}{
		{id: "c-pw", hasPassword: true},
		{id: "c-key", hasPassword: true, hasKey: true},
		{id: "c-tun", hasPassword: true, hasTunnel: true},
		{id: "c-k8s", hasK8s: true},
		{id: "c-sen", hasSentinel: true},
	}
	for _, tc := range cases {
		c, ok := byID[tc.id]
		if !ok {
			t.Fatalf("connection %s missing from output", tc.id)
		}
		if c.Password != "" || c.KeyContent != "" || c.TunnelSSHPassword != "" ||
			c.SentinelPassword != "" || c.K8sConfigInline != "" {
			t.Fatalf("%s: secret field not blanked: %+v", tc.id, c.ConnectionConfig)
		}
		if c.HasPassword != tc.hasPassword || c.HasKeyContent != tc.hasKey ||
			c.HasTunnelSSHPassword != tc.hasTunnel || c.HasSentinelPassword != tc.hasSentinel ||
			c.HasK8sConfigInline != tc.hasK8s {
			t.Fatalf("%s: wrong flags: pw=%v key=%v tun=%v sen=%v k8s=%v", tc.id,
				c.HasPassword, c.HasKeyContent, c.HasTunnelSSHPassword, c.HasSentinelPassword, c.HasK8sConfigInline)
		}
	}
	// Non-secret fields stay intact for the UI.
	if byID["c-pw"].Host != "h1" || byID["c-pw"].User != "u" || byID["c-k8s"].K8sNamespace != "default" {
		t.Fatalf("non-secret fields damaged: %+v %+v", byID["c-pw"], byID["c-k8s"])
	}
}

func TestSaveConnectionsKeepsRedactedSecrets(t *testing.T) {
	a := newRedactionTestApp(t)
	seedConnections(t, a, []session.ConnectionConfig{
		{ID: "c-pw", Name: "pw", Type: "ssh", Host: "h1", Port: 22, User: "u", AuthType: "password", Password: "keep-me"},
		{ID: "c-key", Name: "key", Type: "ssh", Host: "h2", Port: 22, User: "u", AuthType: "keyText", KeyContent: "keep-key-text", Password: "keep-passphrase"},
		{ID: "c-k8s", Name: "k8s", Type: "k8s", AuthType: "password", K8sConfigInline: "keep-kubeconfig"},
	})

	loaded, err := a.LoadConnections()
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}
	// Simulate the frontend edit: rename one connection, blank secrets stay
	// blank with their flags (leave-blank-to-keep).
	for i := range loaded.Connections {
		loaded.Connections[i].Name = loaded.Connections[i].Name + "-renamed"
	}
	// And change one password on purpose.
	for i := range loaded.Connections {
		if loaded.Connections[i].ID == "c-pw" {
			loaded.Connections[i].Password = "rotated"
		}
	}
	if err := a.SaveConnections(loaded); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}

	stored, err := a.connectionStore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	byID := map[string]session.ConnectionConfig{}
	for _, c := range stored.Connections {
		byID[c.ID] = c
	}
	if byID["c-pw"].Password != "rotated" {
		t.Fatalf("explicit new password not saved: %q", byID["c-pw"].Password)
	}
	if byID["c-key"].KeyContent != "keep-key-text" || byID["c-key"].Password != "keep-passphrase" {
		t.Fatalf("blanked keyText secrets wiped: %+v", byID["c-key"])
	}
	if byID["c-k8s"].K8sConfigInline != "keep-kubeconfig" {
		t.Fatalf("blanked kubeconfig wiped: %q", byID["c-k8s"].K8sConfigInline)
	}
	if byID["c-pw"].Name != "pw-renamed" {
		t.Fatalf("rename lost: %q", byID["c-pw"].Name)
	}

	// CreateSession's credential path still resolves after redaction: the
	// store cache (filled by populatePasswords during Load) serves
	// EnsurePassword, and the inline "keep-me" password round-tripped.
	if pw, err := a.connectionStore.EnsurePassword("c-pw"); err != nil || pw != "rotated" {
		t.Fatalf("EnsurePassword after round-trip = %q, %v; want rotated", pw, err)
	}
	// Key-text secrets are not in the password cache (password auth only);
	// CreateSession/TestConnection get them through fillStoredSecrets.
	cfg := session.ConnectionConfig{ID: "c-key", AuthType: "keyText"}
	a.fillStoredSecrets(&cfg)
	if cfg.Password != "keep-passphrase" || cfg.KeyContent != "keep-key-text" {
		t.Fatalf("fillStoredSecrets after round-trip = %q / %q", cfg.Password, cfg.KeyContent)
	}
}

func TestResolveConnectionSecret(t *testing.T) {
	a := newRedactionTestApp(t)
	seedConnections(t, a, []session.ConnectionConfig{
		{ID: "c1", Name: "one", Type: "ssh", Host: "h", Port: 22, User: "u", AuthType: "password", Password: "p1", TunnelSSHPassword: "t1"},
	})

	sec, err := a.ResolveConnectionSecret("c1")
	if err != nil {
		t.Fatalf("ResolveConnectionSecret: %v", err)
	}
	if sec.Password != "p1" || sec.TunnelSSHPassword != "t1" {
		t.Fatalf("wrong secret: %+v", sec)
	}
	if _, err := a.ResolveConnectionSecret("missing"); err == nil {
		t.Fatal("ResolveConnectionSecret(missing) should error")
	}
}

func TestFillStoredSecrets(t *testing.T) {
	a := newRedactionTestApp(t)
	seedConnections(t, a, []session.ConnectionConfig{
		{ID: "c-pw", Name: "pw", Type: "ssh", Host: "h", Port: 22, User: "u", AuthType: "password", Password: "login-pw"},
		{ID: "c-key", Name: "key", Type: "ssh", Host: "h", Port: 22, User: "u", AuthType: "key", Password: "key-passphrase"},
		{ID: "c-keytext", Name: "keytext", Type: "ssh", Host: "h", Port: 22, User: "u", AuthType: "keyText", KeyContent: "pem-inline", Password: "key-passphrase"},
		{ID: "c-sen", Name: "redis", Type: "database", DBType: "redis", AuthType: "password", Password: "login-pw", SentinelPassword: "sen-pw"},
		{ID: "c-api", Name: "es", Type: "elasticsearch", AuthType: "apikey", Password: "es-api-key"},
	})

	cases := []struct {
		name   string
		in     session.ConnectionConfig
		expect func(t *testing.T, c session.ConnectionConfig)
	}{
		{
			name: "password auth resolves login password",
			in:   session.ConnectionConfig{ID: "c-pw", AuthType: "password"},
			expect: func(t *testing.T, c session.ConnectionConfig) {
				if c.Password != "login-pw" {
					t.Fatalf("Password = %q", c.Password)
				}
			},
		},
		{
			name: "inline password wins over stored",
			in:   session.ConnectionConfig{ID: "c-pw", AuthType: "password", Password: "from-dialog"},
			expect: func(t *testing.T, c session.ConnectionConfig) {
				if c.Password != "from-dialog" {
					t.Fatalf("inline password overwritten: %q", c.Password)
				}
			},
		},
		{
			name: "key auth resolves the key passphrase, never the login-password cache",
			in:   session.ConnectionConfig{ID: "c-key", AuthType: "key"},
			expect: func(t *testing.T, c session.ConnectionConfig) {
				if c.Password != "key-passphrase" {
					t.Fatalf("passphrase = %q", c.Password)
				}
			},
		},
		{
			name: "keyText resolves passphrase and inline key",
			in:   session.ConnectionConfig{ID: "c-keytext", AuthType: "keyText"},
			expect: func(t *testing.T, c session.ConnectionConfig) {
				if c.Password != "key-passphrase" || c.KeyContent != "pem-inline" {
					t.Fatalf("keyText secrets = %q / %q", c.Password, c.KeyContent)
				}
			},
		},
		{
			name: "sentinel secret resolves for redis",
			in:   session.ConnectionConfig{ID: "c-sen", AuthType: "password"},
			expect: func(t *testing.T, c session.ConnectionConfig) {
				if c.SentinelPassword != "sen-pw" {
					t.Fatalf("SentinelPassword = %q", c.SentinelPassword)
				}
			},
		},
		{
			name: "apikey resolves the stored API key",
			in:   session.ConnectionConfig{ID: "c-api", AuthType: "apikey"},
			expect: func(t *testing.T, c session.ConnectionConfig) {
				if c.Password != "es-api-key" {
					t.Fatalf("Password = %q", c.Password)
				}
			},
		},
		{
			name: "auth-mode switch never transfers the old mode's secret",
			in:   session.ConnectionConfig{ID: "c-api", AuthType: "password"},
			expect: func(t *testing.T, c session.ConnectionConfig) {
				if c.Password != "" {
					t.Fatalf("apikey secret leaked into password auth: %q", c.Password)
				}
			},
		},
		{
			name: "unknown id leaves config untouched",
			in:   session.ConnectionConfig{ID: "nope", AuthType: "password"},
			expect: func(t *testing.T, c session.ConnectionConfig) {
				if c.Password != "" {
					t.Fatalf("Password = %q", c.Password)
				}
			},
		},
		{
			name: "empty id is a no-op",
			in:   session.ConnectionConfig{AuthType: "password"},
			expect: func(t *testing.T, c session.ConnectionConfig) {
				if c.Password != "" {
					t.Fatalf("Password = %q", c.Password)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.in
			a.fillStoredSecrets(&cfg)
			tc.expect(t, cfg)
		})
	}
}

func TestGetBackgroundImageRejectsTraversal(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	for _, name := range []string{
		"../evil.png", "..\\evil.png", "sub/evil.png", "sub\\evil.png",
		filepath.Join("a", "..", "..", "b.png"),
	} {
		if _, err := a.GetBackgroundImage(name); err == nil {
			t.Fatalf("GetBackgroundImage(%q) accepted a traversal name", name)
		}
	}
	// Missing-but-valid name degrades gracefully (empty, no error).
	if got, err := a.GetBackgroundImage("bg.png"); err != nil || got != "" {
		t.Fatalf("GetBackgroundImage(bg.png) = %q, %v; want empty, nil", got, err)
	}
}
