package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

const quickCommandsFileName = "quickCommands.json"

type QuickCommand struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Remark    string `json:"remark,omitempty"`
	Command   string `json:"command"`
	GroupID   string `json:"groupId,omitempty"`
	SortOrder int    `json:"sortOrder"`
}

type QuickCommandGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sortOrder"`
}

type QuickCommandData struct {
	Version  int                 `json:"version"`
	Groups   []QuickCommandGroup `json:"groups"`
	Commands []QuickCommand      `json:"commands"`
}

type QuickCommandsStore struct {
	configDir     string
	lastSavedHash string // F-105 skip state (pre-encryption content hash)
}

func NewQuickCommandsStore(configDir string) *QuickCommandsStore {
	return &QuickCommandsStore{configDir: configDir}
}

func (s *QuickCommandsStore) filePath() string {
	return filepath.Join(s.configDir, quickCommandsFileName)
}

// Shared Save envelope (filestore.go): hash-skip, encrypt, atomic write.
func (s *QuickCommandsStore) Save(data QuickCommandData) error {
	h, err := saveEncryptedFile(quickCommandsFileName, s.filePath(), &data, nil, s.lastSavedHash)
	s.lastSavedHash = h
	return err
}

func (s *QuickCommandsStore) Load() (QuickCommandData, error) {
	out, _, hash, existed, err := loadEncryptedFile[QuickCommandData](quickCommandsFileName, s.filePath(), nil)
	if !existed {
		return QuickCommandData{Version: 1, Groups: []QuickCommandGroup{}, Commands: []QuickCommand{}}, nil
	}
	if err != nil {
		return QuickCommandData{}, err
	}
	if out.Version == 0 {
		out.Version = 1
	}
	s.lastSavedHash = hash
	return out, nil
}

// BlobName, ExportPlain and ImportPlain implement the sync exchange contract
// (SyncableStore, declared in backend/sync/backend.go).

func (s *QuickCommandsStore) BlobName() string { return quickCommandsFileName }

func (s *QuickCommandsStore) ExportPlain() ([]byte, error) {
	return exportPlainStore(quickCommandsFileName, s.filePath(), s.Load)
}

// ImportPlain backfills Version before saving so the on-disk copy always
// carries the explicit value, mirroring Load's normalization.
func (s *QuickCommandsStore) ImportPlain(raw []byte) error {
	var data QuickCommandData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("parse sync blob: %w", err)
	}
	if data.Version == 0 {
		data.Version = 1
	}
	return s.Save(data)
}
