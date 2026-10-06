//go:build android

package sync

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/ys-ll/uniterm/backend/utils"
)

// syncMetaDir resolves the directory holding sync metadata (sync-config.json
// and the local snapshot repo clone). The Android app sandbox has no
// $HOME/$XDG_CONFIG_HOME, so os.UserConfigDir fails; use the app's private
// files dir reported by the wails bridge instead (same base as the data
// dir). The dir is a sibling of — not inside — the uniTerm data dir so sync
// never tries to sync its own metadata.
func syncMetaDir() (string, error) {
	// First bridge-backed call in the sync flow: also point TMPDIR at the app
	// sandbox so the service's os.MkdirTemp calls never hit /data/local/tmp.
	utils.EnsureTempDir()
	base := application.Android.StoragePath()
	if base == "" {
		return "", errors.New("android: storage path unavailable (bridge not attached)")
	}
	dir := filepath.Join(base, "uniTerm-sync-meta")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
