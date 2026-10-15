package store

import (
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

func mustConnStore(t *testing.T) *ConnectionStore {
	t.Helper()
	return &ConnectionStore{configDir: t.TempDir(), passwordStore: fakeCipherStore{}}
}

// TestConnectionStoreSyncRoundTrip pins the sync exchange contract for
// connections.json: seed, ExportPlain, ImportPlain into a fresh store on a
// different directory, Load back. The data must survive through the plain
// bytes, and the blob must carry plaintext secrets — sync re-encrypts the
// whole blob with the sync key downstream.
func TestConnectionStoreSyncRoundTrip(t *testing.T) {
	a := mustConnStore(t)
	seed := session.ConnectionStoreData{
		Groups:      []session.ConnectionGroup{{ID: "g1", Name: "grp"}},
		Connections: []session.ConnectionConfig{{ID: "c1", Type: "ssh", AuthType: "password", Password: "secret"}},
	}
	if err := a.Save(seed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plain, err := a.ExportPlain()
	if err != nil {
		t.Fatalf("ExportPlain: %v", err)
	}
	if !contains(string(plain), "secret") || contains(string(plain), "enc:v1:") {
		t.Fatalf("blob must carry plaintext secrets: %s", plain)
	}
	b := mustConnStore(t)
	if err := b.ImportPlain(plain); err != nil {
		t.Fatalf("ImportPlain: %v", err)
	}
	got, err := b.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Groups) != 1 || got.Groups[0].ID != "g1" {
		t.Fatalf("groups lost: %+v", got.Groups)
	}
	if len(got.Connections) != 1 || got.Connections[0].ID != "c1" || got.Connections[0].Password != "secret" {
		t.Fatalf("connections lost: %+v", got.Connections)
	}
}

// TestConnectionStoreSyncRoundTripKeyText is the keyText regression guard:
// an inline private key (keyContent) is encrypted in place locally, so the
// export must carry plaintext — the whole blob is protected solely by the
// sync key at rest in the repo. A literal enc:v1: prefix escaping to another
// device breaks it.
func TestConnectionStoreSyncRoundTripKeyText(t *testing.T) {
	a := mustConnStore(t)
	seed := session.ConnectionStoreData{Connections: []session.ConnectionConfig{{
		ID: "c1", Type: "ssh", AuthType: "keyText", KeyContent: "-----BEGIN OPENSSH PRIVATE KEY-----",
	}}}
	if err := a.Save(seed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plain, err := a.ExportPlain()
	if err != nil {
		t.Fatalf("ExportPlain: %v", err)
	}
	if !contains(string(plain), "BEGIN OPENSSH PRIVATE KEY") || contains(string(plain), "enc:v1:") {
		t.Fatalf("blob must carry plaintext key content: %s", plain)
	}
	b := mustConnStore(t)
	if err := b.ImportPlain(plain); err != nil {
		t.Fatalf("ImportPlain: %v", err)
	}
	got, err := b.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Connections) != 1 || got.Connections[0].KeyContent != "-----BEGIN OPENSSH PRIVATE KEY-----" {
		t.Fatalf("key content lost: %+v", got.Connections)
	}
}

// TestConnectionStoreImportPlainRejectsGarbage: ImportPlain must surface
// unparseable input instead of persisting a zero value over good data.
func TestConnectionStoreImportPlainRejectsGarbage(t *testing.T) {
	a := mustConnStore(t)
	if err := a.Save(session.ConnectionStoreData{Connections: []session.ConnectionConfig{{ID: "c1", Type: "ssh", AuthType: "password", Password: "p"}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b := mustConnStore(t)
	if err := b.ImportPlain([]byte("not json")); err == nil {
		t.Fatal("ImportPlain(garbage) = nil, want error")
	}
	if got, err := b.Load(); err != nil || len(got.Connections) != 0 {
		t.Fatalf("garbage import must not persist data: %+v, %v", got, err)
	}
}
