package importer

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ys-ll/uniterm/backend/credentials"
	"github.com/ys-ll/uniterm/backend/session"
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

// ExportUniterm serializes the full store to .utm JSON. When password is empty,
// credential fields (passwords, inline keys) are cleared and encrypted=false.
// Otherwise every secret — Password, KeyContent, SentinelPassword,
// TunnelSSHPassword and PostLoginScript — is encrypted with a fresh
// PBKDF2-derived key and encrypted=true.
func ExportUniterm(data session.ConnectionStoreData, password string) ([]byte, error) {
	f := utmFile{Format: unitermFormat, Version: 1, ConnectionStoreData: data}
	if password == "" {
		for i := range f.Connections {
			f.Connections[i].Password = ""
			f.Connections[i].KeyContent = ""
			f.Connections[i].SentinelPassword = ""
			f.Connections[i].TunnelSSHPassword = ""
		}
		return json.MarshalIndent(f, "", "  ")
	}
	salt, err := newSalt()
	if err != nil {
		return nil, err
	}
	f.Encrypted = true
	f.KDF = &utmKDF{Algo: kdfAlgo, Iterations: kdfIterations, Salt: encodeSalt(salt)}
	for i := range f.Connections {
		c := &f.Connections[i]
		for _, field := range []*string{&c.Password, &c.KeyContent, &c.SentinelPassword, &c.TunnelSSHPassword, &c.PostLoginScript} {
			if *field == "" || strings.HasPrefix(*field, credentials.Prefix) {
				continue
			}
			enc, err := encryptField(*field, password, salt)
			if err != nil {
				return nil, err
			}
			*field = enc
		}
	}
	return json.MarshalIndent(f, "", "  ")
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
		salt, err := decodeSalt(f.KDF.Salt)
		if err != nil {
			return nil, fmt.Errorf("decode salt: %w", err)
		}
		for i := range f.Connections {
			c := &f.Connections[i]
			// Password decrypts strictly: a failure here means the wrong
			// import password (it has always been encrypted when non-empty).
			if c.Password != "" {
				plain, err := decryptField(c.Password, opts.Password, salt)
				if err != nil {
					return nil, fmt.Errorf("wrong import password")
				}
				c.Password = plain
			}
			// Fields the encrypted export protects since the field-encryption
			// fix: decrypt only what carries the encrypted prefix; a value
			// without it is a legacy export that stored the field in plaintext.
			for _, field := range []*string{&c.KeyContent, &c.SentinelPassword, &c.TunnelSSHPassword, &c.PostLoginScript} {
				if !strings.HasPrefix(*field, credentials.Prefix) {
					continue
				}
				plain, err := decryptField(*field, opts.Password, salt)
				if err != nil {
					return nil, fmt.Errorf("wrong import password")
				}
				*field = plain
			}
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
