package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withTestKey swaps a freshly generated ed25519 keypair in as the package
// verification key for one test and returns the private half. Production code
// always verifies against the embedded key; the tests must never depend on it.
func withTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	prev := updatePublicKey
	updatePublicKey = pub
	t.Cleanup(func() { updatePublicKey = prev })
	return priv
}

// TestEmbeddedChecksumsKeyIsWellFormed guards the shipped constant: it must
// decode to a 32-byte ed25519 public key, otherwise every published signature
// would be rejected (fail closed, but for the wrong reason).
func TestEmbeddedChecksumsKeyIsWellFormed(t *testing.T) {
	raw, err := hex.DecodeString(checksumsPublicKeyHex)
	if err != nil {
		t.Fatalf("embedded public key is not valid hex: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Fatalf("embedded public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	if len(updatePublicKey) != ed25519.PublicKeySize {
		t.Fatalf("parsed embedded key is %d bytes, want %d", len(updatePublicKey), ed25519.PublicKeySize)
	}
}

// TestVerifyChecksumsSignature: a checksums.txt is only trusted when the
// detached base64 signature covers its exact bytes and verifies against the
// embedded public key. Anything else — tampering, a foreign signer, a missing
// or malformed signature — is an error, so the caller can fail closed.
func TestVerifyChecksumsSignature(t *testing.T) {
	sums := []byte("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef  uniterm-windows-amd64-installer-v1.9.9.exe\n")

	validPriv := withTestKey(t)
	validSig, err := SignChecksums(sums, validPriv)
	if err != nil {
		t.Fatalf("SignChecksums: %v", err)
	}

	_, foreignPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate foreign key: %v", err)
	}
	foreignSig, err := SignChecksums(sums, foreignPriv)
	if err != nil {
		t.Fatalf("SignChecksums (foreign): %v", err)
	}

	tampered := append(append([]byte(nil), sums...), 'x')

	cases := []struct {
		name      string
		checksums []byte
		sig       string
		wantErr   string // "" means the signature must verify
	}{
		{name: "valid signature", checksums: sums, sig: validSig},
		{name: "tampered content", checksums: tampered, sig: validSig, wantErr: "signature"},
		{name: "signed by a foreign key", checksums: sums, sig: foreignSig, wantErr: "signature"},
		{name: "empty signature", checksums: sums, sig: "", wantErr: "64"},
		{name: "not base64", checksums: sums, sig: "!!! not base64 !!!", wantErr: "base64"},
		{name: "truncated signature", checksums: sums, sig: base64.StdEncoding.EncodeToString(make([]byte, 32)), wantErr: "64"},
		{name: "signature with trailing newline", checksums: sums, sig: validSig + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyChecksumsSignature(tc.checksums, []byte(tc.sig))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected the signature to verify, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error should contain %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestParseSigningKeyHex covers the CI secret / build/release-signing.key
// format: the hex encoding of the 64-byte raw ed25519 private key, tolerating
// surrounding whitespace but nothing else.
func TestParseSigningKeyHex(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	validHex := hex.EncodeToString(priv)

	cases := []struct {
		name    string
		in      string
		wantErr string // "" means the key must parse
	}{
		{name: "bare hex", in: validHex},
		{name: "hex with whitespace", in: "  " + validHex + "\n"},
		{name: "empty", in: "", wantErr: "want 64"},
		{name: "odd length", in: validHex[:len(validHex)-1], wantErr: "hex"},
		{name: "not hex", in: strings.Repeat("z", 128), wantErr: "hex"},
		{name: "seed only (32 bytes)", in: hex.EncodeToString(priv.Seed()), wantErr: "want 64"},
		{name: "too long", in: validHex + "00", wantErr: "want 64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := ParseSigningKeyHex(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected the key to parse, got: %v", err)
				}
				if string(key) != string(priv) {
					t.Fatal("parsed key does not match the original bytes")
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error should contain %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestSignChecksumsRoundTrip is the isomorphic coverage for build/signcmd:
// the CLI decodes the hex-encoded 64-byte private key (ParseSigningKeyHex)
// and writes the base64 signature that VerifyChecksumsSignature consumes.
// If the encodings ever drift apart, release signing breaks here first.
func TestSignChecksumsRoundTrip(t *testing.T) {
	priv := withTestKey(t)
	key, err := ParseSigningKeyHex(hex.EncodeToString(priv))
	if err != nil {
		t.Fatalf("ParseSigningKeyHex: %v", err)
	}

	sums := []byte("cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe *uniterm-linux-amd64-v1.9.9.tar.gz\n")
	sig, err := SignChecksums(sums, key)
	if err != nil {
		t.Fatalf("SignChecksums: %v", err)
	}
	if err := VerifyChecksumsSignature(sums, []byte(sig)); err != nil {
		t.Fatalf("signature produced by SignChecksums must verify: %v", err)
	}

	// A signature over other content must not verify.
	if err := VerifyChecksumsSignature(append(sums, 'x'), []byte(sig)); err == nil {
		t.Fatal("signature must not verify for different content")
	}
}

// TestSignChecksumsRejectsWrongKeySize: only the 64-byte raw key (seed+pub)
// is accepted; a truncated or oversized key must not silently sign.
func TestSignChecksumsRejectsWrongKeySize(t *testing.T) {
	sums := []byte("checksums")
	for _, size := range []int{0, ed25519.SeedSize, ed25519.PrivateKeySize + 1} {
		if _, err := SignChecksums(sums, make(ed25519.PrivateKey, size)); err == nil {
			t.Errorf("SignChecksums must reject a %d-byte key", size)
		}
	}
}

// checksumsReleaseServer serves checksums.txt and, when sig is non-nil,
// checksums.txt.sig from a loopback release server, then returns the release
// metadata the checker consumes. The UNITERM_UPDATE_API_BASE override points
// at the same server so the release-source allowlist accepts it.
func checksumsReleaseServer(t *testing.T, sums, sig []byte) *release {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(sums)
	})
	assets := []releaseAsset{{Name: "checksums.txt"}}
	if sig != nil {
		mux.HandleFunc("/checksums.txt.sig", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(sig)
		})
		assets = append(assets, releaseAsset{Name: "checksums.txt.sig"})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("UNITERM_UPDATE_API_BASE", srv.URL)
	for i := range assets {
		assets[i].BrowserDownloadURL = srv.URL + "/" + assets[i].Name
	}
	return &release{TagName: "v1.9.9", Assets: assets}
}

// TestFetchChecksumsRequiresValidSignature: the hash table may only be built
// from a checksums.txt whose detached ed25519 signature verifies against the
// embedded public key. A missing, foreign or malformed signature must leave
// the table empty, which is the same path as a missing checksums.txt: every
// candidate is then refused at download time (fail closed).
func TestFetchChecksumsRequiresValidSignature(t *testing.T) {
	priv := withTestKey(t)
	sums := []byte("abababababababababababababababababababababababababababababababab  uniterm-linux-amd64-v1.9.9.tar.gz\n")

	goodSig, err := SignChecksums(sums, priv)
	if err != nil {
		t.Fatalf("SignChecksums: %v", err)
	}
	staleSig, err := SignChecksums([]byte("an older checksums.txt"), priv)
	if err != nil {
		t.Fatalf("SignChecksums (stale): %v", err)
	}
	_, foreignPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate foreign key: %v", err)
	}
	foreignSig, err := SignChecksums(sums, foreignPriv)
	if err != nil {
		t.Fatalf("SignChecksums (foreign): %v", err)
	}

	cases := []struct {
		name string
		sig  []byte // nil: the checksums.txt.sig asset is not published at all
		want int
	}{
		{name: "valid signature", sig: []byte(goodSig), want: 1},
		{name: "missing signature asset", sig: nil, want: 0},
		{name: "signature for other content", sig: []byte(staleSig), want: 0},
		{name: "signature from another key", sig: []byte(foreignSig), want: 0},
		{name: "malformed signature", sig: []byte("not a signature"), want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel := checksumsReleaseServer(t, sums, tc.sig)
			got := fetchChecksums(rel)
			if len(got) != tc.want {
				t.Fatalf("fetchChecksums returned %d checksum(s), want %d — an unverified checksums.txt must be discarded (fail closed)", len(got), tc.want)
			}
		})
	}
}
