package importer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ys-ll/uniterm/backend/credentials"
	"github.com/ys-ll/uniterm/backend/session"
	"github.com/ys-ll/uniterm/backend/store"
)

const unitermFormat = "uniterm"

type utmKDF struct {
	Algo       string `json:"algo"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
}

type utmFile struct {
	Format    string  `json:"format"`
	Version   int     `json:"version"`
	Encrypted bool    `json:"encrypted"`
	KDF       *utmKDF `json:"kdf,omitempty"`
	session.ConnectionStoreData
}

// ExportUniterm serializes the connection store to .utm JSON. data must
// already be plaintext — the store decrypts in-place ciphertext on Load, and
// a value still carrying the enc:v1: prefix would make the export depend on
// the source machine's credential store, so it aborts the export instead of
// riding along. Every manifest-declared secret field (the same list saving
// uses — password, key content, sentinel / jump-host / kubeconfig secrets
// and the post-login script) is encrypted with the export password via a
// fresh PBKDF2-derived key; the export password is required.
func ExportUniterm(data session.ConnectionStoreData, password string) ([]byte, error) {
	if password == "" {
		return nil, errors.New("export password required")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	if bytes.Contains(raw, []byte(credentials.Prefix)) {
		return nil, errors.New("connection data still carries in-place encrypted fields")
	}

	salt, err := newSalt()
	if err != nil {
		return nil, err
	}
	f := utmFile{
		Format:              unitermFormat,
		Version:             1,
		Encrypted:           true,
		KDF:                 &utmKDF{Algo: kdfAlgo, Iterations: defaultKDFIterations, Salt: encodeSalt(salt)},
		ConnectionStoreData: data,
	}
	ps := newExportCipher(password, salt)
	if err := store.EncryptSecretFields(connectionsManifestFile, &f.ConnectionStoreData, ps); err != nil {
		return nil, err
	}
	return json.MarshalIndent(f, "", "  ")
}

// connectionsManifestFile keys the store's secret-field manifest for the
// connection store; the export protects exactly the fields saving protects.
const connectionsManifestFile = "connections.json"

// exportCipher adapts the .utm export crypto (PBKDF2-derived key, salt fixed
// per file) to the store's PasswordStore, so the manifest walk encrypts the
// export's secret fields with the export password. The key is derived ONCE
// per file — PBKDF2 at the recorded cost per field would block an export of
// hundreds of connections for minutes.
type exportCipher struct {
	key []byte
}

func newExportCipher(pass string, salt []byte) exportCipher {
	return exportCipher{key: deriveKey(pass, salt, defaultKDFIterations)}
}

// newExportCipherWith derives with an imported file's own iteration count
// (older exports wrote 100k; the count is bounds-checked before this).
func newExportCipherWith(pass string, salt []byte, iterations int) exportCipher {
	return exportCipher{key: deriveKey(pass, salt, iterations)}
}

func (c exportCipher) Encrypt(plaintext string) (string, error) {
	return encryptField(plaintext, c.key)
}

func (c exportCipher) Decrypt(encoded string) (string, error) {
	return decryptField(encoded, c.key)
}

func (c exportCipher) Unlocked() bool { return true }

// MaterializeForExport resolves every identity-referencing connection into a
// plain credential connection, so an export is self-contained: no authType
// "identity" survives and nothing references the source machine's identity
// store. A dangling reference — the identity was deleted after the connection
// referenced it — degrades to a connection without credentials, keeping the
// username already on the connection (it never came from the identity).
func MaterializeForExport(data session.ConnectionStoreData, resolve session.IdentityResolver) (session.ConnectionStoreData, error) {
	for i := range data.Connections {
		c := &data.Connections[i]
		if c.AuthType != "identity" {
			continue
		}
		id, ok := resolve(c.IdentityId)
		if !ok {
			c.AuthType = "password"
			c.IdentityId = ""
			continue
		}
		mc, err := session.MaterializeIdentity(*c, func(string) (session.Identity, bool) { return id, true })
		if err != nil {
			return session.ConnectionStoreData{}, fmt.Errorf("connection %s: %w", c.ID, err)
		}
		// Runtime materialization keeps the reference; the export must not —
		// the credential now travels inside the connection itself.
		mc.IdentityId = ""
		*c = mc
	}
	return data, nil
}

// parseUniterm decodes a .utm file into an ImportResult with freshly generated ids.
func parseUniterm(data []byte, opts ParseOptions) (*ImportResult, error) {
	var f utmFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse uniterm json: %w", err)
	}
	if f.Format != unitermFormat {
		return nil, fmt.Errorf("not a uniterm file (format=%q)", f.Format)
	}
	if f.Encrypted {
		if opts.Password == "" {
			return nil, fmt.Errorf("file is password-protected; provide the import password")
		}
		if f.KDF == nil {
			return nil, fmt.Errorf("missing kdf block")
		}
		// Honor the file's iteration count (older exports wrote 100k) but
		// bounds-check it: an absurd value is either a weakened file or a
		// DoS lever against this importer.
		if err := validateKDFIterations(f.KDF.Iterations); err != nil {
			return nil, err
		}
		salt, err := decodeSalt(f.KDF.Salt)
		if err != nil {
			return nil, fmt.Errorf("decode salt: %w", err)
		}
		// The manifest walk decrypts every secret field the export
		// encrypted (password, key content, sentinel / jump-host /
		// kubeconfig secrets and the post-login script); a failure means
		// the import password is wrong.
		ps := newExportCipherWith(opts.Password, salt, f.KDF.Iterations)
		if _, err := store.DecryptSecretFields(connectionsManifestFile, &f.ConnectionStoreData, ps); err != nil {
			return nil, fmt.Errorf("wrong import password")
		}
	}

	// Remap group ids, then connection ids + groupId references.
	groupIDMap := map[string]string{}
	groups := make([]session.ConnectionGroup, len(f.Groups))
	for i, g := range f.Groups {
		newID := newGroupID()
		groupIDMap[g.ID] = newID
		g.ID = newID
		groups[i] = g
	}
	for i := range groups {
		if groups[i].ParentId != nil {
			if nid, ok := groupIDMap[*groups[i].ParentId]; ok {
				groups[i].ParentId = &nid
			}
		}
	}
	connections := make([]session.ConnectionConfig, len(f.Connections))
	for i, c := range f.Connections {
		c.ID = newConnectionID()
		if c.GroupId != nil {
			if nid, ok := groupIDMap[*c.GroupId]; ok {
				c.GroupId = &nid
			}
		}
		connections[i] = c
	}
	return &ImportResult{Groups: groups, Connections: connections}, nil
}
