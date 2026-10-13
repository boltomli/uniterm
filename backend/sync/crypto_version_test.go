package sync

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// --- atomic pull writes ---

// TestDecryptAtomicWriteRenameFailureKeepsOriginal: a failure before the
// rename in the atomic write path must leave the live config file at its
// previous content instead of truncating it mid-write.
func TestDecryptAtomicWriteRenameFailureKeepsOriginal(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	encoded, err := encryptBytes([]byte(`{"connections":[{"id":"c1","name":"n"}]}`), key, "connections.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "connections.json"), []byte(encoded), 0600); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"connections":[{"id":"keep","name":"me"}]}`)
	dst := filepath.Join(dstDir, "connections.json")
	if err := os.WriteFile(dst, original, 0600); err != nil {
		t.Fatal(err)
	}

	origRename := renameFile
	renameFile = func(oldpath, newpath string) error {
		return errors.New("injected rename failure")
	}
	t.Cleanup(func() { renameFile = origRename })

	if err := DecryptConfigFilesScoped([]string{"connections.json"}, srcDir, dstDir, key, nil); err == nil {
		t.Fatal("expected error when rename fails")
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("destination changed despite failed rename:\n got %q\nwant %q", got, original)
	}
	// The temp file must not be left behind either.
	entries, err := os.ReadDir(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "connections.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("leftover files after failed write: %v", names)
	}
}

// TestSaltFileVersionRoundTrip: new pushes stamp the ciphertext format
// version into .sync-salt; ReadSaltFile still returns the raw salt.
func TestSaltFileVersionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	salt := []byte("0123456789abcdef")
	if err := WriteSaltFile(dir, salt); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSaltFile(dir)
	if err != nil {
		t.Fatalf("ReadSaltFile: %v", err)
	}
	if !bytes.Equal(got, salt) {
		t.Fatalf("salt = %x, want %x", got, salt)
	}
	v, err := ReadFormatVersion(dir)
	if err != nil {
		t.Fatalf("ReadFormatVersion: %v", err)
	}
	if v < ciphertextFormatVersion {
		t.Fatalf("format version = %d, want >= %d after WriteSaltFile", v, ciphertextFormatVersion)
	}
}

// TestReadFormatVersionLegacySaltFile: a legacy repo (pure hex salt, no
// version line) reports version 0 and still reads the salt.
func TestReadFormatVersionLegacySaltFile(t *testing.T) {
	dir := t.TempDir()
	salt := []byte("0123456789abcdef")
	if err := os.WriteFile(filepath.Join(dir, saltFileName), []byte(hex.EncodeToString(salt)), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := ReadFormatVersion(dir)
	if err != nil {
		t.Fatalf("ReadFormatVersion: %v", err)
	}
	if v != 0 {
		t.Fatalf("format version = %d, want 0 for legacy salt file", v)
	}
	got, err := ReadSaltFile(dir)
	if err != nil {
		t.Fatalf("ReadSaltFile: %v", err)
	}
	if !bytes.Equal(got, salt) {
		t.Fatalf("salt = %x, want %x", got, salt)
	}
}

// TestDecryptNilAADVersionedRepo: in a repo stamped with format version >=1
// the legacy nil-AAD fallback must be rejected, name-bound AAD ciphertext
// must decrypt, and a legacy (version-less) repo must keep accepting
// nil-AAD ciphertexts.
func TestDecryptNilAADVersionedRepo(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := []byte(`{"connections":[{"id":"c1","name":"n"}]}`)

	aadCipher, err := encryptBytes(plaintext, key, "connections.json")
	if err != nil {
		t.Fatal(err)
	}
	legacyCipher, err := encryptBytesWithAAD(plaintext, key, nil)
	if err != nil {
		t.Fatal(err)
	}

	writeSalt := func(t *testing.T, dir string, versioned bool) {
		t.Helper()
		if versioned {
			if err := WriteSaltFile(dir, []byte("0123456789abcdef")); err != nil {
				t.Fatal(err)
			}
			return
		}
		// Legacy repo: pure hex salt, no version line.
		if err := os.WriteFile(filepath.Join(dir, saltFileName),
			[]byte(hex.EncodeToString([]byte("0123456789abcdef"))), 0600); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name      string
		cipher    string
		versioned bool
		wantErr   bool
	}{
		{"versioned repo rejects nil-AAD", legacyCipher, true, true},
		{"versioned repo accepts AAD", aadCipher, true, false},
		{"legacy repo accepts nil-AAD", legacyCipher, false, false},
		{"legacy repo accepts AAD", aadCipher, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srcDir := t.TempDir()
			dstDir := t.TempDir()
			writeSalt(t, srcDir, c.versioned)
			if err := os.WriteFile(filepath.Join(srcDir, "connections.json"), []byte(c.cipher), 0600); err != nil {
				t.Fatal(err)
			}
			err := DecryptConfigFilesScoped([]string{"connections.json"}, srcDir, dstDir, key, nil)
			if c.wantErr {
				if err == nil {
					t.Fatal("nil-AAD ciphertext accepted in versioned repo")
				}
				return
			}
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(dstDir, "connections.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plaintext) {
				t.Fatalf("plaintext mismatch:\n got %q\nwant %q", got, plaintext)
			}
		})
	}
}

// TestReadFormatVersionMissingFile: a repo dir without a salt file (fresh
// staging) reports version 0 and no error.
func TestReadFormatVersionMissingFile(t *testing.T) {
	v, err := ReadFormatVersion(t.TempDir())
	if err != nil {
		t.Fatalf("ReadFormatVersion: %v", err)
	}
	if v != 0 {
		t.Fatalf("version = %d, want 0", v)
	}
}

// TestParseSaltFileVersionLine: the salt file parser accepts the legacy
// pure-hex form, the versioned form, and rejects malformed version lines.
func TestParseSaltFileVersionLine(t *testing.T) {
	salt := []byte("0123456789abcdef")
	hexSalt := hex.EncodeToString(salt)

	cases := []struct {
		name        string
		content     string
		wantSalt    []byte
		wantVersion int
		wantErr     bool
	}{
		{"legacy pure hex", hexSalt, salt, 0, false},
		{"legacy hex with trailing newline", hexSalt + "\n", salt, 0, false},
		{"versioned", fmt.Sprintf("%s\nformat=1\n", hexSalt), salt, 1, false},
		{"bad hex", "zzzz", nil, 0, true},
		{"bad version", hexSalt + "\nformat=abc\n", nil, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotSalt, gotVersion, err := parseSaltFileContent([]byte(c.content))
			if c.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !bytes.Equal(gotSalt, c.wantSalt) {
				t.Fatalf("salt = %x, want %x", gotSalt, c.wantSalt)
			}
			if gotVersion != c.wantVersion {
				t.Fatalf("version = %d, want %d", gotVersion, c.wantVersion)
			}
		})
	}
}
