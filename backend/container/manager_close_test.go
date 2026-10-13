package container

import (
	"sync"
	"testing"
	"time"
)

// recordingStream is a minimal LineStream that records Close.
type recordingStream struct {
	lines    chan string
	closed   chan struct{}
	closeOne sync.Once
}

func (s *recordingStream) Lines() <-chan string { return s.lines }
func (s *recordingStream) Wait() error {
	<-s.closed
	return nil
}
func (s *recordingStream) Close() error {
	s.closeOne.Do(func() { close(s.closed) })
	return nil
}

// Close must stop every in-flight stream (logs/pull/push — their goroutines
// run on context.Background() and otherwise outlive the app) and disconnect
// every connection, and it must be safe to call twice. (security.md 残留：
// shutdown 未主动停流)
func TestManagerCloseStopsStreamsAndDisconnects(t *testing.T) {
	m := NewManager()
	p := NewProvider(RuntimeDocker, "", &fakeRunner{})

	st := &recordingStream{lines: make(chan string), closed: make(chan struct{})}
	m.mu.Lock()
	m.conns["c1"] = &conn{id: "c1", provider: p}
	m.streams["s1"] = &streamHandle{connID: "c1", stream: st}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Manager.Close blocked")
	}

	select {
	case <-st.closed:
	case <-time.After(time.Second):
		t.Fatal("stream was not closed by Manager.Close")
	}

	m.mu.RLock()
	nConns, nStreams := len(m.conns), len(m.streams)
	m.mu.RUnlock()
	if nConns != 0 || nStreams != 0 {
		t.Fatalf("after Close: conns=%d streams=%d, want 0/0", nConns, nStreams)
	}

	// Idempotent.
	m.Close()
}
