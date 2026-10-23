//go:build linux

package session

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Use actual Linux procfs and controlling terminals. The ordinary child
// stands in for the SFTP server; mapping self to that child's stat gives the
// same process relationship as a subsystem and PTY sharing an SSH transport.
type nativeCwdFS struct{ subsystemPID int }

func (f nativeCwdFS) ReadFile(name string) ([]byte, error) {
	if name == "/proc/self/stat" {
		name = fmt.Sprintf("/proc/%d/stat", f.subsystemPID)
	}
	// The Go test parent is multithreaded; its children need not belong to
	// the leader thread. Exercise the /proc enumeration fallback here.
	if strings.HasSuffix(name, "/children") {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(name)
}
func (f nativeCwdFS) ReadDir(name string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(name)
	if err != nil {
		return nil, err
	}
	var infos []os.FileInfo
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil {
			infos = append(infos, info)
		}
	}
	return infos, nil
}
func (f nativeCwdFS) ReadLink(name string) (string, error) { return os.Readlink(name) }

func TestCwdLinuxProcfs(t *testing.T) {
	subsystem := exec.Command("/bin/sleep", "30")
	if err := subsystem.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subsystem.Process.Kill(); _ = subsystem.Wait() }()
	startTerminal := func() (*exec.Cmd, *os.File) {
		cmd := exec.Command("/bin/sh")
		terminal, err := pty.Start(cmd)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = terminal.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd, terminal
	}
	shell, terminal := startTerminal()
	fs := nativeCwdFS{subsystem.Process.Pid}
	assigned := make(map[cwdProcessID]bool)
	p, err := findCwdTerminal(fs, assigned)
	if err != nil || p.pid != shell.Process.Pid {
		t.Fatalf("shell identity=%+v err=%v", p, err)
	}
	assigned[p.cwdProcessID] = true
	target := t.TempDir()
	if _, err := terminal.Write([]byte("cd '" + strings.ReplaceAll(target, "'", "'\\''") + "'\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		cwd, err := readTerminalCwd(fs, p)
		if err != nil {
			t.Fatal(err)
		}
		if cwd == target {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cwd=%q want=%q", cwd, target)
		}
		time.Sleep(10 * time.Millisecond)
	}
	clone, _ := startTerminal()
	cp, err := findCwdTerminal(fs, assigned)
	if err != nil || cp.pid != clone.Process.Pid {
		t.Fatalf("clone identity=%+v err=%v", cp, err)
	}
	_ = shell.Process.Kill()
	_ = shell.Wait()
	if _, err := readTerminalCwd(fs, p); err == nil {
		t.Fatal("accepted exited terminal")
	}
	if _, err := readTerminalCwd(fs, cp); err != nil {
		t.Fatalf("clone lost cwd after source exit: %v", err)
	}
}
