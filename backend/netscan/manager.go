package netscan

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Manager owns the lifecycle of all running scans (IP-range and port scans).
// Each scan gets a unique id and a cancellable context; Stop/StopAll cancel
// the workers so no goroutines leak when a tab closes or the app exits.
type Manager struct {
	mu      sync.Mutex
	scans   map[string]context.CancelFunc
	counter atomic.Uint64
	emitter Emitter
}

func NewManager() *Manager {
	return &Manager{scans: make(map[string]context.CancelFunc)}
}

// SetEventEmitter wires the manager to the frontend event bus.
func (m *Manager) SetEventEmitter(fn Emitter) {
	m.emitter = fn
}

func (m *Manager) emit(name string, payload any) {
	if m.emitter != nil {
		m.emitter(name, payload)
	}
}

// newScan registers a new cancellable scan and returns its id and context.
func (m *Manager) newScan() (string, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	id := fmt.Sprintf("scan-%d", m.counter.Add(1))
	m.mu.Lock()
	m.scans[id] = cancel
	m.mu.Unlock()
	return id, ctx
}

// finish deregisters a scan when its goroutine ends.
func (m *Manager) finish(id string) {
	m.mu.Lock()
	delete(m.scans, id)
	m.mu.Unlock()
}

// Stop cancels a running scan. A no-op for unknown or finished ids, so the
// frontend can call it unconditionally on tab close.
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	cancel, ok := m.scans[id]
	m.mu.Unlock()
	if ok {
		cancel()
	}
}

// StopAll cancels every running scan (app-shutdown safety net).
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cancel := range m.scans {
		cancel()
	}
	m.scans = make(map[string]context.CancelFunc)
}
