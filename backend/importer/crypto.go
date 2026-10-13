package importer

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"hash"

	"github.com/ys-ll/uniterm/backend/credentials"
	"golang.org/x/crypto/pbkdf2"
)

const (
	kdfAlgo = "pbkdf2-sha256"
	// defaultKDFIterations is the PBKDF2 cost new exports record. 600k
	// matches the sync master-password KDF; the old 100k is still honored on
	// import so existing .utm files keep working.
	defaultKDFIterations = 600000
	// minKDFIterations/maxKDFIterations bound the iteration count read from
	// an imported file: below the floor the file is weaker than the original
	// 100k exports, above the ceiling it is a cheap denial-of-service lever
	// (each field would burn CPU on import).
	minKDFIterations = 10000
	maxKDFIterations = 5_000_000
	keyLen           = 32 // AES-256
	saltLen          = 16
)

// pbkdf2Fn is the PBKDF2 primitive; a var so tests can count derivations
// and prove export/import derive the key exactly once per file.
var pbkdf2Fn = func(password, salt []byte, iter, keyLen int, h func() hash.Hash) []byte {
	return pbkdf2.Key(password, salt, iter, keyLen, h)
}

func newSalt() ([]byte, error) {
	s := make([]byte, saltLen)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	return s, nil
}

func deriveKey(passphrase string, salt []byte, iterations int) []byte {
	return pbkdf2Fn([]byte(passphrase), salt, iterations, keyLen, sha256.New)
}

// validateKDFIterations rejects iteration counts outside [minKDFIterations,
// maxKDFIterations] so a hostile .utm file cannot force a weakened or
// absurdly expensive key derivation on import.
func validateKDFIterations(n int) error {
	if n < minKDFIterations || n > maxKDFIterations {
		return fmt.Errorf("kdf iterations %d out of range [%d, %d]", n, minKDFIterations, maxKDFIterations)
	}
	return nil
}

// encryptField encrypts one field under an already-derived key. Deriving the
// key once per file (not per field) is what keeps a 500-connection export at
// a single PBKDF2 run.
func encryptField(plaintext string, key []byte) (string, error) {
	return credentials.EncryptField(plaintext, key)
}

// decryptField decrypts one field under an already-derived key.
func decryptField(encoded string, key []byte) (string, error) {
	return credentials.DecryptField(encoded, key)
}

func encodeSalt(salt []byte) string { return base64.StdEncoding.EncodeToString(salt) }
func decodeSalt(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
