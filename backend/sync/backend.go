package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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
	// FileHashes maps config file name → hash of its
	// normalization-normalized plaintext (same normalization as
	// compareConfigFiles), so hashes are device-independent.
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

// normalizedFileHash hashes one config file after the same normalization
// compareConfigFiles applies (keychain backfill + enc:v1:→plaintext), so
// the same logical content hashes identically on every device. Decryption
// is best-effort: if the password store is locked the enc:v1: ciphertext
// remains and the hash won't match a remote plaintext hash — that
// self-heals with one extra merge cycle, it is not corruption.
func normalizedFileHash(path string, kc *Keychain, ps PasswordStore) (string, error) {
	val, err := readJSONValue(path)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if obj, ok := val.(map[string]interface{}); ok {
		backfillFromKeychain(obj, kc)
		decryptFieldsInPlace(obj, ps)
	}
	data, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// hashConfigFiles hashes every file in dir over files. Returns the per-file
// map (for the remote manifest) and the aggregate hash (for
// LastSyncedHash). A missing file hashes as "null", matching readJSONValue.
func hashConfigFiles(files []string, dir string, kc *Keychain, ps PasswordStore) (map[string]string, string, error) {
	hashes := make(map[string]string, len(files))
	for _, name := range files {
		h, err := normalizedFileHash(filepath.Join(dir, name), kc, ps)
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
