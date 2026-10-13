// Command sign signs the update checksum manifest (checksums.txt) with the
// release signing key and writes the detached signature uploaded as
// checksums.txt.sig. Clients verify it against the public key embedded in
// backend/update/signature.go before trusting any hash, so an unsigned or
// wrongly signed release is never installed (fail closed).
//
// Usage in CI:
//
//	printf '%s' "$UPDATE_SIGNING_PRIVATE_KEY" > key.hex
//	go run ./build/signcmd -key key.hex -in checksums.txt -out checksums.txt.sig
//
// -key points at the hex encoding of the 64-byte raw ed25519 private key:
// the GitHub Actions secret UPDATE_SIGNING_PRIVATE_KEY, or locally the
// git-ignored build/release-signing.key. The output is the base64 encoding
// of the raw 64-byte signature — exactly what
// update.VerifyChecksumsSignature consumes.
//
// The key decoding and the signing itself live in backend/update
// (ParseSigningKeyHex, SignChecksums) so the release pipeline and the client
// verifier can never drift apart; both are covered by
// backend/update/signature_test.go.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ys-ll/uniterm/backend/update"
)

func main() {
	keyPath := flag.String("key", "", "path to the hex-encoded 64-byte ed25519 private key")
	inPath := flag.String("in", "", "file to sign (checksums.txt)")
	outPath := flag.String("out", "", "path of the base64 signature to write (checksums.txt.sig)")
	flag.Parse()
	if *keyPath == "" || *inPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: signcmd -key <keyfile> -in <file> -out <sigfile>")
		os.Exit(2)
	}
	if err := run(*keyPath, *inPath, *outPath); err != nil {
		fmt.Fprintf(os.Stderr, "signcmd: %v\n", err)
		os.Exit(1)
	}
}

func run(keyPath, inPath, outPath string) error {
	keyHex, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read key: %w", err)
	}
	key, err := update.ParseSigningKeyHex(string(keyHex))
	if err != nil {
		return err
	}
	message, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", inPath, err)
	}
	signature, err := update.SignChecksums(message, key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, []byte(signature), 0o644); err != nil {
		return fmt.Errorf("write signature: %w", err)
	}
	return nil
}
