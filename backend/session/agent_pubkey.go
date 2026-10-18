package session

import (
	"bytes"
	"fmt"
	"os"

	"github.com/ys-ll/uniterm/backend/utils"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// publicKeyFromFile reports whether KeyPath of a "key" connection names a
// public key (e.g. "IdentityFile ~/.ssh/host.pub" imported from an OpenSSH
// config) rather than a private one. Like OpenSSH, such a key is then used
// through the local agent, which holds the private half.
func publicKeyFromFile(config ConnectionConfig) (ssh.PublicKey, bool) {
	if config.AuthType != "key" || config.KeyPath == "" {
		return nil, false
	}
	data, err := os.ReadFile(utils.ExpandHomePath(config.KeyPath))
	if err != nil {
		return nil, false
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return nil, false
	}
	return pub, true
}

// agentPublicKeyAuthMethod offers only the agent key matching pub. Offering
// every agent key (as authType "agent" does) trips the server's MaxAuthTries
// once the agent holds more than a handful of keys.
func agentPublicKeyAuthMethod(pub ssh.PublicKey, keyPath string) (ssh.AuthMethod, func(), error) {
	client, cleanup, err := localAgentClient()
	if err != nil {
		return nil, cleanup, err
	}
	signer, err := agentSignerFor(client, pub)
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("%s: %w", keyPath, err)
	}
	return ssh.PublicKeys(signer), cleanup, nil
}

func agentSignerFor(client agent.Agent, pub ssh.PublicKey) (ssh.Signer, error) {
	signers, err := client.Signers()
	if err != nil {
		return nil, fmt.Errorf("list SSH agent keys: %w", err)
	}
	want := pub.Marshal()
	for _, s := range signers {
		if bytes.Equal(s.PublicKey().Marshal(), want) {
			return s, nil
		}
	}
	return nil, fmt.Errorf("key %s is not loaded in the SSH agent", ssh.FingerprintSHA256(pub))
}
