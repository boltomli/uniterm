package sync

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// go-git's file transport (used for local / file:// remotes — the transport
// the tests exercise) spawns git-upload-pack / git-receive-pack and looks
// only at PATH and then `git --exec-path`. Some git distributions keep the
// plumbing helpers outside the exec dir (MinGit-style installs put them in
// cmd\, scoop layouts in bin\): the git CLI still finds them, but go-git
// then fails every file-remote operation with "executable file not found in
// %PATH%". Prepend the first directory that holds both helpers to PATH so
// the suite runs wherever the git CLI works. No-op on healthy installs.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git-receive-pack"); err != nil {
		if dir := findGitHelperDir(); dir != "" {
			_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		} else {
			fmt.Fprintln(os.Stderr, "sync tests: git-receive-pack/git-upload-pack not found; install git or put its helper directory on PATH")
		}
	}
	os.Exit(m.Run())
}

// findGitHelperDir probes the git install for the plumbing helpers: the
// exec dir first, then the sibling bin\ and cmd\ directories of the exec
// dir and of the git executable itself (the layouts git's own fallbacks
// cover).
func findGitHelperDir() string {
	var candidates []string
	if out, err := exec.Command("git", "--exec-path").Output(); err == nil {
		execPath := strings.TrimSpace(string(out))
		// <root>/<ucrt64|mingw64>/libexec/git-core → helpers in
		// <root>/<...>/bin and <root>/cmd.
		candidates = append(candidates,
			execPath,
			filepath.Join(execPath, "..", "..", "bin"),
			filepath.Join(execPath, "..", "..", "..", "cmd"),
		)
	}
	if gitExe, err := exec.LookPath("git"); err == nil {
		gitDir := filepath.Dir(gitExe)
		candidates = append(candidates,
			gitDir,
			filepath.Join(gitDir, "..", "libexec", "git-core"),
			filepath.Join(gitDir, "..", "..", "cmd"),
		)
	}
	for _, dir := range candidates {
		if hasGitHelpers(dir) {
			return dir
		}
	}
	return ""
}

func hasGitHelpers(dir string) bool {
	for _, name := range []string{"git-receive-pack", "git-upload-pack"} {
		if _, err := exec.LookPath(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}
