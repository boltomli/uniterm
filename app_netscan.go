package main

// app_netscan.go exposes the Network Scanner tool (IP-range scan + per-host
// port scan) to the frontend. Scans run asynchronously; results stream back
// as "netscan:*" events carrying the scanId returned here.

// StartIPScan scans [startIP, endIP] for alive hosts (TCP liveness, service
// ports, MAC from the ARP cache, reverse-DNS hostname). Returns the scan id.
func (a *App) StartIPScan(startIP, endIP string) (string, error) {
	return a.netscanManager.StartIPScan(startIP, endIP)
}

// StopIPScan cancels a running IP scan. A no-op for unknown/finished ids.
func (a *App) StopIPScan(scanId string) {
	a.netscanManager.Stop(scanId)
}

// StartPortScan scans one host's ports: the known-ports list when commonOnly
// is true, otherwise the inclusive range [startPort, endPort] (1-65535).
// Returns the scan id.
func (a *App) StartPortScan(host string, commonOnly bool, startPort, endPort int) (string, error) {
	return a.netscanManager.StartPortScan(host, commonOnly, startPort, endPort)
}

// StopPortScan cancels a running port scan. A no-op for unknown/finished ids.
func (a *App) StopPortScan(scanId string) {
	a.netscanManager.Stop(scanId)
}
