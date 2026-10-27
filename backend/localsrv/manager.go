package localsrv

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Manager owns the lifecycle of all running local servers (HTTP, TFTP, SSH,
// FTP). Each started server gets a unique id and a cancellable context;
// Stop/StopAll cancel the workers so no goroutines leak when the dialog closes
// or the app exits.
type Manager struct {
	mu       sync.Mutex
	servers  map[string]*runningServer
	counter  atomic.Uint64
	emitter  Emitter
}

// runningServer pairs a server's cancel func with a stop func that closes the
// underlying listener / socket. timer holds the auto-stop scheduler, if any.
type runningServer struct {
	cancel context.CancelFunc
	stop   func()
	timer  *time.Timer
}

func NewManager() *Manager {
	return &Manager{servers: make(map[string]*runningServer)}
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

// log emits a timestamped log line for a server.
func (m *Manager) log(service Service, id, message string) {
	m.emit(EventLog, map[string]any{
		"service": string(service),
		"id":      id,
		"message": message,
	})
}

// state emits the running state of a server.
func (m *Manager) state(service Service, id string, running bool) {
	m.emit(EventState, map[string]any{
		"service": string(service),
		"id":      id,
		"running": running,
	})
}

// newServer registers a new cancellable server and returns its id and context.
func (m *Manager) newServer() (string, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	id := fmt.Sprintf("srv-%d", m.counter.Add(1))
	m.mu.Lock()
	m.servers[id] = &runningServer{cancel: cancel}
	m.mu.Unlock()
	return id, ctx
}

// setStop records the stop function for a server (called once the listener is up).
func (m *Manager) setStop(id string, stop func()) {
	m.mu.Lock()
	if s, ok := m.servers[id]; ok {
		s.stop = stop
	}
	m.mu.Unlock()
}

// finish deregisters a server when its goroutine ends.
func (m *Manager) finish(id string) {
	m.mu.Lock()
	delete(m.servers, id)
	m.mu.Unlock()
}

// Stop cancels a running server. A no-op for unknown or finished ids, so the
// frontend can call it unconditionally.
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	s, ok := m.servers[id]
	if ok && s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	m.mu.Unlock()
	if ok {
		if s.stop != nil {
			s.stop()
		}
		s.cancel()
	}
}

// StopAll cancels every running server (app-shutdown safety net).
func (m *Manager) StopAll() {
	m.mu.Lock()
	servers := make([]*runningServer, 0, len(m.servers))
	for _, s := range m.servers {
		if s.timer != nil {
			s.timer.Stop()
			s.timer = nil
		}
		servers = append(servers, s)
	}
	m.servers = make(map[string]*runningServer)
	m.mu.Unlock()
	for _, s := range servers {
		if s.stop != nil {
			s.stop()
		}
		s.cancel()
	}
}

// startTimeout arms an auto-stop timer for a running server. After seconds the
// server is logged and stopped via Stop(). Passing seconds <= 0 is a no-op.
// The timer is cancelled when Stop/StopAll is called manually, so a manual
// stop never races the timer.
func (m *Manager) startTimeout(id string, service Service, seconds int) {
	if seconds <= 0 {
		return
	}
	m.mu.Lock()
	s, ok := m.servers[id]
	m.mu.Unlock()
	if !ok {
		return
	}
	m.log(service, id, fmt.Sprintf("Auto-stop in %d seconds", seconds))
	s.timer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		m.log(service, id, fmt.Sprintf("Auto-stop after %d seconds timeout", seconds))
		m.Stop(id)
	})
}
