package localsrv

import (
	"fmt"
	"net"
	"os"

	"github.com/smallfz/libnfs-go/auth"
	"github.com/smallfz/libnfs-go/backend"
	"github.com/smallfz/libnfs-go/fs"
	"github.com/smallfz/libnfs-go/memfs"
	"github.com/smallfz/libnfs-go/server"
)

// StartNFSServer starts an experimental NFSv4 server backed by an in-memory
// filesystem. Any data written to the mount is lost when the server stops —
// real OS file-system support is tracked as a future enhancement. Returns the
// server id.
func (m *Manager) StartNFSServer(cfg NFSConfig) (string, error) {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return "", fmt.Errorf("invalid port: %d", cfg.Port)
	}

	id, ctx := m.newServer()
	m.log(ServiceNFS, id, fmt.Sprintf("Starting NFSv4 server on port %d (experimental, memory-backed) ...", cfg.Port))

	// Build the in-memory filesystem and seed a welcome root entry so the
	// client sees content on first mount rather than an empty mountpoint.
	mfs := memfs.NewMemFS()
	_ = mfs.MkdirAll("/", os.FileMode(0o755))
	_ = mfs.MkdirAll("/shared", os.FileMode(0o755))

	b := backend.New(func() fs.FS { return mfs }, auth.Null)

	addr := fmt.Sprintf(":%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		m.finish(id)
		return "", fmt.Errorf("failed to listen on TCP port %d: %w", cfg.Port, err)
	}

	svr, err := server.NewServer(ln, b)
	if err != nil {
		_ = ln.Close()
		m.finish(id)
		return "", fmt.Errorf("failed to create NFS server: %w", err)
	}

	// Close the listener to terminate Serve() — libnfs-go has no Server.Stop.
	m.setStop(id, func() {
		_ = ln.Close()
	})

	go func() {
		defer m.finish(id)
		m.state(ServiceNFS, id, true)
		m.log(ServiceNFS, id, "NFSv4 server started (experimental, in-memory FS — data lost on stop)")
		m.log(ServiceNFS, id, "Mount hint: Linux: mount -o nfsvers=4,minorversion=0,noacl,tcp -t nfs <host>:/ /mnt")

		done := make(chan error, 1)
		go func() { done <- svr.Serve() }()

		select {
		case <-ctx.Done():
			// Manager asked us to shut down — close listener so Serve() exits.
			_ = ln.Close()
			<-done // wait for Serve() to return
		case err := <-done:
			if err != nil {
				m.log(ServiceNFS, id, fmt.Sprintf("NFS server error: %v", err))
			}
		}

		m.state(ServiceNFS, id, false)
		m.log(ServiceNFS, id, "NFSv4 server stopped")
	}()

	m.startTimeout(id, ServiceNFS, cfg.TimeoutSeconds)
	return id, nil
}
