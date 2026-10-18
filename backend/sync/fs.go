package sync

import (
	"errors"
)

// ErrRemoteNotFound reports that a resource is absent on the sync remote.
// Every RemoteFS implementation normalizes its backend's own not-found
// error to this sentinel, so the snapshot engine can tell "remote is
// empty" (Head returning nil) apart from a real failure.
var ErrRemoteNotFound = errors.New("sync: remote resource not found")

// RemoteFS is the minimal filesystem view a snapshot backend needs on the
// remote: verify reachability, read a file, write a file. The snapshot
// engine (remote_snapshot.go) turns this into the SnapshotBackend contract
// of Probe, Head, FetchAll and StoreAll, so one engine serves all remotes.
//
// name arguments are bare file names such as manifest.json or
// connections.json; each implementation owns the base path and joins it.
type RemoteFS interface {
	// Probe verifies reachability and credentials. Idempotent.
	Probe() error
	// Get reads a file and returns ErrRemoteNotFound when it is absent.
	Get(name string) ([]byte, error)
	// Put writes a file, creating any missing parent directories.
	Put(name string, data []byte) error
}
