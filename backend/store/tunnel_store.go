package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/ys-ll/uniterm/backend/credentials"
	"github.com/ys-ll/uniterm/backend/session"
)

const tunnelsFileName = "tunnels.json"

// TunnelStore persists the user's SSH tunnel definitions to tunnels.json in the
// app config dir. The upstream proxy Pass field is handled like ProxyStore:
// encrypted at rest under the PasswordStore, prefix-gated decryption on load,
// and Save fails closed rather than persisting plaintext.
type TunnelStore struct {
	configDir     string
	passwordStore PasswordStore // nil = refuse to write plaintext passwords
	mu            sync.Mutex    // serializes Save + Load migration rewrite
}

func NewTunnelStore(configDir string) *TunnelStore {
	return &TunnelStore{configDir: configDir}
}

func (s *TunnelStore) SetPasswordStore(ps PasswordStore) { s.passwordStore = ps }

func (s *TunnelStore) filePath() string {
	return filepath.Join(s.configDir, tunnelsFileName)
}

// Save writes data to tunnels.json, encrypting each upstream proxy Pass. It
// fails closed rather than persisting a plaintext password when no
// passwordStore is wired. Values already prefixed (credentials.IsEncrypted)
// are skipped so a save doesn't double-encrypt previously encrypted data.
// The caller's data is left untouched (tunnels are deep-copied before the
// upstream structs are replaced).
func (s *TunnelStore) Save(data session.TunnelStoreData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(data)
}

// saveLocked encrypts upstream Pass fields and writes tunnels.json atomically.
// Caller must hold s.mu.
func (s *TunnelStore) saveLocked(data session.TunnelStoreData) error {
	out := data
	out.Tunnels = make([]session.Tunnel, len(data.Tunnels))
	copy(out.Tunnels, data.Tunnels)
	for i := range out.Tunnels {
		up := out.Tunnels[i].Upstream
		if up == nil || up.Pass == "" || credentials.IsEncrypted(up.Pass) {
			continue
		}
		if s.passwordStore == nil {
			return errors.New("passwordStore not initialized; refusing to save plaintext password")
		}
		enc, err := s.passwordStore.Encrypt(up.Pass)
		if err != nil {
			return err
		}
		upCopy := *up
		upCopy.Pass = enc
		out.Tunnels[i].Upstream = &upCopy
	}
	bytes, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(s.filePath(), bytes, 0600)
}

// Load reads tunnels.json, returning an empty store when the file does not
// exist and decrypting each upstream proxy Pass. A legacy plaintext Pass (from
// before in-place encryption) is returned as plaintext for the caller and the
// file is re-written with it encrypted, so existing tunnels.json files migrate
// on first load.
func (s *TunnelStore) Load() (session.TunnelStoreData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bytes, err := os.ReadFile(s.filePath())
	if err != nil {
		if os.IsNotExist(err) {
			return session.TunnelStoreData{Version: 1, Groups: []session.TunnelGroup{}, Tunnels: []session.Tunnel{}}, nil
		}
		return session.TunnelStoreData{}, err
	}
	var data session.TunnelStoreData
	if err := json.Unmarshal(bytes, &data); err != nil {
		return session.TunnelStoreData{}, err
	}
	if data.Version == 0 {
		data.Version = 1
	}
	needsSave := false
	for i := range data.Tunnels {
		up := data.Tunnels[i].Upstream
		if up == nil || up.Pass == "" {
			continue
		}
		if credentials.IsEncrypted(up.Pass) {
			if s.passwordStore == nil {
				continue // no cipher wired — leave as-is (backward compat)
			}
			pw, err := s.passwordStore.Decrypt(up.Pass)
			if err != nil {
				return session.TunnelStoreData{}, err
			}
			up.Pass = pw
			continue
		}
		// Legacy plaintext on disk (tunnels.json predating in-place
		// encryption). Keep the plaintext for the caller but re-save so it
		// lands encrypted.
		if s.passwordStore != nil {
			needsSave = true
		}
	}
	if needsSave {
		if err := s.saveLocked(data); err != nil {
			return session.TunnelStoreData{}, err
		}
	}
	return data, nil
}
