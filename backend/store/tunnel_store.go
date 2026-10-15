package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/ys-ll/uniterm/backend/session"
)

const tunnelsFileName = "tunnels.json"

// TunnelStore persists the user's SSH tunnel definitions to tunnels.json in the
// app config dir. The upstream proxy Pass and the dynamic tunnel's SOCKS
// password are handled like ProxyStore: encrypted at rest under the
// PasswordStore via the secret-field manifest (fail-closed when no cipher is
// wired), prefix-gated decryption on load, and Save refuses to persist
// plaintext.
type TunnelStore struct {
	configDir     string
	passwordStore PasswordStore // nil = refuse to write plaintext passwords (manifest fails closed)
	mu            sync.Mutex    // serializes Save + Load
	lastSavedHash string        // F-105 skip state (pre-encryption content hash)
}

func NewTunnelStore(configDir string) *TunnelStore {
	return &TunnelStore{configDir: configDir}
}

func (s *TunnelStore) SetPasswordStore(ps PasswordStore) { s.passwordStore = ps }

func (s *TunnelStore) filePath() string {
	return filepath.Join(s.configDir, tunnelsFileName)
}

// Save writes data to tunnels.json through the shared envelope
// (filestore.go): hash-skip, manifest encryption, atomic write. The caller's
// data is left untouched (the envelope deep-copies through JSON); the
// manifest walk fails closed when a plaintext secret would land without a
// wired cipher.
func (s *TunnelStore) Save(data session.TunnelStoreData) error {
	return s.save(data, false)
}

// ForceSave rewrites tunnels.json even when the content hash matches the
// seeded state (re-keying: the plaintext is unchanged, so the F-105
// hash-skip would otherwise swallow the re-encryption).
func (s *TunnelStore) ForceSave(data session.TunnelStoreData) error {
	return s.save(data, true)
}

func (s *TunnelStore) save(data session.TunnelStoreData, force bool) error {
	// Deep-copy through JSON first: the envelope's manifest walk unmarshals
	// back into the value it was given, which would otherwise reach the
	// caller's tunnel slice (and its shared upstream structs) and land the
	// encrypted pass in the caller's data.
	clone, err := cloneTunnelData(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	skip := s.lastSavedHash
	if force {
		skip = forceWrite
	}
	h, err := saveEncryptedFile(tunnelsFileName, s.filePath(), &clone, s.passwordStore, skip)
	s.lastSavedHash = h
	return err
}

// Load reads tunnels.json, returning an empty store when the file does not
// exist and decrypting the upstream proxy pass and SOCKS password via the
// manifest. A legacy plaintext secret (from before in-place encryption) is
// returned as plaintext for the caller and the file is re-written with it
// encrypted, so existing tunnels.json files migrate on first load.
func (s *TunnelStore) Load() (session.TunnelStoreData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, res, hash, existed, err := loadEncryptedFile[session.TunnelStoreData](tunnelsFileName, s.filePath(), s.passwordStore)
	if !existed {
		return session.TunnelStoreData{Version: 1, Groups: []session.TunnelGroup{}, Tunnels: []session.Tunnel{}}, nil
	}
	if err != nil {
		return session.TunnelStoreData{}, err
	}
	if out.Version == 0 {
		out.Version = 1
	}
	s.lastSavedHash = hash
	if res.Plaintext > 0 && s.passwordStore != nil {
		// Lazy re-encrypt of legacy plaintext: forceWrite bypasses the
		// hash-skip (the on-disk content is unchanged, so the seeded hash
		// would match and the migration would never run). The write goes
		// through a clone so the caller still receives plaintext — the
		// envelope encrypts the value in place. Best-effort: without a
		// usable cipher the file stays as-is and retries on the next save.
		if clone, cErr := cloneTunnelData(out); cErr == nil {
			if h, err := saveEncryptedFile(tunnelsFileName, s.filePath(), &clone, s.passwordStore, forceWrite); err == nil {
				s.lastSavedHash = h
			}
		}
	}
	return out, nil
}

// cloneTunnelData round-trips data through JSON so the copy shares no
// backing arrays or pointees with the original.
func cloneTunnelData(data session.TunnelStoreData) (session.TunnelStoreData, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return data, err
	}
	var out session.TunnelStoreData
	if err := json.Unmarshal(raw, &out); err != nil {
		return data, err
	}
	return out, nil
}

// BlobName, ExportPlain and ImportPlain implement the sync exchange contract
// (SyncableStore, declared in backend/sync/backend.go).

func (s *TunnelStore) BlobName() string { return tunnelsFileName }

func (s *TunnelStore) ExportPlain() ([]byte, error) {
	return exportPlainStore(tunnelsFileName, s.filePath(), s.Load)
}

// ImportPlain backfills Version before saving, mirroring Load.
func (s *TunnelStore) ImportPlain(raw []byte) error {
	var data session.TunnelStoreData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("parse sync blob: %w", err)
	}
	if data.Version == 0 {
		data.Version = 1
	}
	return s.Save(data)
}
