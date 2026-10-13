package session

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/ssh"
)

const ppkMagic = "PuTTY-User-Key-File-"

// isPPK reports whether key looks like a PuTTY private key (.ppk) file.
func isPPK(key []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(key), []byte(ppkMagic))
}

// parsePPK parses a PuTTY private key (format v2 or v3; RSA, ECDSA and
// Ed25519; optionally encrypted with aes256-cbc) into an SSH signer. This lets
// keys imported from MobaXterm / PuTTY sessions be used as-is, without a manual
// puttygen conversion.
//
// The file's Private-MAC is verified BEFORE any key material is used
// (PuTTY Appendix C.2): a tampered file is rejected outright, and a wrong
// passphrase on an encrypted file is reported as exactly that instead of
// decoding into a garbage signer.
func parsePPK(data []byte, passphrase string) (ssh.Signer, error) {
	headers := map[string]string{}
	// PuTTY writes LF and tolerates CR/LF and CR-only line endings; normalize
	// so header values (the comment is MAC material) are read byte-exact.
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	var pubB64, privB64 strings.Builder
	for i := range lines {
		colon := strings.IndexByte(lines[i], ':')
		if colon < 0 {
			continue // base64 body lines carry no colon
		}
		name := strings.TrimSpace(lines[i][:colon])
		// PuTTY emits "name: value": exactly one space separates them, so only
		// that space is stripped — everything else stays verbatim, because the
		// MAC is computed over the bytes as written (a comment with trailing
		// spaces must still match).
		val := strings.TrimPrefix(lines[i][colon+1:], " ")
		switch name {
		case "Public-Lines", "Private-Lines":
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil || n < 0 || i+n >= len(lines)+1 {
				return nil, fmt.Errorf("ppk: bad %s", name)
			}
			b := &pubB64
			if name == "Private-Lines" {
				b = &privB64
			}
			for j := 1; j <= n && i+j < len(lines); j++ {
				b.WriteString(strings.TrimSpace(lines[i+j]))
			}
		default:
			headers[name] = val
		}
	}

	version := 0
	for k, v := range headers {
		if strings.HasPrefix(k, ppkMagic) {
			version, _ = strconv.Atoi(strings.TrimPrefix(k, ppkMagic))
			headers["algo"] = v
		}
	}
	if version != 2 && version != 3 {
		return nil, fmt.Errorf("ppk: unsupported format version %d", version)
	}
	algo := headers["algo"]
	pub, err := base64.StdEncoding.DecodeString(pubB64.String())
	if err != nil {
		return nil, fmt.Errorf("ppk: public blob: %w", err)
	}
	priv, err := base64.StdEncoding.DecodeString(privB64.String())
	if err != nil {
		return nil, fmt.Errorf("ppk: private blob: %w", err)
	}

	// encryption is the header value exactly as written; it is part of the MAC
	// preimage, so a missing header defaults to "none" for both.
	encryption := headers["Encryption"]
	if encryption == "" {
		encryption = "none"
	}
	var macKey []byte
	switch encryption {
	case "none":
		// Unencrypted: v2 still MACs with a key derived from the EMPTY
		// passphrase, v3 uses an empty MAC key (PuTTY Appendix C.4).
		if version == 2 {
			macKey = ppkV2MACKey("")
		}
	case "aes256-cbc":
		if len(priv)%aes.BlockSize != 0 {
			return nil, fmt.Errorf("ppk: encrypted blob has bad length")
		}
		var key, iv []byte
		if version == 2 {
			key = ppkV2Key(passphrase)
			iv = make([]byte, aes.BlockSize)
			macKey = ppkV2MACKey(passphrase)
		} else {
			key, iv, macKey, err = ppkV3Keys(headers, passphrase)
			if err != nil {
				return nil, err
			}
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		// priv is the plaintext (including the random padding) from here on —
		// that is what the MAC covers.
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(priv, priv)
	default:
		return nil, fmt.Errorf("ppk: unsupported encryption %q", encryption)
	}
	if err := verifyPPKMAC(version, headers["Private-MAC"], algo, encryption, headers["Comment"], pub, priv, macKey, encryption != "none"); err != nil {
		return nil, err
	}

	pr := &ppkReader{b: pub}
	if t, err := pr.str(); err != nil || string(t) != algo {
		return nil, fmt.Errorf("ppk: public blob does not match algorithm %q", algo)
	}
	sr := &ppkReader{b: priv}

	var signer interface{}
	switch {
	case algo == "ssh-rsa":
		e, err1 := pr.mpint()
		n, err2 := pr.mpint()
		d, err3 := sr.mpint()
		p, err4 := sr.mpint()
		q, err5 := sr.mpint()
		if err := firstErr(err1, err2, err3, err4, err5); err != nil {
			return nil, fmt.Errorf("ppk: rsa key: %w", err)
		}
		k := &rsa.PrivateKey{
			PublicKey: rsa.PublicKey{N: n, E: int(e.Int64())},
			D:         d,
			Primes:    []*big.Int{p, q},
		}
		if err := k.Validate(); err != nil {
			return nil, fmt.Errorf("ppk: rsa key invalid (wrong passphrase?): %w", err)
		}
		k.Precompute()
		signer = k
	case algo == "ssh-ed25519":
		if _, err := pr.str(); err != nil {
			return nil, err
		}
		seed, err := sr.mpintBytes()
		if err != nil {
			return nil, fmt.Errorf("ppk: ed25519 key: %w", err)
		}
		if len(seed) > ed25519.SeedSize {
			seed = seed[len(seed)-ed25519.SeedSize:]
		}
		for len(seed) < ed25519.SeedSize {
			seed = append([]byte{0}, seed...)
		}
		signer = ed25519.NewKeyFromSeed(seed)
	case strings.HasPrefix(algo, "ecdsa-sha2-nistp"):
		var curve elliptic.Curve
		switch strings.TrimPrefix(algo, "ecdsa-sha2-") {
		case "nistp256":
			curve = elliptic.P256()
		case "nistp384":
			curve = elliptic.P384()
		case "nistp521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("ppk: unsupported curve in %q", algo)
		}
		if _, err := pr.str(); err != nil {
			return nil, err
		}
		point, err := pr.str()
		if err != nil {
			return nil, err
		}
		d, err := sr.mpint()
		if err != nil {
			return nil, fmt.Errorf("ppk: ecdsa key: %w", err)
		}
		x, y := elliptic.Unmarshal(curve, point)
		if x == nil {
			return nil, fmt.Errorf("ppk: bad ecdsa public point")
		}
		signer = &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}
	default:
		return nil, fmt.Errorf("ppk: unsupported key type %q", algo)
	}
	return ssh.NewSignerFromKey(signer)
}

func ppkV2Key(passphrase string) []byte {
	var key []byte
	for i := range uint32(2) {
		h := sha1.New()
		_ = binary.Write(h, binary.BigEndian, i)
		h.Write([]byte(passphrase))
		key = h.Sum(key)
	}
	return key[:32]
}

// ppkV2MACKey derives the v2 MAC key: SHA-1 over the fixed string
// "putty-private-key-file-mac-key" followed by the passphrase (PuTTY Appendix
// C.5.1). An unencrypted file uses the EMPTY passphrase regardless of what the
// caller passed.
func ppkV2MACKey(passphrase string) []byte {
	h := sha1.New()
	h.Write([]byte("putty-private-key-file-mac-key"))
	h.Write([]byte(passphrase))
	return h.Sum(nil)
}

// Argon2 parameter ceilings for encrypted PPK files (PuTTY's defaults are
// 8 MiB / 1 pass / 1 thread, so these leave enormous headroom while stopping a
// crafted key file from forcing a huge allocation or an arbitrarily long CPU
// stall before anything else in the file is checked).
const (
	ppkArgon2MaxMemoryKiB   = 1 << 20 // 1 GiB
	ppkArgon2MaxPasses      = 16
	ppkArgon2MaxParallelism = 4
)

// ppkV3Keys derives the v3 key material from the Argon2 headers: the AES key,
// the CBC IV and the HMAC-SHA-256 MAC key (PuTTY Appendix C.4: 32 + 16 + 32
// bytes). Parameters are range-checked BEFORE the KDF runs.
func ppkV3Keys(h map[string]string, passphrase string) (key, iv, macKey []byte, err error) {
	salt, err := hex.DecodeString(strings.TrimSpace(h["Argon2-Salt"]))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ppk: bad argon2 salt: %w", err)
	}
	mem, err := ppkArgon2Param("Argon2-Memory", h, ppkArgon2MaxMemoryKiB)
	if err != nil {
		return nil, nil, nil, err
	}
	passes, err := ppkArgon2Param("Argon2-Passes", h, ppkArgon2MaxPasses)
	if err != nil {
		return nil, nil, nil, err
	}
	parallelism, err := ppkArgon2Param("Argon2-Parallelism", h, ppkArgon2MaxParallelism)
	if err != nil {
		return nil, nil, nil, err
	}
	var derive func(pw, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte
	switch strings.ToLower(strings.TrimSpace(h["Key-Derivation"])) {
	case "argon2id":
		derive = argon2.IDKey
	case "argon2i":
		derive = argon2.Key
	default:
		return nil, nil, nil, fmt.Errorf("ppk: unsupported key derivation %q", h["Key-Derivation"])
	}
	out := derive([]byte(passphrase), salt, uint32(passes), uint32(mem), uint8(parallelism), 80)
	return out[:32], out[32:48], out[48:80], nil
}

// ppkArgon2Param parses one positive Argon2 integer header and enforces its
// ceiling. Out-of-range values are named in the error so the user can see
// which parameter made the file unusable.
func ppkArgon2Param(name string, h map[string]string, limit int) (int, error) {
	raw := strings.TrimSpace(h[name])
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		if raw == "" {
			return 0, fmt.Errorf("ppk: missing %s", name)
		}
		if err != nil {
			// Out of int range is an overflow, not a typo: report it as such.
			return 0, fmt.Errorf("ppk: %s %q exceeds the %d limit", name, raw, limit)
		}
		return 0, fmt.Errorf("ppk: bad %s %q", name, raw)
	}
	if v > limit {
		return 0, fmt.Errorf("ppk: %s %d exceeds the %d limit", name, v, limit)
	}
	return v, nil
}

// verifyPPKMAC checks the file's Private-MAC before any key material is used.
//
// The preimage is five SSH strings (PuTTY Appendix C.2): algorithm name,
// encryption type, comment, the public blob and the PLAINTEXT private blob
// (decrypted, padding included). v2 uses HMAC-SHA-1 with the v2 key, v3
// HMAC-SHA-256 with the key derived alongside the cipher material.
// encrypted says whether the file was encrypted, so a failure can be blamed
// on the passphrase the user just typed rather than on the file itself.
func verifyPPKMAC(version int, macHex, algo, encryption, comment string, pub, priv, macKey []byte, encrypted bool) error {
	macHex = strings.TrimSpace(macHex)
	if macHex == "" {
		return fmt.Errorf("ppk: missing Private-MAC header")
	}
	want, err := hex.DecodeString(macHex)
	if err != nil {
		return fmt.Errorf("ppk: bad Private-MAC: %w", err)
	}
	preimage := ppkMACPreimage(algo, encryption, comment, pub, priv)
	var mac []byte
	if version == 2 {
		h := hmac.New(sha1.New, macKey)
		h.Write(preimage)
		mac = h.Sum(nil)
	} else {
		h := hmac.New(sha256.New, macKey)
		h.Write(preimage)
		mac = h.Sum(nil)
	}
	if !hmac.Equal(mac, want) {
		if encrypted {
			return fmt.Errorf("ppk: wrong passphrase or corrupted key file (Private-MAC mismatch)")
		}
		return fmt.Errorf("ppk: Private-MAC mismatch (key file corrupted or modified)")
	}
	return nil
}

// ppkMACPreimage builds the MAC input: the algorithm, encryption type and
// comment headers plus both key blobs, each encoded as an SSH string (RFC 4251
// uint32 length prefix, no mpint sign byte).
func ppkMACPreimage(algo, encryption, comment string, pub, priv []byte) []byte {
	parts := [][]byte{[]byte(algo), []byte(encryption), []byte(comment), pub, priv}
	size := 0
	for _, p := range parts {
		size += 4 + len(p)
	}
	out := make([]byte, 0, size)
	for _, p := range parts {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(p)))
		out = append(out, l[:]...)
		out = append(out, p...)
	}
	return out
}

type ppkReader struct{ b []byte }

func (r *ppkReader) str() ([]byte, error) {
	if len(r.b) < 4 {
		return nil, fmt.Errorf("truncated blob")
	}
	n := binary.BigEndian.Uint32(r.b)
	if uint64(n) > uint64(len(r.b)-4) {
		return nil, fmt.Errorf("truncated blob")
	}
	s := r.b[4 : 4+n]
	r.b = r.b[4+n:]
	return s, nil
}

func (r *ppkReader) mpintBytes() ([]byte, error) { return r.str() }

func (r *ppkReader) mpint() (*big.Int, error) {
	b, err := r.str()
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
