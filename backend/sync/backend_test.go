package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashConfigFilesDeterministicAndScopeAware(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "connections.json"), []byte(`{"b":2,"a":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	files := []string{"connections.json", "settings.json"}
	_, h1, err := hashConfigFiles(files, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Equivalent JSON with different key order must hash identically.
	if err := os.WriteFile(filepath.Join(dir, "connections.json"), []byte(`{"a":1,"b":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, h2, err := hashConfigFiles(files, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("equal content must hash equal: %s vs %s", h1, h2)
	}
	// Changed content must change the hash.
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"light"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, h3, err := hashConfigFiles(files, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h2 {
		t.Fatal("changed content must change hash")
	}
	// A missing file is treated as null and must be equivalent to an empty
	// array (readJSONValue normalizes both to nil).
	_, ha, err := hashConfigFiles([]string{"favorites.json"}, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "favorites.json"), []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	_, hb, err := hashConfigFiles([]string{"favorites.json"}, other, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatal("missing file and empty array must hash equal")
	}
}

// TestHashConfigFilesDeviceIndependent pins the core property of the design:
// a file carrying an enc:v1: secret must hash the same as the same file with
// the plaintext value, because normalization (keychain backfill +
// enc:v1:→plaintext) runs before hashing. A regression here breaks
// device-independence and every sync sees spurious conflicts.
func TestHashConfigFilesDeviceIndependent(t *testing.T) {
	dir := t.TempDir()
	encPath := filepath.Join(dir, "connections.json")
	kc := &Keychain{}
	ps := fakePS{}
	// Ciphertext produced by the same store the hash will decrypt with.
	ciphertext, err := ps.Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(encPath, []byte(`{"connections":[{"id":"c1","authType":"password","password":"`+ciphertext+`"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, hEnc, err := hashConfigFiles([]string{"connections.json"}, dir, kc, ps)
	if err != nil {
		t.Fatal(err)
	}
	// Sanity: without a password store the ciphertext stays in place, so the
	// hash must differ — proves the decrypt path above is actually live.
	_, hNoPS, err := hashConfigFiles([]string{"connections.json"}, dir, kc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hNoPS == hEnc {
		t.Fatal("hashing without a password store must not match the decrypted hash")
	}
	// Same logical content, plaintext instead of ciphertext.
	if err := os.WriteFile(encPath, []byte(`{"connections":[{"id":"c1","authType":"password","password":"secret"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, hPlain, err := hashConfigFiles([]string{"connections.json"}, dir, kc, ps)
	if err != nil {
		t.Fatal(err)
	}
	if hEnc != hPlain {
		t.Fatalf("encrypted and plaintext forms of the same content must hash equal: %s vs %s", hEnc, hPlain)
	}
	// Hashing must be stable across repeated calls with the same kc/ps.
	_, hEnc2, err := hashConfigFiles([]string{"connections.json"}, dir, kc, ps)
	if err != nil {
		t.Fatal(err)
	}
	if hEnc2 != hPlain {
		t.Fatal("repeated hashing with the same kc/ps must be stable")
	}
}

func TestAggregateHash(t *testing.T) {
	build := func() map[string]string {
		h := make(map[string]string)
		h["settings.json"] = "h-settings"
		h["connections.json"] = "h-connections"
		h["favorites.json"] = "h-favorites"
		return h
	}
	// Same entries, different insertion orders → equal aggregate.
	h1 := build()
	h2 := make(map[string]string)
	for _, name := range []string{"favorites.json", "settings.json", "connections.json"} {
		h2[name] = build()[name]
	}
	a1 := aggregateHash(h1)
	a2 := aggregateHash(h2)
	if a1 != a2 {
		t.Fatalf("insertion order must not affect aggregate: %s vs %s", a1, a2)
	}
	// Changing one file's hash must change the aggregate.
	h3 := build()
	h3["favorites.json"] = "h-favorites-changed"
	if aggregateHash(h3) == a1 {
		t.Fatal("changing one file hash must change the aggregate")
	}
}
