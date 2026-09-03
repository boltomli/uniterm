package session

import (
	"os"

	"github.com/ys-ll/uniterm/backend/utils"
	"golang.org/x/crypto/ssh"
)

func makeSSHAuthMethods(config ConnectionConfig, kbCallback ssh.KeyboardInteractiveChallenge) []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	switch config.AuthType {
	case "password":
		methods = append(methods, ssh.Password(config.Password))
	case "key", "keyText":
		if signer, ok := parseAuthKeySigner(config); ok {
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}

	// Keyboard-interactive as fallback for password-less or failed-password scenarios.
	if kbCallback != nil {
		methods = append(methods, ssh.KeyboardInteractive(kbCallback))
	}

	return methods
}

// buildAuthMethods returns the auth methods used by non-interactive SIP sessions
// (SFTP, server monitor) that dial via dialSSHTCP. It shares parsePrivateKeyFile
// with the interactive SSH session so an encrypted private key + its passphrase
// (config.Password) authenticates identically everywhere — the "秘钥加密码" case
// from issue #647. Unlike makeSSHAuthMethods it has no keyboard-interactive
// fallback (unattended), uses the passphrase as the authentication signal for
// key files, and treats "agent" as password for backward compatibility.
func buildAuthMethods(config ConnectionConfig) ([]ssh.AuthMethod, error) {
	switch config.AuthType {
	case "key", "keyText":
		signer, ok := parseAuthKeySigner(config)
		if !ok {
			return nil, utils.UserErr("ssh_key_unavailable", keySourceLabel(config))
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default: // "", "password", "agent" and any unknown type fall back to password
		return []ssh.AuthMethod{ssh.Password(config.Password)}, nil
	}
}

// parseAuthKeySigner returns the SSH signer for an authType of "key" or
// "keyText". "keyText" parses the inline PEM text from KeyContent directly;
// "key" reads the private-key file at KeyPath. The passphrase (config.Password)
// decrypts an encrypted key in both cases. Returns (nil, false) on any error;
// the caller falls back to other auth methods so the SSH handshake surfaces a
// meaningful error to the user.
func parseAuthKeySigner(config ConnectionConfig) (ssh.Signer, bool) {
	if config.AuthType == "keyText" {
		return parsePrivateKey([]byte(config.KeyContent), config.Password)
	}
	return parsePrivateKeyFile(config.KeyPath, config.Password)
}

// keySourceLabel names the key source for error messages — the file path for
// "key", or a friendly label for inline "keyText" content.
func keySourceLabel(config ConnectionConfig) string {
	if config.AuthType == "keyText" {
		return "inline private key text"
	}
	return config.KeyPath
}

// parsePrivateKeyFile reads the private key at path and parses it via
// parsePrivateKey. Returns (nil, false) on any error.
func parsePrivateKeyFile(path, passphrase string) (ssh.Signer, bool) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return parsePrivateKey(key, passphrase)
}

// parsePrivateKey parses a private key from raw bytes, using passphrase when
// the key is encrypted. Returns (nil, false) on any error.
func parsePrivateKey(key []byte, passphrase string) (ssh.Signer, bool) {
	if passphrase != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase(key, []byte(passphrase))
		if err != nil {
			return nil, false
		}
		return signer, true
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, false
	}
	return signer, true
}
