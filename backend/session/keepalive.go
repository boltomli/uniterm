package session

import (
	"sync"

	"github.com/ys-ll/uniterm/backend/log"
)

// Foreground keepalive. On Android the app process is frozen a few seconds
// after being backgrounded (cached app, Android 12+), which kills every open
// TCP connection — the user's terminals all drop. While at least one session
// is connected we promote the app to a foreground service, which exempts it
// from freezing; the service is stopped once the last connection ends.
//
// Tracking is platform-independent: the SessionManager installs
// onSessionStatus as every session's status callback, and this file only
// counts connected sessions and fires start/stop at the 0→N and N→0
// transitions. The actual service calls live in the per-platform files.

type keepaliveState struct {
	mu        sync.Mutex
	connected map[string]bool
	running   bool
}

var keepalive = keepaliveState{connected: make(map[string]bool)}

// onSessionStatus is the status callback installed on every session created
// through the SessionManager. Status transitions may arrive from any
// goroutine (and re-set Connected on reconnect); the map makes it idempotent.
func onSessionStatus(id string, st SessionStatus) {
	keepalive.mu.Lock()
	if st == StatusConnected {
		keepalive.connected[id] = true
	} else {
		delete(keepalive.connected, id)
	}
	n := len(keepalive.connected)
	start := n > 0 && !keepalive.running
	stop := n == 0 && keepalive.running
	if start {
		keepalive.running = true
	}
	if stop {
		keepalive.running = false
	}
	keepalive.mu.Unlock()

	if start {
		log.Writef("[keepalive] %d connected, starting foreground keepalive", n)
		startForegroundKeepalive(n)
	} else if stop {
		log.Writef("[keepalive] no connections left, stopping foreground keepalive")
		stopForegroundKeepalive()
	}
}
