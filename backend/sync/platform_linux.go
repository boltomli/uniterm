//go:build linux && !android

package sync

// Linux keychain: go-keyring over the Secret Service D-Bus API
// (zalando/go-keyring/secret_service). Items carry the entry name in their
// "username" attribute and the service in their "service" attribute.

import (
	"github.com/zalando/go-keyring"
	ss "github.com/zalando/go-keyring/secret_service"
	"os"
	"path/filepath"
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

// keychainList enumerates the Secret Service items whose "service" attribute
// matches, returning each item's "username" attribute (where go-keyring
// stores the entry name). Attributes are read directly via the D-Bus
// property — go-keyring's ss package only exposes search/secret calls.
func keychainList(service string) ([]string, error) {
	svc, err := ss.NewSecretService()
	if err != nil {
		return nil, err
	}
	collection := svc.GetLoginCollection()
	if err := svc.Unlock(collection.Path()); err != nil {
		return nil, err
	}
	paths, err := svc.SearchItems(collection, map[string]string{"service": service})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		variant, err := svc.Object("org.freedesktop.secrets", p).
			GetProperty("org.freedesktop.Secret.Item.Attributes")
		if err != nil {
			continue
		}
		attrs, ok := variant.Value().(map[string]string)
		if !ok {
			continue
		}
		if name := attrs["username"]; name != "" {
			keys = append(keys, name)
		}
	}
	return keys, nil
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
