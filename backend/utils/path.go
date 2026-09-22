package utils

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHomePath resolves a leading "~" to the user's home directory so paths
// like "~/.ssh/id_ed25519" (as written in OpenSSH config IdentityFile lines
// and other third-party sources) can be handed to os.ReadFile as-is. Paths
// without a "~" prefix are returned unchanged.
func ExpandHomePath(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
