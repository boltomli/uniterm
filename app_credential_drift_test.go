package main

// Regression tests for security.md Medium findings on credential-resolution
// drift: every connect path must resolve secrets through the SAME guarded
// helper (fillStoredSecrets — auth-type matched, password-auth-only keychain
// fallback), so a key/keyText connection never picks up a stored LOGIN
// password as its key passphrase (which breaks unencrypted keys), and every
// path that dials must honor the configured proxy and jump host.
//
// The paths under test build real sessions/dialers, so the wiring is pinned
// on the source (same pattern as backend/session/rdp_security_test.go and
// sftp_scp_pin_test.go), while the shared helper's guard is pinned
// behaviourally in TestFillStoredSecretsGuardedKeychainFallback below.

import (
	"os"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

// funcBody extracts the body of a top-level function declaration by name so
// assertions only see one connect path, not the whole file.
func funcBody(t *testing.T, file, name string) string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	s := string(src)
	start := strings.Index(s, "func "+name+"(")
	if start < 0 {
		t.Fatalf("%s: %s not found", file, name)
	}
	open := strings.Index(s[start:], "{")
	if open < 0 {
		t.Fatalf("%s: body of %s not found", file, name)
	}
	depth := 0
	for i := start + open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	t.Fatalf("%s: unbalanced braces in %s", file, name)
	return ""
}

// TestConnectPathsUseSharedSecretResolver: ContainerConnect's ad-hoc
// `EnsurePassword` fallback was unguarded — for key/keyText auth it filled
// the stored login password into the passphrase slot. X11DesktopConnect never
// resolved the configured proxy (cfg.Proxy stayed nil, so DialSSHClient
// dialed direct) nor the jump host. MCP connect had the same unguarded
// password fill. All of them must go through fillStoredSecrets; X11 and MCP
// must additionally wire materializeProxy/setupJumpHostTunnel.
func TestConnectPathsUseSharedSecretResolver(t *testing.T) {
	container := funcBody(t, "app_container.go", "(a *App) ContainerConnect")
	if strings.Contains(container, "EnsurePassword(") {
		t.Errorf("ContainerConnect must resolve secrets via fillStoredSecrets, not an unguarded EnsurePassword fallback")
	}
	if !strings.Contains(container, "fillStoredSecrets(") {
		t.Errorf("ContainerConnect must call fillStoredSecrets (guarded secret resolution)")
	}
	if !strings.Contains(container, "materializeProxy(") {
		t.Errorf("ContainerConnect must materialize the referenced proxy before dialing")
	}

	// mcpConnect now delegates to the shared mcpOpenTerminal path, which
	// creates the session through CreateSession — the single hardened entry
	// point (fillStoredSecrets + identity/proxy materialization + jump-host
	// tunnel). Never an ad-hoc password fill.
	mcp := funcBody(t, "app_mcp.go", "(a *App) mcpConnect")
	if strings.Contains(mcp, "EnsurePassword(") {
		t.Errorf("mcpConnect must not fall back to an unguarded EnsurePassword fill")
	}
	if !strings.Contains(mcp, "mcpOpenTerminal(") {
		t.Errorf("mcpConnect must delegate to mcpOpenTerminal")
	}
	openTerminal := funcBody(t, "app_mcp.go", "(a *App) mcpOpenTerminal")
	if strings.Contains(openTerminal, "EnsurePassword(") {
		t.Errorf("mcpOpenTerminal must not fall back to an unguarded EnsurePassword fill")
	}
	if !strings.Contains(openTerminal, "CreateSession(") {
		t.Errorf("mcpOpenTerminal must create the session through CreateSession (shared credential/tunnel path)")
	}
	createSession := funcBody(t, "app_terminal.go", "(a *App) CreateSession")
	for _, want := range []string{"fillStoredSecrets(", "materializeProxy(", "setupJumpHostTunnel("} {
		if !strings.Contains(createSession, want) {
			t.Errorf("CreateSession must call %s (shared hardened connect path)", want)
		}
	}

	// SessionStart owns the actual dial for terminal types (ssh/local/telnet/
	// mosh/serial): CreateSession defers Connect until the frontend measured
	// the real cols/rows, and the config arriving here is a FRESH redacted
	// one from the webview. CreateSession's fill covers only its own discarded
	// copy — without a fill here a saved-list connect / tab restore /
	// reconnect dials with an empty password (SSH then prompts in the
	// terminal for a secret that is already stored). Regression introduced
	// by the EnsurePassword removal (19747633) and reported as "SSH 保存的
	// 密码无用".
	sessionStart := funcBody(t, "app_terminal.go", "(a *App) SessionStart")
	if strings.Contains(sessionStart, "EnsurePassword(") {
		t.Errorf("SessionStart must resolve secrets via fillStoredSecrets, not an unguarded EnsurePassword fallback")
	}
	if !strings.Contains(sessionStart, "fillStoredSecrets(") {
		t.Errorf("SessionStart must call fillStoredSecrets (terminal types dial here with a fresh redacted config)")
	}

	// The companion file-transfer session (SFTP/SCP for MCP file tools) used
	// to rely on the caller's config being materialized; channel clones pass
	// a redacted frontend config, so it must resolve through the same guarded
	// helper — never a raw password fill.
	fileSession := funcBody(t, "app_mcp.go", "(a *App) mcpCreateFileSession")
	if strings.Contains(fileSession, "EnsurePassword(") {
		t.Errorf("mcpCreateFileSession must resolve secrets via fillStoredSecrets, not an unguarded EnsurePassword fallback")
	}
	if !strings.Contains(fileSession, "fillStoredSecrets(") {
		t.Errorf("mcpCreateFileSession must call fillStoredSecrets (guarded secret resolution)")
	}
}

// TestX11DesktopConnectResolvesProxyAndTunnel: X11DesktopConnect dials through
// DialSSHClient, which honors config.Proxy and the tunnel-rewritten
// Host/Port — but only if the app fills them in first.
func TestX11DesktopConnectResolvesProxyAndTunnel(t *testing.T) {
	body := funcBody(t, "app_remote_desktop.go", "(a *App) X11DesktopConnect")
	if !strings.Contains(body, "materializeProxy(") {
		t.Errorf("X11DesktopConnect must materialize the referenced proxy (otherwise cfg.Proxy is nil and the dial bypasses the proxy)")
	}
	if !strings.Contains(body, "setupJumpHostTunnel(") {
		t.Errorf("X11DesktopConnect must set up the jump-host tunnel before dialing")
	}
	if !strings.Contains(body, "tunnelService.Stop(") {
		t.Errorf("X11DesktopConnect must stop the jump-host tunnel when the connect fails")
	}
}

// TestTestConnectionUsesSharedResolver keeps the earlier fix honest:
// TestConnection resolves through fillStoredSecrets, not a raw password fill.
func TestTestConnectionUsesSharedResolver(t *testing.T) {
	body := funcBody(t, "test_connection.go", "(a *App) TestConnection")
	if strings.Contains(body, "EnsurePassword(") {
		t.Errorf("TestConnection must resolve secrets via fillStoredSecrets, not an unguarded EnsurePassword fallback")
	}
	if !strings.Contains(body, "fillStoredSecrets(") {
		t.Errorf("TestConnection must call fillStoredSecrets")
	}
}

// TestFillStoredSecretsGuardedKeychainFallback pins the helper's guard
// behaviourally: the legacy keychain fallback (conn/<id>) must only back a
// password-AUTH connection's login password, never a key/keyText passphrase
// slot — an unencrypted key fails to load when a password is passed as its
// passphrase.
func TestFillStoredSecretsGuardedKeychainFallback(t *testing.T) {
	a := newRedactionTestApp(t)
	// A password-auth connection whose enc:v1 field is missing and whose
	// password only exists in the legacy keychain.
	if err := a.connectionStore.Save(session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{
			{ID: "c-pw", Name: "pw", Type: "ssh", Host: "h", Port: 22, User: "u", AuthType: "password"},
			{ID: "c-key", Name: "key", Type: "ssh", Host: "h", Port: 22, User: "u", AuthType: "key", KeyPath: "/tmp/id"},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a.connectionStore.SetLegacyKeychain(fakeTestLegacyKC{pw: map[string]string{
		"c-pw":  "legacy-login-pw",
		"c-key": "legacy-login-pw",
	}})

	pw := session.ConnectionConfig{ID: "c-pw", AuthType: "password"}
	a.fillStoredSecrets(&pw)
	if pw.Password != "legacy-login-pw" {
		t.Fatalf("password auth: legacy keychain fallback missing: %q", pw.Password)
	}

	key := session.ConnectionConfig{ID: "c-key", AuthType: "key"}
	a.fillStoredSecrets(&key)
	if key.Password != "" {
		t.Fatalf("key auth must not receive the stored login password as passphrase: %q", key.Password)
	}

	keyText := session.ConnectionConfig{ID: "c-key", AuthType: "keyText"}
	a.fillStoredSecrets(&keyText)
	if keyText.Password != "" {
		t.Fatalf("keyText auth must not receive the stored login password as passphrase: %q", keyText.Password)
	}
}

// fakeTestLegacyKC mirrors store.fakeLegacyKC for the root package.
type fakeTestLegacyKC struct{ pw map[string]string }

func (f fakeTestLegacyKC) GetPassword(id string) (string, error) { return f.pw[id], nil }
func (f fakeTestLegacyKC) GetModelAPIKey(string) (string, error) { return "", nil }
func (f fakeTestLegacyKC) DeletePassword(id string) error        { delete(f.pw, id); return nil }
