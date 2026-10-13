package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const syncConfigFileName = "sync-config.json"

type SyncConfig struct {
	RepoURL  string `json:"repoUrl"`
	Branch   string `json:"branch"`
	Username string `json:"username"`
	AutoSync bool   `json:"autoSync"`
	// SyncScope is this device's per-file sync selection over syncableFiles.
	// nil/empty keeps the legacy default (the original syncedFiles set).
	SyncScope []string `json:"syncScope,omitempty"`
	// Backend selects the sync transport: "git" (default, private git repo)
	// or "webdav" (snapshot over a WebDAV share).
	Backend      string `json:"backend,omitempty"`
	WebDAVServer string `json:"webdavServer,omitempty"`
	WebDAVPath   string `json:"webdavPath,omitempty"`
	WebDAVUser   string `json:"webdavUser,omitempty"`
	// LastSyncedHash is the aggregate content hash both sides agreed on at
	// the last successful snapshot sync (three-way base for WebDAV mode).
	LastSyncedHash string `json:"lastSyncedHash,omitempty"`

	LastSyncAt     time.Time `json:"lastSyncAt"`
	LastSyncStatus string    `json:"lastSyncStatus"`
	LastSyncError  string    `json:"lastSyncError"`
}

type SyncConfigStore struct {
	configDir string
}

func NewSyncConfigStore(configDir string) *SyncConfigStore {
	return &SyncConfigStore{configDir: configDir}
}

func (s *SyncConfigStore) filePath() string {
	return filepath.Join(s.configDir, syncConfigFileName)
}

func (s *SyncConfigStore) Save(config SyncConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath(), data, 0600)
}

// quarantineCorrupt renames a corrupt sync-config.json aside so the next
// Save cannot silently overwrite the user's repo/WebDAV configuration and
// LastSyncedHash (STORE-09 convention: <name>.corrupt-<UTC timestamp>).
// backend/store's helper is unexported and in another package, so the same
// behaviour is mirrored here. Returns the new path, or "" on failure.
func quarantineCorrupt(path string) string {
	target := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405")
	if err := os.Rename(path, target); err != nil {
		return ""
	}
	return target
}

func (s *SyncConfigStore) Load() (SyncConfig, error) {
	data, err := os.ReadFile(s.filePath())
	if err != nil {
		if os.IsNotExist(err) {
			return SyncConfig{Branch: "main"}, nil
		}
		return SyncConfig{}, err
	}
	var config SyncConfig
	if err := json.Unmarshal(data, &config); err != nil {
		// Preserve the corrupt bytes under a quarantine name and surface the
		// error: silently returning defaults would let the next Save wipe
		// the user's sync configuration without a trace.
		if target := quarantineCorrupt(s.filePath()); target != "" {
			return SyncConfig{}, fmt.Errorf("sync config corrupt (quarantined to %s): %w", target, err)
		}
		return SyncConfig{}, fmt.Errorf("sync config corrupt: %w", err)
	}
	if config.Branch == "" {
		config.Branch = "main"
	}
	return config, nil
}
