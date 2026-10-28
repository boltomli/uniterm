package netscan

import (
	"os/exec"
	"regexp"
	"strings"
)

// readARPTable returns the system ARP cache as an IP → MAC map. It shells out
// to `arp -a` (present on Windows/macOS/Linux-with-net-tools), falling back to
// `ip neigh` where arp is missing. The parser matches only the IP/MAC pair
// pattern, so it is immune to localized column headers and layout differences
// (e.g. German or Chinese `arp -a` output).
func readARPTable() map[string]string {
	if out, err := arpCommand().Output(); err == nil {
		return parseARPTable(string(out))
	}
	// Minimal Linux systems without net-tools: `ip neigh` prints lines like
	// "10.0.0.1 dev eth0 lladdr aa:bb:cc:dd:ee:ff REACHABLE" — same regex fits.
	if out, err := exec.Command("ip", "neigh").Output(); err == nil {
		return parseARPTable(string(out))
	}
	return nil
}

// arpLineRE matches "10.0.0.1  90-2E-16-B5-8E-4F" (Windows) and
// "10.0.0.1 ether 90:2e:16:b5:8e:4f ..." (Linux/macOS) style pairs.
var arpLineRE = regexp.MustCompile(`(\d{1,3}(?:\.\d{1,3}){3})\D+([0-9A-Fa-f]{2}(?:[-:][0-9A-Fa-f]{2}){5})`)

func parseARPTable(out string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		mm := arpLineRE.FindStringSubmatch(line)
		if mm == nil {
			continue
		}
		mac := strings.ToUpper(strings.ReplaceAll(mm[2], "-", ":"))
		// Skip broadcast and multicast entries.
		if mac == "FF:FF:FF:FF:FF:FF" ||
			strings.HasPrefix(mac, "01:00:5E") || strings.HasPrefix(mac, "33:33:") {
			continue
		}
		m[mm[1]] = mac
	}
	return m
}
