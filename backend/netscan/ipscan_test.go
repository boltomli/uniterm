package netscan

import (
	"sync"
	"testing"
	"time"
)

// TestRunIPScanEnrichesWithoutBlocking probes loopback only: closed ports on
// 127.0.0.1 answer with RST, so the host is always reported alive without any
// firewall involvement. Verifies the scan still finds the host and finishes
// after the enrichment pass (hostname/MAC) moved off the probe hot path.
func TestRunIPScanEnrichesWithoutBlocking(t *testing.T) {
	var (
		mu    sync.Mutex
		hosts []ScanHost
		done  bool
	)
	m := NewManager()
	m.SetEventEmitter(func(name string, payload any) {
		p := payload.(map[string]any)
		switch name {
		case EventHostFound:
			mu.Lock()
			hosts = append(hosts, p["host"].(ScanHost))
			mu.Unlock()
		case EventDone:
			mu.Lock()
			done = true
			mu.Unlock()
		}
	})
	id, err := m.StartIPScan("127.0.0.1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(15 * time.Second)
	for {
		mu.Lock()
		finished, found := done, len(hosts)
		mu.Unlock()
		if finished {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("scan %s did not finish in time (hosts so far: %d)", id, found)
		case <-time.After(20 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hosts) == 0 {
		t.Fatal("loopback host not reported alive")
	}
	if hosts[0].IP != "127.0.0.1" {
		t.Fatalf("unexpected host %q", hosts[0].IP)
	}
	// The enrichment pass re-emits when a hostname was resolved; either one
	// or two events for the same IP are fine, nothing more.
	if len(hosts) > 2 {
		t.Fatalf("host re-emitted too often: %d events", len(hosts))
	}
}
