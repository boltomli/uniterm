package session

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Set PPK_TEST_FILE to a real .ppk to verify parsing (skipped otherwise).
func TestParsePPKFile(t *testing.T) {
	p := os.Getenv("PPK_TEST_FILE")
	if p == "" {
		t.Skip("PPK_TEST_FILE not set")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s, err := parsePPK(data, os.Getenv("PPK_TEST_PASS"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("parsed %s key", s.PublicKey().Type())
}

// ppkFixturePassphrase decrypts every encrypted fixture in testdata. The
// fixtures come from an independent implementation of the PPK format
// (philr/putty-key), so passing them proves compatibility with a second
// implementation of the spec, not just with our own writer.
const ppkFixturePassphrase = "Test Passphrase"

// ppkFixture loads a .ppk from testdata/.
func ppkFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if len(data) == 0 {
		t.Fatalf("fixture %s is empty", name)
	}
	return data
}

// A real key must parse AND sign: a wrong passphrase that decoded into a
// garbage signer would still build a signer object but never survive Sign for
// RSA, and the MAC check must reject it before that for every key type.
func TestParsePPKAcceptsValidFixtures(t *testing.T) {
	cases := []struct {
		file       string
		passphrase string
		keyType    string
	}{
		{"rsa-2048-format-2.ppk", "", "ssh-rsa"},
		{"rsa-2048-format-3.ppk", "", "ssh-rsa"},
		{"rsa-2048-encrypted-format-2.ppk", ppkFixturePassphrase, "ssh-rsa"},
		{"rsa-2048-encrypted-format-3.ppk", ppkFixturePassphrase, "ssh-rsa"},
		{"ecdsa-sha2-nistp256-encrypted-format-2.ppk", ppkFixturePassphrase, "ecdsa-sha2-nistp256"},
		{"ecdsa-sha2-nistp256-encrypted-format-3.ppk", ppkFixturePassphrase, "ecdsa-sha2-nistp256"},
	}
	for _, tc := range cases {
		signer, err := parsePPK(ppkFixture(t, tc.file), tc.passphrase)
		if err != nil {
			t.Errorf("%s: parsePPK: %v", tc.file, err)
			continue
		}
		if got := signer.PublicKey().Type(); got != tc.keyType {
			t.Errorf("%s: key type = %q, want %q", tc.file, got, tc.keyType)
		}
		if _, err := signer.Sign(rand.Reader, []byte("ppk-fixture-challenge")); err != nil {
			t.Errorf("%s: sign with parsed key: %v", tc.file, err)
		}
	}
}

// macFailure asserts that an error is the Private-MAC check reporting a
// tampered file. An unencrypted key has no passphrase involved at all, so
// blaming the passphrase for a modified file (the old RSA Validate path did)
// would mislead the user about what is wrong.
func macFailure(t *testing.T, file string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: tampered file must be rejected", file)
		return
	}
	if !strings.Contains(err.Error(), "Private-MAC") {
		t.Errorf("%s: error must report the Private-MAC check, got: %v", file, err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "passphrase") {
		t.Errorf("%s: tampering an unencrypted key must not be blamed on the passphrase: %v", file, err)
	}
}

// flipPrivateBlobByte changes exactly one base64 character of the first
// Private-Lines row at a position whose six bits are all significant, so the
// decoded private blob really differs while the file stays valid base64.
func flipPrivateBlobByte(t *testing.T, data []byte) []byte {
	t.Helper()
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		if !strings.HasPrefix(lines[i], "Private-Lines:") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lines[i], "Private-Lines:")))
		if err != nil || n < 1 || i+n >= len(lines) {
			t.Fatalf("bad Private-Lines header %q", lines[i])
		}
		row := []byte(lines[i+1])
		const pos = 8 // 8%4 == 0 → every bit of this sextet lands in the blob
		if len(row) <= pos {
			t.Fatalf("private blob row too short: %q", row)
		}
		if row[pos] == 'A' {
			row[pos] = 'B'
		} else {
			row[pos] = 'A'
		}
		lines[i+1] = string(row)
		out := strings.Join(lines, "\n")
		if out == string(data) {
			t.Fatal("tamper changed nothing")
		}
		return []byte(out)
	}
	t.Fatal("Private-Lines block not found")
	return nil
}

// PuTTY's MAC covers the algorithm, encryption type, comment, public blob and
// plaintext private blob (Appendix C.2), so editing ANY of them must be
// caught before the key material is used.
func TestParsePPKRejectsTamperedFile(t *testing.T) {
	for _, file := range []string{"rsa-2048-format-2.ppk", "rsa-2048-format-3.ppk"} {
		orig := ppkFixture(t, file)

		// The comment is covered by the MAC, which is what stops an attacker
		// from swapping keys and editing the comment to match.
		const from = "Comment: 2048 bit RSA key"
		if !strings.Contains(string(orig), from) {
			t.Fatalf("%s: comment line %q not found", file, from)
		}
		edited := strings.Replace(string(orig), from, "Comment: 2048 bit RSA kez", 1)
		_, err := parsePPK([]byte(edited), "")
		macFailure(t, file+":comment", err)

		// One flipped byte inside the private blob.
		_, err = parsePPK(flipPrivateBlobByte(t, orig), "")
		macFailure(t, file+":private-blob", err)
	}

	// Tampering with the CIPHERTEXT of an encrypted key must be caught too.
	enc := ppkFixture(t, "rsa-2048-encrypted-format-3.ppk")
	_, err := parsePPK(flipPrivateBlobByte(t, enc), ppkFixturePassphrase)
	if err == nil {
		t.Fatal("tampered encrypted file must be rejected")
	}
	if !strings.Contains(err.Error(), "Private-MAC") {
		t.Errorf("encrypted tamper must be reported by the MAC check, got: %v", err)
	}
}

// A wrong passphrase must be reported as such instead of decoding into a
// garbage signer (Ed25519/ECDSA keys never validated, so the user saw a
// misleading auth failure much later).
func TestParsePPKWrongPassphraseIsReported(t *testing.T) {
	for _, file := range []string{
		"rsa-2048-encrypted-format-2.ppk",
		"rsa-2048-encrypted-format-3.ppk",
		"ecdsa-sha2-nistp256-encrypted-format-2.ppk",
		"ecdsa-sha2-nistp256-encrypted-format-3.ppk",
	} {
		_, err := parsePPK(ppkFixture(t, file), "not the passphrase")
		if err == nil {
			t.Fatalf("%s: wrong passphrase must be rejected, not handed back as a signer", file)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "passphrase") {
			t.Errorf("%s: error must name the passphrase, got: %v", file, err)
		}
	}
}

// Argon2 parameters come straight from the file: without bounds a crafted
// key file can force a huge allocation or a long CPU stall before anything
// else is checked.
func TestParsePPKBoundsArgon2Parameters(t *testing.T) {
	base := string(ppkFixture(t, "rsa-2048-encrypted-format-3.ppk"))
	cases := []struct {
		name  string
		edits map[string]string
		want  string
	}{
		{
			name:  "memory over 1GiB",
			edits: map[string]string{"Argon2-Memory: 16384": "Argon2-Memory: 1048577", "Argon2-Passes: 14": "Argon2-Passes: 1"},
			want:  "Argon2-Memory",
		},
		{
			name:  "passes over 16",
			edits: map[string]string{"Argon2-Passes: 14": "Argon2-Passes: 17"},
			want:  "Argon2-Passes",
		},
		{
			name:  "parallelism over 4",
			edits: map[string]string{"Argon2-Parallelism: 2": "Argon2-Parallelism: 5"},
			want:  "Argon2-Parallelism",
		},
	}
	for _, tc := range cases {
		data := base
		for from, to := range tc.edits {
			if !strings.Contains(data, from) {
				t.Fatalf("%s: fixture lacks line %q", tc.name, from)
			}
			data = strings.Replace(data, from, to, 1)
		}
		_, err := parsePPK([]byte(data), ppkFixturePassphrase)
		if err == nil {
			t.Fatalf("%s: out-of-range Argon2 parameters must be rejected", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error must name %s, got: %v", tc.name, tc.want, err)
		}
	}
}
