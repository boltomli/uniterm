package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCPClientConfigValidate(t *testing.T) {
	if err := (MCPClientConfig{Name: "x", Transport: MCPTransportStdio}).Validate(); err == nil {
		t.Error("stdio without command should fail")
	}
	if err := (MCPClientConfig{Name: "x", Transport: MCPTransportStreamableHTTP, URL: "http://x"}).Validate(); err != nil {
		t.Errorf("valid http config: %v", err)
	}
	if err := (MCPClientConfig{Name: "x", Transport: "ftp"}).Validate(); err == nil {
		t.Error("unknown transport should fail")
	}
}

func TestMCPClientStoreRoundTrip(t *testing.T) {
	s := NewMCPClientStore(t.TempDir())
	in := []MCPClientConfig{
		{Name: "B", Transport: MCPTransportStdio, Command: "npx", Enabled: true},
		{Name: "a", Transport: MCPTransportSSE, URL: "http://x/sse", Enabled: false},
	}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// Saved sorted by name.
	if got[0].Name != "a" || got[1].Name != "B" {
		t.Errorf("not sorted: %s, %s", got[0].Name, got[1].Name)
	}
	// Missing file → empty, not error.
	empty := NewMCPClientStore(t.TempDir())
	list, err := empty.Load()
	if err != nil || len(list) != 0 {
		t.Errorf("missing file: list=%v err=%v", list, err)
	}
}

func TestMCPClientSecretsUseSharedEnvelopeWithoutMutatingInput(t *testing.T) {
	s := NewMCPClientStore(t.TempDir())
	clients := []MCPClientConfig{{Name: "http", Transport: MCPTransportStreamableHTTP, URL: "http://localhost/mcp",
		Headers: map[string]string{"Authorization": "sensitive-header"}, Env: map[string]string{"KEY": "sensitive-env"}}}
	if err := s.Save(clients); !errors.Is(err, ErrNoCipher) {
		t.Fatalf("unwired secret save: %v", err)
	}
	if _, err := os.Stat(s.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unwired save created config")
	}
	s.SetPasswordStore(fakeCipherStore{})
	if err := s.Save(clients); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.Path())
	if strings.Contains(string(raw), "sensitive-") || !strings.Contains(string(raw), "enc:v1:") {
		t.Fatalf("secrets not encrypted")
	}
	if clients[0].Headers["Authorization"] != "sensitive-header" || clients[0].Env["KEY"] != "sensitive-env" {
		t.Fatal("save mutated input maps")
	}
	reopened := NewMCPClientStore(s.configDir)
	reopened.SetPasswordStore(fakeCipherStore{})
	got, err := reopened.Load()
	if err != nil || got[0].Headers["Authorization"] != "sensitive-header" || got[0].Env["KEY"] != "sensitive-env" {
		t.Fatalf("secret round trip failed: %v", err)
	}
	old := time.Unix(1700000000, 0)
	if err := os.Chtimes(s.Path(), old, old); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Save(got); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(s.Path())
	if !info.ModTime().Equal(old) {
		t.Fatal("unchanged config was rewritten")
	}
}

func TestMCPClientStoreMigratesPlaintextAndQuarantinesCorruption(t *testing.T) {
	s := NewMCPClientStore(t.TempDir())
	s.SetPasswordStore(fakeCipherStore{})
	if err := os.WriteFile(s.Path(), []byte(`{"clients":[{"name":"legacy","transport":"stdio","command":"cmd","env":{"KEY":"legacy-secret"}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || got[0].Env["KEY"] != "legacy-secret" {
		t.Fatalf("migration: %v", err)
	}
	raw, _ := os.ReadFile(s.Path())
	if strings.Contains(string(raw), "legacy-secret") {
		t.Fatal("legacy plaintext not migrated")
	}
	if err := os.WriteFile(s.Path(), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil || len(got) != 0 {
		t.Fatalf("corrupt config fallback: %v", err)
	}
	quarantined, _ := filepath.Glob(s.Path() + ".corrupt-*")
	if len(quarantined) != 1 {
		t.Fatal("corrupt config not quarantined")
	}
	if err := s.Save(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path()); err != nil {
		t.Fatal("save after quarantine was skipped")
	}
}
