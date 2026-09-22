package session

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// newDelayedSFTPSession is newTestSFTPSession with a transparent proxy between
// the SFTP client and server that delays every protocol frame by `delay` in
// each direction, simulating an RTT of 2×delay. Frames are forwarded via
// time.AfterFunc (asynchronously), so arbitrarily many requests may be in
// flight at once: the delay penalises stop-and-wait request loops but not
// pipelined ones. This is what makes request pipelining observable in a unit
// test: a stop-and-wait transfer of N chunks needs N round trips, a pipelined
// one needs about N/64.
func newDelayedSFTPSession(t *testing.T, delay time.Duration) (*SFTPSession, string) {
	t.Helper()
	root := t.TempDir()
	a1, a2 := net.Pipe() // a1: client end
	b1, b2 := net.Pipe() // b2: server end
	t.Cleanup(func() { a1.Close(); a2.Close(); b1.Close(); b2.Close() })

	srv, err := sftp.NewServer(b2)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })

	go forwardFrames(a2, b1, delay) // client -> server
	go forwardFrames(b1, a2, delay) // server -> client

	// Configure the client exactly as production does (sftpClientOptions),
	// otherwise ReadFrom silently falls back to sequential writes.
	client, err := sftp.NewClientPipe(a1, a1, sftpClientOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	s := NewSFTPSession("test-sftp")
	s.sftpClient = client
	s.sshClient = nil
	return s, root
}

// forwardFrames relays length-prefixed SFTP frames from src to dst, each after
// an asynchronous delay. The SFTP wire format is uint32 length + payload, so
// no parsing beyond the frame header is needed.
func forwardFrames(src io.Reader, dst io.Writer, delay time.Duration) {
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(src, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[:])
		frame := make([]byte, 4+n)
		copy(frame, hdr[:])
		if _, err := io.ReadFull(src, frame[4:]); err != nil {
			return
		}
		time.AfterFunc(delay, func() { _, _ = dst.Write(frame) })
	}
}

// waitCompleteLong waits for the task's "complete" event with a deadline long
// enough to also accommodate a failed (non-pipelined) transfer, so the tests
// below fail via their latency assertion, not via a wait timeout.
func waitCompleteLong(t *testing.T, events <-chan map[string]any, id string) map[string]any {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev["taskId"] == id && ev["event"] == "complete" {
				return ev
			}
		case <-deadline:
			t.Fatal("transfer did not finish in time")
			return nil
		}
	}
}

// pipelineProbeSize is 128 chunks of 32 KiB. At a simulated RTT of 80ms a
// stop-and-wait transfer takes ~10s (upload, one round trip per 32 KiB chunk)
// or ~5s (download, one per 64 KiB chunk); a pipelined one takes a couple of
// round trips. The tests fail above 2s, far below the stop-and-wait floor and
// far above the pipelined time.
const pipelineProbeSize = 4 << 20

func pipelineProbeData() []byte {
	data := make([]byte, pipelineProbeSize)
	for i := range data {
		data[i] = byte(i * 7)
	}
	return data
}

func assertPipelinedElapsed(t *testing.T, start time.Time) {
	t.Helper()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("transfer took %v: SFTP requests are not pipelined", elapsed)
	}
}

func TestUploadTransferPipelinesRequests(t *testing.T) {
	s, root := newDelayedSFTPSession(t, 40*time.Millisecond)
	events := captureTransferEvents(t)
	local := filepath.Join(root, "in")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "big.bin"), pipelineProbeData(), 0o644); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(root, "out")

	start := time.Now()
	id, err := s.startDirTransfer("upload", local, remote, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := waitCompleteLong(t, events, id)
	if ev["status"] != "done" {
		t.Fatalf("status = %v, failed=%v", ev["status"], ev["failedFiles"])
	}
	assertPipelinedElapsed(t, start)

	got, err := os.ReadFile(filepath.Join(remote, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pipelineProbeData()) {
		t.Fatal("uploaded content mismatch")
	}
}

func TestDownloadTransferPipelinesRequests(t *testing.T) {
	s, root := newDelayedSFTPSession(t, 40*time.Millisecond)
	events := captureTransferEvents(t)
	remote := filepath.Join(root, "src")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "big.bin"), pipelineProbeData(), 0o644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(root, "out")

	start := time.Now()
	id, err := s.startDirTransfer("download", local, remote, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := waitCompleteLong(t, events, id)
	if ev["status"] != "done" {
		t.Fatalf("status = %v, failed=%v", ev["status"], ev["failedFiles"])
	}
	assertPipelinedElapsed(t, start)

	got, err := os.ReadFile(filepath.Join(local, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pipelineProbeData()) {
		t.Fatal("downloaded content mismatch")
	}
}
