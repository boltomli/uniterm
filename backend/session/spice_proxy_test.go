package session

import (
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newSPICEProxyUnderTest(target string) proxyUnderTest { return NewSPICEProxy(target) }

// SPICE serves one WebSocket per channel (main/display/inputs/cursor), so the
// VNC single-client-slot helper does not apply. These tests pin the SPICE
// contract instead: token gate, per-channel relay, Stop closes everything.

func TestSPICEProxyRequiresToken(t *testing.T) {
	p := NewSPICEProxy(startProxyTarget(t))
	wsURL := startTestProxy(t, p)
	t.Cleanup(p.Stop)

	bareURL, err := url.Parse(wsURL)
	if err != nil {
		t.Fatal(err)
	}
	bareURL.RawQuery = ""
	if _, _, err := websocket.DefaultDialer.Dial(bareURL.String(), nil); err == nil {
		t.Fatal("connection without the per-listen token was accepted")
	}
	wrongURL, err := url.Parse(wsURL)
	if err != nil {
		t.Fatal(err)
	}
	wrongURL.RawQuery = "token=deadbeef"
	if _, _, err := websocket.DefaultDialer.Dial(wrongURL.String(), nil); err == nil {
		t.Fatal("connection with a wrong token was accepted")
	}
}

func TestSPICEProxyServesConcurrentChannels(t *testing.T) {
	p := NewSPICEProxy(startProxyTarget(t))
	wsURL := startTestProxy(t, p)
	t.Cleanup(p.Stop)

	var socks []*websocket.Conn
	for i := 0; i < 4; i++ {
		ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("channel %d: %v", i, err)
		}
		socks = append(socks, ws)
	}
	for i, ws := range socks {
		_ = ws.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		payload := []byte("REDQ:channel")
		if err := ws.WriteMessage(websocket.BinaryMessage, payload); err != nil {
			t.Fatalf("channel %d write: %v", i, err)
		}
		kind, echoed, err := ws.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage || string(echoed) != string(payload) {
			t.Fatalf("channel %d echo: %q, %v", i, echoed, err)
		}
	}
	for _, ws := range socks {
		ws.Close()
	}
}

func TestSPICEProxyStopClosesActiveConns(t *testing.T) {
	p := NewSPICEProxy(startProxyTarget(t))
	wsURL := startTestProxy(t, p)

	c1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	// Let the handler finish registering the connection.
	time.Sleep(100 * time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		p.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop blocked with an active connection")
	}

	// A second Stop must not block or panic either (SESSION-04).
	done := make(chan struct{})
	go func() { p.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("second Stop blocked")
	}
}
