package sync

import (
	"net"
	"strconv"
	"strings"

	"github.com/ys-ll/uniterm/backend/session"
)

// sessionFS is the slice of the file-transfer sessions the remote shims
// use. The concrete sessions (session.WebDAVSession / SFTPSession /
// S3Session / SMBSession) satisfy it structurally; tests inject fakes.
type sessionFS interface {
	Connect(config session.ConnectionConfig) error
	Disconnect() error
	MakeDir(dir string) error
	ChangeRemoteDir(dir string) (session.FileListResult, error)
	GetContent(remotePath string) ([]byte, error)
	PutContent(remotePath string, content []byte) error
}

// sessionBackend embeds the snapshot engine (Probe/Head/FetchAll/StoreAll
// over a RemoteFS shim) and adds Close, so every sync run disconnects the
// dedicated session it dialed.
type sessionBackend struct {
	SnapshotBackend
	closer func() error
}

// Close disconnects the session. Sync callers defer this at every
// newRemoteBackend site.
func (b *sessionBackend) Close() error { return b.closer() }

// splitHostPort parses host[:port], defaulting to defaultPort.
func splitHostPort(server string, defaultPort int) (string, int) {
	if host, portStr, err := net.SplitHostPort(server); err == nil {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			return host, p
		}
		return host, defaultPort
	}
	return strings.Trim(server, "[]"), defaultPort
}

// splitSegments splits a base path into non-empty segments.
func splitSegments(base string) []string {
	base = strings.Trim(base, "/")
	if base == "" {
		return nil
	}
	return strings.Split(base, "/")
}
