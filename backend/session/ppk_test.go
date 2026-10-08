package session

import (
	"os"
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
