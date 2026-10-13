package session

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ys-ll/uniterm/backend/log"
)

// SPICEProxy bridges WebSocket (frontend spice-html5) to TCP (SPICE server).
// One instance per SPICE session, bound to a random local port. The client
// bookkeeping (wsPair / wsPairRegistry) is shared with VNCProxy.
type SPICEProxy struct {
	listener net.Listener
	target   string
	token    string // per-listen secret carried in the returned URL; rejects anyone who finds the port
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup // the http.Serve goroutine
	pairs    wsPairRegistry
}

func NewSPICEProxy(target string) *SPICEProxy {
	return &SPICEProxy{
		target: target,
		stopCh: make(chan struct{}),
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
		pair, ok := p.pairs.claim()
		if !ok {
			log.Writef("[SPICEProxy] Rejecting additional WebSocket connection")
			http.Error(w, "already connected", http.StatusConflict)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			p.pairs.drop(pair)
			log.Writef("[SPICEProxy] WebSocket upgrade failed: %v", err)
			return
		}
		p.handleWebSocket(ws, pair)
	})

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		_ = http.Serve(ln, mux)
	}()

	addr := ln.Addr().(*net.TCPAddr)
	log.Writef("[SPICEProxy] Listening on ws://127.0.0.1:%d, target: %s", addr.Port, p.target)
	return fmt.Sprintf("ws://127.0.0.1:%d?token=%s", addr.Port, p.token), nil
}

// handleWebSocket bridges one claimed pair. It returns as soon as the pumps
// are running: the pair (not the handler's lifetime) owns the slot, so a
// second connection stays rejected until both pumps exit.
func (p *SPICEProxy) handleWebSocket(ws *websocket.Conn, pair *wsPair) {
	if !p.pairs.attachWS(pair, ws) {
		ws.Close()
		p.pairs.drop(pair)
		return
	}
	log.Writef("[SPICEProxy] WebSocket client connected")

	// Allow large SPICE frames (up to 16 MB).
	ws.SetReadLimit(16 << 20)

	tcp, err := net.DialTimeout("tcp", p.target, 5*time.Second)
	if err != nil {
		log.Writef("[SPICEProxy] TCP dial to %s failed: %v", p.target, err)
		ws.Close()
		p.pairs.drop(pair)
		return
	}
	if !p.pairs.attachTCP(pair, tcp) {
		tcp.Close()
		ws.Close()
		p.pairs.drop(pair)
		return
	}
	log.Writef("[SPICEProxy] TCP connection to %s established", p.target)

	var pumps sync.WaitGroup
	pumps.Add(2)

	go func() {
		defer pumps.Done()
		defer tcp.Close()
		for {
			select {
			case <-p.stopCh:
				return
			default:
			}
			msgType, data, err := ws.ReadMessage()
			if err != nil {
				log.Writef("[SPICEProxy] WebSocket read error: %v", err)
				return
			}
			if msgType == websocket.BinaryMessage {
				if _, err := tcp.Write(data); err != nil {
					log.Writef("[SPICEProxy] TCP write error: %v", err)
					return
				}
			}
		}
	}()

	go func() {
		defer pumps.Done()
		defer ws.Close()
		buf := make([]byte, 32768)
		for {
			select {
			case <-p.stopCh:
				return
			default:
			}
			n, err := tcp.Read(buf)
			if err != nil {
				log.Writef("[SPICEProxy] TCP read error: %v", err)
				return
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				log.Writef("[SPICEProxy] WebSocket write error: %v", err)
				return
			}
		}
	}()

	go func() {
		pumps.Wait()
		p.pairs.drop(pair)
	}()
}

// Stop closes every active pair and returns once none of them is left
// running. The listener is closed before waiting so an in-flight handler
// cannot claim a slot nobody will ever close (SESSION-04), and it is safe to
// call Stop more than once.
func (p *SPICEProxy) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	pairs := p.pairs.closeAll()
	if p.listener != nil {
		p.listener.Close()
	}
	for _, pr := range pairs {
		<-pr.done
	}
	p.wg.Wait()
}
