package session

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha1"
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
func parsePPK(data []byte, passphrase string) (ssh.Signer, error) {
	headers := map[string]string{}
	var pubLines, privLines int
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var pubB64, privB64 strings.Builder
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		name, val := line[:colon], strings.TrimSpace(line[colon+1:])
		switch name {
		case "Public-Lines", "Private-Lines":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 || i+n >= len(lines)+1 {
				return nil, fmt.Errorf("ppk: bad %s", name)
			}
			b := &pubB64
			if name == "Private-Lines" {
				b = &privB64
				privLines = n
			} else {
				pubLines = n
			}
			for j := 1; j <= n && i+j < len(lines); j++ {
				b.WriteString(strings.TrimSpace(lines[i+j]))
			}
			i += n
		default:
			headers[name] = val
		}
	}
	_ = pubLines
	_ = privLines

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

	switch enc := headers["Encryption"]; enc {
	case "none", "":
	case "aes256-cbc":
		if len(priv)%aes.BlockSize != 0 {
			return nil, fmt.Errorf("ppk: encrypted blob has bad length")
		}
		var key, iv []byte
		if version == 2 {
			key = ppkV2Key(passphrase)
			iv = make([]byte, aes.BlockSize)
		} else {
			key, iv, err = ppkV3KeyIV(headers, passphrase)
			if err != nil {
				return nil, err
			}
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(priv, priv)
	default:
		return nil, fmt.Errorf("ppk: unsupported encryption %q", enc)
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
	for i := uint32(0); i < 2; i++ {
		h := sha1.New()
		_ = binary.Write(h, binary.BigEndian, i)
		h.Write([]byte(passphrase))
		key = h.Sum(key)
	}
	return key[:32]
}

func ppkV3KeyIV(h map[string]string, passphrase string) (key, iv []byte, err error) {
	salt, err := hex.DecodeString(h["Argon2-Salt"])
	if err != nil {
		return nil, nil, fmt.Errorf("ppk: bad argon2 salt: %w", err)
	}
	mem, _ := strconv.Atoi(h["Argon2-Memory"])
	passes, _ := strconv.Atoi(h["Argon2-Passes"])
	par, _ := strconv.Atoi(h["Argon2-Parallelism"])
	if mem <= 0 || passes <= 0 || par <= 0 {
		return nil, nil, fmt.Errorf("ppk: bad argon2 parameters")
	}
	var derive func(pw, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte
	switch strings.ToLower(h["Key-Derivation"]) {
	case "argon2id":
		derive = argon2.IDKey
	case "argon2i":
		derive = argon2.Key
	default:
		return nil, nil, fmt.Errorf("ppk: unsupported key derivation %q", h["Key-Derivation"])
	}
	out := derive([]byte(passphrase), salt, uint32(passes), uint32(mem), uint8(par), 80)
	return out[:32], out[32:48], nil
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
