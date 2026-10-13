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

// wsPair is one active WebSocket↔TCP bridge: both live legs plus a channel
// that closes once the pair's two pump goroutines have exited.
type wsPair struct {
	ws   *websocket.Conn
	tcp  net.Conn
	done chan struct{}
	once sync.Once
}

func (pr *wsPair) finish() { pr.once.Do(func() { close(pr.done) }) }

// wsPairRegistry tracks the active pairs of a local proxy (shared by the VNC
// and SPICE bridges). A proxy serves one client at a time: while a pair is
// registered, new connections are rejected, and Stop closes every registered
// pair so no pump goroutine can outlive it.
type wsPairRegistry struct {
	mu      sync.Mutex
	pairs   []*wsPair
	stopped bool
}

// claim reserves the single client slot. false means the slot is taken or the
// proxy is stopping. Claiming happens before the WebSocket upgrade, so a
// client only learns it owns the slot once the 101 has been sent.
func (r *wsPairRegistry) claim() (*wsPair, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || len(r.pairs) > 0 {
		return nil, false
	}
	p := &wsPair{done: make(chan struct{})}
	r.pairs = append(r.pairs, p)
	return p, true
}

// attachWS binds the upgraded WebSocket to a claimed pair. false means the
// proxy stopped in the meantime: the caller must close ws and finish the pair.
func (r *wsPairRegistry) attachWS(p *wsPair, ws *websocket.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return false
	}
	p.ws = ws
	return true
}

// attachTCP binds the TCP leg to a claimed pair. false means the proxy
// stopped in the meantime: the caller must close both legs and finish the pair.
func (r *wsPairRegistry) attachTCP(p *wsPair, tcp net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return false
	}
	p.tcp = tcp
	return true
}

// drop unregisters a finished (or never-started) pair, releasing the slot and
// signalling Stop. Safe to call more than once.
func (r *wsPairRegistry) drop(p *wsPair) {
	r.mu.Lock()
	for i, q := range r.pairs {
		if q == p {
			r.pairs = append(r.pairs[:i], r.pairs[i+1:]...)
			break
		}
	}
	r.mu.Unlock()
	p.finish()
}

// closeAll marks the registry stopped, closes every leg under the lock (so a
// concurrent attach cannot leave a connection nobody closes) and returns the
// pairs for the caller to wait on.
func (r *wsPairRegistry) closeAll() []*wsPair {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	pairs := r.pairs
	r.pairs = nil
	for _, p := range pairs {
		if p.ws != nil {
			p.ws.Close()
		}
		if p.tcp != nil {
			p.tcp.Close()
		}
	}
	return pairs
}

// VNCProxy bridges WebSocket (frontend noVNC) to TCP (VNC server).
// One instance per VNC session, bound to a random local port.
type VNCProxy struct {
	listener net.Listener
	target   string
	token    string // per-listen secret carried in the returned URL; rejects anyone who finds the port
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup // the http.Serve goroutine
	pairs    wsPairRegistry
}

func NewVNCProxy(target string) *VNCProxy {
	return &VNCProxy{
		target: target,
		stopCh: make(chan struct{}),
	}
}

// Start begins listening on a random local port and returns the WebSocket URL.
func (p *VNCProxy) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("vnc proxy listen: %w", err)
	}
	p.listener = ln

	// Gate the bridge with a random per-listen token: CheckOrigin cannot
	// distinguish the app's webview from another local process or a web
	// page, and the first connector wins the single slot.
	var tok [16]byte
	if _, err := rand.Read(tok[:]); err != nil {
		ln.Close()
		return "", fmt.Errorf("vnc proxy token: %w", err)
	}
	p.token = hex.EncodeToString(tok[:])

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(p.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		pair, ok := p.pairs.claim()
		if !ok {
			log.Writef("[VNCProxy] Rejecting additional WebSocket connection")
			http.Error(w, "already connected", http.StatusConflict)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			p.pairs.drop(pair)
			log.Writef("[VNCProxy] WebSocket upgrade failed: %v", err)
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
	log.Writef("[VNCProxy] Listening on ws://127.0.0.1:%d, target: %s", addr.Port, p.target)
	return fmt.Sprintf("ws://127.0.0.1:%d?token=%s", addr.Port, p.token), nil
}

// handleWebSocket bridges one claimed pair. It returns as soon as the pumps
// are running: the pair (not the handler's lifetime) owns the slot, so a
// second connection stays rejected until both pumps exit.
func (p *VNCProxy) handleWebSocket(ws *websocket.Conn, pair *wsPair) {
	if !p.pairs.attachWS(pair, ws) {
		ws.Close()
		p.pairs.drop(pair)
		return
	}
	log.Writef("[VNCProxy] WebSocket client connected")

	tcp, err := net.DialTimeout("tcp", p.target, 5*time.Second)
	if err != nil {
		log.Writef("[VNCProxy] TCP dial to %s failed: %v", p.target, err)
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
	log.Writef("[VNCProxy] TCP connection to %s established", p.target)

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
				log.Writef("[VNCProxy] WebSocket read error: %v", err)
				return
			}
			if msgType == websocket.BinaryMessage {
				if _, err := tcp.Write(data); err != nil {
					log.Writef("[VNCProxy] TCP write error: %v", err)
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
				log.Writef("[VNCProxy] TCP read error: %v", err)
				return
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				log.Writef("[VNCProxy] WebSocket write error: %v", err)
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
// running. It is safe to call more than once, and safe while a handler is
// still dialing: the handler notices the stopped registry and finishes its
// pair instead of leaking it.
func (p *VNCProxy) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	pairs := p.pairs.closeAll()
	// Close the listener BEFORE waiting so an in-flight handler cannot claim
	// a slot that nobody will ever close (SESSION-04).
	if p.listener != nil {
		p.listener.Close()
	}
	for _, pr := range pairs {
		<-pr.done
	}
	p.wg.Wait()
}
