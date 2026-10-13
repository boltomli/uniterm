package main

// Regression tests for the LoadIdentities/LoadProxies secret redaction
// (security.md, "Go 根包 High" residual):
//   - the identity/proxy lists ship no plaintext password / key text and no
//     enc:v1: ciphertext, only presence flags (hasPassword/HasKeyContent,
//     hasPass);
//   - a load→edit→save round-trip keeps stored secrets ("leave blank to
//     keep");
//   - secrets still resolve Go-side by ID through ResolveIdentitySecret /
//     ResolveProxySecret.
//
// The assertions are written against the JSON wire shape so they compile and
// FAIL against the old plaintext-returning LoadIdentities/LoadProxies, then
// pass once the redacted types land.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/credentials"
	"github.com/ys-ll/uniterm/backend/session"
	"github.com/ys-ll/uniterm/backend/store"
)

// newIdentityProxyTestApp wires an App with encrypted identity and proxy
// stores seeded via their own Save paths (so the on-disk form is enc:v1:).
func newIdentityProxyTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cs := credentials.New(dir, newCredTestKeychain())
	key, err := randomKey32()
	if err != nil {
		t.Fatalf("randomKey32: %v", err)
	}
	cs.SetKey(key)
	idents, err := store.NewIdentityStore(dir)
	if err != nil {
		t.Fatalf("NewIdentityStore: %v", err)
	}
	idents.SetPasswordStore(cs)
	proxies, err := store.NewProxyStore(dir)
	if err != nil {
		t.Fatalf("NewProxyStore: %v", err)
	}
	proxies.SetPasswordStore(cs)
	return &App{dataDir: dir, credentialStore: cs, identityStore: idents, proxyStore: proxies}
}

func seedIdentities(t *testing.T, a *App, ids ...session.Identity) {
	t.Helper()
	if err := a.identityStore.Save(session.IdentityStoreData{Identities: ids}); err != nil {
		t.Fatalf("seed identities: %v", err)
	}
}

func seedProxies(t *testing.T, a *App, ps ...session.Proxy) {
	t.Helper()
	if err := a.proxyStore.Save(session.ProxyStoreData{Proxies: ps}); err != nil {
		t.Fatalf("seed proxies: %v", err)
	}
}

// rawJSON marshals any Load* result for wire-shape assertions.
func rawJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestLoadIdentitiesRedactsSecrets(t *testing.T) {
	a := newIdentityProxyTestApp(t)
	seedIdentities(t, a,
		session.Identity{ID: "i-pw", Name: "prod", Username: "root", AuthType: "password", Password: "identity-secret"},
		session.Identity{ID: "i-key", Name: "key", Username: "root", AuthType: "key", KeyPath: "/home/u/.ssh/id_ed25519", Password: "key-passphrase"},
		session.Identity{ID: "i-keytext", Name: "kt", Username: "root", AuthType: "keyText", KeyContent: "-----BEGIN key-----", Password: "keytext-passphrase"},
	)

	out, err := a.LoadIdentities()
	if err != nil {
		t.Fatalf("LoadIdentities: %v", err)
	}

	raw := rawJSON(t, out)
	for _, secret := range []string{
		"identity-secret", "key-passphrase", "keytext-passphrase",
		"BEGIN key", "enc:v1:",
	} {
		if strings.Contains(raw, secret) {
			t.Fatalf("LoadIdentities payload leaked %q: %s", secret, raw)
		}
	}

	// Flags must tell the edit dialog which secrets are stored. They ride on
	// the JSON payload (the redacted types add them).
	for _, want := range []string{
		`"id":"i-pw"`, `"hasPassword":true`,
		`"id":"i-key"`,
		`"id":"i-keytext"`, `"hasKeyContent":true`,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("LoadIdentities payload missing %s: %s", want, raw)
		}
	}

	// Non-secret fields stay intact for the UI.
	byID := map[string]map[string]any{}
	var wire struct {
		Identities []map[string]any `json:"identities"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	for _, id := range wire.Identities {
		byID[id["id"].(string)] = id
	}
	if byID["i-pw"]["username"] != "root" || byID["i-key"]["keyPath"] != "/home/u/.ssh/id_ed25519" {
		t.Fatalf("non-secret fields damaged: %+v", byID)
	}
	if _, leaked := byID["i-pw"]["password"]; leaked {
		t.Fatalf("password field still present for i-pw: %+v", byID["i-pw"])
	}
}

func TestSaveIdentitiesKeepsRedactedSecrets(t *testing.T) {
	a := newIdentityProxyTestApp(t)
	seedIdentities(t, a,
		session.Identity{ID: "i-pw", Name: "prod", Username: "root", AuthType: "password", Password: "keep-me"},
		session.Identity{ID: "i-keytext", Name: "kt", Username: "root", AuthType: "keyText", KeyContent: "keep-key-text", Password: "keep-passphrase"},
	)

	loaded, err := a.LoadIdentities()
	if err != nil {
		t.Fatalf("LoadIdentities: %v", err)
	}
	// Simulate the frontend edit: rename, secrets stay blank with their flags
	// (leave-blank-to-keep). One password is rotated on purpose.
	if err := rewriteJSONSlice(&loaded, "identities", func(m map[string]any) {
		m["name"] = m["name"].(string) + "-renamed"
		if m["id"] == "i-pw" {
			m["password"] = "rotated"
		}
	}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := a.SaveIdentities(loaded); err != nil {
		t.Fatalf("SaveIdentities: %v", err)
	}

	stored, err := a.identityStore.Load()
	if err != nil {
		t.Fatalf("identityStore.Load: %v", err)
	}
	byID := map[string]session.Identity{}
	for _, id := range stored.Identities {
		byID[id.ID] = id
	}
	if byID["i-pw"].Password != "rotated" {
		t.Fatalf("explicit new password not saved: %q", byID["i-pw"].Password)
	}
	if byID["i-keytext"].KeyContent != "keep-key-text" || byID["i-keytext"].Password != "keep-passphrase" {
		t.Fatalf("blanked keyText secrets wiped: %+v", byID["i-keytext"])
	}
	if byID["i-pw"].Name != "prod-renamed" {
		t.Fatalf("rename lost: %q", byID["i-pw"].Name)
	}
}

func TestResolveIdentitySecret(t *testing.T) {
	a := newIdentityProxyTestApp(t)
	seedIdentities(t, a,
		session.Identity{ID: "i1", Name: "prod", Username: "root", AuthType: "keyText", KeyContent: "pem-material", Password: "passphrase-1"},
	)

	sec, err := a.ResolveIdentitySecret("i1")
	if err != nil {
		t.Fatalf("ResolveIdentitySecret: %v", err)
	}
	got := rawJSON(t, sec)
	if !strings.Contains(got, "pem-material") || !strings.Contains(got, "passphrase-1") {
		t.Fatalf("wrong secret: %s", got)
	}
	if _, err := a.ResolveIdentitySecret("missing"); err == nil {
		t.Fatal("ResolveIdentitySecret(missing) should error")
	}
}

func TestLoadProxiesRedactsPass(t *testing.T) {
	a := newIdentityProxyTestApp(t)
	seedProxies(t, a,
		session.Proxy{ID: "p1", Name: "vpn", Kind: "socks5", Host: "127.0.0.1", Port: 1080, User: "u", Pass: "proxy-secret"},
		session.Proxy{ID: "p2", Name: "bare", Kind: "http", Host: "10.0.0.1", Port: 8080},
	)

	out, err := a.LoadProxies()
	if err != nil {
		t.Fatalf("LoadProxies: %v", err)
	}

	raw := rawJSON(t, out)
	for _, secret := range []string{"proxy-secret", "enc:v1:"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("LoadProxies payload leaked %q: %s", secret, raw)
		}
	}
	if !strings.Contains(raw, `"id":"p1"`) || !strings.Contains(raw, `"hasPass":true`) {
		t.Fatalf("hasPass flag missing: %s", raw)
	}
	if !strings.Contains(raw, `"id":"p2"`) || !strings.Contains(raw, `"hasPass":false`) {
		t.Fatalf("hasPass=false flag missing for passwordless proxy: %s", raw)
	}
	if !strings.Contains(raw, `"host":"127.0.0.1"`) || !strings.Contains(raw, `"user":"u"`) {
		t.Fatalf("non-secret fields damaged: %s", raw)
	}
	var wire struct {
		Proxies []map[string]any `json:"proxies"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	for _, p := range wire.Proxies {
		if _, leaked := p["pass"]; leaked {
			t.Fatalf("pass field still present for %v: %+v", p["id"], p)
		}
	}
}

func TestSaveProxiesKeepsRedactedPass(t *testing.T) {
	a := newIdentityProxyTestApp(t)
	seedProxies(t, a,
		session.Proxy{ID: "p1", Name: "vpn", Kind: "socks5", Host: "127.0.0.1", Port: 1080, Pass: "keep-me"},
	)

	loaded, err := a.LoadProxies()
	if err != nil {
		t.Fatalf("LoadProxies: %v", err)
	}
	if err := rewriteJSONSlice(&loaded, "proxies", func(m map[string]any) {
		m["name"] = "vpn-renamed"
		m["port"] = 1081.0
	}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := a.SaveProxies(loaded); err != nil {
		t.Fatalf("SaveProxies: %v", err)
	}

	stored, err := a.proxyStore.Load()
	if err != nil {
		t.Fatalf("proxyStore.Load: %v", err)
	}
	if len(stored.Proxies) != 1 || stored.Proxies[0].Pass != "keep-me" {
		t.Fatalf("blanked pass wiped: %+v", stored.Proxies)
	}
	if stored.Proxies[0].Name != "vpn-renamed" || stored.Proxies[0].Port != 1081 {
		t.Fatalf("non-secret edit lost: %+v", stored.Proxies[0])
	}

	// Override semantics: a typed pass replaces the stored one.
	loaded2, err := a.LoadProxies()
	if err != nil {
		t.Fatalf("LoadProxies: %v", err)
	}
	if err := rewriteJSONSlice(&loaded2, "proxies", func(m map[string]any) {
		m["pass"] = "rotated"
	}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := a.SaveProxies(loaded2); err != nil {
		t.Fatalf("SaveProxies: %v", err)
	}
	stored2, err := a.proxyStore.Load()
	if err != nil {
		t.Fatalf("proxyStore.Load: %v", err)
	}
	if stored2.Proxies[0].Pass != "rotated" {
		t.Fatalf("explicit new pass not saved: %q", stored2.Proxies[0].Pass)
	}
}

func TestResolveProxySecret(t *testing.T) {
	a := newIdentityProxyTestApp(t)
	seedProxies(t, a,
		session.Proxy{ID: "p1", Name: "vpn", Kind: "socks5", Host: "127.0.0.1", Port: 1080, Pass: "proxy-secret"},
	)

	sec, err := a.ResolveProxySecret("p1")
	if err != nil {
		t.Fatalf("ResolveProxySecret: %v", err)
	}
	if got := rawJSON(t, sec); !strings.Contains(got, "proxy-secret") {
		t.Fatalf("wrong secret: %s", got)
	}
	if _, err := a.ResolveProxySecret("missing"); err == nil {
		t.Fatal("ResolveProxySecret(missing) should error")
	}
}

// TestIdentityProxyAppDataDirNotLeaked keeps the helper honest: the test app
// writes into a temp dir, never the user's real data dir.
func TestIdentityProxyAppDataDirNotLeaked(t *testing.T) {
	a := newIdentityProxyTestApp(t)
	if a.dataDir == "" || filepath.Base(a.dataDir) == "uniTerm" {
		t.Fatalf("suspicious dataDir: %q", a.dataDir)
	}
}

// rewriteJSONSlice mutates a slice-of-objects field inside a marshalled
// Load* result, simulating a frontend edit on the redacted payload. It goes
// through map[string]any so the test keeps compiling against either the old
// (plaintext) or the new (redacted) payload types.
func rewriteJSONSlice(payload any, field string, mutate func(map[string]any)) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		return err
	}
	items, _ := top[field].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			mutate(m)
		}
	}
	top[field] = items
	encoded, err := json.Marshal(top)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, payload)
}
