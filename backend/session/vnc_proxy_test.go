package session

import (
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// proxyUnderTest is the surface the VNC and SPICE bridges share for these
// tests: Start returns the ws:// URL (with token), Stop tears everything down.
type proxyUnderTest interface {
	Start() (string, error)
	Stop()
}

// newProxyUnderTest builds a bridge against a freshly started target server.
type newProxyUnderTest func(target string) proxyUnderTest

// startProxyTarget runs a TCP echo server the proxy can bridge to, so the
// bridge goroutines have a live peer on the TCP leg.
func startProxyTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func startTestProxy(t *testing.T, p proxyUnderTest) string {
	t.Helper()
	wsURL, err := p.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := url.Parse(wsURL); err != nil {
		t.Fatalf("Start returned an unparsable URL %q: %v", wsURL, err)
	}
	// Bounded so a test that fails because Stop hangs still lets the rest of
	// the binary run instead of wedging in cleanup.
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() { p.Stop(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Logf("cleanup: Stop still blocked, giving up")
		}
	})
	return wsURL
}

// checkProxyRejectsSecondClient: the bridge serves exactly one client; a
// second WebSocket (a frontend remount, another local process that found the
// port) must be turned away while the first one is attached.
func checkProxyRejectsSecondClient(t *testing.T, newProxy newProxyUnderTest) {
	t.Helper()
	p := newProxy(startProxyTarget(t))
	wsURL := startTestProxy(t, p)

	c1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("first connection: %v", err)
	}
	defer c1.Close()

	// Give the handler time to register the pair before probing the slot —
	// the rejection must hold for the whole life of c1, not just a moment.
	time.Sleep(100 * time.Millisecond)

	c2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		c2.Close()
		t.Fatal("second WebSocket connection was accepted while a client was attached")
	}
}

// checkProxyStopReturns: Stop must close every active pair — including one
// that an earlier, already-returned handleWebSocket left behind — so it never
// blocks in wg.Wait().
func checkProxyStopReturns(t *testing.T, newProxy newProxyUnderTest) {
	t.Helper()
	p := newProxy(startProxyTarget(t))
	wsURL := startTestProxy(t, p)

	c1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("first connection: %v", err)
	}
	defer c1.Close()

	// Let the first handler finish registering its pair, so the second dial
	// probes the steady state instead of the upgrade window.
	time.Sleep(100 * time.Millisecond)

	// After the fix this dial is rejected; before it, the proxy happily
	// bridges a second client — either way Stop must still return promptly.
	c2, _, dialErr := websocket.DefaultDialer.Dial(wsURL, nil)
	if dialErr != nil {
		t.Logf("second connection rejected: %v", dialErr)
	}

	stopped := make(chan struct{})
	go func() {
		p.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		if c2 != nil {
			c2.Close()
		}
		t.Fatal("Stop did not return: an active ws/tcp pair was never closed")
	}
	if c2 != nil {
		c2.Close()
	}

	// The surviving client must have been cut off by Stop.
	_ = c1.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c1.ReadMessage(); err == nil {
		t.Error("client connection still readable after Stop")
	}
}

// checkProxyFreesSlotOnDisconnect: once the attached client goes away the
// pair is released, so the next connector (frontend remount) gets in.
func checkProxyFreesSlotOnDisconnect(t *testing.T, newProxy newProxyUnderTest) {
	t.Helper()
	p := newProxy(startProxyTarget(t))
	wsURL := startTestProxy(t, p)

	c1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("first connection: %v", err)
	}
	if err := c1.Close(); err != nil {
		t.Fatalf("close first client: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		c2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err == nil {
			c2.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot never released after the first client disconnected: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func newVNCProxyUnderTest(target string) proxyUnderTest { return NewVNCProxy(target) }

func TestVNCProxyRejectsSecondClient(t *testing.T) {
	checkProxyRejectsSecondClient(t, newVNCProxyUnderTest)
}

func TestVNCProxyStopClosesActivePairs(t *testing.T) {
	checkProxyStopReturns(t, newVNCProxyUnderTest)
}

func TestVNCProxyFreesSlotOnDisconnect(t *testing.T) {
	checkProxyFreesSlotOnDisconnect(t, newVNCProxyUnderTest)
}
