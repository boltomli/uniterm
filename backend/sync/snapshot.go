package sync

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ys-ll/uniterm/backend/utils"
)

const (
	manifestName = "manifest.json"
	saltName     = ".sync-salt"
)

// fetchableNames is the whitelist FetchAll enforces on manifest.json file
// names: the sync-scope menu (syncableFiles) plus the two reserved
// resources. The sync server is untrusted — it never sees the encryption
// key — so a hostile manifest naming anything else (separators, "..",
// absolute paths) must never reach the disk (security.md P0 traversal).
var fetchableNames = func() map[string]bool {
	names := make(map[string]bool, len(syncableFiles)+2)
	for _, name := range syncableFiles {
		names[name] = true
	}
	names[saltName] = true
	names[manifestName] = true
	return names
}()

// remoteSnapshotBackend is the unified snapshot engine: it implements
// SnapshotBackend over any RemoteFS, so webdav, sftp, s3 and smb share one
// implementation of the snapshot semantics (remote layout, manifest last
// as commit point, salt hex encoding).
//
// The remote layout is unchanged from the original WebDAV backend:
// basePath/.sync-salt (hex), one ciphertext resource per scoped config
// file, and manifest.json written LAST as the commit point — readers
// never observe a half-written snapshot.
type remoteSnapshotBackend struct {
	fs    RemoteFS
	files []string
}

// NewRemoteSnapshotBackend builds the engine over fs. files is the scoped
// file list used by StoreAll's staging upload.
func NewRemoteSnapshotBackend(fs RemoteFS, files []string) SnapshotBackend {
	return &remoteSnapshotBackend{fs: fs, files: append([]string{}, files...)}
}

// Probe verifies the share is reachable and creates the collection.
func (b *remoteSnapshotBackend) Probe() error {
	return b.fs.Probe()
}

// Head returns the remote manifest, or nil when none exists yet (fresh
// remote). ErrRemoteNotFound maps to the nil case.
func (b *remoteSnapshotBackend) Head() (*SnapshotInfo, error) {
	data, err := b.fs.Get(manifestName)
	if err != nil {
		if errors.Is(err, ErrRemoteNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var info SnapshotInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &info, nil
}

// FetchAll mirrors the remote snapshot into dir. A missing manifest means
// an empty remote, which is a no-op.
func (b *remoteSnapshotBackend) FetchAll(dir string) error {
	info, err := b.Head()
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	// The manifest is attacker-controlled: validate every name against the
	// whitelist before any network read or disk write so a hostile entry
	// can neither escape dir nor half-mirror its whitelisted siblings.
	for name := range info.FileHashes {
		if !fetchableNames[name] {
			return fmt.Errorf("manifest: disallowed file name %q", name)
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if salt, err := b.fs.Get(saltName); err == nil {
		// The remote salt resource is written verbatim by new pushes (hex
		// salt plus an optional format= version line) and as pure hex by
		// legacy ones. Validate it parses, then mirror it byte-for-byte so
		// the format version declared by the remote survives the fetch —
		// re-stamping it locally would lie about the remote's ciphertext.
		if _, _, perr := parseSaltFileContent(salt); perr != nil {
			return fmt.Errorf("decode %s: %w", saltName, perr)
		}
		if err := atomicWriteFile(filepath.Join(dir, saltName), salt, 0600); err != nil {
			return err
		}
	} else if !errors.Is(err, ErrRemoteNotFound) {
		return err
	}
	nullHash := nullStoreHash()
	for name, h := range info.FileHashes {
		if h == nullHash {
			// A never-persisted store: the manifest records the null
			// hash so aggregate hashes stay comparable, but the remote
			// carries no blob for it.
			continue
		}
		// Defense in depth beside the fetchableNames whitelist above: join
		// through the traversal-safe helper so a hostile name can never
		// escape dir even if the whitelist regresses.
		target, err := utils.SafeLocalJoin(dir, name)
		if err != nil {
			return fmt.Errorf("manifest: disallowed file name %q: %w", name, err)
		}
		data, err := b.fs.Get(name)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", name, err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return err
		}
	}
	manifestPath, err := utils.SafeLocalJoin(dir, manifestName)
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, mustJSON(info), 0600)
}

// StoreAll uploads ciphertext files from stagingDir (named by
// manifest.FileHashes), the salt, then the manifest last.
func (b *remoteSnapshotBackend) StoreAll(stagingDir string, salt []byte, manifest SnapshotInfo) error {
	// Upload only the blobs actually staged: the caller stages the delta
	// (the blobs whose plaintext changed), so the remote keeps its copy of
	// every unchanged blob under the same key. The manifest still
	// describes the whole snapshot.
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return fmt.Errorf("read staging: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == saltName {
			continue
		}
		data, err := os.ReadFile(filepath.Join(stagingDir, e.Name()))
		if err != nil {
			return fmt.Errorf("stage %s: %w", e.Name(), err)
		}
		if err := b.fs.Put(e.Name(), data); err != nil {
			return err
		}
	}
	// The salt resource mirrors the staged .sync-salt byte-for-byte: new
	// pushes stage it via WriteSaltFile, so the file carries the ciphertext
	// format version the remote must declare. Fall back to plain hex for
	// callers that stage no salt file (tests seeding a remote directly).
	saltData := []byte(hex.EncodeToString(salt))
	if staged, err := os.ReadFile(filepath.Join(stagingDir, saltName)); err == nil {
		saltData = staged
	}
	if err := b.fs.Put(saltName, saltData); err != nil {
		return err
	}
	return b.fs.Put(manifestName, mustJSON(manifest))
}

func mustJSON(v interface{}) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err) // SnapshotInfo only contains serializable fields
	}
	return data
}
