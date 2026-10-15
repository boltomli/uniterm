package store

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/ys-ll/uniterm/backend/session"
)

const proxyStoreFileName = "proxies.json"

type ProxyStore struct {
	configDir     string
	passwordStore PasswordStore // nil = refuse to write plaintext passwords
	mu            sync.Mutex    // serializes Save + Load password rewrites
	lastSavedHash string        // F-105 skip state (pre-encryption content hash)
}

func NewProxyStore(configDir string) (*ProxyStore, error) {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, err
	}
	return &ProxyStore{configDir: configDir}, nil
}

func (s *ProxyStore) SetPasswordStore(ps PasswordStore) { s.passwordStore = ps }

func (s *ProxyStore) filePath() string {
	return filepath.Join(s.configDir, proxyStoreFileName)
}

// Save writes data to proxies.json, encrypting only the Pass field. It fails
// closed rather than persisting a plaintext password when no passwordStore is
// wired. Values already prefixed (credentials.IsEncrypted) are skipped so a
// save doesn't double-encrypt previously encrypted data.
func (s *ProxyStore) Save(data session.ProxyStoreData) error {
	return s.save(data, false)
}

// ForceSave rewrites the file even when the content hash matches the
// seeded state. Used by re-keying: the plaintext is unchanged, so the
// F-105 hash-skip would otherwise swallow the re-encryption and leave
// the file under the old key.
func (s *ProxyStore) ForceSave(data session.ProxyStoreData) error {
	return s.save(data, true)
}

func (s *ProxyStore) save(data session.ProxyStoreData, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Deep-copy proxies so we don't mutate the caller's backing array.
	proxies := make([]session.Proxy, len(data.Proxies))
	copy(proxies, data.Proxies)

	out := session.ProxyStoreData{Proxies: proxies}
	// Shared Save envelope (filestore.go): hash-skip, encrypt, atomic write.
	skip := s.lastSavedHash
	if force {
		// Re-keying saves unchanged plaintext under a new key — the
		// F-105 hash-skip would otherwise swallow the rewrite.
		skip = forceWrite
	}
	h, err := saveEncryptedFile(proxyStoreFileName, s.filePath(), &out, s.passwordStore, skip)
	s.lastSavedHash = h
	return err
}

// Load reads proxies.json: empty store when the file does not exist,
// quarantine+error on corrupt JSON, decrypting the manifest secret fields
// (filestore.go envelope).
func (s *ProxyStore) Load() (session.ProxyStoreData, error) {
	out, _, hash, existed, err := loadEncryptedFile[session.ProxyStoreData](proxyStoreFileName, s.filePath(), s.passwordStore)
	if !existed {
		return session.ProxyStoreData{Proxies: []session.Proxy{}}, nil
	}
	if err != nil {
		return session.ProxyStoreData{}, err
	}
	if out.Proxies == nil {
		out.Proxies = []session.Proxy{}
	}
	s.lastSavedHash = hash
	return out, nil
}

// BlobName, ExportPlain and ImportPlain implement the sync exchange contract
// (SyncableStore, declared in backend/sync/backend.go).

func (s *ProxyStore) BlobName() string { return proxyStoreFileName }

func (s *ProxyStore) ExportPlain() ([]byte, error) {
	return exportPlainStore(proxyStoreFileName, s.filePath(), s.Load)
}

func (s *ProxyStore) ImportPlain(raw []byte) error {
	return importPlainStore(raw, s.Save)
}
