package session

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// TestX11DesktopHelperProcess is the long-running child that stands in for
// VcXsrv (see startSleepingProcess). It exits immediately unless the env var
// is set, so a plain `go test` run is unaffected.
func TestX11DesktopHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_X11_HELPER") != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

func startSleepingProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestX11DesktopHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_X11_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	return cmd
}

// killVcXsrv must stop the dedicated X server on EVERY failure path (not just
// Disconnect) and must be safe to call when nothing was started, and again
// after it already ran.
func TestKillVcXsrvStopsProcessAndIsIdempotent(t *testing.T) {
	// Nothing started yet: must not panic.
	(&X11DesktopSession{}).killVcXsrv()

	cmd := startSleepingProcess(t)
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	s := &X11DesktopSession{vcxsrv: cmd}
	s.killVcXsrv()
	if s.vcxsrv != nil {
		t.Fatal("killVcXsrv must clear the vcxsrv field")
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("VcXsrv stand-in still running after killVcXsrv")
	}

	s.killVcXsrv() // idempotent on a cleared field
}

// probeLocalX must close the connection it opens. Discarding it leaked a
// socket (and kept the X server's per-connection state) for every X11 Desktop
// session, for the life of the process.
func TestProbeLocalXClosesConnection(t *testing.T) {
	var ln net.Listener
	disp := 0
	for d := 1; d < 40; d++ {
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(6000+d)))
		if err == nil {
			ln, disp = l, d
			break
		}
	}
	if ln == nil {
		t.Skip("no free 6000+N port for the fake X server")
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c
	}()

	display := "localhost:" + strconv.Itoa(disp)
	if err := probeLocalX(display); err != nil {
		t.Fatalf("probeLocalX(%s): %v", display, err)
	}

	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("probe never reached the fake X server")
	}
	defer server.Close()

	// The probe side must be gone: a blocking read that hits OUR deadline
	// means the client connection is still open.
	if err := server.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, err := server.Read(make([]byte, 1)); err == nil {
		t.Fatal("probe connection still open")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("probe connection was not closed by probeLocalX")
	}
}
