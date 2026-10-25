//go:build windows

package netscan

import (
	"os/exec"
	"syscall"
)

// arpCommand returns the ARP-table command with the console window hidden, so
// running it from the GUI app doesn't flash a console window.
func arpCommand() *exec.Cmd {
	cmd := exec.Command("arp", "-a")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return cmd
}
