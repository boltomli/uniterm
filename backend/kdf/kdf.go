// Package kdf holds the sync master-password key derivation primitives.
// It is a leaf package on purpose: backend/credentials needs the same
// derivation, and importing backend/sync for it would close the
// session → credentials → sync cycle that keeps sync from importing the
// file-transfer sessions.
package kdf

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"golang.org/x/crypto/pbkdf2"
)

const (
	pbkdf2Iterations = 600000
	keyLength        = 32
	saltLength       = 16
)

// DeriveKey derives the sync encryption key from a master password and
// salt via PBKDF2-SHA256.
func DeriveKey(password string, salt []byte) []byte {
	return pbkdf2.Key([]byte(password), salt, pbkdf2Iterations, keyLength, sha256.New)
}

// GenerateSalt generates a random 16-byte salt.
func GenerateSalt() ([]byte, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	return salt, nil
}
