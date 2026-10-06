package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunPoolRunsAllItems(t *testing.T) {
	var count atomic.Int64
	err := runPool(context.Background(), 23, 4, func(i int) error {
		count.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count.Load() != 23 {
		t.Fatalf("ran %d items, want 23", count.Load())
	}
}

func TestRunPoolReturnsFirstError(t *testing.T) {
	sentinel := errors.New("boom")
	err := runPool(context.Background(), 50, 3, func(i int) error {
		if i == 7 {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want sentinel", err)
	}
}

func TestRunPoolStopsEarlyOnError(t *testing.T) {
	var ran atomic.Int64
	err := runPool(context.Background(), 1000, 2, func(i int) error {
		if i == 1 {
			return errors.New("boom")
		}
		time.Sleep(time.Millisecond)
		ran.Add(1)
		return nil
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if ran.Load() >= 999 {
		t.Fatalf("pool kept running after error: %d items ran", ran.Load())
	}
}

func TestRunPoolHonoursContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Int64
	err := runPool(ctx, 10000, 2, func(i int) error {
		ran.Add(1)
		if i == 5 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if ran.Load() >= 10000 {
		t.Fatal("pool did not stop after cancellation")
	}
}

func TestIsS3Retryable(t *testing.T) {
	cases := map[string]bool{
		"status code: 500 Internal Server Error": true,
		"status code: 503 Service Unavailable":   true,
		"net/http: request timeout":              true,
		"read: connection reset by peer":         true,
		"unexpected EOF":                         true,
		"status code: 404 Not Found":             false,
		"status code: 403 Forbidden":             false,
		"access denied":                          false,
	}
	for msg, want := range cases {
		if got := isS3Retryable(errors.New(msg)); got != want {
			t.Errorf("isS3Retryable(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestS3ContentType(t *testing.T) {
	if got := s3ContentType(".PNG"); got != "image/png" {
		t.Errorf("s3ContentType(.PNG) = %q", got)
	}
	if got := s3ContentType(".unknown"); got != "application/octet-stream" {
		t.Errorf("s3ContentType(.unknown) = %q", got)
	}
}

func TestS3PartSizeFor(t *testing.T) {
	const mb = 1024 * 1024
	cases := []struct {
		size int64
		want int64
	}{
		{8 * mb, 5 * mb},                   // tiny upload -> protocol floor
		{40 << 30, 5 * mb},                 // 40 GB still fits 9000 x 5 MB parts
		{90 << 30, (90<<30 + 8999) / 9000}, // 90 GB needs larger parts
	}
	for _, c := range cases {
		if got := s3PartSizeFor(c.size); got != c.want {
			t.Errorf("s3PartSizeFor(%d) = %d, want %d", c.size, got, c.want)
		}
	}
}
