package importer

import (
	"strconv"
	"strings"

	"github.com/ys-ll/uniterm/backend/session"
)

// MobaXterm bookmark values look like `#<icon>#<subtype>%<field>%...`. The
// leading icon number changes between releases, so the session type is read
// from the subtype — the first payload field — which follows MobaXterm's own
// session-type enum.
var mobaSubtypeToType = map[string]string{
	"0": "ssh", "1": "telnet", "4": "rdp", "5": "vnc", "6": "ftp", "7": "sftp",
	"8": "serial", "10": "local", "12": "mosh", "13": "s3", "14": "wsl",
}

// MobaXterm session types with no uniTerm equivalent.
var mobaSubtypeNames = map[string]string{
	"2": "Rsh", "3": "Xdmcp", "9": "File", "11": "Browser",
}

func parseMobaXterm(data []byte) (*ImportResult, error) {
	res := &ImportResult{}
	pathMap := map[string]string{}
	text := decodeBytes(data)
	section := ""
	subRep := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			subRep = ""
			continue
		}
		if !strings.HasPrefix(section, "Bookmarks") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		name := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])
		if name == "SubRep" {
			subRep = value
			continue
		}
		if !strings.HasPrefix(value, "#") {
			continue
		}
		rest := strings.TrimPrefix(value, "#")
		typeEnd := strings.Index(rest, "#")
		if typeEnd < 0 {
			continue
		}
		payload := rest[typeEnd+1:]
		fields := strings.Split(payload, "%")
		subtype := ""
		if len(fields) > 0 {
			subtype = strings.TrimSpace(fields[0])
		}
		typ, ok := mobaSubtypeToType[subtype]
		if !ok {
			skipped := subtype
			if skipped == "" {
				skipped = "unknown"
			}
			if label, ok := mobaSubtypeNames[subtype]; ok {
				skipped += " (" + label + ")"
			}
			res.Warnings = append(res.Warnings, "skipped MobaXterm type "+skipped)
			continue
		}
		field := func(i int) string {
			if i >= len(fields) {
				return ""
			}
			v := strings.TrimSpace(fields[i])
			if v == "-1" || v == "<none>" {
				return ""
			}
			return v
		}
		host, user := field(1), field(3)
		port, _ := strconv.Atoi(field(2))
		if port < 1 || port > 65535 {
			port = defaultPort(typ)
		}
		conn := session.ConnectionConfig{
			ID: newConnectionID(), Name: name, Type: typ, Host: host, Port: port, User: user,
		}
		switch typ {
		case "ssh", "sftp", "mosh":
			conn.AuthType = "password"
			if conn.User == "" {
				conn.User = "root"
			}
			// fields[14] is the private key file ("Use private key"); MobaXterm
			// typically stores a PuTTY .ppk, which the SSH layer reads directly.
			if kp := field(14); kp != "" {
				conn.AuthType = "key"
				conn.KeyPath = kp
			}
		case "ftp":
			conn.AuthType = "password"
		case "serial":
			conn.Host, conn.Port, conn.User = host, 0, ""
			conn.SerialPort = host
			baud, err := strconv.Atoi(field(2))
			if err != nil || baud <= 0 {
				baud = 115200
			}
			conn.SerialBaudRate = baud
		case "local":
			conn.Host, conn.Port, conn.User = "", 0, ""
		case "wsl":
			conn.Host, conn.Port, conn.User = "", 0, ""
			if host != "" {
				conn.ShellPath = "wsl://" + host
			}
		}
		segs := splitNonEmpty(strings.ReplaceAll(subRep, "\\", "/"), "/")
		if gid := ensureGroupPath(segs, pathMap, &res.Groups, newGroupID); gid != nil {
			conn.GroupId = gid
		}
		res.Connections = append(res.Connections, conn)
	}
	return res, nil
}
