package sync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const encFieldPrefix = "enc:v1:"

// isEncryptedField reports whether a secret field value is already in-place
// encrypted (opaque to sync). Sync must carry it through without backfilling
// from keychain or stripping it.
func isEncryptedField(s string) bool { return strings.HasPrefix(s, encFieldPrefix) }

// PasswordStore encrypts/decrypts individual in-place secret field values
// (enc:v1:). It is the credential store. Sync uses it to normalize fields to
// plaintext before upload (so the whole file is protected solely by the sync
// key) and back to enc:v1: after download (so local storage stays encrypted
// under the local credential mode's key).
type PasswordStore interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
	// Unlocked reports whether the credential store currently holds a usable
	// key. When false (master-password not yet entered, keychain lost, or never
	// set up), sync must refuse to run rather than leak or mis-carry secrets.
	Unlocked() bool
}

// EncryptConfigFiles encrypts entire config files from srcDir into destDir.
// kc is used to backfill legacy empty passwords from keychain before
// encryption. ps is used to normalize enc:v1: fields to plaintext so only the
// sync key protects the file at rest in the repo. Pass nil for either to skip
// that step.
func EncryptConfigFiles(srcDir, destDir string, key []byte, kc *Keychain, ps PasswordStore) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	for _, name := range syncedFiles {
		src := filepath.Join(srcDir, name)
		dest := filepath.Join(destDir, name)
		var err error
		switch name {
		case "connections.json":
			err = encryptConnectionsFile(src, dest, key, kc, ps)
		case "ai.json":
			err = encryptAIConfigFile(src, dest, key, kc, ps)
		case "identities.json":
			err = encryptIdentitiesFile(src, dest, key, ps)
		case "proxies.json":
			err = encryptProxiesFile(src, dest, key, ps)
		default:
			err = encryptGenericFile(src, dest, key)
		}
		if err != nil {
			return fmt.Errorf("encrypt %s: %w", name, err)
		}
	}

	return nil
}

func encryptConnectionsFile(src, dest string, key []byte, kc *Keychain, ps PasswordStore) error {
	data, err := readJSONFile(src)
	if err != nil {
		return err
	}

	if kc != nil || ps != nil {
		var wrapper struct {
			Groups      []map[string]interface{} `json:"groups"`
			Connections []map[string]interface{} `json:"connections"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return fmt.Errorf("parse connections: %w", err)
		}
		normalize := func(m map[string]interface{}, field string) {
			if v, _ := m[field].(string); isEncryptedField(v) && ps != nil {
				if pt, err := ps.Decrypt(v); err == nil {
					m[field] = pt
				}
			}
		}
		for _, cm := range wrapper.Connections {
			if pw, _ := cm["password"].(string); cm["authType"] == "password" && pw == "" {
				// Legacy: password stored in keychain, not in JSON.
				if id, ok := cm["id"].(string); ok && kc != nil {
					if kcPw, err := kc.GetPassword(id); err == nil && kcPw != "" {
						cm["password"] = kcPw
					}
				}
			}
			// Normalize every in-place encrypted secret — whatever the authType —
			// to plaintext for upload, so the whole file is protected solely by
			// the sync key (enc:v1: is bound to the per-device credential store
			// and must never escape into the repo).
			normalize(cm, "password")
			normalize(cm, "keyContent")
			normalize(cm, "sentinelPassword")
			normalize(cm, "tunnelSSHPassword")
		}
		data, _ = json.MarshalIndent(wrapper, "", "  ")
	}

	encoded, err := encryptBytes(data, key, filepath.Base(dest))
	if err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(encoded), 0600)
}

// encryptAIConfigFile normalizes model apiKeys before encrypting ai.json:
// keychain-backed keys (empty apiKey field) are pulled in via kc and
// enc:v1:-prefixed keys are normalized to plaintext through ps, so only
// the sync key protects the file at rest in the repo.
func encryptAIConfigFile(src, dest string, key []byte, kc *Keychain, ps PasswordStore) error {
	data, err := readJSONFile(src)
	if err != nil {
		return err
	}

	if kc != nil || ps != nil {
		var obj map[string]interface{}
		if err := json.Unmarshal(data, &obj); err == nil {
			if models, ok := obj["models"].([]interface{}); ok {
				for _, m := range models {
					if mm, ok := m.(map[string]interface{}); ok {
						ak, _ := mm["apiKey"].(string)
						if ak == "" {
							// Legacy: apiKey stored in keychain, not in JSON.
							if id, ok := mm["id"].(string); ok && kc != nil {
								if kcAk, err := kc.GetModelAPIKey(id); err == nil && kcAk != "" {
									mm["apiKey"] = kcAk
								}
							}
						} else if isEncryptedField(ak) && ps != nil {
							if pt, err := ps.Decrypt(ak); err == nil {
								mm["apiKey"] = pt
							}
						}
					}
				}
			}
			data, _ = json.MarshalIndent(obj, "", "  ")
		}
	}

	encoded, err := encryptBytes(data, key, filepath.Base(dest))
	if err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(encoded), 0600)
}

// encryptIdentitiesFile encrypts identities.json, normalizing any enc:v1:
// Password field to plaintext for upload.
func encryptIdentitiesFile(src, dest string, key []byte, ps PasswordStore) error {
	data, err := readJSONFile(src)
	if err != nil {
		return err
	}

	if ps != nil {
		var wrapper struct {
			Identities []map[string]interface{} `json:"identities"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return fmt.Errorf("parse identities: %w", err)
		}
		for _, im := range wrapper.Identities {
			if pw, ok := im["password"].(string); ok && isEncryptedField(pw) {
				if pt, err := ps.Decrypt(pw); err == nil {
					im["password"] = pt
				}
			}
			if kc, ok := im["keyContent"].(string); ok && isEncryptedField(kc) {
				if pt, err := ps.Decrypt(kc); err == nil {
					im["keyContent"] = pt
				}
			}
		}
		data, _ = json.MarshalIndent(wrapper, "", "  ")
	}

	encoded, err := encryptBytes(data, key, filepath.Base(dest))
	if err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(encoded), 0600)
}

// encryptProxiesFile encrypts proxies.json, normalizing any enc:v1: pass field
// to plaintext for upload.
func encryptProxiesFile(src, dest string, key []byte, ps PasswordStore) error {
	data, err := readJSONFile(src)
	if err != nil {
		return err
	}

	if ps != nil {
		var wrapper struct {
			Proxies []map[string]interface{} `json:"proxies"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return fmt.Errorf("parse proxies: %w", err)
		}
		for _, pm := range wrapper.Proxies {
			if pass, ok := pm["pass"].(string); ok && isEncryptedField(pass) {
				if pt, err := ps.Decrypt(pass); err == nil {
					pm["pass"] = pt
				}
			}
		}
		data, _ = json.MarshalIndent(wrapper, "", "  ")
	}

	encoded, err := encryptBytes(data, key, filepath.Base(dest))
	if err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(encoded), 0600)
}

// DecryptConfigFiles decrypts config files from srcDir into destDir.
// ps is used to re-encrypt plaintext secret fields back to enc:v1: under the
// local credential key after download. Pass nil for ps to keep fields as
// plaintext (e.g. temporary dirs used only for comparison).
func DecryptConfigFiles(srcDir, destDir string, key []byte, ps PasswordStore) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	for _, name := range syncedFiles {
		src := filepath.Join(srcDir, name)
		dest := filepath.Join(destDir, name)
		var err error
		switch name {
		case "connections.json":
			err = decryptConnectionsFile(src, dest, key, ps)
		case "ai.json":
			err = decryptAIConfigFile(src, dest, key, ps)
		case "identities.json":
			err = decryptIdentitiesFile(src, dest, key, ps)
		case "proxies.json":
			err = decryptProxiesFile(src, dest, key, ps)
		default:
			err = decryptGenericFile(src, dest, key)
		}
		if err != nil {
			return fmt.Errorf("decrypt %s: %w", name, err)
		}
	}

	return nil
}

func decryptConnectionsFile(src, dest string, key []byte, ps PasswordStore) error {
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(dest, []byte("{}"), 0600)
		}
		return err
	}

	plaintext, err := decryptBytes(string(data), key, filepath.Base(dest))
	if err != nil {
		return fmt.Errorf("decrypt connections: %w", err)
	}

	if ps != nil {
		var wrapper struct {
			Groups      []map[string]interface{} `json:"groups"`
			Connections []map[string]interface{} `json:"connections"`
		}
		if err := json.Unmarshal(plaintext, &wrapper); err != nil {
			return fmt.Errorf("parse connections: %w", err)
		}
		for _, cm := range wrapper.Connections {
			// Re-encrypt plaintext secrets under the local credential key.
			// Every authType is covered: the local store encrypts password,
			// keyContent, sentinelPassword and tunnelSSHPassword at rest
			// regardless of authType, so pull must mirror that here (the old
			// keyText carve-out would now leave plaintext passphrases on disk).
			reenc := func(field string) {
				if v, ok := cm[field].(string); ok && v != "" && !isEncryptedField(v) {
					if enc, err := ps.Encrypt(v); err == nil {
						cm[field] = enc
					}
				}
			}
			reenc("password")
			reenc("keyContent")
			reenc("sentinelPassword")
			reenc("tunnelSSHPassword")
		}
		plaintext, _ = json.MarshalIndent(wrapper, "", "  ")
	}

	return os.WriteFile(dest, plaintext, 0600)
}

func decryptAIConfigFile(src, dest string, key []byte, ps PasswordStore) error {
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(dest, []byte("{}"), 0600)
		}
		return err
	}

	plaintext, err := decryptBytes(string(data), key, filepath.Base(dest))
	if err != nil {
		return fmt.Errorf("decrypt ai config: %w", err)
	}

	if ps != nil {
		var obj map[string]interface{}
		if err := json.Unmarshal(plaintext, &obj); err == nil {
			if models, ok := obj["models"].([]interface{}); ok {
				for _, m := range models {
					if mm, ok := m.(map[string]interface{}); ok {
						if ak, ok := mm["apiKey"].(string); ok && ak != "" && !isEncryptedField(ak) {
							// Re-encrypt plaintext under the local credential key.
							if enc, err := ps.Encrypt(ak); err == nil {
								mm["apiKey"] = enc
							}
						}
					}
				}
			}
			plaintext, _ = json.MarshalIndent(obj, "", "  ")
		}
	}

	return os.WriteFile(dest, plaintext, 0600)
}

// decryptIdentitiesFile decrypts identities.json, re-encrypting any plaintext
// Password field back to enc:v1: under the local credential key.
func decryptIdentitiesFile(src, dest string, key []byte, ps PasswordStore) error {
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(dest, []byte("{}"), 0600)
		}
		return err
	}

	plaintext, err := decryptBytes(string(data), key, filepath.Base(dest))
	if err != nil {
		return fmt.Errorf("decrypt identities: %w", err)
	}

	if ps != nil {
		var wrapper struct {
			Identities []map[string]interface{} `json:"identities"`
		}
		if err := json.Unmarshal(plaintext, &wrapper); err != nil {
			return fmt.Errorf("parse identities: %w", err)
		}
		for _, im := range wrapper.Identities {
			if pw, ok := im["password"].(string); ok && pw != "" && !isEncryptedField(pw) {
				if enc, err := ps.Encrypt(pw); err == nil {
					im["password"] = enc
				}
			}
			if kc, ok := im["keyContent"].(string); ok && kc != "" && !isEncryptedField(kc) {
				if enc, err := ps.Encrypt(kc); err == nil {
					im["keyContent"] = enc
				}
			}
		}
		plaintext, _ = json.MarshalIndent(wrapper, "", "  ")
	}

	return os.WriteFile(dest, plaintext, 0600)
}

// decryptProxiesFile decrypts proxies.json, re-encrypting any plaintext pass
// field back to enc:v1: under the local credential key.
func decryptProxiesFile(src, dest string, key []byte, ps PasswordStore) error {
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(dest, []byte("{}"), 0600)
		}
		return err
	}

	plaintext, err := decryptBytes(string(data), key, filepath.Base(dest))
	if err != nil {
		return fmt.Errorf("decrypt proxies: %w", err)
	}

	if ps != nil {
		var wrapper struct {
			Proxies []map[string]interface{} `json:"proxies"`
		}
		if err := json.Unmarshal(plaintext, &wrapper); err != nil {
			return fmt.Errorf("parse proxies: %w", err)
		}
		for _, pm := range wrapper.Proxies {
			if pass, ok := pm["pass"].(string); ok && pass != "" && !isEncryptedField(pass) {
				if enc, err := ps.Encrypt(pass); err == nil {
					pm["pass"] = enc
				}
			}
		}
		plaintext, _ = json.MarshalIndent(wrapper, "", "  ")
	}

	return os.WriteFile(dest, plaintext, 0600)
}

// encryptGenericFile encrypts a config file that has no sensitive keychain-managed fields.
func encryptGenericFile(src, dest string, key []byte) error {
	data, err := readJSONFile(src)
	if err != nil {
		return err
	}
	encoded, err := encryptBytes(data, key, filepath.Base(dest))
	if err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(encoded), 0600)
}

// decryptGenericFile decrypts a config file that has no sensitive keychain-managed fields.
func decryptGenericFile(src, dest string, key []byte) error {
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(dest, []byte("{}"), 0600)
		}
		return err
	}
	plaintext, err := decryptBytes(string(data), key, filepath.Base(dest))
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	return os.WriteFile(dest, plaintext, 0600)
}

// decryptFieldsInPlace decrypts any enc:v1: secret fields in a decoded config
// object so both sides of a sync comparison are plaintext. Best-effort: on a
// decrypt error (locked/wrong key) the field is left untouched.
func decryptFieldsInPlace(obj map[string]interface{}, ps PasswordStore) {
	if ps == nil {
		return
	}
	if conns, ok := obj["connections"].([]interface{}); ok {
		for _, c := range conns {
			if cm, ok := c.(map[string]interface{}); ok {
				for _, field := range []string{"password", "keyContent", "sentinelPassword", "tunnelSSHPassword"} {
					if v, ok := cm[field].(string); ok && isEncryptedField(v) {
						if pt, err := ps.Decrypt(v); err == nil {
							cm[field] = pt
						}
					}
				}
			}
		}
	}
	if ai, ok := obj["ai"].(map[string]interface{}); ok {
		if models, ok := ai["models"].([]interface{}); ok {
			for _, m := range models {
				if mm, ok := m.(map[string]interface{}); ok {
					if ak, ok := mm["apiKey"].(string); ok && isEncryptedField(ak) {
						if pt, err := ps.Decrypt(ak); err == nil {
							mm["apiKey"] = pt
						}
					}
				}
			}
		}
	}
	if ids, ok := obj["identities"].([]interface{}); ok {
		for _, id := range ids {
			if im, ok := id.(map[string]interface{}); ok {
				if pw, ok := im["password"].(string); ok && isEncryptedField(pw) {
					if pt, err := ps.Decrypt(pw); err == nil {
						im["password"] = pt
					}
				}
			}
		}
	}
	if proxies, ok := obj["proxies"].([]interface{}); ok {
		for _, p := range proxies {
			if pm, ok := p.(map[string]interface{}); ok {
				if pass, ok := pm["pass"].(string); ok && isEncryptedField(pass) {
					if pt, err := ps.Decrypt(pass); err == nil {
						pm["pass"] = pt
					}
				}
			}
		}
	}
}

// encryptBytes encrypts plaintext under key, binding the ciphertext to the
// synced file's name via additional data so an attacker who can swap
// ciphertexts across files (e.g. paste connections.json.enc over
// settings.json.enc) fails the AAD check (SYNC-P1-1).
func encryptBytes(plaintext []byte, key []byte, file string) (string, error) {
	return encryptBytesWithAAD(plaintext, key, []byte(file))
}

func encryptBytesWithAAD(plaintext []byte, key []byte, aad []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := aesGCM.Seal(nonce, nonce, plaintext, aad)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptBytes decrypts a synced file ciphertext bound to file via AAD.
// Ciphertexts written before SYNC-P1-1 was wired up carry a nil AAD; they
// are accepted as a legacy fallback and upgraded to the name-bound form on
// the next push or password rotation.
func decryptBytes(encoded string, key []byte, file string) ([]byte, error) {
	if pt, err := decryptBytesWithAAD(encoded, key, []byte(file)); err == nil {
		return pt, nil
	}
	return decryptBytesWithAAD(encoded, key, nil)
}

func decryptBytesWithAAD(encoded string, key []byte, aad []byte) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

func readJSONFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []byte("{}"), nil
		}
		return nil, err
	}
	return data, nil
}

// ReadSaltFile reads the .sync-salt file from the repo directory.
// Returns nil if the file doesn't exist (new repo).
func ReadSaltFile(repoPath string) ([]byte, error) {
	saltPath := filepath.Join(repoPath, ".sync-salt")
	data, err := os.ReadFile(saltPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read salt file: %w", err)
	}
	salt, err := hex.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	return salt, nil
}

// WriteSaltFile writes the salt to .sync-salt in the repo directory.
func WriteSaltFile(repoPath string, salt []byte) error {
	saltPath := filepath.Join(repoPath, ".sync-salt")
	return os.WriteFile(saltPath, []byte(hex.EncodeToString(salt)), 0600)
}
