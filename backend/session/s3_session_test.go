package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// --- Fake S3 endpoint (downloadDir walker) ----------------------------------

// newFakeS3Session wires an S3Session to an httptest server that serves a
// scripted bucket: ListObjectsV2 (query "list-type=2") returns the key set,
// GET returns each key's body. Keys are arbitrary strings — a planted
// "../" segment never exists on a real filesystem, so the walker must be
// driven over HTTP (path-style URLs, like AWS/MinIO).
func newFakeS3Session(t *testing.T, keys map[string]string) *S3Session {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			var b strings.Builder
			b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
			b.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>buck</Name><IsTruncated>false</IsTruncated>`)
			for k, body := range keys {
				fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>2026-01-01T00:00:00Z</LastModified><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>", k, len(body))
			}
			b.WriteString("</ListBucketResult>")
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, b.String())
			return
		}
		// Object GET: path-style URL is /<bucket>/<key...>.
		key := strings.TrimPrefix(r.URL.Path, "/buck/")
		body, ok := keys[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	s := NewS3Session("s3-test")
	if err := s.Connect(ConnectionConfig{
		S3Region:   "us-east-1",
		User:       "ak",
		Password:   "sk",
		Host:       srv.URL,
		S3Bucket:   "buck",
		S3URLStyle: "path",
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestS3DownloadDirRejectsTraversalKeys: an object key planted with "../"
// segments (legal in a shared bucket; anyone with write access can create
// one) must abort the download instead of planning a local path outside the
// download folder (security.md 文件会话 High).
func TestS3DownloadDirRejectsTraversalKeys(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "dl")
	s := newFakeS3Session(t, map[string]string{
		"dir/a/../../../evil.txt": "BAD",
	})
	task := &TransferTask{ID: "dl-1", Type: "download", RemotePath: "/buck/dir", LocalPath: localDir, Status: "running"}
	task.start()

	if err := s.downloadDir("/buck/dir", localDir, task); err == nil {
		t.Fatal("downloadDir accepted the traversal key dir/a/../../../evil.txt")
	}
	filepath.WalkDir(tmp, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "evil.txt" {
			t.Errorf("traversal key wrote %s outside the download folder", p)
		}
		return nil
	})
}

// TestS3DownloadDirNestedKeys: nested keys download with parent directories
// created, alongside a flat key (security.md 文件会话 Medium: "S3 recursive
// download fails for every object below the first level").
func TestS3DownloadDirNestedKeys(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "dl")
	s := newFakeS3Session(t, map[string]string{
		"dir/a/b/c.txt": "DEEP",
		"dir/ok.txt":    "FLAT",
	})
	task := &TransferTask{ID: "dl-2", Type: "download", RemotePath: "/buck/dir", LocalPath: localDir, Status: "running"}
	task.start()

	if err := s.downloadDir("/buck/dir", localDir, task); err != nil {
		t.Fatalf("downloadDir: %v", err)
	}
	if len(task.FailedFiles) != 0 {
		t.Fatalf("FailedFiles = %+v, want none", task.FailedFiles)
	}
	b, err := os.ReadFile(filepath.Join(localDir, "a", "b", "c.txt"))
	if err != nil {
		t.Fatalf("nested key a/b/c.txt: %v", err)
	}
	if string(b) != "DEEP" {
		t.Fatalf("nested key content = %q, want DEEP", b)
	}
	flat, err := os.ReadFile(filepath.Join(localDir, "ok.txt"))
	if err != nil {
		t.Fatalf("flat key ok.txt: %v", err)
	}
	if string(flat) != "FLAT" {
		t.Fatalf("flat key content = %q, want FLAT", flat)
	}
}
