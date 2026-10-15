package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ys-ll/uniterm/backend/session"
)

// LegacyPasswordSource reads secrets from the legacy keychain entries
// (conn/<id>, ai-model/<id>). *sync.Keychain satisfies this interface.
type LegacyPasswordSource interface {
	GetPassword(connID string) (string, error)
	GetModelAPIKey(modelID string) (string, error)
	// DeletePassword removes one legacy conn/<id> entry. Move semantics: the
	// backfill deletes the entry once its re-encrypted copy is on disk, so a
	// user-cleared password can't resurrect from a kept-forever copy.
	DeletePassword(connID string) error
}

// MigrateLegacyKeychainToInPlace reads plaintext secrets from legacy keychain
// entries and writes them encrypted into connections.json / settings.json.
// Migrated conn/<id> entries are deleted once their encrypted copy is on disk
// (move semantics — a kept-forever copy would let a user-cleared password
// resurrect on the next load); ai-model/<id> entries are kept (rollback
// safety, and the model API key has no load-time resurrection path).
func MigrateLegacyKeychainToInPlace(configDir string, legacy LegacyPasswordSource, cred PasswordStore) (int, error) {
	count := 0

	// connections.json
	connPath := filepath.Join(configDir, storeFileName)
	if data, err := os.ReadFile(connPath); err == nil {
		var d session.ConnectionStoreData
		if err := json.Unmarshal(data, &d); err != nil {
			// Previously swallowed — surface it so a malformed file doesn't
			// silently leave every connection's password unmigrated.
			return count, fmt.Errorf("parse connections for migration: %w", err)
		}
		changed := false
		var migrated []string
		for i := range d.Connections {
			conn := &d.Connections[i]
			if conn.AuthType != "password" || conn.Password != "" {
				continue
			}
			if pw, err := legacy.GetPassword(conn.ID); err == nil && pw != "" {
				enc, err := cred.Encrypt(pw)
				if err != nil {
					return count, err
				}
				conn.Password = enc
				count++
				changed = true
				migrated = append(migrated, conn.ID)
			}
		}
		if changed {
			if err := writeJSONFile(connPath, d); err != nil {
				return count, err
			}
			// Move semantics: the encrypted copies are on disk, so the legacy
			// entries can go. Best-effort — a failed delete leaves a stale
			// entry that only resurfaces if that password is cleared later
			// (same accepted edge as the load-time backfill in
			// connection_store.go).
			for _, id := range migrated {
				_ = legacy.DeletePassword(id)
			}
		}
	}

	// settings.json
	setPath := filepath.Join(configDir, settingsFileName)
	if data, err := os.ReadFile(setPath); err == nil {
		var s AppSettings
		if err := json.Unmarshal(data, &s); err != nil {
			return count, fmt.Errorf("parse settings for migration: %w", err)
		}
		changed := false
		for i := range s.AI.Models {
			m := &s.AI.Models[i]
			if m.APIKey != "" {
				continue
			}
			if ak, err := legacy.GetModelAPIKey(m.ID); err == nil && ak != "" {
				enc, err := cred.Encrypt(ak)
				if err != nil {
					return count, err
				}
				m.APIKey = enc
				count++
				changed = true
			}
		}
		if changed {
			if err := writeJSONFile(setPath, s); err != nil {
				return count, err
			}
		}
	}

	return count, nil
}
