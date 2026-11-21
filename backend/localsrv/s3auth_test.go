package localsrv

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestResolveWithinRoot is the unit test for the symlink-aware containment
// check shared by FTP, TFTP, HTTP, S3 and the SSH/SFTP subsystem.
func TestResolveWithinRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := resolveWithinRoot(root, filepath.Join(root, "sub", "new.txt")); !ok {
		t.Fatalf("ordinary path rejected: %s (%v)", got, ok)
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}
	if _, ok := resolveWithinRoot(root, link); ok {
		t.Fatal("symlink to a file outside the root was accepted")
	}

	dirLink := filepath.Join(root, "linkdir")
	if err := os.Symlink(outsideDir, dirLink); err != nil {
		t.Skipf("cannot create directory symlink: %v", err)
	}
	if _, ok := resolveWithinRoot(root, filepath.Join(dirLink, "secret.txt")); ok {
		t.Fatal("file reached through a directory symlink outside the root was accepted")
	}
	// A not-yet-existing target below a symlinked directory must be refused
	// too, otherwise a PUT would create the file outside the root.
	if _, ok := resolveWithinRoot(root, filepath.Join(dirLink, "created.txt")); ok {
		t.Fatal("new file below a directory symlink outside the root was accepted")
	}
}

// ── S3 SigV4 ────────────────────────────────────────────────────────────────

// signS3 fills in the SigV4 headers of req using an independent canonical
// request implementation (deliberately duplicated so a regression in the
// production canonicalisation is caught). Bodies are signed with the empty
// payload hash; the server authenticates on the header signature.
func signS3(t *testing.T, req *http.Request, accessKey, secret, region string) {
	t.Helper()
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", emptyHash)

	// Real clients (mc, aws-cli) sign content-length too; Go promotes it out
	// of r.Header, so this also covers the server's special case.
	headers := map[string]string{
		"host":                 req.Host,
		"x-amz-content-sha256": emptyHash,
		"x-amz-date":           amzDate,
	}
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if req.ContentLength >= 0 && req.Body != nil && req.Body != http.NoBody {
		headers["content-length"] = strconv.FormatInt(req.ContentLength, 10)
		signed = append(signed, "content-length")
	}
	sort.Strings(signed)
	signedHeaders := strings.Join(signed, ";")

	var canonicalHeaders strings.Builder
	for _, name := range signed {
		canonicalHeaders.WriteString(name + ":" + headers[name] + "\n")
	}

	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		"",
		canonicalHeaders.String(),
		signedHeaders,
		emptyHash,
	}, "\n")

	sum := sha256.Sum256([]byte(canonicalRequest))
	scope := date + "/" + region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(sum[:]),
	}, "\n")

	mac := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(data))
		return h.Sum(nil)
	}
	k := mac([]byte("AWS4"+secret), date)
	k = mac(k, region)
	k = mac(k, "s3")
	k = mac(k, "aws4_request")
	signature := hex.EncodeToString(mac(k, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signedHeaders, signature))
}

func startTestS3(t *testing.T, root string, cfg S3Config) int {
	t.Helper()
	port := freeTCPPort(t)
	cfg.RootDir = root
	cfg.Port = port
	m := NewManager()
	id, err := m.StartS3Server(cfg)
	if err != nil {
		t.Fatalf("StartS3Server: %v", err)
	}
	t.Cleanup(func() { m.Stop(id) })
	time.Sleep(100 * time.Millisecond)
	return port
}

func s3Request(t *testing.T, method string, port int, path string, cfg S3Config) int {
	t.Helper()
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessKey != "" {
		signS3(t, req, cfg.AccessKey, cfg.SecretKey, "us-east-1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestS3AuthEnforced is the regression test for the unauthenticated object
// store: once keys are configured, unsigned and mis-signed requests must be
// refused while a correctly signed one succeeds.
func TestS3AuthEnforced(t *testing.T) {
	root := t.TempDir()
	cfg := S3Config{AccessKey: "uniterm", SecretKey: "s3cr3t"}
	port := startTestS3(t, root, cfg)

	if got := s3Request(t, http.MethodGet, port, "/", cfg); got != http.StatusOK {
		t.Fatalf("signed list buckets: status %d, want 200", got)
	}

	anonymous := S3Config{}
	if got := s3Request(t, http.MethodGet, port, "/", anonymous); got != http.StatusForbidden {
		t.Fatalf("unsigned request with keys configured: status %d, want 403", got)
	}

	// Wrong secret → signature mismatch.
	bad := S3Config{AccessKey: "uniterm", SecretKey: "wrong"}
	if got := s3Request(t, http.MethodGet, port, "/", bad); got != http.StatusForbidden {
		t.Fatalf("mis-signed request: status %d, want 403", got)
	}

	// Unknown access key → refused before any signature check.
	unknown := S3Config{AccessKey: "other", SecretKey: "s3cr3t"}
	if got := s3Request(t, http.MethodGet, port, "/", unknown); got != http.StatusForbidden {
		t.Fatalf("unknown access key: status %d, want 403", got)
	}

	// Authenticated write + read round-trip.
	if got := s3Request(t, http.MethodPut, port, "/bucket", cfg); got != http.StatusOK {
		t.Fatalf("signed create bucket: status %d, want 200", got)
	}

	// A signed PUT object (body + signed content-length) and its read-back.
	body := strings.NewReader("hello object")
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("http://127.0.0.1:%d/bucket/hello.txt", port), body)
	if err != nil {
		t.Fatal(err)
	}
	signS3(t, req, cfg.AccessKey, cfg.SecretKey, "us-east-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT object: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed PUT object: status %d, want 200", resp.StatusCode)
	}
	if got := s3Request(t, http.MethodGet, port, "/bucket/hello.txt", cfg); got != http.StatusOK {
		t.Fatalf("signed GET object: status %d, want 200", got)
	}
}

// TestS3AnonymousModeStillWorks keeps --no-sign-request clients working when
// no credentials are configured.
func TestS3AnonymousModeStillWorks(t *testing.T) {
	root := t.TempDir()
	port := startTestS3(t, root, S3Config{})
	if got := s3Request(t, http.MethodGet, port, "/", S3Config{}); got != http.StatusOK {
		t.Fatalf("anonymous list buckets: status %d, want 200", got)
	}
}

// TestS3RejectsHalfConfiguredKeys pins the start-up validation.
func TestS3RejectsHalfConfiguredKeys(t *testing.T) {
	m := NewManager()
	if _, err := m.StartS3Server(S3Config{RootDir: t.TempDir(), Port: freeTCPPort(t), AccessKey: "only"}); err == nil {
		t.Fatal("StartS3Server accepted an access key without a secret")
	}
}

// TestS3SymlinkBucketRefused covers the bucket-level symlink escape.
func TestS3SymlinkBucketRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "evil")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	port := startTestS3(t, root, S3Config{})
	if got := s3Request(t, http.MethodGet, port, "/evil/secret.txt", S3Config{}); got == http.StatusOK {
		t.Fatal("object outside the root was served through a symlinked bucket")
	}
}
