//go:build android

package utils

import (
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// EnsureTempDir points TMPDIR at the app's private storage. Android leaves
// TMPDIR unset, so Go's os.TempDir/os.MkdirTemp fall back to /data/local/tmp,
// which the app sandbox cannot write to ("mkdir /data/local/tmp/...:
// permission denied"). Resolve the app files dir via the wails bridge,
// create a tmp/ subdir next to it, and export it as TMPDIR so every
// os.MkdirTemp("", ...) in the process lands somewhere writable. No-op
// before the bridge is attached — call again from flows that need a temp
// dir later (e.g. sync service init).
func EnsureTempDir() {
	if os.Getenv("TMPDIR") != "" {
		return
	}
	base := application.Android.StoragePath()
	if base == "" {
		return
	}
	dir := filepath.Join(base, "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	os.Setenv("TMPDIR", dir)
}
