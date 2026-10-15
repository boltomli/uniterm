package store

import (
	"path/filepath"
	"sync"
	"time"
)

const terminalHistoryFileName = "terminal-history.json"
const maxHistorySize = 500
const terminalHistoryDebounce = 500 * time.Millisecond

type TerminalHistoryStore struct {
	configDir string

	mu      sync.Mutex
	pending []HistoryEntry // latest snapshot from the most recent Save call
	timer   *time.Timer
	stop    chan struct{}
	done    chan struct{}
	closed  bool
	closeOnce sync.Once
	lastSavedHash string // F-105 skip state (pre-encryption content hash)
}

type HistoryEntry struct {
	ID      string `json:"id"`
	Command string `json:"command"`
}

type TerminalHistoryData struct {
	Entries []HistoryEntry `json:"entries"`
}

func NewTerminalHistoryStore(configDir string) *TerminalHistoryStore {
	s := &TerminalHistoryStore{
		configDir: configDir,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go s.flushLoop()
	return s
}

func (s *TerminalHistoryStore) filePath() string {
	return filepath.Join(s.configDir, terminalHistoryFileName)
}

// Save records the latest snapshot and arms a 500ms debounce timer. Concurrent
// calls coalesce: only the most recent entries list is written. Use Close or
// Flush to force a synchronous write.
//
// Fixes: F-101.
func (s *TerminalHistoryStore) Save(entries []HistoryEntry) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.pending = entries
	if s.timer == nil {
		s.timer = time.AfterFunc(terminalHistoryDebounce, s.fire)
	} else {
		s.timer.Reset(terminalHistoryDebounce)
	}
	s.mu.Unlock()
	return nil
}

// fire is the timer callback; it triggers a flush.
func (s *TerminalHistoryStore) fire() {
	s.flush(false)
}

// Flush forces an immediate synchronous write of any pending entries. Safe to
// call concurrently with Save; subsequent timer-fired writes within the
// debounce window are coalesced as usual.
func (s *TerminalHistoryStore) Flush() error {
	return s.flush(true)
}

// flush performs the actual write. If sync is true it runs in the caller
// goroutine (used by Flush); otherwise it runs in the timer goroutine (or
// the shutdown goroutine for Close).
func (s *TerminalHistoryStore) flush(sync bool) error {
	s.mu.Lock()
	if sync {
		// Cancel any pending timer so a concurrent fire doesn't double-write.
		if s.timer != nil {
			s.timer.Stop()
		}
	}
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return nil
	}
	entries := s.pending
	s.pending = nil
	last := s.lastSavedHash
	s.mu.Unlock()
	h, err := s.writeAtomic(entries, last)
	s.mu.Lock()
	s.lastSavedHash = h
	s.mu.Unlock()
	return err
}

// writeAtomic dedups and trims, then writes through the shared Save envelope
// (filestore.go): hash-skip, encrypt, atomic write.
func (s *TerminalHistoryStore) writeAtomic(entries []HistoryEntry, lastSavedHash string) (string, error) {
	seen := make(map[string]bool)
	result := make([]HistoryEntry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Command == "" || seen[entry.Command] {
			continue
		}
		seen[entry.Command] = true
		result = append([]HistoryEntry{entry}, result...)
	}
	if len(result) > maxHistorySize {
		result = result[len(result)-maxHistorySize:]
	}
	data := TerminalHistoryData{Entries: result}
	return saveEncryptedFile(terminalHistoryFileName, s.filePath(), &data, nil, lastSavedHash)
}

// flushLoop waits for Close.
func (s *TerminalHistoryStore) flushLoop() {
	defer close(s.done)
	<-s.stop
	s.mu.Lock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
	// Final synchronous flush before signaling shutdown. Bounded so a hung
	// disk cannot block app shutdown forever.
	done := make(chan struct{})
	go func() { _ = s.flush(false); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// disk is hung — let the app exit anyway
	}
}

// Close stops the debounce loop. Idempotent and bounded — a hung flushLoop
// (e.g. blocked on disk I/O or a stuck timer callback) cannot block shutdown
// for more than 2 s. Subsequent calls return immediately.
func (s *TerminalHistoryStore) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if !closed {
			close(s.stop)
		}
	})
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		// flushLoop never finished — abandon and let the process exit
	}
	return nil
}

func (s *TerminalHistoryStore) Load() ([]HistoryEntry, error) {
	out, _, hash, existed, err := loadEncryptedFile[TerminalHistoryData](terminalHistoryFileName, s.filePath(), nil)
	if !existed || err != nil {
		return []HistoryEntry{}, nil
	}
	s.mu.Lock()
	s.lastSavedHash = hash
	s.mu.Unlock()
	return out.Entries, nil
}

func (s *TerminalHistoryStore) DeleteByIDs(ids []string) error {
	if err := s.Flush(); err != nil {
		return err
	}
	entries, err := s.Load()
	if err != nil {
		return err
	}
	idSet := make(map[string]bool)
	for _, id := range ids {
		idSet[id] = true
	}
	filtered := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if !idSet[entry.ID] {
			filtered = append(filtered, entry)
		}
	}
	s.mu.Lock()
	last := s.lastSavedHash
	s.mu.Unlock()
	h, err := s.writeAtomic(filtered, last)
	s.mu.Lock()
	s.lastSavedHash = h
	if err == nil {
		s.pending = nil
		if s.timer != nil {
			s.timer.Stop()
		}
	}
	s.mu.Unlock()
	return err
}