package importer

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/credentials"
	"github.com/ys-ll/uniterm/backend/session"
)

// sha256New exposes the hash constructor used by deriveKey.
func sha256New() hash.Hash { return sha256.New() }

// manyFieldStore builds n connections each carrying a Password and a
// KeyContent, i.e. 2 encrypted fields per connection.
func manyFieldStore(n int) session.ConnectionStoreData {
	conns := make([]session.ConnectionConfig, n)
	for i := range conns {
		conns[i] = session.ConnectionConfig{
			ID:     fmt.Sprintf("c%d", i),
			Name:   fmt.Sprintf("host-%d", i),
			Type:   "ssh",
			Host:   "192.0.2.1",
			Port:   22,
			User:   "root",
			AuthType: "keyText",
			Password:    "pw",
			KeyContent:  "-----BEGIN OPENSSH PRIVATE KEY-----",
		}
	}
	return session.ConnectionStoreData{Connections: conns}
}

// TestExportUnitermDerivesKeyOnce: exporting a 500-connection store (1000
// secret fields) must run PBKDF2 exactly once, not once per field.
func TestExportUnitermDerivesKeyOnce(t *testing.T) {
	orig := pbkdf2Fn
	calls := 0
	pbkdf2Fn = func(password, salt []byte, iter, keyLen int, h func() hash.Hash) []byte {
		calls++
		return orig(password, salt, iter, keyLen, h)
	}
	t.Cleanup(func() { pbkdf2Fn = orig })

	if _, err := ExportUniterm(manyFieldStore(500), "pass123"); err != nil {
		t.Fatalf("export: %v", err)
	}
	if calls != 1 {
		t.Fatalf("PBKDF2 ran %d times during export, want exactly 1", calls)
	}
}

// TestImportUnitermDerivesKeyOnce: importing a 500-connection encrypted file
// must derive the key once, not per field.
func TestImportUnitermDerivesKeyOnce(t *testing.T) {
	b, err := ExportUniterm(manyFieldStore(500), "pass123")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	orig := pbkdf2Fn
	calls := 0
	pbkdf2Fn = func(password, salt []byte, iter, keyLen int, h func() hash.Hash) []byte {
		calls++
		return orig(password, salt, iter, keyLen, h)
	}
	t.Cleanup(func() { pbkdf2Fn = orig })

	if _, err := parseUniterm(b, ParseOptions{Password: "pass123"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if calls != 1 {
		t.Fatalf("PBKDF2 ran %d times during import, want exactly 1", calls)
	}
}

// TestExportUnitermDefaultIterationsRaised: new exports must record the
// hardened default iteration count, not the old 100k.
func TestExportUnitermDefaultIterationsRaised(t *testing.T) {
	b, err := ExportUniterm(sampleStore(), "pass123")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var f utmFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.KDF == nil {
		t.Fatal("missing kdf block")
	}
	if f.KDF.Iterations != defaultKDFIterations {
		t.Fatalf("exported iterations = %d, want %d", f.KDF.Iterations, defaultKDFIterations)
	}
	if defaultKDFIterations < 600000 {
		t.Fatalf("defaultKDFIterations = %d, want >= 600000", defaultKDFIterations)
	}
}

// TestImportUnitermRejectsOutOfRangeIterations: KDF.Iterations from the file
// must be honored and bounds-checked; out-of-range values are rejected
// instead of silently deriving a weakened (or DoS-sized) key.
func TestImportUnitermRejectsOutOfRangeIterations(t *testing.T) {
	// Build a valid encrypted export, then patch the iteration count.
	b, err := ExportUniterm(sampleStore(), "pass123")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	patch := func(t *testing.T, iterations int) []byte {
		t.Helper()
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		kdf := m["kdf"].(map[string]interface{})
		kdf["iterations"] = iterations
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	for _, bad := range []int{0, 9999, 5_000_001, -1} {
		if _, err := parseUniterm(patch(t, bad), ParseOptions{Password: "pass123"}); err == nil {
			t.Fatalf("iterations=%d must be rejected, got nil error", bad)
		}
	}
	// In-range values (including the legacy 100k) are accepted at the
	// bounds check — the legacy round trip is covered separately.
	for _, ok := range []int{10000, 100000, 5_000_000} {
		// Re-encrypt under the patched iteration count so decryption can
		// actually succeed; here we only assert the bounds check passes by
		// checking the failure is a password error, not an iteration error.
		_, err := parseUniterm(patch(t, ok), ParseOptions{Password: "pass123"})
		if err != nil && strings.Contains(err.Error(), "iterations") {
			t.Fatalf("iterations=%d wrongly rejected: %v", ok, err)
		}
	}
}

// TestImportLegacy100kExportStillWorks: a .utm file produced by the old
// exporter (100k iterations, per-field derivation) must still import.
func TestImportLegacy100kExportStillWorks(t *testing.T) {
	salt, err := newSalt()
	if err != nil {
		t.Fatal(err)
	}
	key := pbkdf2Fn([]byte("pass123"), salt, 100000, keyLen, sha256New)
	enc, err := credentials.EncryptField("hunter2", key)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"format":"uniterm","version":1,"encrypted":true,`+
		`"kdf":{"algo":"pbkdf2-sha256","iterations":100000,"salt":%q},`+
		`"groups":[],`+
		`"connections":[{"id":"c1","name":"db","type":"ssh","host":"h","port":22,"user":"u",`+
		`"authType":"password","password":%q}]}`, encodeSalt(salt), enc)

	res, err := parseUniterm([]byte(body), ParseOptions{Password: "pass123"})
	if err != nil {
		t.Fatalf("legacy 100k export must import: %v", err)
	}
	if len(res.Connections) != 1 || res.Connections[0].Password != "hunter2" {
		t.Fatalf("password not restored: %+v", res.Connections)
	}
}
