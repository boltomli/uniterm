package store

import (
	"path/filepath"
	"sync"
)

const favoriteFileName = "favorites.json"

// FavoriteStore persists the user's ordered list of favorite connection ids.
// The array order is the display order (drag & drop rewrites the whole list).
type FavoriteStore struct {
	filePath string

	mu            sync.Mutex
	ids           []string
	lastSavedHash string // F-105 skip state (pre-encryption content hash)
}

func NewFavoriteStore(configDir string) *FavoriteStore {
	return &FavoriteStore{
		filePath: filepath.Join(configDir, favoriteFileName),
		ids:      make([]string, 0),
	}
}

// Load reads favorites.json into memory and returns a copy. A missing file
// resets to an empty list; a corrupt one is quarantined (filestore.go
// envelope) and also resets.
func (s *FavoriteStore) Load() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids, _, hash, existed, err := loadEncryptedFile[[]string](favoriteFileName, s.filePath, nil)
	if !existed || err != nil {
		s.ids = make([]string, 0)
		return make([]string, 0), nil
	}
	s.ids = ids
	// Seed the skip-state with the just-read content so an unchanged first
	// Save after startup doesn't rewrite the file.
	s.lastSavedHash = hash
	result := make([]string, len(s.ids))
	copy(result, s.ids)
	return result, nil
}

// Save replaces the favorite list with ids (deduplicated, first occurrence
// order kept) and writes the file synchronously. Favorites change rarely
// (toggle / drag reorder), so an immediate atomic write needs no debouncing.
func (s *FavoriteStore) Save(ids []string) error {
	s.mu.Lock()
	seen := make(map[string]struct{}, len(ids))
	deduped := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		deduped = append(deduped, id)
	}
	s.ids = deduped
	current := make([]string, len(s.ids))
	copy(current, s.ids)
	s.mu.Unlock()

	// Shared Save envelope (filestore.go): hash-skip, encrypt, atomic write.
	h, err := saveEncryptedFile(favoriteFileName, s.filePath, &current, nil, s.lastSavedHash)
	s.lastSavedHash = h
	return err
}

func (s *FavoriteStore) GetAll() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]string, len(s.ids))
	copy(result, s.ids)
	return result
}

// BlobName, ExportPlain and ImportPlain implement the sync exchange contract
// (SyncableStore, declared in backend/sync/backend.go).

func (s *FavoriteStore) BlobName() string { return favoriteFileName }

func (s *FavoriteStore) ExportPlain() ([]byte, error) {
	return exportPlainStore(favoriteFileName, s.filePath, s.Load)
}

func (s *FavoriteStore) ImportPlain(raw []byte) error {
	return importPlainStore(raw, s.Save)
}
