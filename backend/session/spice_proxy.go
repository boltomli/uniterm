package session

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ys-ll/uniterm/backend/log"
)

// SPICEProxy bridges WebSocket (frontend spice-html5) to TCP (SPICE server).
// One instance per SPICE session, bound to a random local port.
//
// Unlike VNCProxy (one client at a time), SPICE serves MULTIPLE concurrent
// WebSockets: spice-html5 opens one connection per channel (main, display,
// inputs, cursor) and each carries its own TCP leg to the server. Every
// connection is still gated by the per-listen token below — knowing the port
// is not enough to attach.
type SPICEProxy struct {
	listener net.Listener
	server   *http.Server
	target   string
	token    string // per-listen secret carried in the returned URL; rejects anyone who finds the port
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup // pump goroutines + the http.Serve goroutine
	mu       sync.Mutex
	conns    map[*websocket.Conn]net.Conn
}

func NewSPICEProxy(target string) *SPICEProxy {
	return &SPICEProxy{
		target: target,
		stopCh: make(chan struct{}),
		conns:  make(map[*websocket.Conn]net.Conn),
	}
}

// Start begins listening on a random local port and returns the WebSocket URL.
func (p *SPICEProxy) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("spice proxy listen: %w", err)
	}
	p.listener = ln

	// Same token gate as VNCProxy: a random per-listen secret in the returned
	// URL, since CheckOrigin alone cannot authenticate the connector.
	var tok [16]byte
	if _, err := rand.Read(tok[:]); err != nil {
		ln.Close()
		return "", fmt.Errorf("spice proxy token: %w", err)
	}
	p.token = hex.EncodeToString(tok[:])

	upgrader := websocket.Upgrader{
		CheckOrigin:  func(r *http.Request) bool { return true },
		Subprotocols: []string{"binary"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(p.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Writef("[SPICEProxy] WebSocket upgrade failed: %v", err)
			return
		}
		p.handleWebSocket(ws)
	})
	p.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		_ = p.server.Serve(ln)
	}()

	addr := ln.Addr().(*net.TCPAddr)
	log.Writef("[SPICEProxy] Listening on ws://127.0.0.1:%d, target: %s", addr.Port, p.target)
	return fmt.Sprintf("ws://127.0.0.1:%d?token=%s", addr.Port, p.token), nil
}

func (p *SPICEProxy) handleWebSocket(ws *websocket.Conn) {
	defer ws.Close()

	// Allow large SPICE frames (up to 16 MB).
	ws.SetReadLimit(16 << 20)

	select {
	case <-p.stopCh:
		return
	default:
	}

	dialer := net.Dialer{Timeout: 5 * time.Second}
	tcp, err := dialer.Dial("tcp", p.target)
	if err != nil {
		log.Writef("[SPICEProxy] TCP dial to %s failed: %v", p.target, err)
		return
	}

	// Register AND account for both pump goroutines under the same lock Stop
	// snapshots: once Stop has nil'd the map, no further Add can reach the
	// WaitGroup, so wg.Wait cannot race a late Add (and a connection that
	// arrives after Stop simply drops).
	p.mu.Lock()
	if p.conns == nil {
		p.mu.Unlock()
		tcp.Close()
		return
	}
	p.conns[ws] = tcp
	p.wg.Add(2)
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.conns, ws)
		p.mu.Unlock()
		tcp.Close()
		ws.Close()
	}()

	readDone := make(chan struct{})
	go func() {
		defer p.wg.Done()
		defer close(readDone)
		defer tcp.Close()
		defer ws.Close()
		for {
			msgType, reader, err := ws.NextReader()
			if err != nil {
				return
			}
			if msgType == websocket.BinaryMessage {
				if _, err := io.Copy(tcp, reader); err != nil {
					return
				}
			}
		}
	}()

	go func() {
		defer p.wg.Done()
		buf := make([]byte, 32768)
		for {
			select {
			case <-p.stopCh:
				return
			default:
			}
			n, err := tcp.Read(buf)
			if n > 0 {
				if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Hold the handler until both directions are done so a replaced
	// connection cannot leak its goroutines.
	<-readDone
}

// Stop closes every active connection and returns once none of them is left
// running. The listener is closed before waiting so an in-flight handler
// cannot register a connection nobody will ever close (SESSION-04), and it is
// safe to call Stop more than once.
func (p *SPICEProxy) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	for ws, tcp := range conns {
		ws.Close()
		if tcp != nil {
			tcp.Close()
		}
	}
	p.mu.Unlock()
	if p.listener != nil {
		p.listener.Close()
	}
	if p.server != nil {
		p.server.Close()
	}
	p.wg.Wait()
}
