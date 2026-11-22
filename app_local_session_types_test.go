package main

import "testing"

// Server mode must refuse every session type that reaches the host's shell or
// filesystem; wsl-file reaches the distro through wsl.exe just like local
// reaches the shell.
func TestLocalSessionTypesDisabledInServerMode(t *testing.T) {
	for _, typ := range []string{"local", "local-file", "wsl-file"} {
		if !isLocalSessionType(typ) {
			t.Errorf("isLocalSessionType(%q) = false, want true", typ)
		}
	}
	for _, typ := range []string{"ssh", "sftp", "ftp", "wsl", "database", "container-file", "rdp", "vnc"} {
		if isLocalSessionType(typ) {
			t.Errorf("isLocalSessionType(%q) = true, want false", typ)
		}
	}
}
