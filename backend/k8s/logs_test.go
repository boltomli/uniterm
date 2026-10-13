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

func TestLogStreamDeliversLines(t *testing.T) {
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/log") {
			t.Errorf("path missing /log: %q", r.URL.Path)
		}
		if !strings.Contains(r.URL.RawQuery, "follow=true") {
			t.Errorf("query missing follow=true: %q", r.URL.RawQuery)
		}
		flusher, _ := w.(http.Flusher)
		w.WriteHeader(200)
		for _, l := range []string{"line-1", "line-2", "line-3"} {
			fmt.Fprintln(w, l)
			flusher.Flush()
			time.Sleep(3 * time.Millisecond)
		}
	}))
	defer srv.Close()

	kc := &Kubeconfig{
		CurrentContext: "t",
		Contexts:       map[string]contextEntry{"t": {Cluster: "c", User: "u"}},
		Clusters:       map[string]clusterEntry{"c": {Server: srv.URL, CertificateAuthorityData: ca}},
		Users:          map[string]userEntry{"u": {Token: "x"}},
	}
	client, base, err := BuildClient(kc, "t")
	if err != nil {
		t.Fatalf("BuildClient: %v", err)
	}

	m := NewManager()
	// 直接注入一个连接，避开 kubeconfig->connection 的 YAML 编解码环节。
	m.mu.Lock()
	m.conns["c1conn"] = &connection{
		id:      "c1conn",
		client:  client,
		base:    base,
		watches: make(map[string]struct{}),
	}
	m.mu.Unlock()

	var mu sync.Mutex
	var got []string
	m.SetEventEmitter(func(name string, payload any) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(name, "k8s:log:") {
			got = append(got, payload.(string))
		}
	})

	sid, err := m.StartLogStream("c1conn", "default", "p1", "c1", 100, false, false)
	if err != nil {
		t.Fatalf("StartLogStream: %v", err)
	}
	if sid == "" {
		t.Fatal("empty streamID")
	}
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("want 3 lines, got %d: %v", len(got), got)
	}
}

// TestLogReconnectDoesNotCallOnEnd (F-404): same contract as
// TestWatchReconnectDoesNotCallOnEnd but for the log stream — a
// transient pod log disconnect fires onReconnect and reopens the
// stream, onEnd is reserved for the terminal case.
func TestLogReconnectDoesNotCallOnEnd(t *testing.T) {
	// Atomic: the handler runs on server goroutines while the test body
	// reads it, so a plain int is a data race under -race.
	var hits atomic.Int32
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		flusher, _ := w.(http.Flusher)
		w.WriteHeader(200)
		if n == 1 {
			fmt.Fprintln(w, "boot-line")
			flusher.Flush()
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
			return
		}
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			fmt.Fprintf(w, "line-%d\n", i)
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
		mu         sync.Mutex
		endCalls   int
		reconCalls int
	)
	ended := make(chan struct{})
	wrappedEnd := func(err error) {
		mu.Lock()
		endCalls++
		mu.Unlock()
		select {
		case <-ended:
			t.Errorf("onEnd called more than once (err=%v)", err)
		default:
			close(ended)
		}
	}
	onReconnect := func(err error) {
		mu.Lock()
		reconCalls++
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := startLogStream(ctx, client, base, "/api/v1/namespaces/default/pods/p1/log?follow=true&container=c",
		func(string) {}, wrappedEnd, onReconnect, make(chan struct{})); err != nil {
		t.Fatalf("startLogStream: %v", err)
	}

	time.Sleep(1500 * time.Millisecond)
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
		t.Errorf("server saw %d requests, want >= 2", hits.Load())
	}

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

func TestBuildLogPath(t *testing.T) {
	// follow 模式（previous=false）：follow=true & previous=false
	p := buildLogPath("default", "p1", "c1", 100, true, false)
	if !strings.HasPrefix(p, "/api/v1/namespaces/default/pods/p1/log?") {
		t.Errorf("bad prefix: %q", p)
	}
	if !strings.Contains(p, "container=c1") ||
		!strings.Contains(p, "follow=true") ||
		!strings.Contains(p, "previous=false") ||
		!strings.Contains(p, "tailLines=100") ||
		!strings.Contains(p, "timestamps=true") {
		t.Errorf("follow path wrong: %q", p)
	}

	// previous 模式：一次性，follow=false & previous=true
	p = buildLogPath("ns2", "p2", "", 50, false, true)
	if !strings.Contains(p, "follow=false") || !strings.Contains(p, "previous=true") {
		t.Errorf("previous path wrong: %q", p)
	}
}

// TestLogStream4xxTerminates guards the fix: a 4xx from the apiserver is
// terminal for pod logs too. The old loop retried forever, so a pod the user
// can no longer read (deleted, RBAC revoked) left the UI stuck on
// "reconnecting" and the goroutine never exited.
func TestLogStream4xxTerminates(t *testing.T) {
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
	err := startLogStream(ctx, client, base, "/api/v1/namespaces/default/pods/p1/log?follow=true&container=c",
		func(string) {},
		func(err error) { ended <- err },
		nil, make(chan struct{}))
	if err != nil {
		t.Fatalf("startLogStream: %v", err)
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
		t.Fatal("log stream did not terminate on 403")
	}
}

// TestLogReconnectUsesSinceTime guards the anti-replay fix: the first
// attempt's tailLines window must not be reused on reconnect (it replays the
// last N lines every time the stream bounces). The loop must resume with
// sinceTime set to the timestamp of the last delivered line.
func TestLogReconnectUsesSinceTime(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		n := len(queries)
		mu.Unlock()
		flusher, _ := w.(http.Flusher)
		if n == 1 {
			// First attempt: one timestamped line, then drop the transport.
			w.WriteHeader(200)
			fmt.Fprintln(w, "2024-01-02T03:04:05.123456789Z boot line")
			flusher.Flush()
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
			return
		}
		// Subsequent attempts: hold the stream open.
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
	err := startLogStream(ctx, client, base,
		"/api/v1/namespaces/default/pods/p1/log?follow=true&container=c&timestamps=true&tailLines=100",
		func(string) {},
		func(error) {},
		func(error) {},
		make(chan struct{}))
	if err != nil {
		t.Fatalf("startLogStream: %v", err)
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
			if !strings.Contains(last, "sinceTime=") {
				t.Fatalf("reconnect query = %q, want sinceTime", last)
			}
			if !strings.Contains(last, "2024-01-02T03%3A04%3A05") {
				t.Fatalf("reconnect query = %q, want sinceTime anchored to the last delivered line", last)
			}
			if strings.Contains(last, "tailLines=") {
				t.Fatalf("reconnect query = %q, must not replay the tailLines window", last)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("log stream never reconnected")
}
