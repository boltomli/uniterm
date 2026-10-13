package session

// Regression test for security.md (SSH stack, Medium): "Wrong X11 port for
// display ≥10" — "600"+strconv.Itoa(disp) renders 60010 for display :10,
// while the X server actually listens on 6000+disp (6010). SSH X11 on Linux
// with DISPLAY=localhost:10 and X11 Desktop on Windows (displays 1-9 taken)
// both dial the wrong port.

import (
	"net"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestDialLocalXUses6000PlusDisplayPort(t *testing.T) {
	// Bind a free TCP port and derive the display number from it, so the
	// expected X port (6000+disp) is exactly the listener's port. Only
	// ports ≥6010 give a double-digit display — the buggy range.
	var ln net.Listener
	var disp int
	for range 64 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		if p := l.Addr().(*net.TCPAddr).Port; p >= 6010 {
			ln, disp = l, p-6000
			break
		}
		l.Close()
	}
	if ln == nil {
		t.Skip("no bindable port ≥6010 for a double-digit display")
	}
	t.Cleanup(func() { ln.Close() })

	conn, err := dialLocal(runtime.GOOS, ":"+strconv.Itoa(disp), disp, time.Second)
	if err != nil {
		t.Fatalf("dialLocal(:%d) = %v — display N must dial 6000+N (%d), not \"600\"+N", disp, err, 6000+disp)
	}
	conn.Close()
}
