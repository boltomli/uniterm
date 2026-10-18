package sync

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ys-ll/uniterm/backend/session"
)

// s3FS adapts an S3 session to the sync engine's RemoteFS. Object keys are
// the base prefix joined with the bare file name; S3 has no directories,
// so the prefix is implicit.
//
// The session's s3Key strips a leading "<bucket>/" segment (file-manager
// semantics, where paths look like /bucket/dir/key), so every path this
// shim builds carries the bucket as its first segment and lands on
// "<base>/<name>" under the configured bucket — the base is the sync
// directory INSIDE the bucket, never the whole bucket.
type s3FS struct {
	sess   sessionFS
	bucket string
	base   string
}

func (f *s3FS) full(name string) string {
	p := path.Join(f.base, name)
	if f.bucket != "" {
		p = "/" + f.bucket + "/" + p
	}
	return p
}

// Probe exercises the endpoint with an authenticated request: a read of a
// probe key that is expected to be absent. Any definitive not-found (or a
// hit, if the key somehow exists) proves reachability and credentials.
func (f *s3FS) Probe() error {
	// Route through Get so the backend's not-found normalization applies.
	_, err := f.Get(".sync-probe")
	if err == nil || errors.Is(err, ErrRemoteNotFound) {
		return nil
	}
	return fmt.Errorf("s3 probe: %w", err)
}

func (f *s3FS) Get(name string) ([]byte, error) {
	data, err := f.sess.GetContent(f.full(name))
	if err != nil {
		if isS3NotFound(err) {
			return nil, ErrRemoteNotFound
		}
		return nil, fmt.Errorf("s3 get %s: %w", name, err)
	}
	return data, nil
}

func (f *s3FS) Put(name string, data []byte) error {
	return f.sess.PutContent(f.full(name), data)
}

// isS3NotFound: simples3 surfaces non-200 reads as a plain error carrying
// the HTTP status text ("status code: 404 Not Found"); there is no typed
// sentinel, so the status string is matched. Pinned by TestS3FSGetNotFound.
func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "404")
}

func newS3Backend(remote RemoteConfig, secret string, files []string, newSession func() sessionFS) (SnapshotBackend, error) {
	if remote.Server == "" {
		return nil, fmt.Errorf("s3: endpoint not set")
	}
	if remote.Bucket == "" {
		return nil, fmt.Errorf("s3: bucket not set")
	}
	sess := newSession()
	err := sess.Connect(session.ConnectionConfig{
		Type:       "s3",
		Host:       remote.Server,
		User:       remote.User,
		Password:   secret,
		S3Bucket:   remote.Bucket,
		S3Region:   remote.Region,
		S3URLStyle: remote.URLStyle,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 connect: %w", err)
	}
	fs := &s3FS{sess: sess, bucket: remote.Bucket, base: strings.Trim(remote.Path, "/")}
	return &sessionBackend{
		SnapshotBackend: NewRemoteSnapshotBackend(fs, files),
		closer:          sess.Disconnect,
	}, nil
}
