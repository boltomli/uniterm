package session

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func newAgentTestKey(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return priv, sshPub
}

func TestPublicKeyFromFile(t *testing.T) {
	_, pub := newAgentTestKey(t)
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "host.pub")
	if err := os.WriteFile(pubPath, ssh.MarshalAuthorizedKey(pub), 0o644); err != nil {
		t.Fatal(err)
	}
	junkPath := filepath.Join(dir, "junk")
	if err := os.WriteFile(junkPath, []byte("not a key"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := publicKeyFromFile(ConnectionConfig{AuthType: "key", KeyPath: pubPath})
	if !ok || string(got.Marshal()) != string(pub.Marshal()) {
		t.Fatalf("publicKeyFromFile(.pub) = %v, %v; want the public key", got, ok)
	}
	for _, cfg := range []ConnectionConfig{
		{AuthType: "key", KeyPath: junkPath},
		{AuthType: "key", KeyPath: filepath.Join(dir, "missing.pub")},
		{AuthType: "keyText", KeyPath: pubPath},
		{AuthType: "key"},
	} {
		if _, ok := publicKeyFromFile(cfg); ok {
			t.Fatalf("publicKeyFromFile(%+v) = ok, want not a public key", cfg)
		}
	}
}

func TestAgentSignerFor(t *testing.T) {
	keyring := agent.NewKeyring()
	var pubs []ssh.PublicKey
	for i := 0; i < 8; i++ {
		priv, pub := newAgentTestKey(t)
		if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
			t.Fatal(err)
		}
		pubs = append(pubs, pub)
	}

	want := pubs[5]
	signer, err := agentSignerFor(keyring, want)
	if err != nil {
		t.Fatalf("agentSignerFor() error = %v", err)
	}
	if string(signer.PublicKey().Marshal()) != string(want.Marshal()) {
		t.Fatalf("agentSignerFor() returned a different key")
	}

	_, missing := newAgentTestKey(t)
	if _, err := agentSignerFor(keyring, missing); err == nil {
		t.Fatalf("agentSignerFor(missing key) error = nil, want not loaded error")
	}
}
