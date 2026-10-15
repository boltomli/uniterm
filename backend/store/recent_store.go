package store

import (
	"path/filepath"
	"sync"
	"time"
)

const recentFileName = "recent.json"
const maxRecent = 20
const recentDebounce = 500 * time.Millisecond

type RecentStore struct {
	filePath string

	mu      sync.Mutex
	pending map[string]struct{}
	ids     []string

	timer *time.Timer
	stop  chan struct{}
	done  chan struct{}

	lastSavedHash string // F-105 skip state (pre-encryption content hash)
}

func NewRecentStore(configDir string) *RecentStore {
	s := &RecentStore{
		filePath: filepath.Join(configDir, recentFileName),
		ids:      make([]string, 0),
		pending:  make(map[string]struct{}),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go s.flushLoop()
	return s
}

func (s *RecentStore) Load() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids, _, hash, existed, err := loadEncryptedFile[[]string](recentFileName, s.filePath, nil)
	if !existed || err != nil {
		s.ids = make([]string, 0)
		s.lastSavedHash = ""
		return make([]string, 0), nil
	}
	s.ids = ids
	// Seed the skip-state with the just-read content so an unchanged first
	// flush after startup doesn't rewrite the file.
	s.lastSavedHash = hash
	result := make([]string, len(s.ids))
	copy(result, s.ids)
	return result, nil
}

// Record records an id and arms a 500ms debounce timer that coalesces
// subsequent Records. The id is moved to the front of the list immediately
// so GetAll returns up-to-date results; only the file write is debounced.
// Use Flush or Close to force a synchronous write.
//
// Fixes: F-102.
func (s *RecentStore) Record(id string) error {
	if id == "" {
		return nil
	}

	s.mu.Lock()
	if s.isClosed() {
		s.mu.Unlock()
		return nil
	}

	if len(s.ids) > 0 && s.ids[0] == id {
		// Already at front; still trigger a write so persistence catches up
		// when callers rely on a save side-effect (e.g. tests).
	} else {
		// Deduplicate
		filtered := make([]string, 0, len(s.ids))
		for _, existing := range s.ids {
			if existing != id {
				filtered = append(filtered, existing)
			}
		}
		// Prepend
		s.ids = append([]string{id}, filtered...)
		// Trim
		if len(s.ids) > maxRecent {
			s.ids = s.ids[:maxRecent]
		}
	}

	s.pending[id] = struct{}{}
	if s.timer == nil {
		s.timer = time.AfterFunc(recentDebounce, s.fire)
	} else {
		s.timer.Reset(recentDebounce)
	}
	s.mu.Unlock()
	return nil
}

func (s *RecentStore) GetAll() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]string, len(s.ids))
	copy(result, s.ids)
	return result
}

// Flush forces an immediate synchronous write of any pending id changes.
// Safe to call concurrently with Record.
func (s *RecentStore) Flush() error {
	return s.flush(true)
}

// fire is the timer callback.
func (s *RecentStore) fire() {
	s.flush(false)
}

// flush writes the current id list atomically. If sync is true, the timer is
// stopped first so a concurrent fire cannot double-write.
func (s *RecentStore) flush(syncWrite bool) error {
	s.mu.Lock()
	if syncWrite && s.timer != nil {
		s.timer.Stop()
	}
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return nil
	}
	s.pending = make(map[string]struct{})
	ids := make([]string, len(s.ids))
	copy(ids, s.ids)
	last := s.lastSavedHash
	s.mu.Unlock()

	// Shared Save envelope (filestore.go): hash-skip, encrypt, atomic write.
	h, err := saveEncryptedFile(recentFileName, s.filePath, &ids, nil, last)
	s.mu.Lock()
	s.lastSavedHash = h
	s.mu.Unlock()
	return err
}

func (s *RecentStore) isClosed() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// flushLoop waits for Close and exits. The actual final flush happens in Close.
func (s *RecentStore) flushLoop() {
	defer close(s.done)
	<-s.stop
}

// Close stops the debounce loop and performs a final synchronous flush. Idempotent.
func (s *RecentStore) Close() error {
	s.mu.Lock()
	select {
	case <-s.stop:
		// already closed
		s.mu.Unlock()
		<-s.done
		return nil
	default:
	}
	close(s.stop)
	s.mu.Unlock()
	<-s.done
	return s.flush(false)
}