package sync

import (
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/ys-ll/uniterm/backend/session"
)

// fakeSession records the file-session calls the remote shims make.
type fakeSession struct {
	content      map[string][]byte
	missingErr   error // returned for unknown keys on GetContent
	putErr       error
	puts         []string
	mkdirs       []string
	changedTo    []string
	connectCfg   *session.ConnectionConfig
	connectErr   error
	changedErr   error
	mkdirErr     error
	statExists   map[string]bool
	disconnected bool
}

func (f *fakeSession) Connect(cfg session.ConnectionConfig) error {
	f.connectCfg = &cfg
	return f.connectErr
}

func (f *fakeSession) Disconnect() error {
	f.disconnected = true
	return nil
}

func (f *fakeSession) MakeDir(dir string) error {
	f.mkdirs = append(f.mkdirs, dir)
	if f.mkdirErr != nil {
		return f.mkdirErr
	}
	return nil
}

// Stat implements the optional remoteStater capability; it reports every
// path in statExists as present.
func (f *fakeSession) Stat(p string) (os.FileInfo, error) {
	if f.statExists[p] {
		return fakeFileInfo{}, nil
	}
	return nil, os.ErrNotExist
}

// fakeFileInfo is a minimal os.FileInfo for the Stat fake.
type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return true }
func (fakeFileInfo) Sys() interface{}   { return nil }

func (f *fakeSession) ChangeRemoteDir(dir string) (session.FileListResult, error) {
	f.changedTo = append(f.changedTo, dir)
	if f.changedErr != nil {
		return session.FileListResult{}, f.changedErr
	}
	return session.FileListResult{}, nil
}

func (f *fakeSession) GetContent(remotePath string) ([]byte, error) {
	if f.missingErr != nil {
		if _, ok := f.content[remotePath]; !ok {
			return nil, f.missingErr
		}
	}
	if data, ok := f.content[remotePath]; ok {
		return data, nil
	}
	return nil, errors.New("fake: missing " + remotePath)
}

func (f *fakeSession) PutContent(remotePath string, content []byte) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.puts = append(f.puts, remotePath)
	return nil
}

func newFakeSession() *fakeSession {
	return &fakeSession{content: map[string][]byte{}, statExists: map[string]bool{}}
}

func TestSFTPFSGetNotFound(t *testing.T) {
	sess := newFakeSession()
	sess.missingErr = fs.ErrNotExist
	_, err := newSFTPBackend(RemoteConfig{Server: "host", Path: "sync"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Disconnect()
	f := &sftpFS{sess: sess, base: "sync"}
	if _, err := f.Get("manifest.json"); !errors.Is(err, ErrRemoteNotFound) {
		t.Fatalf("want ErrRemoteNotFound, got %v", err)
	}
}

func TestS3FSGetNotFound(t *testing.T) {
	sess := newFakeSession()
	sess.missingErr = errors.New("status code: 404 Not Found")
	_, err := newS3Backend(RemoteConfig{Server: "https://s3", Bucket: "bkt"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Disconnect()
	f := &s3FS{sess: sess, bucket: "bkt", base: ""}
	if _, err := f.Get("manifest.json"); !errors.Is(err, ErrRemoteNotFound) {
		t.Fatalf("want ErrRemoteNotFound, got %v", err)
	}
}

func TestS3FSServerErrorNotNotFound(t *testing.T) {
	sess := newFakeSession()
	sess.missingErr = errors.New("status code: 500 Internal Server Error")
	_, err := newS3Backend(RemoteConfig{Server: "https://s3", Bucket: "bkt"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Disconnect()
	f := &s3FS{sess: sess, bucket: "bkt", base: ""}
	if _, err := f.Get("manifest.json"); errors.Is(err, ErrRemoteNotFound) {
		t.Fatal("a 500 must not map to ErrRemoteNotFound")
	}
}

func TestSMBFSGetNotFound(t *testing.T) {
	sess := newFakeSession()
	sess.missingErr = os.ErrNotExist
	_, err := newSMBBackend(RemoteConfig{Server: "host", Share: "team"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Disconnect()
	f := &smbFS{sess: sess, share: "team", base: ""}
	if _, err := f.Get("manifest.json"); !errors.Is(err, ErrRemoteNotFound) {
		t.Fatalf("want ErrRemoteNotFound, got %v", err)
	}
}

// TestS3FSPathsCarryBucketPrefix pins the key contract: the shim paths
// carry the bucket as the first segment because the session's s3Key strips
// it — so base lands as a directory INSIDE the bucket, not on the bucket
// root.
func TestS3FSPathsCarryBucketPrefix(t *testing.T) {
	sess := newFakeSession()
	_, err := newS3Backend(RemoteConfig{Server: "https://s3", Bucket: "bkt", Path: "sync"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Disconnect()
	f := &s3FS{sess: sess, bucket: "bkt", base: "sync"}
	if err := f.Put("connections.json", []byte("cipher")); err != nil {
		t.Fatal(err)
	}
	if len(sess.puts) != 1 || sess.puts[0] != "/bkt/sync/connections.json" {
		t.Fatalf("puts = %v, want [/bkt/sync/connections.json]", sess.puts)
	}
}

func TestS3ProbeMissingProbeKeySucceeds(t *testing.T) {
	sess := newFakeSession()
	sess.missingErr = errors.New("status code: 404 Not Found")
	b, err := newS3Backend(RemoteConfig{Server: "https://s3", Bucket: "bkt"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer closeBackend(b)
	if err := b.Probe(); err != nil {
		t.Fatalf("probe on an empty bucket must succeed, got %v", err)
	}
}

func TestSFTPProbeCreatesSegments(t *testing.T) {
	sess := newFakeSession()
	b, err := newSFTPBackend(RemoteConfig{Server: "host", Path: "a/b/c"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer closeBackend(b)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "a/b", "a/b/c"}
	if len(sess.mkdirs) != len(want) {
		t.Fatalf("mkdirs = %v, want %v", sess.mkdirs, want)
	}
	for i, w := range want {
		if sess.mkdirs[i] != w {
			t.Fatalf("mkdirs = %v, want %v", sess.mkdirs, want)
		}
	}
}

// TestSFTPProbeToleratesGenericFailureOnExisting pins the real-server
// behavior behind issue reports like "mkdir uniTerm-sync: Failure
// (SSH_FX_FAILURE)": servers that answer mkdir-of-existing with the
// generic failure code instead of FILE_ALREADY_EXISTS. The Stat re-check
// must clear it, and the session must expose Stat for that to work.
func TestSFTPProbeToleratesGenericFailureOnExisting(t *testing.T) {
	sess := newFakeSession()
	sess.mkdirErr = errors.New(`"Failure" (SSH_FX_FAILURE)`)
	for _, seg := range []string{"/srv", "/srv/uniTerm-sync"} {
		sess.statExists[seg] = true
	}
	b, err := newSFTPBackend(RemoteConfig{Server: "host", Path: "/srv/uniTerm-sync"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer closeBackend(b)
	if err := b.Probe(); err != nil {
		t.Fatalf("existing directory reported via generic FAILURE must be tolerated, got %v", err)
	}
}

// TestSFTPProbeFailsWhenSegmentMissing pins the negative case: the generic
// failure on a directory that Stat cannot find is a real error (e.g.
// permission denied creating it).
func TestSFTPProbeFailsWhenSegmentMissing(t *testing.T) {
	sess := newFakeSession()
	sess.mkdirErr = errors.New(`"Permission denied" (SSH_FX_PERMISSION_DENIED)`)
	b, err := newSFTPBackend(RemoteConfig{Server: "host", Path: "/srv/uniTerm-sync"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer closeBackend(b)
	if err := b.Probe(); err == nil {
		t.Fatal("a mkdir failure on a missing segment must surface")
	}
}

func TestSFTPProbeKeepsAbsolutePathRoot(t *testing.T) {
	sess := newFakeSession()
	b, err := newSFTPBackend(RemoteConfig{Server: "host", Path: "/srv/sync"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer closeBackend(b)
	if err := b.Probe(); err != nil {
		t.Fatal(err)
	}
	want := []string{"/srv", "/srv/sync"}
	if len(sess.mkdirs) != len(want) {
		t.Fatalf("mkdirs = %v, want %v", sess.mkdirs, want)
	}
	for i, w := range want {
		if sess.mkdirs[i] != w {
			t.Fatalf("mkdirs = %v, want %v", sess.mkdirs, want)
		}
	}
}

func TestSMBFSPathsCarrySharePrefix(t *testing.T) {
	sess := newFakeSession()
	_, err := newSMBBackend(RemoteConfig{Server: "host", Share: "team", Path: "sync"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Disconnect()
	f := &smbFS{sess: sess, share: "team", base: "sync"}
	if err := f.Put("connections.json", []byte("cipher")); err != nil {
		t.Fatal(err)
	}
	if len(sess.puts) != 1 || sess.puts[0] != "/team/sync/connections.json" {
		t.Fatalf("puts = %v, want [/team/sync/connections.json]", sess.puts)
	}
}

func TestSMBMountsShareViaChangeRemoteDir(t *testing.T) {
	sess := newFakeSession()
	_, err := newSMBBackend(RemoteConfig{Server: "host", Share: "team", Path: "sync"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Disconnect()
	if len(sess.changedTo) != 1 || sess.changedTo[0] != "/team" {
		t.Fatalf("changedTo = %v, want [/team]", sess.changedTo)
	}
}

func TestSMBMountFailureDisconnects(t *testing.T) {
	sess := newFakeSession()
	sess.changedErr = errors.New("no such share")
	_, err := newSMBBackend(RemoteConfig{Server: "host", Share: "nope"}, "pw", nil, func() sessionFS { return sess })
	if err == nil {
		t.Fatal("want error")
	}
	if !sess.disconnected {
		t.Fatal("mount failure must disconnect the session")
	}
}

func TestS3ConnectionConfigCarriesRemoteFields(t *testing.T) {
	sess := newFakeSession()
	_, err := newS3Backend(RemoteConfig{
		Server: "https://s3.example.com", Bucket: "bkt", Region: "us-east-1",
		URLStyle: "path", User: "AK",
	}, "SECRET", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	cfg := sess.connectCfg
	if cfg == nil {
		t.Fatal("Connect was not called")
	}
	if cfg.Host != "https://s3.example.com" || cfg.S3Bucket != "bkt" ||
		cfg.S3Region != "us-east-1" || cfg.S3URLStyle != "path" ||
		cfg.User != "AK" || cfg.Password != "SECRET" {
		t.Fatalf("connection config = %+v", cfg)
	}
}

func TestSFTPConnectionConfigUsesPasswordAuth(t *testing.T) {
	sess := newFakeSession()
	_, err := newSFTPBackend(RemoteConfig{Server: "host:2222", Path: "sync", User: "alice"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	cfg := sess.connectCfg
	if cfg.Host != "host" || cfg.Port != 2222 || cfg.User != "alice" ||
		cfg.AuthType != "password" || cfg.Password != "pw" {
		t.Fatalf("connection config = %+v", cfg)
	}
}

func TestSessionBackendCloseDisconnects(t *testing.T) {
	sess := newFakeSession()
	b, err := newSFTPBackend(RemoteConfig{Server: "host"}, "pw", nil, func() sessionFS { return sess })
	if err != nil {
		t.Fatal(err)
	}
	if err := b.(interface{ Close() error }).Close(); err != nil {
		t.Fatal(err)
	}
	if !sess.disconnected {
		t.Fatal("Close must disconnect the session")
	}
}

func TestNewRemoteBackendRejectsUnknown(t *testing.T) {
	s := &SyncService{}
	if _, err := s.newRemoteBackend("ftp", RemoteConfig{}, "", nil); err == nil {
		t.Fatal("want error for unknown backend")
	}
}

func TestNewS3RequiresBucket(t *testing.T) {
	s := &SyncService{}
	if _, err := s.newRemoteBackend("s3", RemoteConfig{Server: "https://s3"}, "", nil); err == nil {
		t.Fatal("want error for missing bucket")
	}
}

func TestNewSMBRequiresShare(t *testing.T) {
	s := &SyncService{}
	if _, err := s.newRemoteBackend("smb", RemoteConfig{Server: "host"}, "", nil); err == nil {
		t.Fatal("want error for missing share")
	}
}

func TestSplitHostPort(t *testing.T) {
	if h, p := splitHostPort("host:2222", 22); h != "host" || p != 2222 {
		t.Fatalf("got %s:%d", h, p)
	}
	if h, p := splitHostPort("host", 445); h != "host" || p != 445 {
		t.Fatalf("got %s:%d", h, p)
	}
}

func TestSFTPMkdirToleratedCodes(t *testing.T) {
	if !sftpMkdirTolerated(&sftp.StatusError{Code: 11}) {
		t.Fatal("SSH_FX_FILE_ALREADY_EXISTS must be tolerated")
	}
	if !sftpMkdirTolerated(errors.New("File already exists")) {
		t.Fatal("a spelled-out already-exists message must be tolerated")
	}
	if sftpMkdirTolerated(errors.New("permission denied")) {
		t.Fatal("unrelated errors must not be tolerated")
	}
}
