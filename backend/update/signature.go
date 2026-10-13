package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// checksumsPublicKeyHex is the ed25519 public key that signs the published
// checksums.txt. The private half lives only in build/release-signing.key
// (git-ignored) and the CI secret UPDATE_SIGNING_PRIVATE_KEY; build/signcmd
// produces the detached signature uploaded as checksums.txt.sig.
//
// SHA-256 fingerprint of the 32 raw public key bytes:
//
//	d18e2a876b20de9c48341cd42a2f488ad1b58d89a393f7b49cdd40d540e4f9bf
const checksumsPublicKeyHex = "48ca098b90d7e5ba4bcb8b15b24c97260bb85405361af128c46b4e7807954ea0"

// updatePublicKey is the parsed embedded key. It is a package variable only
// so tests can substitute a freshly generated key; production code never
// reassigns it.
var updatePublicKey = mustPublicKey(checksumsPublicKeyHex)

// mustPublicKey parses the embedded hex public key. A malformed constant is a
// build-time programming error, so it fails fast at init rather than silently
// rejecting (or, worse, mis-verifying) every update.
func mustPublicKey(hexKey string) ed25519.PublicKey {
	raw, err := hex.DecodeString(hexKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		panic(fmt.Sprintf("update: embedded checksums public key is invalid (err=%v, len=%d)", err, len(raw)))
	}
	return ed25519.PublicKey(raw)
}

// VerifyChecksumsSignature verifies the detached ed25519 signature over the
// exact bytes of checksums.txt. sig is the base64 encoding of the raw
// 64-byte signature, i.e. the content of the checksums.txt.sig release asset
// (trailing whitespace such as a final newline is ignored).
//
// It fails closed: a missing, malformed or non-matching signature is always an
// error, so callers must refuse the update instead of falling back to
// unverified hashes.
func VerifyChecksumsSignature(checksums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sig)))
	if err != nil {
		return fmt.Errorf("signature is not valid base64: %w", err)
	}
	if len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("signature is %d bytes, want %d", len(raw), ed25519.SignatureSize)
	}
	if !ed25519.Verify(updatePublicKey, checksums, raw) {
		return errors.New("checksums signature does not verify against the embedded public key")
	}
	return nil
}

// SignChecksums signs checksums with key and returns the base64 encoding of
// the raw 64-byte signature — the exact content of the checksums.txt.sig
// asset. It is the inverse of VerifyChecksumsSignature and the signing half
// used by build/signcmd.
func SignChecksums(checksums []byte, key ed25519.PrivateKey) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("signing key is %d bytes, want %d", len(key), ed25519.PrivateKeySize)
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, checksums)), nil
}

// ParseSigningKeyHex decodes the hex-encoded 64-byte raw ed25519 private key
// used for release signing: the CI secret UPDATE_SIGNING_PRIVATE_KEY and the
// local build/release-signing.key both store it this way. Surrounding
// whitespace (e.g. a trailing newline) is ignored.
func ParseSigningKeyHex(s string) (ed25519.PrivateKey, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("signing key is not valid hex: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("signing key is %d bytes, want %d", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}
