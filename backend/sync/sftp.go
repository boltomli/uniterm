package sync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"github.com/ys-ll/uniterm/backend/session"
)

// sftpFS adapts an SFTP session to the sync engine's RemoteFS. Paths are
// joined with the configured base path; a relative base resolves against
// the login directory (the session joins cwd), mirroring the file manager.
type sftpFS struct {
	sess sessionFS
	base string
}

func (f *sftpFS) full(name string) string {
	return path.Join(f.base, name)
}

// sftpFxFileAlreadyExists is SSH_FX_FILE_ALREADY_EXISTS from the SFTP
// spec; pkg/sftp leaves the code unmapped (it normalises only a handful
// of codes), so it is matched explicitly.
const sftpFxFileAlreadyExists = 11

// sftpMkdirTolerated reports whether a failed MakeDir means the segment
// already exists: either the spec code, or a server whose status message
// spells it out (some servers answer the generic failure code).
func sftpMkdirTolerated(err error) bool {
	if err == nil {
		return false
	}
	var se *sftp.StatusError
	if errors.As(err, &se) && se.Code == sftpFxFileAlreadyExists {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "exist")
}

// remoteStater is an optional sessionFS capability: checking whether a
// remote path exists. SFTPSession implements it; the Probe fallback uses
// it to tell "directory already exists" from a real failure.
type remoteStater interface {
	Stat(p string) (os.FileInfo, error)
}

// Probe creates every missing segment of the base path. Segment-wise so a
// deep base path works even where only single-level Mkdir exists. An
// absolute base keeps its leading slash through every segment — splitSegments
// strips it, and without restoring it the mkdirs would land relative to the
// login directory instead of the filesystem root.
//
// A failed MakeDir is only an error when the segment does not already
// exist: many servers answer mkdir-of-existing with the generic
// SSH_FX_FAILURE instead of the spec's FILE_ALREADY_EXISTS code, and a
// message match cannot cover every server's wording. The existence
// re-check does.
func (f *sftpFS) Probe() error {
	prefix := ""
	if strings.HasPrefix(f.base, "/") {
		prefix = "/"
	}
	segs := splitSegments(f.base)
	cur := prefix
	for i := range segs {
		cur = path.Join(cur, segs[i])
		err := f.sess.MakeDir(cur)
		if err == nil {
			continue
		}
		if !sftpMkdirTolerated(err) && !f.segmentExists(cur) {
			return fmt.Errorf("sftp mkdir %s: %w", cur, err)
		}
	}
	return nil
}

// segmentExists reports whether the remote path exists, via the session's
// optional Stat capability. A session without Stat reports "unknown"
// (false) — the caller then surfaces the original mkdir error.
func (f *sftpFS) segmentExists(p string) bool {
	st, ok := f.sess.(remoteStater)
	if !ok {
		return false
	}
	_, err := st.Stat(p)
	return err == nil
}

func (f *sftpFS) Get(name string) ([]byte, error) {
	data, err := f.sess.GetContent(f.full(name))
	if err != nil {
		if isSFTPNotFound(err) {
			return nil, ErrRemoteNotFound
		}
		return nil, fmt.Errorf("sftp get %s: %w", name, err)
	}
	return data, nil
}

func (f *sftpFS) Put(name string, data []byte) error {
	// PutContent mkdirs the parent directory itself.
	return f.sess.PutContent(f.full(name), data)
}

// isSFTPNotFound: pkg/sftp maps SSH_FX_NO_SUCH_FILE onto fs.ErrNotExist.
func isSFTPNotFound(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

func newSFTPBackend(remote RemoteConfig, secret string, files []string, newSession func() sessionFS) (SnapshotBackend, error) {
	if remote.Server == "" {
		return nil, fmt.Errorf("sftp: server not set")
	}
	host, port := splitHostPort(remote.Server, 22)
	sess := newSession()
	err := sess.Connect(session.ConnectionConfig{
		Type:     "sftp",
		Host:     host,
		Port:     port,
		User:     remote.User,
		AuthType: "password",
		Password: secret,
	})
	if err != nil {
		return nil, fmt.Errorf("sftp connect: %w", err)
	}
	// Keep the leading slash: an absolute Path must stay absolute — the
	// session resolves relative paths against the login directory, so
	// trimming it would silently retarget the sync root.
	fs := &sftpFS{sess: sess, base: strings.TrimSuffix(remote.Path, "/")}
	return &sessionBackend{
		SnapshotBackend: NewRemoteSnapshotBackend(fs, files),
		closer:          sess.Disconnect,
	}, nil
}
