package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHomePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no user home dir available")
	}
	rest := ".ssh/id_ed25519"
	cases := []struct {
		in   string
		want string
	}{
		{"~/", home},
		{"~", home},
		{"~/" + rest, filepath.Join(home, rest)},
		{`~\` + rest, filepath.Join(home, rest)},
		{"/abs/" + rest, "/abs/" + rest},
		{`C:\keys\id_ed25519`, `C:\keys\id_ed25519`},
		{"relative/" + rest, "relative/" + rest},
		{"", ""},
		{"~user/" + rest, "~user/" + rest}, // "~name" is a different user's home, left untouched
	}
	for _, c := range cases {
		if got := ExpandHomePath(c.in); got != c.want {
			t.Errorf("ExpandHomePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
