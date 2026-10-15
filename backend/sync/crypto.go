package sync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ys-ll/uniterm/backend/log"
)

const saltFileName = ".sync-salt"

// ciphertextFormatVersion is the format version new pushes record in
// .sync-salt. Version >=1 means every ciphertext in the repo is bound to
// its file name via AAD, so the legacy nil-AAD fallback must be rejected:
// an attacker who can move ciphertexts between file slots (SYNC-P1-1) would
// otherwise stay undetected indefinitely in upgraded repos. Legacy repos
// carry no version line (version 0) and keep the fallback for migration.
const ciphertextFormatVersion = 1

// parseSaltFileContent parses .sync-salt content: the first line is the hex
// salt, optionally followed by a "format=<n>" line recording the ciphertext
// format version. A legacy file (pure hex, no version line) reports
// version 0.
func parseSaltFileContent(content []byte) (salt []byte, formatVersion int, err error) {
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	salt, err = hex.DecodeString(strings.TrimSpace(lines[0]))
	if err != nil {
		return nil, 0, fmt.Errorf("decode salt: %w", err)
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if v, ok := strings.CutPrefix(line, "format="); ok {
			n, convErr := strconv.Atoi(v)
			if convErr != nil || n < 0 {
				return nil, 0, fmt.Errorf("bad salt format version %q", line)
			}
			formatVersion = n
			continue
		}
		return nil, 0, fmt.Errorf("unexpected salt file line %q", line)
	}
	return salt, formatVersion, nil
}

// PasswordStore is the local credential store as sync sees it. Sync only
// consults Unlocked: every field-level concern (which fields are secret,
// enc:v1: encryption, legacy keychain backfill) lives in the stores, so a
// locked vault is the only thing that can still make sync misbehave.
type PasswordStore interface {
	// Unlocked reports whether the credential store currently holds a usable
	// key. When false (master-password not yet entered, keychain lost, or never
	// set up), sync must refuse to run rather than leak or mis-carry secrets.
	Unlocked() bool
}

// EncryptConfigFiles encrypts the whole default sync set. Kept for
// callers/tests outside a SyncService scope context (the password rotation
// re-encrypts a staging dir rather than going through the store registry).
func EncryptConfigFiles(srcDir, destDir string, key []byte) error {
	return EncryptConfigFilesScoped(defaultSyncedFiles, srcDir, destDir, key)
}

// EncryptConfigFilesScoped encrypts exactly the given config files from
// srcDir into destDir. files must come from EffectiveSyncFiles. The body is
// byte-level: read the plaintext blob, encryptBytes under the sync key,
// write — all field-level semantics live in the stores (ExportPlain /
// ImportPlain), so nothing here knows what a secret field is. A file srcDir
// never carried is skipped: sync must not upload a document that would
// clobber the remote copy.
func EncryptConfigFilesScoped(files []string, srcDir, destDir string, key []byte) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	for _, name := range files {
		plain, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read %s: %w", name, err)
		}
		enc, err := encryptBytes(plain, key, name)
		if err != nil {
			return fmt.Errorf("encrypt %s: %w", name, err)
		}
		if err := atomicWriteFile(filepath.Join(destDir, name), []byte(enc), 0600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	return nil
}

// DecryptConfigFiles decrypts the whole default sync set. Kept for
// callers/tests outside a SyncService scope context (the password rotation
// and the wrong-key guard work on staging dirs, not the store registry).
func DecryptConfigFiles(srcDir, destDir string, key []byte) error {
	return DecryptConfigFilesScoped(defaultSyncedFiles, srcDir, destDir, key)
}

// DecryptConfigFilesScoped decrypts exactly the given config files from
// srcDir into destDir. files must come from EffectiveSyncFiles. Byte-level:
// read the encrypted blob, decryptBytes under the sync key, write the
// plaintext — no field handling. A blob the source never carried is
// skipped: sync must not stage an empty document that would disagree with
// the null sentinel hashStoresScoped records for a store whose backing
// file was never written.
func DecryptConfigFilesScoped(files []string, srcDir, destDir string, key []byte) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	formatVersion, err := readFormatVersion(srcDir)
	if err != nil {
		return err
	}

	for _, name := range files {
		enc, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read %s: %w", name, err)
		}
		plain, err := decryptBytes(string(enc), key, name, formatVersion)
		if err != nil {
			return fmt.Errorf("decrypt %s: %w", name, err)
		}
		if err := atomicWriteFile(filepath.Join(destDir, name), plain, 0600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	return nil
}

// encryptBytes encrypts plaintext under key, binding the ciphertext to the
// synced file's name via additional data so an attacker who can
// swap ciphertexts across files (e.g. paste connections.json.enc over
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
// are accepted as a legacy fallback ONLY in repos that predate the format
// version (formatVersion 0, no version line in .sync-salt). Repos stamped
// with ciphertextFormatVersion or higher have been re-pushed under the
// name-bound form, so a nil-AAD blob there is a slot-moved attacker
// ciphertext (or a pre-upgrade leftover a re-push should have replaced)
// and must be rejected instead of silently decrypted. A successful legacy
// fallback is logged so use of the weaker form stays observable.
func decryptBytes(encoded string, key []byte, file string, formatVersion int) ([]byte, error) {
	pt, aadErr := decryptBytesWithAAD(encoded, key, []byte(file))
	if aadErr == nil {
		return pt, nil
	}
	if formatVersion >= ciphertextFormatVersion {
		// The name-bound attempt failed and the repo has declared itself
		// fully name-bound — do not fall back to the movable nil-AAD form.
		return nil, fmt.Errorf("decrypt %s (nil-AAD fallback disabled in format v%d repo): %w", file, formatVersion, aadErr)
	}
	// Migration path for pre-SYNC-P1-1 ciphertexts: retry without the AAD so
	// existing repos keep syncing. Do not remove until those ciphertexts have
	// been re-pushed with the name-bound form (next push or password rotation).
	pt, err := decryptBytesWithAAD(encoded, key, nil)
	if err != nil {
		return nil, err
	}
	log.Writef("[sync] %s decrypted via legacy nil-AAD fallback (pre-SYNC-P1-1 ciphertext)", file)
	return pt, nil
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

// Returns nil if the file doesn't exist (new repo).
func ReadSaltFile(repoPath string) ([]byte, error) {
	saltPath := filepath.Join(repoPath, saltFileName)
	data, err := os.ReadFile(saltPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read salt file: %w", err)
	}
	salt, _, err := parseSaltFileContent(data)
	if err != nil {
		return nil, err
	}
	return salt, nil
}

// readFormatVersion returns the ciphertext format version recorded in the
// repo directory's .sync-salt. A missing salt file (fresh staging dir) or a
// legacy pure-hex salt file reports version 0.
func readFormatVersion(repoPath string) (int, error) {
	data, err := os.ReadFile(filepath.Join(repoPath, saltFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read salt file: %w", err)
	}
	_, formatVersion, err := parseSaltFileContent(data)
	if err != nil {
		return 0, err
	}
	return formatVersion, nil
}

// ReadFormatVersion reports the ciphertext format version of the repo
// directory, for callers outside the decrypt plumbing.
func ReadFormatVersion(repoPath string) (int, error) {
	return readFormatVersion(repoPath)
}

// WriteSaltFile writes the salt to .sync-salt in the repo directory,
// stamping the current ciphertext format version so later decrypts in this
// repo reject legacy nil-AAD ciphertext.
func WriteSaltFile(repoPath string, salt []byte) error {
	saltPath := filepath.Join(repoPath, saltFileName)
	content := hex.EncodeToString(salt) + "\nformat=" + strconv.Itoa(ciphertextFormatVersion) + "\n"
	return atomicWriteFile(saltPath, []byte(content), 0600)
}

// encryptStoresScoped encrypts each store's canonical plaintext blob into
// destDir under the sync key. files must come from EffectiveSyncFiles; a
// store whose blob is not in files is skipped. A store whose backing file
// was never written reports fs.ErrNotExist from ExportPlain — sync skips
// it instead of uploading an empty document that would clobber the remote
// copy.
//
// Two change-detection rules keep a one-file edit from rewriting every
// blob — the per-blob nonce is random, so re-encrypting unchanged content
// yields different bytes and the commit/upload would carry the whole
// config. Both compare plaintext, never generated ciphertext:
//
//   - skip names blobs the caller already knows are unchanged (snapshot
//     mode diffs the store exports against the remote manifest's plaintext
//     hashes); they are not staged, so the remote keeps its copy.
//   - otherwise, a blob already present in destDir that decrypts to the
//     export is left alone (git mode: the working tree holds the previous
//     ciphertext, so the commit only carries files whose content moved).
//     The comparison is on the decrypted plaintext, so a stale or foreign
//     blob never survives by accident.
func encryptStoresScoped(stores []SyncableStore, files []string, destDir string, key []byte, skip map[string]bool) error {
	if err := requireStores(stores, files); err != nil {
		return err
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	wanted := make(map[string]bool, len(files))
	for _, name := range files {
		wanted[name] = true
	}

	for _, st := range stores {
		name := st.BlobName()
		if !wanted[name] {
			continue
		}
		if skip[name] {
			continue
		}
		plain, err := st.ExportPlain()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("export %s: %w", name, err)
		}
		if reusableCiphertext(filepath.Join(destDir, name), plain, key) {
			continue
		}
		enc, err := encryptBytes(plain, key, name)
		if err != nil {
			return fmt.Errorf("encrypt %s: %w", name, err)
		}
		if err := atomicWriteFile(filepath.Join(destDir, name), []byte(enc), 0600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	return nil
}

// reusableCiphertext reports whether the ciphertext at path decrypts under
// key to exactly plain (canonical plaintext comparison). Only then may the
// existing bytes stand in for a fresh encryption.
func reusableCiphertext(path string, plain []byte, key []byte) bool {
	enc, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	formatVersion, err := readFormatVersion(filepath.Dir(path))
	if err != nil {
		return false
	}
	ref, err := decryptBytes(string(enc), key, filepath.Base(path), formatVersion)
	if err != nil {
		return false
	}
	refHash, err := canonicalPlainHash(ref)
	if err != nil {
		return false
	}
	plainHash, err := canonicalPlainHash(plain)
	if err != nil {
		return false
	}
	return refHash == plainHash
}

// decryptStoresScoped decrypts exactly the given store blobs from srcDir
// and persists each through its store's ImportPlain, so every save-time
// invariant (password clearing for identity-auth, manifest encryption,
// three hash-skip, quarantine) applies unchanged. files must come from
// EffectiveSyncFiles. A blob the remote never carried is skipped: sync
// must not wipe a local store just because the remote copy is absent.
func decryptStoresScoped(stores []SyncableStore, files []string, srcDir string, key []byte) error {
	if err := requireStores(stores, files); err != nil {
		return err
	}
	formatVersion, err := readFormatVersion(srcDir)
	if err != nil {
		return err
	}
	wanted := make(map[string]bool, len(files))
	for _, name := range files {
		wanted[name] = true
	}

	for _, st := range stores {
		name := st.BlobName()
		if !wanted[name] {
			continue
		}
		enc, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read %s: %w", name, err)
		}
		plain, err := decryptBytes(string(enc), key, name, formatVersion)
		if err != nil {
			return fmt.Errorf("decrypt %s: %w", name, err)
		}
		if err := st.ImportPlain(plain); err != nil {
			return fmt.Errorf("import %s: %w", name, err)
		}
	}

	return nil
}
