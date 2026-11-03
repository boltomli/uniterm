package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSftpAndScpPinHostKeyUnderOriginalAddr: jump-host tunnels rewrite
// Host/Port to 127.0.0.1:<ephemeral> before the session dials
// (app_terminal.go setupJumpHostTunnel). Every verifier call site must verify
// under the original host:port via hostKeyVerifyTarget, or each connection
// through a bastion is a fresh first-trust of whatever the tunnel endpoint
// presents — a compromised jump host can MITM the target and capture its
// password, and known_hosts gains one entry per ephemeral port (security.md P1).
func TestSftpAndScpPinHostKeyUnderOriginalAddr(t *testing.T) {
	for _, file := range []string{"sftp_session.go", "scp_session.go", "ssh_session.go", "mosh_session.go"} {
		path := filepath.Join(".", file)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		src := string(data)
		if strings.Contains(src, "HostKeyCallback(config.Host,") {
			t.Errorf("%s verifies under the rewritten dial address; "+
				"use HostKeyCallback(verifyHost, ...) with hostKeyVerifyTarget(config)", file)
		}
		if !strings.Contains(src, "hostKeyVerifyTarget(config)") {
			t.Errorf("%s does not call hostKeyVerifyTarget(config)", file)
		}
	}
}
