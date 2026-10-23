package session

import (
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// spiceProxySocket dials the proxy's tokenized WebSocket URL (the token
// travels in the query string of the URL the session handed out).
func spiceProxySocket(t *testing.T, addr string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"binary"}, HandshakeTimeout: 3 * time.Second}
	ws, _, err := dialer.Dial(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	if ws.Subprotocol() != "binary" {
		t.Fatal("SPICE binary subprotocol was not negotiated")
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	return ws
}

func TestSPICESessionUsesBackendProxy(t *testing.T) {
	s := NewSPICESession("spice")
	if err := s.Connect(ConnectionConfig{Host: "guest.invalid", Port: 2}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Disconnect() })
	addr, err := url.Parse(s.ProxyAddr())
	if err != nil {
		t.Fatal(err)
	}
	// The proxy hands out a loopback URL gated by a per-listen token (in the
	// query string), never a direct socket to the remote server.
	if addr.Hostname() != "127.0.0.1" || addr.Query().Get("token") == "" || !s.IsConnected() {
		t.Fatalf("SPICE session exposed a remote endpoint: %q", s.ProxyAddr())
	}
	if s.proxy.target != "guest.invalid:5902" {
		t.Fatalf("display port was not normalized before proxying: %q", s.proxy.target)
	}
}

func TestSPICESessionDisconnectStopsProxy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	s := NewSPICESession("spice")
	if err := s.Connect(ConnectionConfig{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Disconnect() })
	ws := spiceProxySocket(t, s.ProxyAddr())
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second))
	upstream, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	if err := s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if s.IsConnected() || s.proxy != nil {
		t.Fatal("SPICE proxy was not released on disconnect")
	}
	_ = upstream.SetReadDeadline(time.Now().Add(3 * time.Second))
	var buf [1]byte
	if _, err := upstream.Read(buf[:]); err == nil {
		t.Fatal("SPICE TCP channel survived disconnect")
	}
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("SPICE WebSocket channel survived disconnect")
	}
}
