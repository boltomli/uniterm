//go:build windows

package sync

// Windows keychain: go-keyring over the Windows Credential Manager
// (danieljoos/wincred). Entries are stored as generic credentials whose
// target name is "<service>:<key>".

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljoos/wincred"
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

// keychainList enumerates the generic credentials whose target name starts
// with the app's service prefix ("uniTerm:"), returning the entry names
// after the prefix. Same convention go-keyring's own DeleteAll uses to find
// entries under a service.
func keychainList(service string) ([]string, error) {
	creds, err := wincred.List()
	if err != nil {
		return nil, err
	}
	prefix := service + ":"
	keys := make([]string, 0, len(creds))
	for _, c := range creds {
		if strings.HasPrefix(c.TargetName, prefix) {
			keys = append(keys, strings.TrimPrefix(c.TargetName, prefix))
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
