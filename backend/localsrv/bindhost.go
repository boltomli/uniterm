package localsrv

import (
	"fmt"
	"net"
	"strconv"
)

// bindHost is the host every listener in this package binds to. Empty means
// all interfaces, which is the production behaviour: these servers exist to
// serve other machines on the LAN, so a connection from a peer must reach them.
//
// Tests set it to loopback once (see TestMain in main_test.go). Binding a
// non-loopback address on Windows raises a Windows Defender Firewall prompt for
// the test binary — a freshly built exe on every run, so the prompt comes back
// every time and the pending dialog stalls the affected tests.
var bindHost = ""

// listenAddr formats the "host:port" a listener should bind for port, honouring
// bindHost. A port of 0 asks the OS for a free ephemeral port.
func listenAddr(port int) string {
	if bindHost == "" {
		return fmt.Sprintf(":%d", port)
	}
	return net.JoinHostPort(bindHost, strconv.Itoa(port))
}
