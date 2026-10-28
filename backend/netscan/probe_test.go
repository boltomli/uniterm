package netscan

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
)

// TestClassifyDialError checks that port classification prefers the wrapped
// errno over error text — the errno path is what keeps classification correct
// on non-English systems and future Go message changes.
func TestClassifyDialError(t *testing.T) {
	syscallErr := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: err}}
	}
	for _, test := range []struct {
		name string
		err  error
		want portState
	}{
		{"conn refused errno", syscallErr(syscall.ECONNREFUSED), portClosed},
		{"conn reset errno", syscallErr(syscall.ECONNRESET), portClosed},
		{"timeout errno", syscallErr(os.ErrDeadlineExceeded), portTimeout},
		{"host unreachable errno", syscallErr(syscall.EHOSTUNREACH), portTimeout},
		{"context canceled", context.Canceled, portTimeout},
		// Fallback: message matching for errors without a wrapped errno.
		{"windows forcibly closed text", errors.New("read: An existing connection was forcibly closed by the remote host"), portClosed},
		{"windows actively refused text", errors.New("connect: No connection could be made because the target machine actively refused it"), portClosed},
		{"generic text", errors.New("something else entirely"), portTimeout},
	} {
		if got := classifyDialError(test.err); got != test.want {
			t.Errorf("%s: classifyDialError() = %v, want %v", test.name, got, test.want)
		}
	}
}
