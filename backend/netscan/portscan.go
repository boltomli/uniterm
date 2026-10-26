package netscan

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// commonPorts is the "known ports" list for the quick scan mode.
var commonPorts = []int{
	21, 22, 23, 25, 53, 80, 110, 135, 139, 143, 443, 445, 993, 995,
	1433, 1521, 3306, 3389, 5432, 5900, 6379, 8080, 8443, 27017,
}

// serviceNames maps well-known ports to service names for display.
var serviceNames = map[int]string{
	21: "ftp", 22: "ssh", 23: "telnet", 25: "smtp", 53: "dns", 80: "http",
	110: "pop3", 135: "epmap", 139: "netbios-ssn", 143: "imap", 443: "https",
	445: "microsoft-ds", 993: "imaps", 995: "pop3s", 1433: "ms-sql-s",
	1521: "oracle", 3306: "mysql", 3389: "rdp", 5432: "postgresql",
	5900: "vnc", 6379: "redis", 8080: "http-proxy", 8443: "https-alt",
	27017: "mongodb",
}

const (
	portScanWorkers   = 128
	// portProbeTimeout tolerates the slow first TCP connect to a target (DNS /
	// ARP resolution, routing table warm-up) so a cold scan isn't lost to timeouts.
	portProbeTimeout  = 1200 * time.Millisecond
	progressInterval  = 200 * time.Millisecond
	progressBatchSize = 512
)

// StartPortScan launches an asynchronous port scan against one host. When
// commonOnly is true only the known-ports list is scanned; otherwise the
// inclusive range [startPort, endPort] (clamped to 1-65535).
func (m *Manager) StartPortScan(host string, commonOnly bool, startPort, endPort int) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("host is required")
	}
	var ports []int
	if commonOnly {
		ports = append([]int(nil), commonPorts...)
	} else {
		if startPort < 1 {
			startPort = 1
		}
		if endPort > 65535 {
			endPort = 65535
		}
		if endPort < startPort {
			return "", fmt.Errorf("invalid port range")
		}
		ports = make([]int, 0, endPort-startPort+1)
		for p := startPort; p <= endPort; p++ {
			ports = append(ports, p)
		}
	}

	id, ctx := m.newScan()
	go m.runPortScan(ctx, id, host, ports)
	return id, nil
}

func (m *Manager) runPortScan(ctx context.Context, id, host string, ports []int) {
	defer m.finish(id)
	total := len(ports)

	var (
		wg       sync.WaitGroup
		done     atomic.Int32
		emitMu   sync.Mutex
		lastEmit time.Time
	)
	sem := make(chan struct{}, portScanWorkers)

	// emitProgress throttles progress events: at most one per
	// progressInterval, unless force is set (final update).
	emitProgress := func(n int32, force bool) {
		emitMu.Lock()
		defer emitMu.Unlock()
		if !force && time.Since(lastEmit) < progressInterval && n%progressBatchSize != 0 {
			return
		}
		lastEmit = time.Now()
		m.emit(EventProgress, map[string]any{"scanId": id, "kind": "port", "done": n, "total": total})
	}

	for _, p := range ports {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(port int) {
			defer wg.Done()
			defer func() { <-sem }()
			if probeTCP(ctx, host, port, portProbeTimeout) == portOpen {
				m.emit(EventPortOpen, map[string]any{
					"scanId":  id,
					"port":    port,
					"service": serviceNames[port],
					"state":   "listening",
				})
			}
			emitProgress(done.Add(1), false)
		}(p)
	}
	wg.Wait()
	emitProgress(done.Load(), true)

	reason := "finished"
	if ctx.Err() != nil {
		reason = "stopped"
	}
	m.emit(EventDone, map[string]any{"scanId": id, "kind": "port", "reason": reason})
}
