//go:build android

package sync

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/zalando/go-keyring"
)

// keychainGet reads from the wails Android bridge secure storage
// (EncryptedSharedPreferences, AES256-GCM). A missing key maps to
// keyring.ErrNotFound so desktop and mobile callers observe identical
// not-found semantics (e.g. GetPassword returning "").
func keychainGet(service, key string) (string, error) {
	v, found, err := application.Android.SecureGet(key)
	if err != nil {
		return "", err
	}
	if !found {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func keychainSet(service, key, value string) error {
	// service is ignored: the wails bridge stores everything in one
	// encrypted prefs file; our keys are already namespaced
	// ("conn/<id>", "ai-model/<id>", ...).
	return application.Android.SecureSet(key, value)
}

func keychainDelete(service, key string) error {
	return application.Android.SecureDelete(key)
}
