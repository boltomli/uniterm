package session

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

// Tests for the async, bounded emit path (issue #1053): a flood of terminal
// output must never block the session's readLoop against the synchronous
// Wails event emit, and high-frequency chunks should be coalesced before they
// cross the IPC boundary.

func TestEmitDataSyncWhenLoopNotStarted(t *testing.T) {
	s := &baseSession{}
	var mu sync.Mutex
	var got []byte
	s.SetOnDataCallback(func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, data...)
	})

	s.emitData([]byte("hello"))

	mu.Lock()
	defer mu.Unlock()
	if string(got) != "hello" {
		t.Fatalf("emitData without emitLoop delivered %q synchronously, want %q", got, "hello")
	}
}

func TestEmitDataBatchesWhenLoopStarted(t *testing.T) {
	oldInterval := emitFlushInterval
	emitFlushInterval = 40 * time.Millisecond
	t.Cleanup(func() { emitFlushInterval = oldInterval })

	s := &baseSession{}
	var mu sync.Mutex
	var got []byte
	calls := 0
	s.SetOnDataCallback(func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, data...)
		calls++
	})
	s.startEmitLoop()
	t.Cleanup(s.stopEmitLoop)

	const n = 50
	for i := 0; i < n; i++ {
		s.emitData([]byte("x"))
	}
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != n {
		t.Fatalf("delivered %d bytes, want %d (no loss)", len(got), n)
	}
	if bytes.Count(got, []byte("x")) != n {
		t.Fatalf("delivered content %q is corrupted", got)
	}
	if calls >= n {
		t.Fatalf("callback ran %d times for %d chunks; batching did not happen", calls, n)
	}
}

func TestEmitDataDropsOldestWhenQueueFull(t *testing.T) {
	oldInterval := emitFlushInterval
	emitFlushInterval = 5 * time.Second
	t.Cleanup(func() { emitFlushInterval = oldInterval })

	s := &baseSession{}
	var mu sync.Mutex
	var got []byte
	s.SetOnDataCallback(func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, data...)
	})
	s.startEmitLoop()

	// Enqueue well past the queue capacity before the first flush window
	// opens: the oldest chunks must be dropped, keeping the newest.
	for i := 0; i < emitQueueCapacity+10; i++ {
		s.emitData([]byte{byte(i)})
	}
	// stopEmitLoop force-flushes whatever survived in the queue.
	s.stopEmitLoop()

	mu.Lock()
	defer mu.Unlock()
	want := make([]byte, 0, emitQueueCapacity)
	for i := 10; i < emitQueueCapacity+10; i++ {
		want = append(want, byte(i))
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("delivered %v, want chunks %d..%d in order (drop-oldest)", got, 10, emitQueueCapacity+9)
	}
}

func TestEmitDataSyncDuringZmodem(t *testing.T) {
	oldInterval := emitFlushInterval
	emitFlushInterval = 5 * time.Second
	t.Cleanup(func() { emitFlushInterval = oldInterval })

	s := &baseSession{}
	var mu sync.Mutex
	var got []byte
	s.SetOnDataCallback(func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, data...)
	})
	s.startEmitLoop()
	t.Cleanup(s.stopEmitLoop)
	s.SetZmodemMode(true)

	s.emitData([]byte("zmodem payload"))

	mu.Lock()
	defer mu.Unlock()
	if string(got) != "zmodem payload" {
		t.Fatalf("emitData during zmodem delivered %q synchronously, want %q", got, "zmodem payload")
	}
}

func TestStopEmitLoopFlushesPending(t *testing.T) {
	oldInterval := emitFlushInterval
	emitFlushInterval = 5 * time.Second
	t.Cleanup(func() { emitFlushInterval = oldInterval })

	s := &baseSession{}
	var mu sync.Mutex
	var got []byte
	s.SetOnDataCallback(func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, data...)
	})
	s.startEmitLoop()

	s.emitData([]byte("tail"))
	s.stopEmitLoop()

	mu.Lock()
	if string(got) != "tail" {
		mu.Unlock()
		t.Fatalf("stopEmitLoop delivered %q, want pending %q flushed", got, "tail")
	}
	mu.Unlock()

	// After the loop stopped, emitData must fall back to the synchronous path.
	s.emitData([]byte("sync"))
	mu.Lock()
	if string(got) != "tailsync" {
		mu.Unlock()
		t.Fatalf("emitData after stop delivered %q, want %q", got, "tailsync")
	}
	mu.Unlock()
}

func TestEmitDataDoesNotBlockOnSlowConsumer(t *testing.T) {
	oldInterval := emitFlushInterval
	emitFlushInterval = 40 * time.Millisecond
	t.Cleanup(func() { emitFlushInterval = oldInterval })

	s := &baseSession{}
	blocked := make(chan struct{})
	release := make(chan struct{})
	signalOnce := &sync.Once{}
	s.SetOnDataCallback(func(data []byte) {
		signalOnce.Do(func() { close(blocked) })
		<-release
	})
	s.startEmitLoop()
	t.Cleanup(s.stopEmitLoop)

	s.emitData([]byte("first"))

	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("callback was never invoked")
	}

	done := make(chan struct{})
	go func() {
		s.emitData([]byte("second"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emitData blocked on a stuck consumer; readLoop would freeze")
	}
	close(release)
}
