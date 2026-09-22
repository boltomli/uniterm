package importer

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ys-ll/uniterm/backend/session"
)

// parseVirtViewer imports a .vv connection file — the INI format SPICE/VNC
// remote display clients use to hand out one-shot desktop sessions, typically
// downloaded from a hypervisor VM console page. The file carries a mandatory
// [virt-viewer] INI section (the literal section token of the format).
//
// Only the statically importable subset is mapped: type spice/vnc with
// host/port/password/username. Type "ovirt" resolves its real SPICE endpoint
// through a management REST API, so it cannot be imported as-is;
// unix-path-only files have no host/port model; TLS ports are imported
// (with a warning) because the built-in sessions bridge over plaintext.
func parseVirtViewer(data []byte, srcPath string) (*ImportResult, error) {
	res := &ImportResult{}
	text := string(data)

	typ := strings.ToLower(iniVal(text, "virt-viewer", "type"))
	switch typ {
	case "spice", "vnc":
	case "ovirt":
		res.Warnings = append(res.Warnings, "skipped .vv type \"ovirt\": the connection target is resolved via a management REST API and cannot be imported statically")
		return res, nil
	case "":
		return nil, fmt.Errorf("missing [virt-viewer] type key")
	default:
		res.Warnings = append(res.Warnings, "skipped unsupported .vv type "+typ)
		return res, nil
	}

	host := iniVal(text, "virt-viewer", "host")
	if host == "" {
		if iniVal(text, "virt-viewer", "unix-path") != "" {
			res.Warnings = append(res.Warnings, "skipped unix-socket .vv: no host/port to import")
		} else {
			res.Warnings = append(res.Warnings, "skipped .vv without host")
		}
		return res, nil
	}

	port := 0
	warn := ""
	if p, err := strconv.Atoi(iniVal(text, "virt-viewer", "port")); err == nil && p > 0 {
		port = p
	} else if p, err := strconv.Atoi(iniVal(text, "virt-viewer", "tls-port")); err == nil && p > 0 {
		port = p
		warn = "only a tls-port is present in the .vv; the built-in SPICE/VNC sessions connect without TLS so the connection may fail"
	}
	if port == 0 {
		port = 5900
	}

	name := iniVal(text, "virt-viewer", "title")
	if name == "" {
		base := filepath.Base(srcPath)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	res.Connections = append(res.Connections, session.ConnectionConfig{
		ID:       newConnectionID(),
		Name:     name,
		Type:     typ,
		Host:     host,
		Port:     port,
		User:     iniVal(text, "virt-viewer", "username"),
		AuthType: "password",
		Password: iniVal(text, "virt-viewer", "password"),
	})
	if warn != "" {
		res.Warnings = append(res.Warnings, warn)
	}
	return res, nil
}
