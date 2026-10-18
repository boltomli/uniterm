//go:build windows

package session

import (
	"fmt"
	"io"

	sshagent "github.com/xanzy/ssh-agent"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// localAgentAuthMethod uses Pageant when available, then falls back to the
// Windows OpenSSH agent named pipe. sshagent.New implements that priority.
func localAgentAuthMethod() (ssh.AuthMethod, func(), error) {
	client, cleanup, err := localAgentClient()
	if err != nil {
		return nil, cleanup, err
	}
	return ssh.PublicKeysCallback(client.Signers), cleanup, nil
}

// localAgentClient opens Pageant or the Windows OpenSSH agent. The returned
// cleanup closes the connection; signers obtained from the client stay usable
// until then.
func localAgentClient() (agent.Agent, func(), error) {
	localAgent, conn, err := sshagent.New()
	if err != nil {
		return nil, func() {}, fmt.Errorf("Pageant or Windows OpenSSH agent unavailable: %w", err)
	}
	cleanup := func() {}
	if conn != nil {
		cleanup = func() { _ = conn.Close() }
	}
	return localAgent, cleanup, nil
}

// forwardLocalAgent uses PuTTY Pageant when it is running, then falls back to
// the Windows OpenSSH agent at \\.\pipe\openssh-ssh-agent. That selection
// order is implemented by github.com/xanzy/ssh-agent.
//
// The pipe that sshagent.New opens must live exactly as long as the SSH client
// it serves: agent requests are answered over it for the whole session.
// Closing it when the client closes (which is what Disconnect does through the
// last reference release) stops every session from leaking a named-pipe
// handle; failing to close it on a forward error stops the error path from
// leaking one immediately.
func forwardLocalAgent(client *ssh.Client) error {
	localAgent, conn, err := sshagent.New()
	if err != nil {
		return fmt.Errorf("Pageant or Windows OpenSSH agent unavailable: %w", err)
	}
	if err := agent.ForwardToAgent(client, localAgent); err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return fmt.Errorf("forward Pageant or Windows OpenSSH agent: %w", err)
	}
	if conn != nil {
		closeAgentConnWithClient(client, conn)
	}
	return nil
}

// closeAgentConnWithClient closes conn once the SSH client's transport ends.
// client.Wait returns when the client is closed (session disconnect, last
// reference release) or the connection drops — either way the agent pipe is no
// longer needed.
func closeAgentConnWithClient(client *ssh.Client, conn io.Closer) {
	go func() {
		_ = client.Wait()
		_ = conn.Close()
	}()
}
