//go:build windows

package session

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeAddr is a net.Addr for the fake SSH transport.
type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

// fakeSSHConn is the minimum ssh.Conn the client wrapper needs: no traffic,
// just a close signal that unblocks Wait (exactly what a transport teardown
// looks like to the agent-forwarding code).
type fakeSSHConn struct {
	closed chan struct{}
	once   sync.Once
}

func newFakeSSHConn() *fakeSSHConn { return &fakeSSHConn{closed: make(chan struct{})} }

func (c *fakeSSHConn) User() string          { return "tester" }
func (c *fakeSSHConn) SessionID() []byte     { return nil }
func (c *fakeSSHConn) ClientVersion() []byte { return []byte("SSH-2.0-fake") }
func (c *fakeSSHConn) ServerVersion() []byte { return []byte("SSH-2.0-fake") }
func (c *fakeSSHConn) RemoteAddr() net.Addr  { return fakeAddr("remote") }
func (c *fakeSSHConn) LocalAddr() net.Addr   { return fakeAddr("local") }

func (c *fakeSSHConn) SendRequest(string, bool, []byte) (bool, []byte, error) {
	return false, nil, nil
}

func (c *fakeSSHConn) OpenChannel(string, []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	return nil, nil, errors.New("no channels in fake transport")
}

func (c *fakeSSHConn) Read(p []byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *fakeSSHConn) Write(p []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, io.ErrClosedPipe
	default:
		return len(p), nil
	}
}

func (c *fakeSSHConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeSSHConn) Wait() error {
	<-c.closed
	return io.EOF
}

// assertPipeClosed fails unless peer stops accepting reads within the deadline
// because the other end was closed (io.ErrClosedPipe/EOF), not because our own
// deadline fired.
func assertPipeClosed(t *testing.T, peer net.Conn, what string) {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		// A closed pipe will not even take a deadline — already closed.
		if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
			return
		}
		t.Fatalf("set read deadline: %v", err)
	}
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatalf("%s: agent pipe still open", what)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("%s: agent pipe was not closed", what)
	}
}

// The Windows agent pipe that sshagent.New returns used to be dropped, so every
// session (and every reconnect) leaked a named-pipe handle. Its lifetime is the
// SSH client's: closing the client — which is what Disconnect does via the last
// reference release — must close the pipe, and so must the transport dropping.
func TestAgentForwardPipeClosedWithClient(t *testing.T) {
	t.Run("client closed", func(t *testing.T) {
		fc := newFakeSSHConn()
		client := ssh.NewClient(fc, nil, nil)
		agentEnd, peer := net.Pipe()
		defer peer.Close()
		closeAgentConnWithClient(client, agentEnd)

		if err := client.Close(); err != nil {
			t.Fatalf("close client: %v", err)
		}
		assertPipeClosed(t, peer, "client close")
	})

	t.Run("transport dropped", func(t *testing.T) {
		fc := newFakeSSHConn()
		client := ssh.NewClient(fc, nil, nil)
		agentEnd, peer := net.Pipe()
		defer peer.Close()
		closeAgentConnWithClient(client, agentEnd)

		if err := fc.Close(); err != nil {
			t.Fatalf("close transport: %v", err)
		}
		assertPipeClosed(t, peer, "transport close")
	})
}
