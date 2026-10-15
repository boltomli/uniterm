package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCanonicalPlainHashMatchesLegacyNormalization pins the digest the
// pre-registry scheme produced: sha256 of MarshalIndent of the normalized
// generic value. An upgrading device's stored LastSyncedHash and the remote
// snapshot manifest still carry those digests, so a compact re-marshal here
// would make every upgraded WebDAV device push — or hit the conflict dialog
// on identical content — on its first sync.
func TestCanonicalPlainHashMatchesLegacyNormalization(t *testing.T) {
	raw := []byte(`{"connections":[{"id":"c1","password":"p"}],"z":1}`)
	got, err := canonicalPlainHash(raw)
	if err != nil {
		t.Fatal(err)
	}
	var val interface{}
	if err := json.Unmarshal(raw, &val); err != nil {
		t.Fatal(err)
	}
	legacy, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(legacy)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("canonicalPlainHash = %s, want legacy digest %s", got, want)
	}
}

// fileStores builds a file-backed registry over dir for the given files.
// The fake mirrors app.go's assembly: sync holds SyncableStore interface
// values and never imports store.
func fileStores(dir string, files []string) []SyncableStore {
	stores := make([]SyncableStore, 0, len(files))
	for _, name := range files {
		stores = append(stores, &snapshotBlobStore{name: name, dir: dir})
	}
	return stores
}

func writeSyncFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestHashStoresScopedDeterministicAndScopeAware(t *testing.T) {
	dir := t.TempDir()
	writeSyncFile(t, dir, "connections.json", `{"b":2,"a":1}`)
	writeSyncFile(t, dir, "settings.json", `{"theme":"dark"}`)
	files := []string{"connections.json", "settings.json"}
	stores := fileStores(dir, files)

	_, h1, err := hashStoresScoped(stores, files)
	if err != nil {
		t.Fatal(err)
	}
	// Equivalent JSON with different key order must hash identically.
	writeSyncFile(t, dir, "connections.json", `{"a":1,"b":2}`)
	_, h2, err := hashStoresScoped(stores, files)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("equal content must hash equal: %s vs %s", h1, h2)
	}
	// Changed content must change the hash.
	writeSyncFile(t, dir, "settings.json", `{"theme":"light"}`)
	_, h3, err := hashStoresScoped(stores, files)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h2 {
		t.Fatal("changed content must change hash")
	}
	// A store whose blob was never written hashes as null, which must be
	// equivalent to the empty array a store writes when it holds no data
	// (jsonValue normalizes both to nil) — otherwise two devices that both
	// have "no favorites" would disagree forever.
	_, ha, err := hashStoresScoped(fileStores(dir, []string{"favorites.json"}), []string{"favorites.json"})
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	writeSyncFile(t, other, "favorites.json", `[]`)
	_, hb, err := hashStoresScoped(fileStores(other, []string{"favorites.json"}), []string{"favorites.json"})
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatal("missing blob and empty array must hash equal")
	}
}

// TestCanonicalDirHashesMatchesStoreHashes pins the invariant the password
// rotation depends on: a manifest rebuilt from a re-encrypted staging dir
// (canonicalDirHashes) carries exactly the hashes a push computes from the
// stores (hashStoresScoped), so the next sync does not see a phantom change
// after the user changes the sync password.
func TestCanonicalDirHashesMatchesStoreHashes(t *testing.T) {
	dir := t.TempDir()
	// ai.json is deliberately absent on both sides: the null sentinel must
	// agree too.
	files := []string{"connections.json", "favorites.json", "ai.json"}
	writeSyncFile(t, dir, "connections.json", `{"connections":[{"id":"c1","password":"p"}]}`)
	writeSyncFile(t, dir, "favorites.json", `[]`)

	wantHashes, wantAgg, err := hashStoresScoped(fileStores(dir, files), files)
	if err != nil {
		t.Fatal(err)
	}
	gotHashes, gotAgg, err := canonicalDirHashes(files, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotHashes) != len(wantHashes) {
		t.Fatalf("hash set size = %d, want %d", len(gotHashes), len(wantHashes))
	}
	for name, want := range wantHashes {
		if gotHashes[name] != want {
			t.Errorf("%s hash = %s, want %s", name, gotHashes[name], want)
		}
	}
	if gotAgg != wantAgg {
		t.Errorf("aggregate = %s, want %s", gotAgg, wantAgg)
	}
}

// TestStoresMatchDir pins the local-vs-remote comparison used by the conflict
// paths: the local side is the store's canonical export and the remote side
// the already-decrypted blob, so equal content with different key order
// matches, a never-persisted store matches a remote that never carried the
// blob, and a real difference is detected.
func TestStoresMatchDir(t *testing.T) {
	local := t.TempDir()
	remote := t.TempDir()
	files := []string{"connections.json", "favorites.json"}
	writeSyncFile(t, local, "connections.json", `{"connections":[{"id":"c1","password":"p"}]}`)
	// Same content, different key order — must compare equal.
	writeSyncFile(t, remote, "connections.json", `{"connections":[{"password":"p","id":"c1"}]}`)
	stores := fileStores(local, files)

	same, err := storesMatchDir(stores, files, remote)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("key order must not make equal content differ")
	}

	// A remote that never carried the blob matches a never-persisted store.
	writeSyncFile(t, remote, "favorites.json", `[]`)
	same, err = storesMatchDir(stores, files, remote)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("empty array remote must match a store that never persisted")
	}

	// A genuine difference must be detected.
	writeSyncFile(t, remote, "connections.json", `{"connections":[{"id":"c1","password":"CHANGED"}]}`)
	same, err = storesMatchDir(stores, files, remote)
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Fatal("changed remote content must not match")
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

var testKey = []byte("0123456789abcdef0123456789abcdef")

// TestConfigExchangeRefusesEmptyRegistry: an empty registry means every
// store failed to init. Sync must fail loudly rather than run blind — with
// an empty registry the WebDAV flow reports a successful pull of nothing,
// records the remote hash as synced, and then uploads an empty snapshot
// over the remote's only copy on the next cycle.
func TestConfigExchangeRefusesEmptyRegistry(t *testing.T) {
	files := []string{"connections.json"}
	dir := t.TempDir()
	writeSyncFile(t, dir, "connections.json", `{"connections":[]}`)

	if _, _, err := hashStoresScoped(nil, files); err == nil {
		t.Error("hashStoresScoped with no registry must fail")
	}
	if _, err := storesMatchDir(nil, files, dir); err == nil {
		t.Error("storesMatchDir with no registry must fail")
	}
	if err := encryptStoresScoped(nil, files, t.TempDir(), testKey, nil); err == nil {
		t.Error("encryptStoresScoped with no registry must fail")
	}
	if err := decryptStoresScoped(nil, files, dir, testKey); err == nil {
		t.Error("decryptStoresScoped with no registry must fail")
	}
}

// TestConfigExchangeRefusesPartialRegistry: a scoped file whose store is not
// registered would be silently dropped from the next upload — and a snapshot
// upload replaces the remote wholesale, so the omitted blob is deleted from
// the remote.
func TestConfigExchangeRefusesPartialRegistry(t *testing.T) {
	dir := t.TempDir()
	writeSyncFile(t, dir, "connections.json", `{"connections":[]}`)
	writeSyncFile(t, dir, "favorites.json", `[]`)
	files := []string{"connections.json", "favorites.json"}
	partial := fileStores(dir, []string{"connections.json"})
	staging := t.TempDir()
	if err := EncryptConfigFilesScoped(files, dir, staging, testKey); err != nil {
		t.Fatalf("build staging: %v", err)
	}

	if _, _, err := hashStoresScoped(partial, files); err == nil {
		t.Error("hashStoresScoped with a scoped file missing from the registry must fail")
	}
	if _, err := storesMatchDir(partial, files, dir); err == nil {
		t.Error("storesMatchDir with a scoped file missing from the registry must fail")
	}
	if err := encryptStoresScoped(partial, files, t.TempDir(), testKey, nil); err == nil {
		t.Error("encryptStoresScoped with a scoped file missing from the registry must fail")
	}
	if err := decryptStoresScoped(partial, files, staging, testKey); err == nil {
		t.Error("decryptStoresScoped with a scoped file missing from the registry must fail")
	}
}
