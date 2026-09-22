package session

import (
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Per-connection SSH algorithm modes stored in ConnectionConfig.SSHAlgorithms.
// An absent (nil) configuration selects the compatible mode.
const (
	SSHAlgoModeCompatible = "compatible"
	SSHAlgoModeSecure     = "secure"
	SSHAlgoModeCustom     = "custom"
)

// sshAlgoSet bundles the four negotiated algorithm dimensions. Host keys live
// on ssh.ClientConfig.HostKeyAlgorithms rather than in ssh.Config, so the set
// is resolved first and applied to the client config afterwards.
type sshAlgoSet struct {
	KeyExchanges []string
	Ciphers      []string
	MACs         []string
	HostKeys     []string
}

func (a sshAlgoSet) config() ssh.Config {
	return ssh.Config{
		KeyExchanges: a.KeyExchanges,
		Ciphers:      a.Ciphers,
		MACs:         a.MACs,
	}
}

func (a sshAlgoSet) apply(clientConfig *ssh.ClientConfig) {
	clientConfig.Config = a.config()
	clientConfig.HostKeyAlgorithms = a.HostKeys
}

// sshKeyExchanges returns the KEX algorithm list for the compatible mode:
// every algorithm this package implements, modern ones first, legacy SHA-1
// based exchanges last. Order is the negotiation preference.
func sshKeyExchanges() []string {
	return []string{
		"mlkem768x25519-sha256",
		"curve25519-sha256",
		"curve25519-sha256@libssh.org",
		"ecdh-sha2-nistp256",
		"ecdh-sha2-nistp384",
		"ecdh-sha2-nistp521",
		"diffie-hellman-group14-sha256",
		"diffie-hellman-group16-sha512",
		"diffie-hellman-group-exchange-sha256",
		// Legacy algorithms for old servers (issue #208)
		ssh.InsecureKeyExchangeDH14SHA1,
		ssh.InsecureKeyExchangeDHGEXSHA1,
		ssh.InsecureKeyExchangeDH1SHA1,
	}
}

// sshCiphers returns the cipher list for the compatible mode, including the
// legacy CBC/3DES ciphers (issue #497). aes192-cbc and aes256-cbc come from
// the maintained fork of golang.org/x/crypto and complete the AES-CBC family
// for devices that only offer the larger key sizes.
//
// The default order prefers AEAD ciphers. Some old SSH servers advertise GCM
// but close the connection when it is negotiated; callers retry those EOF
// handshakes once with sshAlgorithmsCTRFirst.
func sshCiphers() []string {
	return []string{
		ssh.CipherAES128GCM,
		ssh.CipherAES256GCM,
		ssh.CipherChaCha20Poly1305,
		ssh.CipherAES128CTR,
		ssh.CipherAES192CTR,
		ssh.CipherAES256CTR,
		// Legacy CBC/3DES ciphers for old servers (issue #497)
		ssh.InsecureCipherAES128CBC,
		ssh.InsecureCipherAES192CBC,
		ssh.InsecureCipherAES256CBC,
		ssh.InsecureCipherTripleDESCBC,
	}
}

func sshCiphersCTRFirst() []string {
	return []string{
		ssh.CipherAES128CTR,
		ssh.CipherAES192CTR,
		ssh.CipherAES256CTR,
		ssh.CipherChaCha20Poly1305,
		ssh.CipherAES128GCM,
		ssh.CipherAES256GCM,
		ssh.InsecureCipherAES128CBC,
		ssh.InsecureCipherAES192CBC,
		ssh.InsecureCipherAES256CBC,
		ssh.InsecureCipherTripleDESCBC,
	}
}

// sshMACs returns the MAC list for the compatible mode, including legacy
// algorithms for compatibility with old servers (issue #497).
func sshMACs() []string {
	return []string{
		ssh.HMACSHA256ETM,
		ssh.HMACSHA512ETM,
		ssh.HMACSHA256,
		ssh.HMACSHA512,
		ssh.HMACSHA1,
		ssh.InsecureHMACSHA196,
	}
}

// sshHostKeys returns the host-key algorithm list for the compatible mode.
//
// An explicit list is set instead of the library default so that plain RSA
// (ssh-rsa, SHA-1 signature) is preferred over ECDSA P-521: some network
// devices advertise malformed P-521 host keys, and preferring RSA lets the
// handshake reach authentication on those hosts. Healthy servers offering
// rsa-sha2-* still negotiate those first because they rank above plain RSA.
func sshHostKeys() []string {
	return []string{
		ssh.CertAlgoRSASHA256v01,
		ssh.CertAlgoRSASHA512v01,
		ssh.CertAlgoECDSA256v01,
		ssh.CertAlgoECDSA384v01,
		ssh.CertAlgoECDSA521v01,
		ssh.CertAlgoED25519v01,
		ssh.KeyAlgoED25519,
		ssh.KeyAlgoECDSA256,
		ssh.KeyAlgoECDSA384,
		ssh.KeyAlgoRSASHA512,
		ssh.KeyAlgoRSASHA256,
		ssh.KeyAlgoRSA,
		ssh.KeyAlgoECDSA521,
		ssh.InsecureKeyAlgoDSA,
	}
}

// sshAlgorithms returns the compatible-mode set: the full support set with
// security-leaning ordering.
func sshAlgorithms() sshAlgoSet {
	return sshAlgoSet{
		KeyExchanges: sshKeyExchanges(),
		Ciphers:      sshCiphers(),
		MACs:         sshMACs(),
		HostKeys:     sshHostKeys(),
	}
}

func sshAlgorithmsCTRFirst() sshAlgoSet {
	set := sshAlgorithms()
	set.Ciphers = sshCiphersCTRFirst()
	return set
}

// sshKeyExchangesSecure returns the modern-only KEX list for the secure mode.
func sshKeyExchangesSecure() []string {
	return []string{
		"mlkem768x25519-sha256",
		"curve25519-sha256",
		"curve25519-sha256@libssh.org",
		"ecdh-sha2-nistp256",
		"ecdh-sha2-nistp384",
		"ecdh-sha2-nistp521",
		"diffie-hellman-group14-sha256",
		"diffie-hellman-group16-sha512",
		"diffie-hellman-group-exchange-sha256",
	}
}

// sshCiphersSecure returns the modern-only cipher list for the secure mode.
func sshCiphersSecure() []string {
	return []string{
		ssh.CipherAES128GCM,
		ssh.CipherAES256GCM,
		ssh.CipherChaCha20Poly1305,
		ssh.CipherAES128CTR,
		ssh.CipherAES192CTR,
		ssh.CipherAES256CTR,
	}
}

// sshMACsSecure returns the modern-only MAC list for the secure mode.
func sshMACsSecure() []string {
	return []string{
		ssh.HMACSHA256ETM,
		ssh.HMACSHA512ETM,
		ssh.HMACSHA256,
		ssh.HMACSHA512,
	}
}

// sshHostKeysSecure returns the modern-only host-key list for the secure
// mode: certificate algorithms plus ed25519/ecdsa and RSA with SHA-2
// signatures. Plain ssh-rsa (SHA-1) and DSA are excluded.
func sshHostKeysSecure() []string {
	return []string{
		ssh.CertAlgoRSASHA256v01,
		ssh.CertAlgoRSASHA512v01,
		ssh.CertAlgoECDSA256v01,
		ssh.CertAlgoECDSA384v01,
		ssh.CertAlgoECDSA521v01,
		ssh.CertAlgoED25519v01,
		ssh.KeyAlgoED25519,
		ssh.KeyAlgoECDSA256,
		ssh.KeyAlgoECDSA384,
		ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoRSASHA256,
		ssh.KeyAlgoRSASHA512,
	}
}

// sshAlgorithmsSecure returns the secure-mode set: modern algorithms only,
// for environments that must not offer SHA-1/CBC/DSA.
func sshAlgorithmsSecure() sshAlgoSet {
	return sshAlgoSet{
		KeyExchanges: sshKeyExchangesSecure(),
		Ciphers:      sshCiphersSecure(),
		MACs:         sshMACsSecure(),
		HostKeys:     sshHostKeysSecure(),
	}
}

// resolveSSHAlgorithms maps a connection's algorithm preferences to the set
// used for dialing, plus the set for the EOF handshake retry. nil preferences
// select the compatible mode.
func resolveSSHAlgorithms(p *SSHAlgoConfig) (base sshAlgoSet, retry sshAlgoSet, err error) {
	if p == nil {
		return sshAlgorithms(), sshAlgorithmsCTRFirst(), nil
	}
	switch p.Mode {
	case "", SSHAlgoModeCompatible:
		return sshAlgorithms(), sshAlgorithmsCTRFirst(), nil
	case SSHAlgoModeSecure:
		return sshAlgorithmsSecure(), sshAlgorithmsSecure(), nil
	case SSHAlgoModeCustom:
		set, err := customSSHAlgorithms(p)
		if err != nil {
			return sshAlgoSet{}, sshAlgoSet{}, err
		}
		return set, set, nil
	default:
		return sshAlgoSet{}, sshAlgoSet{}, fmt.Errorf("unknown SSH algorithm mode %q", p.Mode)
	}
}

// customSSHAlgorithms validates a custom algorithm configuration against the
// supported set and builds the algorithm lists. x/crypto silently drops
// unknown algorithm names during SetDefaults, so they are rejected here
// instead of producing a configuration that looks configured but does not
// apply.
func customSSHAlgorithms(p *SSHAlgoConfig) (sshAlgoSet, error) {
	compatible := sshAlgorithms()
	build := func(label string, values, supported []string) ([]string, error) {
		if len(values) == 0 {
			return nil, fmt.Errorf("custom SSH algorithm list %q is empty", label)
		}
		out := make([]string, 0, len(values))
		for _, v := range values {
			if !containsSSHAlgo(supported, v) {
				return nil, fmt.Errorf("unsupported SSH %s algorithm %q", label, v)
			}
			out = append(out, v)
		}
		return out, nil
	}
	kex, err := build("key exchange", p.KeyExchanges, compatible.KeyExchanges)
	if err != nil {
		return sshAlgoSet{}, err
	}
	ciphers, err := build("cipher", p.Ciphers, compatible.Ciphers)
	if err != nil {
		return sshAlgoSet{}, err
	}
	macs, err := build("MAC", p.MACs, compatible.MACs)
	if err != nil {
		return sshAlgoSet{}, err
	}
	hostKeys, err := build("host key", p.HostKeys, compatible.HostKeys)
	if err != nil {
		return sshAlgoSet{}, err
	}
	return sshAlgoSet{
		KeyExchanges: kex,
		Ciphers:      ciphers,
		MACs:         macs,
		HostKeys:     hostKeys,
	}, nil
}

func containsSSHAlgo(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// Security levels attached to algorithm candidates surfaced to the UI. The
// level is our own classification and deliberately finer-grained than the
// library's binary insecure marking: SHA-1 KEX with a 2048-bit group and
// AES-CBC are deprecated but not broken, while 3DES, group1, truncated SHA-1
// and DSA have practical attacks.
const (
	SSHAlgoLevelModern   = "modern"
	SSHAlgoLevelLegacy   = "legacy"
	SSHAlgoLevelInsecure = "insecure"
)

// SSHAlgorithmOption is one selectable algorithm in the candidate pool.
type SSHAlgorithmOption struct {
	ID    string `json:"id"`
	Level string `json:"level"`
}

// SSHAlgorithmsPreset is a named algorithm set (compatible/secure) the UI can
// load as a starting point.
type SSHAlgorithmsPreset struct {
	Mode         string   `json:"mode"`
	KeyExchanges []string `json:"keyExchanges"`
	Ciphers      []string `json:"ciphers"`
	MACs         []string `json:"macs"`
	HostKeys     []string `json:"hostKeys"`
}

// SupportedSSHAlgorithms is the candidate pool plus both presets. The pool is
// closed: custom configurations may only reference these IDs.
type SupportedSSHAlgorithms struct {
	KeyExchanges []SSHAlgorithmOption `json:"keyExchanges"`
	Ciphers      []SSHAlgorithmOption `json:"ciphers"`
	MACs         []SSHAlgorithmOption `json:"macs"`
	HostKeys     []SSHAlgorithmOption `json:"hostKeys"`
	Compatible   SSHAlgorithmsPreset  `json:"compatible"`
	Secure       SSHAlgorithmsPreset  `json:"secure"`
}

func kexLevel(id string) string {
	switch id {
	case ssh.InsecureKeyExchangeDH1SHA1:
		return SSHAlgoLevelInsecure
	case ssh.InsecureKeyExchangeDH14SHA1, ssh.InsecureKeyExchangeDHGEXSHA1:
		return SSHAlgoLevelLegacy
	}
	return SSHAlgoLevelModern
}

func cipherLevel(id string) string {
	if id == ssh.InsecureCipherTripleDESCBC {
		return SSHAlgoLevelInsecure
	}
	if strings.HasSuffix(id, "-cbc") {
		return SSHAlgoLevelLegacy
	}
	return SSHAlgoLevelModern
}

func macLevel(id string) string {
	switch id {
	case ssh.InsecureHMACSHA196:
		return SSHAlgoLevelInsecure
	case ssh.HMACSHA1:
		return SSHAlgoLevelLegacy
	}
	return SSHAlgoLevelModern
}

func hostKeyLevel(id string) string {
	switch id {
	case ssh.InsecureKeyAlgoDSA:
		return SSHAlgoLevelInsecure
	case ssh.KeyAlgoRSA:
		return SSHAlgoLevelLegacy
	}
	return SSHAlgoLevelModern
}

func presetFromMode(mode string, set sshAlgoSet) SSHAlgorithmsPreset {
	return SSHAlgorithmsPreset{
		Mode:         mode,
		KeyExchanges: set.KeyExchanges,
		Ciphers:      set.Ciphers,
		MACs:         set.MACs,
		HostKeys:     set.HostKeys,
	}
}

// GetSupportedSSHAlgorithms builds the algorithm candidate pool and both
// presets. Certificate host keys are validated in custom configurations but
// excluded from the pool: they are an OpenSSH CA detail, not a per-device
// compatibility knob.
func GetSupportedSSHAlgorithms() SupportedSSHAlgorithms {
	compatible := sshAlgorithms()
	secure := sshAlgorithmsSecure()

	pool := func(list []string, level func(string) string) []SSHAlgorithmOption {
		out := make([]SSHAlgorithmOption, 0, len(list))
		for _, id := range list {
			out = append(out, SSHAlgorithmOption{ID: id, Level: level(id)})
		}
		return out
	}
	hostKeyPool := pool(compatible.HostKeys, hostKeyLevel)
	filtered := hostKeyPool[:0]
	for _, opt := range hostKeyPool {
		if !strings.Contains(opt.ID, "-cert-") {
			filtered = append(filtered, opt)
		}
	}

	return SupportedSSHAlgorithms{
		KeyExchanges: pool(compatible.KeyExchanges, kexLevel),
		Ciphers:      pool(compatible.Ciphers, cipherLevel),
		MACs:         pool(compatible.MACs, macLevel),
		HostKeys:     filtered,
		Compatible:   presetFromMode(SSHAlgoModeCompatible, compatible),
		Secure:       presetFromMode(SSHAlgoModeSecure, secure),
	}
}

// algorithmMismatchMarkers are markers within x/crypto's error strings for a
// failed negotiation. The library returns plain fmt.Errorf values with no
// typed sentinel, so the message is the only stable handle.
var algorithmMismatchMarkers = []string{
	"no common algorithm",
}

// wrapSSHAlgorithmError annotates a failed negotiation with mode-specific
// guidance so the raw library error does not dead-end the user.
func wrapSSHAlgorithmError(mode string, err error) error {
	msg := err.Error()
	for _, marker := range algorithmMismatchMarkers {
		if strings.Contains(msg, marker) {
			hint := "the server supports none of the offered algorithms; switching the connection's SSH algorithms to custom and enabling older algorithms may help"
			if mode == SSHAlgoModeCustom {
				hint = "the server supports none of the algorithms in this connection's custom SSH algorithm lists; review the configured lists"
			}
			return fmt.Errorf("%w (%s)", err, hint)
		}
	}
	return err
}
