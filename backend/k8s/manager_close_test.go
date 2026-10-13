package k8s

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Close must stop the background sweeper, cancel every in-flight watch/log
// stream (they are bound to context.Background() and otherwise outlive the
// app), and drop every connection — promptly, even while a watch handler
// still holds its connection open. (security.md 残留：shutdown 未主动停流)
func TestManagerCloseStopsStreamsAndDisconnects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") == "true" {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			// Hang until the client goes away: only ctx cancellation
			// (i.e. Manager.Close) unblocks this.
			<-r.Context().Done()
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	raw := []byte(fmt.Sprintf(`
apiVersion: v1
kind: Config
current-context: t
contexts:
- name: t
  context: {cluster: c, user: u}
clusters:
- name: c
  cluster: {server: %s, insecure-skip-tls-verify: true}
users:
- name: u
  user: {token: xyz}
`, srv.URL))

	m := NewManager()
	ended := make(chan string, 4)
	m.SetEventEmitter(func(name string, _ any) {
		if strings.Contains(name, "-end:") {
			ended <- name
		}
	})

	connID, err := m.Connect(context.Background(), raw, "t")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	watchID, err := m.StartWatch(connID, "/api/v1/pods?watch=true")
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	// Wait until the server side actually sees the hanging watch, so Close
	// provably tears down an ESTABLISHED stream, not a not-yet-started one.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, err := m.Request(context.Background(), connID, "GET", "/watch-probe", nil, ""); err == nil {
			break // handler is running; the probe just returns 200
		}
		if time.Now().After(deadline) {
			t.Fatal("watch stream never reached the server")
		}
		time.Sleep(10 * time.Millisecond)
	}

	done := make(chan struct{})
	go func() {
		m.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Manager.Close blocked on an in-flight watch")
	}

	// The watch's terminal event must arrive: ctx was cancelled.
	select {
	case name := <-ended:
		if !strings.Contains(name, watchID) {
			t.Fatalf("end event %q does not belong to watch %q", name, watchID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not end after Close")
	}

	// Connections are gone.
	if _, _, err := m.Request(context.Background(), connID, "GET", "/version", nil, ""); err == nil {
		t.Fatal("Request after Close should fail")
	}

	// Idempotent: a second Close must not panic (double-close of quit, etc.).
	m.Close()
}
