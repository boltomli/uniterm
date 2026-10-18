//go:build !windows

package session

import (
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func localAgentAuthMethod() (ssh.AuthMethod, func(), error) {
	client, cleanup, err := localAgentClient()
	if err != nil {
		return nil, cleanup, err
	}
	return ssh.PublicKeysCallback(client.Signers), cleanup, nil
}

// localAgentClient connects to the agent at SSH_AUTH_SOCK. The returned
// cleanup closes the connection; signers obtained from the client stay usable
// until then.
func localAgentClient() (agent.Agent, func(), error) {
	addr, err := localAgentSocket()
	if err != nil {
		return nil, func() {}, err
	}
	conn, err := net.Dial("unix", addr)
	if err != nil {
		return nil, func() {}, fmt.Errorf("connect to SSH_AUTH_SOCK %q: %w", addr, err)
	}
	return agent.NewClient(conn), func() { _ = conn.Close() }, nil
}

// localAgentSocket returns the Unix socket exported by the SSH agent in the
// uniTerm process environment. On macOS this is normally inherited from
// launchd; third-party agents must expose SSH_AUTH_SOCK to the app process.
func localAgentSocket() (string, error) {
	addr := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK"))
	if addr == "" {
		return "", fmt.Errorf("SSH_AUTH_SOCK is not set; start an SSH agent and expose its socket to uniTerm")
	}
	return addr, nil
}

func forwardLocalAgent(client *ssh.Client) error {
	addr, err := localAgentSocket()
	if err != nil {
		return err
	}
	if err := agent.ForwardToRemote(client, addr); err != nil {
		return fmt.Errorf("connect to SSH_AUTH_SOCK %q: %w", addr, err)
	}
	return nil
}
