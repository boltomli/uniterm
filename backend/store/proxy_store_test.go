package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

func TestProxyStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewProxyStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	s.SetPasswordStore(fakePasswordStore{prefix: "enc:v1:"})

	in := session.ProxyStoreData{Proxies: []session.Proxy{
		{ID: "p1", Name: "vpn", Kind: "socks5", Host: "127.0.0.1", Port: 1080, User: "u", Pass: "pw"},
		{ID: "p2", Name: "corp", Kind: "http", Host: "proxy.corp", Port: 8080},
	}}
	if err := s.Save(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := readFileString(filepath.Join(dir, "proxies.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if contains(raw, `"pass": "pw"`) {
		t.Fatal("pass was not encrypted on disk")
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(out.Proxies) != 2 || out.Proxies[0].Pass != "pw" || out.Proxies[1].Host != "proxy.corp" {
		t.Fatalf("bad round-trip: %+v", out)
	}
}

func TestProxyStoreEmpty(t *testing.T) {
	s, _ := NewProxyStore(t.TempDir())
	out, err := s.Load()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if out.Proxies == nil || len(out.Proxies) != 0 {
		t.Fatalf("expected empty, got %+v", out)
	}
}

func TestProxyStoreFailClosed(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewProxyStore(dir)
	// No passwordStore wired → saving a plaintext pass must fail, not persist.
	err := s.Save(session.ProxyStoreData{Proxies: []session.Proxy{
		{ID: "p1", Name: "vpn", Kind: "socks5", Host: "h", Port: 1080, Pass: "secret"},
	}})
	if err == nil {
		t.Fatal("expected error saving plaintext pass without passwordStore")
	}
}

func TestProxyStoreCorrupt(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewProxyStore(dir)
	if err := os.WriteFile(filepath.Join(dir, "proxies.json"), []byte("{not json"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("expected error loading corrupt file")
	}
}

func mustProxyStore(t *testing.T) *ProxyStore {
	t.Helper()
	s, err := NewProxyStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProxyStore: %v", err)
	}
	s.SetPasswordStore(fakeCipherStore{})
	return s
}

// TestProxyStoreSyncRoundTrip pins the sync exchange contract for
// proxies.json: the blob must carry plaintext secrets and survive a
// round-trip through a store on another directory.
func TestProxyStoreSyncRoundTrip(t *testing.T) {
	a := mustProxyStore(t)
	seed := session.ProxyStoreData{Proxies: []session.Proxy{{
		ID: "p1", Name: "px", Kind: "socks5", Host: "h", Port: 1080, Pass: "pp",
	}}}
	if err := a.Save(seed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plain, err := a.ExportPlain()
	if err != nil {
		t.Fatalf("ExportPlain: %v", err)
	}
	if !contains(string(plain), "pp") || contains(string(plain), "enc:v1:") {
		t.Fatalf("blob must carry plaintext secrets: %s", plain)
	}
	b := mustProxyStore(t)
	if err := b.ImportPlain(plain); err != nil {
		t.Fatalf("ImportPlain: %v", err)
	}
	got, err := b.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Proxies) != 1 || got.Proxies[0].ID != "p1" || got.Proxies[0].Pass != "pp" {
		t.Fatalf("proxies lost: %+v", got.Proxies)
	}
}
