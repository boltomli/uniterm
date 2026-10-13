package credentials

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
)

const metaFileName = "credentials.meta"

const (
	ModeKeychain       = "keychain"
	ModeMasterPassword = "master-password"
)

// Meta is the on-disk shape of credentials.meta: plaintext JSON holding the
// encryption mode and (master-password mode only) the PBKDF2 salt.
// Verifier is an EncryptField(VerifyPlaintext, key) blob that proves a key
// (derived password or keychain entry) opens this vault; "" on files written
// before the verifier existed.
type Meta struct {
	Mode     string
	Salt     []byte
	Verifier string
}

type metaJSON struct {
	Mode     string `json:"mode"`
	Salt     string `json:"salt,omitempty"`
	Verifier string `json:"verifier,omitempty"`
}

// VerifyPlaintext is the canary encrypted into Meta.Verifier. It is not a
// secret: its only job is to fail authentication under the wrong key.
const VerifyPlaintext = "uniterm-verify"

// MetaPath returns the path of credentials.meta inside dataDir.
func MetaPath(dataDir string) string { return filepath.Join(dataDir, metaFileName) }

// ReadMeta reads credentials.meta. Returns (nil, nil) when the file does not exist.
func ReadMeta(dataDir string) (*Meta, error) {
	data, err := os.ReadFile(MetaPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var mj metaJSON
	if err := json.Unmarshal(data, &mj); err != nil {
		return nil, err
	}
	m := &Meta{Mode: mj.Mode, Verifier: mj.Verifier}
	if mj.Salt != "" {
		salt, err := base64.StdEncoding.DecodeString(mj.Salt)
		if err != nil {
			return nil, err
		}
		m.Salt = salt
	}
	return m, nil
}

// WriteMeta writes credentials.meta (base64-encodes the salt). The write is
// atomic — temp file in the same directory, fsync-free rename — so a crash
// can never leave a half-written meta that locks the user out of the vault.
func WriteMeta(dataDir string, m *Meta) error {
	mj := metaJSON{Mode: m.Mode, Verifier: m.Verifier}
	if len(m.Salt) > 0 {
		mj.Salt = base64.StdEncoding.EncodeToString(m.Salt)
	}
	data, err := json.Marshal(mj)
	if err != nil {
		return err
	}
	return writeFileAtomic(MetaPath(dataDir), data, 0600)
}

// writeFileAtomic replaces path with data through a temp file + rename in the
// same directory, keeping the previous file intact on any failure.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
