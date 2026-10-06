//go:build !android

package sync

import (
	"os"
	"path/filepath"
)

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
