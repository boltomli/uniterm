package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

// TestTunnelStoreRoundTrip locks in that the upstream proxy Pass is encrypted
// at rest in tunnels.json (never written as plaintext) and comes back as
// plaintext on Load. The caller's data must be left untouched so SaveTunnels
// can still emit the plaintext snapshot to the frontend.
func TestTunnelStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewTunnelStore(dir)
	s.SetPasswordStore(fakePasswordStore{prefix: "enc:v1:"})

	in := session.TunnelStoreData{Version: 1, Tunnels: []session.Tunnel{{
		ID:       "t1",
		Name:     "jump",
		Mode:     session.TunnelLocal,
		Upstream: &session.SocksProxy{Kind: "socks5", Host: "127.0.0.1", Port: 1080, User: "u", Pass: "pw"},
	}}}
	if err := s.Save(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	if in.Tunnels[0].Upstream.Pass != "pw" {
		t.Fatalf("Save mutated the caller's data: pass = %q", in.Tunnels[0].Upstream.Pass)
	}

	raw, err := os.ReadFile(filepath.Join(dir, tunnelsFileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if contains(string(raw), `"pass": "pw"`) {
		t.Fatalf("pass was not encrypted on disk: %s", raw)
	}
	if !contains(string(raw), "enc:v1:") {
		t.Fatalf("expected encrypted pass on disk: %s", raw)
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(out.Tunnels) != 1 || out.Tunnels[0].Upstream == nil || out.Tunnels[0].Upstream.Pass != "pw" {
		t.Fatalf("bad round-trip: %+v", out)
	}
}

// TestTunnelStoreFailClosed: without a passwordStore, Save must refuse to
// write a plaintext pass instead of persisting it.
func TestTunnelStoreFailClosed(t *testing.T) {
	dir := t.TempDir()
	s := NewTunnelStore(dir)

	err := s.Save(session.TunnelStoreData{Version: 1, Tunnels: []session.Tunnel{{
		ID:       "t1",
		Name:     "jump",
		Upstream: &session.SocksProxy{Kind: "socks5", Host: "h", Port: 1080, Pass: "secret"},
	}}})
	if err == nil {
		t.Fatal("expected error saving plaintext pass without passwordStore")
	}
	if _, statErr := os.Stat(filepath.Join(dir, tunnelsFileName)); statErr == nil {
		t.Fatal("tunnels.json must not be written when Save fails closed")
	}
}

// TestTunnelStoreMigratesLegacyPlaintextOnLoad: a tunnels.json written before
// in-place encryption (plaintext upstream pass) must load as plaintext for the
// caller AND be re-written with the pass encrypted, then keep round-tripping.
func TestTunnelStoreMigratesLegacyPlaintextOnLoad(t *testing.T) {
	dir := t.TempDir()
	s := NewTunnelStore(dir)
	s.SetPasswordStore(fakePasswordStore{prefix: "enc:v1:"})

	seed := `{"version":1,"groups":[],"tunnels":[{"id":"t1","name":"legacy","mode":"local","upstream":{"kind":"socks5","host":"h","port":1080,"pass":"legacy-pass"}}]}`
	if err := os.WriteFile(filepath.Join(dir, tunnelsFileName), []byte(seed), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	data, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(data.Tunnels) != 1 || data.Tunnels[0].Upstream == nil || data.Tunnels[0].Upstream.Pass != "legacy-pass" {
		t.Fatalf("Load should return the legacy plaintext pass: %+v", data)
	}

	raw, err := os.ReadFile(filepath.Join(dir, tunnelsFileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !contains(string(raw), "enc:v1:") {
		t.Fatalf("legacy plaintext not migrated to ciphertext: %s", raw)
	}
	if contains(string(raw), `"pass":"legacy-pass"`) {
		t.Fatalf("legacy plaintext still on disk: %s", raw)
	}

	// The migrated file must load (decrypt) cleanly on the next read.
	again, err := s.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.Tunnels[0].Upstream.Pass != "legacy-pass" {
		t.Fatalf("migrated pass did not decrypt: %q", again.Tunnels[0].Upstream.Pass)
	}
}

// TestTunnelStoreEmpty pins the unchanged missing-file contract: Load returns
// an empty, versioned store rather than an error.
func TestTunnelStoreEmpty(t *testing.T) {
	s := NewTunnelStore(t.TempDir())
	out, err := s.Load()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if out.Version != 1 || out.Tunnels == nil || len(out.Tunnels) != 0 {
		t.Fatalf("expected empty v1 store, got %+v", out)
	}
}

// TestTunnelStoreEncryptsSOCKSPass: the dynamic tunnel's SOCKS password is a
// credential like the upstream proxy pass — it must be ciphertext on disk
// (enc:v1, same as upstream.pass) and plaintext for the caller on Load.
func TestTunnelStoreEncryptsSOCKSPass(t *testing.T) {
	dir := t.TempDir()
	s := NewTunnelStore(dir)
	s.SetPasswordStore(fakePasswordStore{prefix: "enc:v1:"})

	in := session.TunnelStoreData{Version: 1, Tunnels: []session.Tunnel{{
		ID:        "t1",
		Name:      "socks",
		Mode:      session.TunnelDynamic,
		SOCKSUser: "alice",
		SOCKSPass: "socks-secret",
	}}}
	if err := s.Save(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	if in.Tunnels[0].SOCKSPass != "socks-secret" {
		t.Fatalf("Save mutated the caller's data: socksPass = %q", in.Tunnels[0].SOCKSPass)
	}

	raw, err := os.ReadFile(filepath.Join(dir, tunnelsFileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if contains(string(raw), `"socksPass": "socks-secret"`) {
		t.Fatalf("socksPass was written as plaintext: %s", raw)
	}
	if !contains(string(raw), "enc:v1:") {
		t.Fatalf("expected encrypted socksPass on disk: %s", raw)
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(out.Tunnels) != 1 || out.Tunnels[0].SOCKSPass != "socks-secret" {
		t.Fatalf("bad round-trip: %+v", out)
	}
	if out.Tunnels[0].SOCKSUser != "alice" {
		t.Fatalf("username must survive the round-trip: %+v", out.Tunnels[0])
	}
}

// TestTunnelStoreFailClosedOnPlaintextSOCKSPass: with no cipher wired, Save
// must refuse rather than land a plaintext SOCKS password in tunnels.json.
func TestTunnelStoreFailClosedOnPlaintextSOCKSPass(t *testing.T) {
	dir := t.TempDir()
	s := NewTunnelStore(dir)

	err := s.Save(session.TunnelStoreData{Version: 1, Tunnels: []session.Tunnel{{
		ID:        "t1",
		Mode:      session.TunnelDynamic,
		SOCKSUser: "alice",
		SOCKSPass: "socks-secret",
	}}})
	if err == nil {
		t.Fatal("expected error saving a plaintext socksPass without passwordStore")
	}
	if _, statErr := os.Stat(filepath.Join(dir, tunnelsFileName)); statErr == nil {
		t.Fatal("tunnels.json must not be written when Save fails closed")
	}
}

// TestTunnelStoreMigratesLegacyPlaintextSOCKSPass: a plaintext socksPass (or
// one written by hand) loads as plaintext for the caller and is rewritten as
// ciphertext, like the upstream pass migration.
func TestTunnelStoreMigratesLegacyPlaintextSOCKSPass(t *testing.T) {
	dir := t.TempDir()
	s := NewTunnelStore(dir)
	s.SetPasswordStore(fakePasswordStore{prefix: "enc:v1:"})

	seed := `{"version":1,"groups":[],"tunnels":[{"id":"t1","name":"legacy","mode":"dynamic","socksUser":"alice","socksPass":"legacy-socks-pass"}]}`
	if err := os.WriteFile(filepath.Join(dir, tunnelsFileName), []byte(seed), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	data, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(data.Tunnels) != 1 || data.Tunnels[0].SOCKSPass != "legacy-socks-pass" {
		t.Fatalf("Load should return the legacy plaintext socksPass: %+v", data)
	}

	raw, err := os.ReadFile(filepath.Join(dir, tunnelsFileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if contains(string(raw), `"socksPass": "legacy-socks-pass"`) {
		t.Fatalf("legacy plaintext socksPass still on disk: %s", raw)
	}
	if !contains(string(raw), "enc:v1:") {
		t.Fatalf("legacy plaintext not migrated to ciphertext: %s", raw)
	}

	again, err := s.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.Tunnels[0].SOCKSPass != "legacy-socks-pass" {
		t.Fatalf("migrated socksPass did not decrypt: %q", again.Tunnels[0].SOCKSPass)
	}
}

// TestTunnelStoreSyncRoundTrip pins the sync exchange contract for
// tunnels.json, including the Version backfill ImportPlain applies.
func TestTunnelStoreSyncRoundTrip(t *testing.T) {
	a := NewTunnelStore(t.TempDir())
	seed := session.TunnelStoreData{
		Version: 1,
		Groups:  []session.TunnelGroup{{ID: "tg1", Name: "g"}},
		Tunnels: []session.Tunnel{{ID: "t1", Name: "tn", Mode: "local", SSHConnID: "c1", ListenPort: 8080}},
	}
	if err := a.Save(seed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plain, err := a.ExportPlain()
	if err != nil {
		t.Fatalf("ExportPlain: %v", err)
	}
	b := NewTunnelStore(t.TempDir())
	if err := b.ImportPlain(plain); err != nil {
		t.Fatalf("ImportPlain: %v", err)
	}
	got, err := b.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Groups) != 1 || len(got.Tunnels) != 1 || got.Tunnels[0].ID != "t1" || got.Tunnels[0].ListenPort != 8080 {
		t.Fatalf("tunnels lost: %+v", got)
	}
}
