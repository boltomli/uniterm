//go:build !windows

package netscan

import "os/exec"

func arpCommand() *exec.Cmd {
	return exec.Command("arp", "-a")
}
