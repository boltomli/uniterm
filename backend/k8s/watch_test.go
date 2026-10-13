package k8s

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchDeliversEvents(t *testing.T) {
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "watch=true") {
			t.Errorf("query missing watch=true: %q", r.URL.RawQuery)
		}
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(200)
		lines := []string{
			`{"type":"ADDED","object":{"kind":"Pod","metadata":{"name":"p1","uid":"u1","resourceVersion":"1"}}}`,
			`{"type":"MODIFIED","object":{"kind":"Pod","metadata":{"name":"p1","uid":"u1","resourceVersion":"2"}}}`,
			`{"type":"DELETED","object":{"kind":"Pod","metadata":{"name":"p1","uid":"u1","resourceVersion":"3"}}}`,
		}
		for _, l := range lines {
			fmt.Fprintln(w, l)
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	kc := &Kubeconfig{
		CurrentContext: "t",
		Contexts:       map[string]contextEntry{"t": {Cluster: "c", User: "u"}},
		Clusters:       map[string]clusterEntry{"c": {Server: srv.URL, CertificateAuthorityData: ca}},
		Users:          map[string]userEntry{"u": {Token: "x"}},
	}
	client, base, _ := BuildClient(kc, "t")

	var mu sync.Mutex
	var got []string
	done := make(chan struct{})
	cb := func(ev WatchEvent) {
		mu.Lock()
		got = append(got, ev.Type)
		mu.Unlock()
	}
	onEnd := func(err error) {
		close(done)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := startWatchStream(ctx, client, base, "/api/v1/namespaces/default/pods?watch=true", cb, onEnd, nil, make(chan struct{}))
	if err != nil {
		t.Fatalf("startWatchStream: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watch timeout")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[0] != "ADDED" || got[2] != "DELETED" {
		t.Errorf("got = %v", got)
	}
}

// TestWatchReconnectDoesNotCallOnEnd (F-404): when the apiserver drops
// the connection mid-stream, the watch loop must fire onReconnect, NOT
// onEnd. onEnd is reserved for the terminal case (ctx cancel, clean EOF)
// — otherwise the manager would drop the watch handle on every retry
// attempt and the UI would never see new events.
func TestWatchReconnectDoesNotCallOnEnd(t *testing.T) {
	// Atomic: the handler runs on server goroutines while the test body
	// reads it, so a plain int is a data race under -race.
	var hits atomic.Int32
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(200)
		// First attempt: send 1 event then drop the connection — simulates
		// apiserver bounce mid-stream.
		if n == 1 {
			fmt.Fprintln(w, `{"type":"ADDED","object":{"metadata":{"resourceVersion":"1"}}}`)
			flusher.Flush()
			// Hijack & close so the scanner sees an EOF, not a clean stream
			// end. Hijacker is httptest.Server safe.
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
			return
		}
		// Subsequent attempts: keep the connection alive with events so
		// the loop has to back off + retry but never reaches onEnd.
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			fmt.Fprintf(w, `{"type":"MODIFIED","object":{"metadata":{"resourceVersion":"%d"}}}`+"\n", i+2)
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	kc := &Kubeconfig{
		CurrentContext: "t",
		Contexts:       map[string]contextEntry{"t": {Cluster: "c", User: "u"}},
		Clusters:       map[string]clusterEntry{"c": {Server: srv.URL, CertificateAuthorityData: ca}},
		Users:          map[string]userEntry{"u": {Token: "x"}},
	}
	client, base, _ := BuildClient(kc, "t")

	var (
		mu          sync.Mutex
		endCalls    int
		reconCalls  int
	)
	endName := "ended"
	reconName := "reconnect"
	onEnd := func(err error) {
		mu.Lock()
		endCalls++
		mu.Unlock()
	}
	onReconnect := func(err error) {
		mu.Lock()
		reconCalls++
		mu.Unlock()
	}
	// Wrap to assert onEnd is called exactly once at terminal cancel.
	ended := make(chan struct{})
	wrappedEnd := func(err error) {
		onEnd(err)
		select {
		case <-ended:
			t.Errorf("onEnd called more than once (err=%v)", err)
		default:
			close(ended)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := startWatchStream(ctx, client, base, "/api/v1/pods?watch=true",
		func(WatchEvent) {}, wrappedEnd, onReconnect, make(chan struct{})); err != nil {
		t.Fatalf("startWatchStream: %v", err)
	}

	// Give the loop time for at least one reconnect attempt.
	time.Sleep(1500 * time.Millisecond)

	// At this point the loop has had one transport failure → at least
	// one reconnect, but should NOT have called onEnd.
	mu.Lock()
	preEnd := endCalls
	preRecon := reconCalls
	mu.Unlock()
	if preEnd != 0 {
		t.Errorf("onEnd called %d times during reconnect (want 0)", preEnd)
	}
	if preRecon == 0 {
		t.Errorf("onReconnect called %d times (want >= 1)", preRecon)
	}
	if hits.Load() < 2 {
		t.Errorf("server saw %d requests, want >= 2 (initial + reconnect)", hits.Load())
	}
	_ = endName
	_ = reconName

	// Now cancel — onEnd must fire exactly once.
	cancel()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("onEnd never called after ctx cancel")
	}
	mu.Lock()
	defer mu.Unlock()
	if endCalls != 1 {
		t.Errorf("onEnd calls = %d, want exactly 1", endCalls)
	}
}

func TestWatchContextCancel(t *testing.T) {
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.WriteHeader(200)
		for i := 0; ; i++ {
			_, err := fmt.Fprintf(w, `{"type":"ADDED","object":{"metadata":{"resourceVersion":"%d"}}}`+"\n", i)
			if err != nil {
				return
			}
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer srv.Close()

	kc := &Kubeconfig{
		CurrentContext: "t",
		Contexts:       map[string]contextEntry{"t": {Cluster: "c", User: "u"}},
		Clusters:       map[string]clusterEntry{"c": {Server: srv.URL, CertificateAuthorityData: ca}},
		Users:          map[string]userEntry{"u": {Token: "x"}},
	}
	client, base, _ := BuildClient(kc, "t")
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	err := startWatchStream(ctx, client, base, "/api/v1/pods?watch=true",
		func(WatchEvent) {},
		func(err error) { close(ended) },
		nil, make(chan struct{}))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-ended:
	case <-time.After(1 * time.Second):
		t.Fatal("cancel did not stop watch")
	}
}

// TestWatch4xxTerminates guards the fix: a 4xx from the apiserver (bad
// resourceVersion, forbidden, gone) is terminal. The old loop treated every
// HTTP error as transient and retried every 30s forever, so a watch that the
// server will never accept again spun in the UI's "reconnecting" state
// indefinitely.
func TestWatch4xxTerminates(t *testing.T) {
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"kind":"Status","code":403,"message":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	kc := &Kubeconfig{
		CurrentContext: "t",
		Contexts:       map[string]contextEntry{"t": {Cluster: "c", User: "u"}},
		Clusters:       map[string]clusterEntry{"c": {Server: srv.URL, CertificateAuthorityData: ca}},
		Users:          map[string]userEntry{"u": {Token: "x"}},
	}
	client, base, _ := BuildClient(kc, "t")

	ended := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := startWatchStream(ctx, client, base, "/api/v1/pods?watch=true",
		func(WatchEvent) {},
		func(err error) { ended <- err },
		nil, make(chan struct{}))
	if err != nil {
		t.Fatalf("startWatchStream: %v", err)
	}

	select {
	case err := <-ended:
		if err == nil {
			t.Fatal("onEnd got nil error for a 403, want the HTTP error")
		}
		if !strings.Contains(err.Error(), "403") {
			t.Fatalf("onEnd error = %q, want it to mention 403", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not terminate on 403")
	}
}

// TestWatch410Retries guards the 410-Gone exception: the resourceVersion expired
// (watch cache too old), which IS recoverable by re-listing/re-watching. The
// loop must retry rather than terminate.
func TestWatch410Retries(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusGone)
			return
		}
		flusher, _ := w.(http.Flusher)
		w.WriteHeader(200)
		fmt.Fprintln(w, `{"type":"ADDED","object":{"metadata":{"resourceVersion":"1"}}}`)
		flusher.Flush()
		// Keep the stream open so only ctx cancel ends it.
		<-r.Context().Done()
	}))
	defer srv.Close()

	kc := &Kubeconfig{
		CurrentContext: "t",
		Contexts:       map[string]contextEntry{"t": {Cluster: "c", User: "u"}},
		Clusters:       map[string]clusterEntry{"c": {Server: srv.URL, CertificateAuthorityData: ca}},
		Users:          map[string]userEntry{"u": {Token: "x"}},
	}
	client, base, _ := BuildClient(kc, "t")

	gotEvent := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := startWatchStream(ctx, client, base, "/api/v1/pods?watch=true",
		func(WatchEvent) {
			select {
			case gotEvent <- struct{}{}:
			default:
			}
		},
		func(error) {},
		func(error) {}, // onReconnect must fire for the 410
		make(chan struct{}))
	if err != nil {
		t.Fatalf("startWatchStream: %v", err)
	}

	select {
	case <-gotEvent: // recovered from 410 via retry
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not retry after 410 Gone")
	}
}

// TestWatchReconnectTracksResourceVersion guards the fix: after a transport
// drop the loop must resume from the last delivered resourceVersion, not from
// the original (stale) one — otherwise the client replays the whole watch
// history after every apiserver bounce.
func TestWatchReconnectTracksResourceVersion(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		n := len(queries)
		mu.Unlock()
		if n == 1 {
			// First request: deliver one event carrying resourceVersion 42,
			// then drop the transport so the loop reconnects.
			flusher, _ := w.(http.Flusher)
			w.WriteHeader(200)
			fmt.Fprintln(w, `{"type":"ADDED","object":{"metadata":{"resourceVersion":"42"}}}`)
			flusher.Flush()
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
			return
		}
		// Second request: just hold the stream open.
		flusher, _ := w.(http.Flusher)
		w.WriteHeader(200)
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	kc := &Kubeconfig{
		CurrentContext: "t",
		Contexts:       map[string]contextEntry{"t": {Cluster: "c", User: "u"}},
		Clusters:       map[string]clusterEntry{"c": {Server: srv.URL, CertificateAuthorityData: ca}},
		Users:          map[string]userEntry{"u": {Token: "x"}},
	}
	client, base, _ := BuildClient(kc, "t")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := startWatchStream(ctx, client, base, "/api/v1/pods?watch=true",
		func(WatchEvent) {},
		func(error) {},
		func(error) {},
		make(chan struct{}))
	if err != nil {
		t.Fatalf("startWatchStream: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(queries)
		var last string
		if n > 0 {
			last = queries[n-1]
		}
		mu.Unlock()
		if n >= 2 {
			if !strings.Contains(last, "resourceVersion=42") {
				t.Fatalf("reconnect query = %q, want resourceVersion=42", last)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("watch never reconnected")
}
