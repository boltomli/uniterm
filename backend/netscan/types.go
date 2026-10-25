// Package netscan implements the Network Scanner tool: an IP-range scan that
// discovers alive hosts (TCP-based liveness, MAC via the system ARP cache,
// reverse-DNS hostnames, common service ports) and a per-host port scan.
// Progress and results are streamed to the frontend as Wails events.
package netscan

// ScanHost describes one alive host found by an IP-range scan.
type ScanHost struct {
	IP       string       `json:"ip"`
	MAC      string       `json:"mac"`
	Hostname string       `json:"hostname"`
	Ports    map[int]bool `json:"ports"` // probed service ports that are open
}

// PortResult is one open port found by a port scan.
type PortResult struct {
	Port    int    `json:"port"`
	Service string `json:"service"`
	State   string `json:"state"` // always "listening"
}

// Emitter forwards scan events to the frontend (wired to Wails' event bus).
type Emitter func(name string, payload any)

// Event names emitted during scans. Every payload carries the "scanId".
const (
	EventHostFound = "netscan:host-found" // {scanId, host: ScanHost}
	EventProgress  = "netscan:progress"   // {scanId, kind, done, total}
	EventPortOpen  = "netscan:port-open"  // {scanId, port, service, state}
	EventDone      = "netscan:done"       // {scanId, kind, reason, message?}
)
