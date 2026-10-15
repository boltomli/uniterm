package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SyncableStore is the only surface the sync package sees for config
// exchange. Sync never imports store model types: it holds a registry of
// these interface values, moving canonical plaintext bytes in and out.
//
// The interface lives here, on the consumer side: store's implementations
// satisfy it structurally, and the registry is assembled in app.go (the only
// place that imports both packages). backend/store must not import
// backend/sync — store → credentials → sync means sync → store would cycle,
// which is also what keeps the dependency graph acyclic.
//
// The contract:
//
//   - ExportPlain renders the store's canonical plaintext — the same shape
//     the on-disk JSON carries after decryption (Load applies the store's
//     full semantics: legacy keychain backfill with move semantics, lazy
//     re-encryption, defaults backfill). Secret fields come out as plaintext;
//     the sync layer re-encrypts the whole blob with the sync key
//     downstream, so only the sync key protects it at rest in the repo. A
//     cleared secret exports as cleared — the store is the single source of
//     truth, and a stale keychain entry can never resurrect it through sync.
//   - A store whose backing file was never written returns an error wrapping
//     fs.ErrNotExist: sync skips such blobs instead of uploading an empty
//     document that would clobber the remote copy.
//   - ImportPlain persists the blob through the store's Save, so every
//     save-time invariant (password clearing for identity-auth, manifest
//     encryption, hash-skip, quarantine) applies unchanged.
type SyncableStore interface {
	BlobName() string
	ExportPlain() ([]byte, error)
	ImportPlain([]byte) error
}

// SnapshotInfo describes one remote snapshot state. In git mode history
// provides the merge base; snapshot backends (WebDAV, future S3/OneDrive)
// carry FileHashes instead and the three-way base lives in the device's
// SyncConfig.LastSyncedHash.
type SnapshotInfo struct {
	// Version is an opaque token that changes on every successful upload
	// (timestamp-based; no server-side monotonic counter is assumed). It
	// must be unique per upload — the sync driver re-Heads after StoreAll
	// to detect lost-update races, and a duplicate Version makes that
	// check meaningless. Writers should use an RFC3339Nano timestamp plus
	// a random suffix.
	Version   string    `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
	// FileHashes maps config file name → hash of its canonical plaintext
	// (hashStoresScoped / canonicalPlainHash), so hashes are
	// device-independent.
	FileHashes map[string]string `json:"fileHashes"`
}

// SnapshotBackend is the contract for snapshot-type sync remotes.
type SnapshotBackend interface {
	// Probe verifies reachability/credentials and creates the remote
	// collection if missing.
	Probe() error
	// Head returns the current remote snapshot, or nil when the remote
	// is empty (freshly created / wiped).
	Head() (*SnapshotInfo, error)
	// FetchAll downloads the current snapshot's encrypted resources into
	// dir (.sync-salt + ciphertext files + manifest.json). A missing
	// remote snapshot leaves dir untouched.
	FetchAll(dir string) error
	// StoreAll uploads the encrypted files from stagingDir as a new
	// snapshot with the given salt and manifest. The manifest is written
	// last so readers never observe a half-written snapshot.
	StoreAll(stagingDir string, salt []byte, manifest SnapshotInfo) error
}

// requireStores refuses to exchange configs unless the registry covers every
// file in scope. A missing store would silently drop that blob from the next
// upload — and a snapshot upload replaces the remote wholesale, so the
// omitted blob would be deleted from the remote. An empty registry is the
// catastrophic case: sync would report a successful pull of nothing, record
// the remote hash as synced, and upload an empty snapshot next cycle.
func requireStores(stores []SyncableStore, files []string) error {
	if len(stores) == 0 {
		return fmt.Errorf("sync registry not wired")
	}
	have := make(map[string]bool, len(stores))
	for _, st := range stores {
		have[st.BlobName()] = true
	}
	for _, name := range files {
		if !have[name] {
			return fmt.Errorf("sync registry has no store for %s", name)
		}
	}
	return nil
}

// canonicalDirHashes hashes every plaintext file in dir over files with the
// same canonical rule hashStoresScoped applies to a store's export, so a
// manifest rebuilt from a re-encrypted staging dir carries hashes the next
// push agrees with. Used by the password rotation, which re-encrypts the
// remote's current content rather than re-reading the local stores. A
// missing file hashes as null — the sentinel hashStoresScoped records for a
// store whose blob was never written.
func canonicalDirHashes(files []string, dir string) (map[string]string, string, error) {
	nullHash := nullStoreHash()
	hashes := make(map[string]string, len(files))
	for _, name := range files {
		plain, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				hashes[name] = nullHash
				continue
			}
			return nil, "", fmt.Errorf("read %s: %w", name, err)
		}
		h, err := canonicalPlainHash(plain)
		if err != nil {
			return nil, "", fmt.Errorf("hash %s: %w", name, err)
		}
		hashes[name] = h
	}
	return hashes, aggregateHash(hashes), nil
}

// aggregateHash folds per-file hashes into one deterministic string.
func aggregateHash(hashes map[string]string) string {
	lines := make([]string, 0, len(hashes))
	for name, h := range hashes {
		lines = append(lines, name+":"+h)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// hashStoresScoped hashes each store's canonical export over files, so the
// manifest hashes and LastSyncedHash come from the exact bytes the sync
// path carries — a store may flush its backing file lazily, so a file hash
// could disagree with the uploaded content. Both sides canonicalize:
// unmarshal→remarshal before sha256, so a store's struct-order marshal and
// an older file's map-order marshal hash identically when content matches.
// A store whose blob was never written hashes as null — the sentinel
// readJSONValue yields for a missing file — keeping the manifest shape
// stable across the store transition.
func hashStoresScoped(stores []SyncableStore, files []string) (map[string]string, string, error) {
	if err := requireStores(stores, files); err != nil {
		return nil, "", err
	}
	wanted := make(map[string]bool, len(files))
	for _, name := range files {
		wanted[name] = true
	}

	nullHash, err := canonicalPlainHash([]byte("null"))
	if err != nil {
		return nil, "", err
	}
	hashes := make(map[string]string, len(files))
	for _, name := range files {
		hashes[name] = nullHash
	}

	for _, st := range stores {
		name := st.BlobName()
		if !wanted[name] {
			continue
		}
		plain, err := st.ExportPlain()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, "", fmt.Errorf("export %s: %w", name, err)
		}
		h, err := canonicalPlainHash(plain)
		if err != nil {
			return nil, "", fmt.Errorf("hash %s: %w", name, err)
		}
		hashes[name] = h
	}

	return hashes, aggregateHash(hashes), nil
}

// canonicalPlainHash hashes canonical JSON: normalize empty wrappers away
// (absent, blank, {} and [] all mean "no data" — the same rule
// compareConfigFiles applies), then re-marshal before sha256 so key order
// from a struct marshal and from a map re-marshal produce the same digest
// for equal content.
//
// The re-marshal is MarshalIndent because that is what the pre-registry
// scheme hashed: digests already stored in a device's LastSyncedHash and in
// a remote snapshot manifest must stay valid, or every upgraded WebDAV
// device would push (or conflict) on its first sync. Pinned by
// TestCanonicalPlainHashMatchesLegacyNormalization.
func canonicalPlainHash(plain []byte) (string, error) {
	val, err := jsonValue(plain)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	data, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return "", fmt.Errorf("canonicalize: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// nullStoreHash is the manifest hash of a store whose blob was never
// written: its export reads as "no data", and the entry exists only so the
// aggregate hash stays comparable across devices that do carry the blob.
func nullStoreHash() string {
	h, err := canonicalPlainHash([]byte("null"))
	if err != nil {
		// Unreachable: "null" is valid JSON.
		return ""
	}
	return h
}
