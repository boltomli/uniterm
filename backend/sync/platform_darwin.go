//go:build darwin

package sync

// macOS keychain: go-keyring, which shells out to the `security` CLI. The
// CLI has no listing call, so keychain enumeration is unsupported —
// credential recovery after a data-dir move is skipped and the recovery
// dialog handles the lost-key state instead.

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

func keychainGet(service, key string) (string, error) {
	return keyring.Get(service, key)
}

func keychainSet(service, key, value string) error {
	return keyring.Set(service, key, value)
}

func keychainDelete(service, key string) error {
	return keyring.Delete(service, key)
}

// keychainList is unsupported on macOS: `security` offers no way to
// enumerate a service's entries.
func keychainList(string) ([]string, error) {
	return nil, errors.New("keychain enumeration not supported on this platform")
}

// syncMetaDir resolves the directory holding sync metadata (sync-config.json
// and the local snapshot repo clone) via the standard user config dir.
func syncMetaDir() (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cfgDir, "uniTerm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
