package localsrv

import (
	"os"
	"testing"
)

// TestMain pins every listener this package starts to loopback.
//
// The servers under test normally bind all interfaces — that is what makes them
// useful on a LAN — but on Windows each listen on a non-loopback address
// triggers a Windows Defender Firewall prompt for the (freshly built) test
// binary. The dialog cannot be dismissed from the test run, so the ports stay
// blocked and the round-trip tests hang or see zero bytes. Binding loopback
// keeps the same code paths under test without asking the OS for network
// exposure.
func TestMain(m *testing.M) {
	bindHost = "127.0.0.1"
	os.Exit(m.Run())
}
