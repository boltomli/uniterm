package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ys-ll/uniterm/backend/container"
	"github.com/ys-ll/uniterm/backend/k8s"
)

// shutdown must tear down the k8s/container managers: their watch/log
// streams run on context.Background() and would otherwise outlive the app.
// (security.md 残留：shutdown 未主动停流)
func TestShutdownClosesK8sAndContainerManagers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") == "true" {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done() // hangs until the client's ctx is cancelled
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

	a := &App{webviewDataPath: t.TempDir()}
	a.k8sManager = k8s.NewManager()
	a.containerManager = container.NewManager()

	ended := make(chan string, 4)
	a.k8sManager.SetEventEmitter(func(name string, _ any) {
		if strings.Contains(name, "-end:") {
			ended <- name
		}
	})
	connID, err := a.k8sManager.Connect(context.Background(), raw, "t")
	if err != nil {
		t.Fatalf("k8s Connect: %v", err)
	}
	if _, err := a.k8sManager.StartWatch(connID, "/api/v1/pods?watch=true"); err != nil {
		t.Fatalf("StartWatch: %v", err)
	}

	done := make(chan struct{})
	go func() {
		a.shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown blocked on in-flight k8s streams")
	}

	// The live watch must have been cancelled by shutdown.
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("k8s watch survived shutdown")
	}
	if _, _, err := a.k8sManager.Request(context.Background(), connID, "GET", "/version", nil, ""); err == nil {
		t.Fatal("k8s connection survived shutdown")
	}
}
