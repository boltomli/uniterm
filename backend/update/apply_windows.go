//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ys-ll/uniterm/backend/log"
)

func (m *Manager) applyPlatform(pend *PendingUpdate, onProgress func(Progress)) error {
	switch pend.Kind {
	case "installer":
		return launchInstallerUpdate(pend, onProgress)
	case "portable":
		if err := applyBinary(pend.NewBinary, onProgress); err != nil {
			return err
		}
		return copyPlugins(pend.StageDir)
	}
	return fmt.Errorf("unsupported payload kind %q", pend.Kind)
}

// copyPlugins merges the extracted plugins/ directory (VcXsrv) next to the
// running executable. New files are added; existing ones are overwritten.
func copyPlugins(stage string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	src := filepath.Join(stage, "plugins")
	if _, err := os.Stat(src); err != nil {
		// No plugins payload (older release) — not fatal.
		return nil
	}
	dst := filepath.Join(filepath.Dir(exe), "plugins")
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0755)
	})
}

// launchInstallerUpdate writes a detached updater script that waits for the
// running app to exit, runs the new NSIS installer silently, relaunches the
// app, and finally removes the staging dir and itself. The app must quit right
// after Apply() returns.
//
// Two NSIS quirks are handled here:
//   - /D=<dir> (last parameter, unquoted) forces the installer to target the
//     directory the app actually runs from, since project.nsi has a fixed
//     default InstallDir and no InstallDirRegKey.
//   - Silent installs skip the MUI finish page, so MUI_FINISHPAGE_RUN never
//     fires — the script relaunches the app explicitly.
//
// The script body is built by installerScript: it waits for this exact PID
// (polling the uniTerm.exe image name also matches a second copy or another
// user's session and waits forever), escapes every interpolated path for its
// quoting context, and re-hashes the installer immediately before starting it
// — the payload sits in a user-writable staging dir while the app waits to
// exit, so the download-time check alone leaves a swap window open.
func launchInstallerUpdate(pend *PendingUpdate, onProgress func(Progress)) error {
	if onProgress != nil {
		onProgress(Progress{Phase: "applying", Total: -1, Message: "installer-launched"})
	}

	appExe, err := os.Executable()
	if err != nil {
		return err
	}

	script := filepath.Join(pend.StageDir, "updater.cmd")
	content := installerScript(pend, appExe, os.Getpid())
	if err := os.WriteFile(script, []byte(content), 0644); err != nil {
		return err
	}

	cmd := exec.Command("cmd.exe", "/C", "start", "", "cmd.exe", "/C", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008 | 0x00000200} // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	if err := cmd.Start(); err != nil {
		return err
	}
	log.Writef("[update] installer updater launched: %s", script)
	return nil
}

// installerScript renders the detached updater.cmd for a staged Windows
// installer: wait for pid to exit, re-verify the payload, run it silently,
// relaunch appExe and clean up the staging dir. Every path is escaped for the
// context it is interpolated into (installerScript callers only ever pass
// absolute paths).
func installerScript(pend *PendingUpdate, appExe string, pid int) string {
	return fmt.Sprintf(`@echo off
rem wait for this process to exit
:waitpid
tasklist /FI "PID eq %[1]d" /NH 2>nul | find /I "%[1]d" >nul
if not errorlevel 1 (
  timeout /t 1 /nobreak >nul
  goto waitpid
)
rem re-hash the staged installer before starting it
certutil -hashfile "%[2]s" SHA256 | findstr /I /X /C:"%[3]s" >nul
if errorlevel 1 (
  echo [updater] installer sha256 mismatch, aborting
  rmdir /s /q "%[4]s"
  del "%%~f0"
  exit /b 1
)
start "" /wait "%[2]s" /S /D=%[5]s
start "" "%[6]s"
rmdir /s /q "%[4]s"
del "%%~f0"
`,
		pid,
		escapeBatchPath(pend.FilePath),
		pend.Asset.SHA256,
		escapeBatchPath(pend.StageDir),
		escapeBatchDArg(filepath.Dir(appExe)),
		escapeBatchPath(appExe),
	)
}

// batchMetacharReplacer escapes the cmd.exe metacharacters in a single pass,
// so each input character is replaced exactly once: the caret produced for &,
// <, > or | is never itself re-escaped, and a literal caret becomes ^^.
var batchMetacharReplacer = strings.NewReplacer(
	"%", "%%",
	"^", "^^",
	"&", "^&",
	"<", "^<",
	">", "^>",
	"|", "^|",
)

// escapeBatchDArg escapes a path used as NSIS's unquoted /D=<dir> argument,
// the one position in the script that cmd.exe parses as ordinary command
// text. Without this an install dir containing & splits the line (and a
// crafted dir could inject commands), and a % starts a variable expansion.
func escapeBatchDArg(path string) string {
	return batchMetacharReplacer.Replace(path)
}

// escapeBatchPath escapes a path the script keeps inside double quotes:
// there cmd.exe treats & < > | ^ as literal text — caret-escaping them would
// put a caret into the path — while a percent sign still expands, so only it
// must be doubled.
func escapeBatchPath(path string) string {
	return strings.ReplaceAll(path, "%", "%%")
}
