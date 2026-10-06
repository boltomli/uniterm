package sync

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	manifestName = "manifest.json"
	saltName     = ".sync-salt"
)

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
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if salt, err := b.client.Get(saltName); err == nil {
		// The remote (like the git-mode repo dir) stores the salt
		// hex-encoded; decode and rewrite via WriteSaltFile so the
		// mirrored dir matches the layout ReadSaltFile expects.
		raw, err := hex.DecodeString(string(salt))
		if err != nil {
			return fmt.Errorf("decode %s: %w", saltName, err)
		}
		if err := WriteSaltFile(dir, raw); err != nil {
			return err
		}
	} else if err != ErrWebDAVNotFound {
		return err
	}
	for name := range info.FileHashes {
		data, err := b.client.Get(name)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, manifestName), mustJSON(info), 0600)
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
	// The salt is stored hex-encoded on the remote, matching
	// WriteSaltFile/ReadSaltFile in git mode.
	if err := b.client.Put(saltName, []byte(hex.EncodeToString(salt))); err != nil {
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
