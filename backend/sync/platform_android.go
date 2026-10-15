//go:build android

package sync

// Platform integration for the sync package: where sync metadata lives, and
// which secure storage backs the keychain. Everything else in the package is
// platform-neutral.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/ys-ll/uniterm/backend/utils"
	"github.com/zalando/go-keyring"
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

// keychainGet reads from the wails Android bridge secure storage
// (EncryptedSharedPreferences, AES256-GCM). Values are stored hex-encoded:
// the bridge passes strings via NewStringUTF (modified UTF-8), which can
// mangle non-BMP characters (emoji, some CJK extensions) — encoding guards
// against that. A missing key maps to keyring.ErrNotFound so desktop and
// mobile callers observe identical not-found semantics (e.g. GetPassword
// returning "").
func keychainGet(service, key string) (string, error) {
	raw, found, err := application.Android.SecureGet(key)
	if err != nil {
		return "", err
	}
	if !found {
		return "", keyring.ErrNotFound
	}
	v, err := hex.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("secure storage: corrupt value: %w", err)
	}
	return string(v), nil
}

func keychainSet(service, key, value string) error {
	// service is ignored: the wails bridge stores everything in one
	// encrypted prefs file; our keys are already namespaced
	// ("conn/<id>", "ai-model/<id>", ...). Values are hex-encoded before
	// crossing the JNI bridge — see keychainGet.
	return application.Android.SecureSet(key, hex.EncodeToString([]byte(value)))
}

func keychainDelete(service, key string) error {
	// Deleting a missing key is a no-op success on Android (desktop
	// go-keyring returns ErrNotFound); all callers tolerate either.
	return application.Android.SecureDelete(key)
}

// keychainList is unsupported on Android: the wails bridge exposes only
// get/set/delete on the encrypted prefs, with no enumeration. The data dir
// lives in the app sandbox and cannot be copied elsewhere, so credential
// recovery has no scenario here anyway.
func keychainList(string) ([]string, error) {
	return nil, errors.New("keychain enumeration not supported on this platform")
}
