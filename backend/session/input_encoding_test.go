package session

import (
	"bytes"
	"sync"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// TestInputEncoderGBK pins the shared input-encoding helper: with GBK
// configured, UTF-8 keystrokes must actually convert (the seven per-session
// copies never did — their zero-length scratch made Transform return
// ErrShortDst and the raw UTF-8 was sent).
func TestInputEncoderGBK(t *testing.T) {
	var e inputEncoder
	e.set(simplifiedchinese.GBK.NewEncoder())

	got := e.encode([]byte("中文"))
	want := []byte{0xD6, 0xD0, 0xCE, 0xC4} // GBK "中文"
	if !bytes.Equal(got, want) {
		t.Errorf("encode(中文) = % x, want % x", got, want)
	}
}

// TestInputEncoderPassthrough: no transformer set means the input is returned
// unchanged (UTF-8 sessions must not pay a copy).
func TestInputEncoderPassthrough(t *testing.T) {
	var e inputEncoder
	in := []byte("ascii $?\x1b[0m")
	if got := e.encode(in); !bytes.Equal(got, in) {
		t.Errorf("encode = %q, want %q", got, in)
	}
	e.set(nil)
	if got := e.encode(in); !bytes.Equal(got, in) {
		t.Errorf("encode after set(nil) = %q, want %q", got, in)
	}
}

// TestInputEncoderResultNotAliased: callers write the returned slice to the
// socket after encode returns, so a later encode (possibly from a hook or
// post-login writer) must not clobber an earlier result.
func TestInputEncoderResultNotAliased(t *testing.T) {
	var e inputEncoder
	e.set(simplifiedchinese.GBK.NewEncoder())

	first := append([]byte(nil), e.encode([]byte("中文"))...)
	second := e.encode([]byte("中文中文中文"))
	if len(second) == 0 {
		t.Fatal("second encode returned empty")
	}
	if !bytes.Equal(first, []byte{0xD6, 0xD0, 0xCE, 0xC4}) {
		t.Errorf("first result changed after second encode: % x", first)
	}
}

// TestInputEncoderGrowsForLargeInput: the destination must grow past the
// initial size instead of falling back to raw UTF-8 (ErrShortDst).
func TestInputEncoderGrowsForLargeInput(t *testing.T) {
	var e inputEncoder
	e.set(simplifiedchinese.GBK.NewEncoder())

	in := []byte("中文内容")
	// Repeat to exceed any single-call initial capacity.
	var big []byte
	for range 4096 {
		big = append(big, in...)
	}
	got := e.encode(big)
	if len(got) == 0 {
		t.Fatal("encode returned empty")
	}
	if bytes.Equal(got, big) && len(big) > 0 {
		// GBK output for Chinese is 2 bytes/char vs 3 UTF-8 — output must differ.
		t.Errorf("encode returned input unchanged; conversion did not run")
	}
}

// TestInputEncoderConcurrent: encode races with set (encoding changes from
// settings while keystrokes/hook writes are in flight). Run with -race.
func TestInputEncoderConcurrent(t *testing.T) {
	var e inputEncoder
	e.set(simplifiedchinese.GBK.NewEncoder())

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				_ = e.encode([]byte("中文abc"))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 50 {
			e.set(simplifiedchinese.GBK.NewEncoder())
			e.set(nil)
		}
	}()
	wg.Wait()
}
