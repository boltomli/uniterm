//go:build !android

package session

// Desktop platforms have no cached-app freezing; connections survive
// backgrounding natively, so the keepalive is a no-op.

func startForegroundKeepalive(int) {}

func stopForegroundKeepalive() {}
