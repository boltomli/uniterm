package k8s

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestManagerConnectAndRequest(t *testing.T) {
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	caB64 := base64.StdEncoding.EncodeToString(ca)
	raw := []byte(fmt.Sprintf(`
apiVersion: v1
kind: Config
current-context: t
contexts:
- name: t
  context: {cluster: c, user: u}
clusters:
- name: c
  cluster: {server: %s, certificate-authority-data: %s}
users:
- name: u
  user: {token: xyz}
`, srv.URL, caB64))

	m := NewManager()
	m.SetEventEmitter(func(string, any) {}) // no-op

	connID, err := m.Connect(context.Background(), raw, "t")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	status, body, err := m.Request(context.Background(), connID, "GET", "/version", nil, "")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if status != 200 || string(body) != `{"ok":true}` {
		t.Errorf("status=%d body=%s", status, body)
	}
	m.Disconnect(connID)
	if _, _, err := m.Request(context.Background(), connID, "GET", "/", nil, ""); err == nil {
		t.Error("Request after Disconnect should fail")
	}
}

func TestManagerListContexts(t *testing.T) {
	raw := []byte(`
apiVersion: v1
kind: Config
current-context: a
contexts:
- name: a
  context: {cluster: c1, user: u1, namespace: ns-a}
- name: b
  context: {cluster: c2, user: u2}
`)
	m := NewManager()
	ctxs, err := m.ListContexts(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(ctxs) != 2 {
		t.Fatalf("len = %d", len(ctxs))
	}
}

func TestManagerWatchEmitsEvents(t *testing.T) {
	srv, ca := startTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.WriteHeader(200)
		fmt.Fprintln(w, `{"type":"ADDED","object":{"kind":"Pod"}}`)
		flusher.Flush()
		time.Sleep(20 * time.Millisecond)
	}))
	defer srv.Close()
	caB64 := base64.StdEncoding.EncodeToString(ca)
	raw := []byte(fmt.Sprintf(`
apiVersion: v1
kind: Config
current-context: t
contexts: [{name: t, context: {cluster: c, user: u}}]
clusters: [{name: c, cluster: {server: %s, certificate-authority-data: %s}}]
users: [{name: u, user: {token: x}}]
`, srv.URL, caB64))

	m := NewManager()
	var mu sync.Mutex
	var got []string
	m.SetEventEmitter(func(name string, payload any) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, name)
	})
	connID, _ := m.Connect(context.Background(), raw, "t")
	watchID, err := m.StartWatch(connID, "/api/v1/pods?watch=true")
	if err != nil {
		t.Fatal(err)
	}
	// 轮询等待首个事件到达（避免固定 sleep 造成的偶发 flake）
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.StopWatch(watchID)
	m.Disconnect(connID)

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 || got[0] != "k8s:watch:"+watchID {
		t.Errorf("events = %v", got)
	}
}

// cacheTestKubeconfig is a small valid kubeconfig for cache-expiry tests;
// it needs no base64 fields, so it can be a plain literal.
const cacheTestKubeconfig = `
apiVersion: v1
kind: Config
current-context: dev
contexts:
- name: dev
  context:
    cluster: dev-cluster
    user: dev-user
clusters:
- name: dev-cluster
  cluster:
    server: https://dev.example.com:6443
    insecure-skip-tls-verify: true
users:
- name: dev-user
  user:
    token: dev-token-xyz
`

// TestDialExecHandshakeTimeout guards the fix: DialExec used a zero
// websocket.Dialer (no HandshakeTimeout, Dial without ctx), so an apiserver
// that accepts the TCP connection but never answers the upgrade blocked the
// terminal open forever. execDialHandshakeTimeout bounds it; the test shrinks
// it so the wait stays short.
func TestDialExecHandshakeTimeout(t *testing.T) {
	old := execDialHandshakeTimeout
	execDialHandshakeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { execDialHandshakeTimeout = old })

	// Accept TCP but never complete the HTTP upgrade.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open, never respond.
			defer conn.Close()
		}
	}()

	m := NewManager()
	m.mu.Lock()
	m.conns["c1"] = &connection{
		id:     "c1",
		client: &http.Client{},
		base:   "http://" + ln.Addr().String(),
	}
	m.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := m.DialExec("c1", "ns", "pod", "container", 80, 24)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("DialExec succeeded against a server that never completed the upgrade")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DialExec did not return; handshake is unbounded again")
	}
}

// TestKubeconfigCacheExpires guards the fix: the cache was never evicted, so
// parsed kubeconfigs (including client keys) lived for the life of the
// process. Entries now expire after kubeconfigCacheTTL; the test shrinks the
// TTL and observes a re-parse via pointer identity.
func TestKubeconfigCacheExpires(t *testing.T) {
	old := kubeconfigCacheTTL
	kubeconfigCacheTTL = 50 * time.Millisecond
	t.Cleanup(func() { kubeconfigCacheTTL = old })

	m := NewManager()
	raw := []byte(cacheTestKubeconfig)

	first, err := m.cachedParseBytes(raw)
	if err != nil {
		t.Fatalf("cachedParseBytes: %v", err)
	}

	// Within the TTL the cached entry is reused.
	again, err := m.cachedParseBytes(raw)
	if err != nil {
		t.Fatalf("cachedParseBytes: %v", err)
	}
	if again != first {
		t.Fatal("cache missed within the TTL; entries must be reused before expiry")
	}

	// After the TTL the entry must be re-parsed, not served stale forever.
	time.Sleep(kubeconfigCacheTTL + 30*time.Millisecond)
	third, err := m.cachedParseBytes(raw)
	if err != nil {
		t.Fatalf("cachedParseBytes after expiry: %v", err)
	}
	if third == first {
		t.Fatal("expired kubeconfig entry was served from cache; TTL eviction is not working")
	}
	if third.CurrentContext != first.CurrentContext {
		t.Fatalf("re-parsed kubeconfig differs: %q vs %q", third.CurrentContext, first.CurrentContext)
	}
}
