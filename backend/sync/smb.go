package sync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/ys-ll/uniterm/backend/session"
)

// smbFS adapts an SMB session to the sync engine's RemoteFS. The session
// speaks share-prefixed frontend paths ("/share/dir/file"), so every path
// this FS builds carries the share as its first segment; the session's
// smbInternal strips it before hitting the wire.
type smbFS struct {
	sess  sessionFS
	share string
	base  string // trimmed remote path inside the share
}

// full builds the share-prefixed path of a bare file name.
func (f *smbFS) full(name string) string {
	return "/" + f.share + "/" + path.Join(f.base, name)
}

// Probe creates every missing segment of the base path inside the share.
// The session's MakeDir stats before creating, so re-creating an existing
// directory is tolerated by the session itself.
func (f *smbFS) Probe() error {
	root := "/" + f.share
	if f.base == "" {
		return nil
	}
	cur := root
	for _, seg := range splitSegments(f.base) {
		cur = cur + "/" + seg
		if err := f.sess.MakeDir(cur); err != nil && !strings.Contains(strings.ToLower(err.Error()), "exist") {
			return fmt.Errorf("smb mkdir %s: %w", cur, err)
		}
	}
	return nil
}

func (f *smbFS) Get(name string) ([]byte, error) {
	data, err := f.sess.GetContent(f.full(name))
	if err != nil {
		if isSMBNotFound(err) {
			return nil, ErrRemoteNotFound
		}
		return nil, fmt.Errorf("smb get %s: %w", name, err)
	}
	return data, nil
}

func (f *smbFS) Put(name string, data []byte) error {
	// PutContent mkdirs the parent directory inside the share itself.
	return f.sess.PutContent(f.full(name), data)
}

// isSMBNotFound: go-smb2 maps STATUS_OBJECT_NAME_NOT_FOUND and
// STATUS_OBJECT_PATH_NOT_FOUND onto os.ErrNotExist (conn.go), so the
// standard sentinel covers both file and path misses.
func isSMBNotFound(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, fs.ErrNotExist)
}

func newSMBBackend(remote RemoteConfig, secret string, files []string, newSession func() sessionFS) (SnapshotBackend, error) {
	if remote.Server == "" {
		return nil, fmt.Errorf("smb: server not set")
	}
	if remote.Share == "" {
		return nil, fmt.Errorf("smb: share not set")
	}
	host, port := splitHostPort(remote.Server, 445)
	sess := newSession()
	err := sess.Connect(session.ConnectionConfig{
		Type:      "smb",
		Host:      host,
		Port:      port,
		User:      remote.User,
		Password:  secret,
		SmbDomain: remote.Domain,
	})
	if err != nil {
		return nil, fmt.Errorf("smb connect: %w", err)
	}
	// Navigating into the share name mounts it; this also verifies the
	// share exists before anything else runs.
	if _, err := sess.ChangeRemoteDir("/" + remote.Share); err != nil {
		_ = sess.Disconnect()
		return nil, fmt.Errorf("smb mount share %s: %w", remote.Share, err)
	}
	fs := &smbFS{sess: sess, share: remote.Share, base: strings.Trim(remote.Path, "/")}
	return &sessionBackend{
		SnapshotBackend: NewRemoteSnapshotBackend(fs, files),
		closer:          sess.Disconnect,
	}, nil
}
