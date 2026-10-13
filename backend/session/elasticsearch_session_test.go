package session

import (
	"net/http"
	"testing"
)

// recordingTransport is an http.RoundTripper that records idle-connection
// cleanup. The requests themselves never happen: these tests only exercise
// Disconnect.
type recordingTransport struct {
	idleCloses int
}

func (rt *recordingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrUseLastResponse
}

func (rt *recordingTransport) CloseIdleConnections() { rt.idleCloses++ }

// Disconnect must release the keep-alive sockets before dropping the client,
// otherwise they linger for the transport's IdleConnTimeout (90 s by
// construction) after the session is gone.
func TestElasticsearchDisconnectClosesIdleConnections(t *testing.T) {
	rt := &recordingTransport{}
	s := NewElasticsearchSession("es-idle-close")
	s.mu.Lock()
	s.closed = false
	s.httpClient = &http.Client{Transport: rt}
	s.baseURL = "http://127.0.0.1:9200"
	s.mu.Unlock()
	s.setStatus(StatusConnected)

	if err := s.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if rt.idleCloses == 0 {
		t.Error("Disconnect dropped the http.Client without closing its idle keep-alive connections")
	}
	s.mu.RLock()
	client := s.httpClient
	s.mu.RUnlock()
	if client != nil {
		t.Error("httpClient still set after Disconnect")
	}
}
