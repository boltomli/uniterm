package sync

import (
	"fmt"
	"path"
	"strings"

	"github.com/ys-ll/uniterm/backend/session"
)

// webdavFS adapts a WebDAV file session to RemoteFS, so WebDAV sync goes
// through the same session layer as sftp/s3/smb.
type webdavFS struct {
	sess sessionFS
	base string // trimmed base path, "" = server root
}

// newWebDAVFS builds the shim over an already-connected WebDAV session.
func newWebDAVFS(sess sessionFS, basePath string) *webdavFS {
	return &webdavFS{sess: sess, base: strings.Trim(basePath, "/")}
}

// join builds the remote path of a bare file name.
func (f *webdavFS) join(name string) string {
	if f.base == "" {
		return name
	}
	return f.base + "/" + name
}

// webdavMkdirTolerated reports whether a failed MakeDir means the
// collection already exists: gowebdav surfaces the server's 405 (MKCOL on
// an existing collection) in its error message.
func webdavMkdirTolerated(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "405") || strings.Contains(strings.ToLower(err.Error()), "exist")
}

// Probe creates every missing segment of the base path, segment-wise
// because the session's MakeDir is single-level. An empty base path is a
// no-op, keeping the httptest fakes (which handle no OPTIONS) working.
func (f *webdavFS) Probe() error {
	cur := ""
	for _, seg := range splitSegments(f.base) {
		cur = path.Join(cur, seg)
		if err := f.sess.MakeDir(cur); err != nil && !webdavMkdirTolerated(err) {
			return fmt.Errorf("webdav mkdir %s: %w", cur, err)
		}
	}
	return nil
}

// webdavIsNotFound matches the HTTP status text gowebdav embeds in its
// error messages ("404 Not Found"); there is no stable exported sentinel,
// so the status string is matched. Pinned by TestWebDAVFSGetNotFound.
func webdavIsNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "404")
}

func (f *webdavFS) Get(name string) ([]byte, error) {
	data, err := f.sess.GetContent(f.join(name))
	if err != nil {
		if webdavIsNotFound(err) {
			return nil, ErrRemoteNotFound
		}
		return nil, fmt.Errorf("webdav get %s: %w", name, err)
	}
	return data, nil
}

func (f *webdavFS) Put(name string, data []byte) error {
	// The session's PutContent creates the parent directory itself.
	return f.sess.PutContent(f.join(name), data)
}

// newWebDAVSessionBackend dials a dedicated WebDAV session for the sync
// run and wraps it in the snapshot engine. newSession exists for test
// injection; production passes session.NewWebDAVSession.
func newWebDAVSessionBackend(remote RemoteConfig, secret string, files []string, newSession func() sessionFS) (SnapshotBackend, error) {
	if remote.Server == "" {
		return nil, fmt.Errorf("webdav: server not set")
	}
	sess := newSession()
	err := sess.Connect(session.ConnectionConfig{
		Type:     "webdav",
		Host:     remote.Server,
		User:     remote.User,
		Password: secret,
	})
	if err != nil {
		return nil, fmt.Errorf("webdav connect: %w", err)
	}
	fs := newWebDAVFS(sess, remote.Path)
	return &sessionBackend{
		SnapshotBackend: NewRemoteSnapshotBackend(fs, files),
		closer:          sess.Disconnect,
	}, nil
}

// NewWebDAVBackend builds the engine over a WebDAV share through the
// session layer, dialing a dedicated session for the sync run. Kept as the
// legacy constructor name so existing call sites and tests stay valid.
func NewWebDAVBackend(serverURL, basePath, user, password string, files []string) SnapshotBackend {
	sess := session.NewWebDAVSession("sync-webdav")
	if err := sess.Connect(session.ConnectionConfig{Type: "webdav", Host: serverURL, User: user, Password: password}); err != nil {
		return &failedBackend{err: fmt.Errorf("webdav connect: %w", err)}
	}
	fs := newWebDAVFS(sess, basePath)
	return &sessionBackend{
		SnapshotBackend: NewRemoteSnapshotBackend(fs, files),
		closer:          sess.Disconnect,
	}
}

// failedBackend reports a construction failure on first use. It exists
// because NewWebDAVBackend's legacy signature cannot return an error; all
// in-package call sites go through newRemoteBackend, which propagates the
// error properly and never produces this type.
type failedBackend struct {
	err error
}

func (b *failedBackend) Probe() error                 { return b.err }
func (b *failedBackend) Head() (*SnapshotInfo, error) { return nil, b.err }
func (b *failedBackend) FetchAll(dir string) error    { return b.err }
func (b *failedBackend) StoreAll(stagingDir string, salt []byte, manifest SnapshotInfo) error {
	return b.err
}
