package sync

import (
	"encoding/hex"
	"encoding/json"
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

// WebDAVBackend stores one encrypted snapshot in a WebDAV collection:
// .sync-salt, one ciphertext resource per scoped config file, and
// manifest.json (written last as the commit point).
type WebDAVBackend struct {
	client *WebDAVClient
	files  []string
}

// NewWebDAVBackend builds a backend over serverURL + basePath for files.
func NewWebDAVBackend(serverURL, basePath, user, password string, files []string) *WebDAVBackend {
	return &WebDAVBackend{
		client: NewWebDAVClient(serverURL, basePath, user, password),
		files:  append([]string{}, files...),
	}
}

// Probe verifies the share is reachable and creates the collection.
func (b *WebDAVBackend) Probe() error {
	return b.client.MkdirAll()
}

// Head returns the remote manifest, or nil when none exists yet.
func (b *WebDAVBackend) Head() (*SnapshotInfo, error) {
	data, err := b.client.Get(manifestName)
	if err != nil {
		if err == ErrWebDAVNotFound {
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
// an empty remote — a no-op.
func (b *WebDAVBackend) FetchAll(dir string) error {
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
	if salt, err := b.client.Get(saltName); err == nil {
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
	} else if err != ErrWebDAVNotFound {
		return err
	}
	for name := range info.FileHashes {
		target, err := utils.SafeLocalJoin(dir, name)
		if err != nil {
			return fmt.Errorf("manifest: disallowed file name %q: %w", name, err)
		}
		data, err := b.client.Get(name)
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
func (b *WebDAVBackend) StoreAll(stagingDir string, salt []byte, manifest SnapshotInfo) error {
	for name := range manifest.FileHashes {
		data, err := os.ReadFile(filepath.Join(stagingDir, name))
		if err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
		if err := b.client.Put(name, data); err != nil {
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
	if err := b.client.Put(saltName, saltData); err != nil {
		return err
	}
	return b.client.Put(manifestName, mustJSON(manifest))
}

func mustJSON(v interface{}) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err) // SnapshotInfo only contains serializable fields
	}
	return data
}
