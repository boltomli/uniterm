//go:build windows

package update

import (
	"strings"
	"testing"
)

// TestEscapeBatchDArg covers NSIS's unquoted /D=<dir> argument: cmd.exe parses
// it as ordinary command text, so the batch escape (%% for a percent sign) and
// caret escaping are both required or an install path containing & or % breaks
// the script — or injects extra commands.
func TestEscapeBatchDArg(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain path", in: `C:\Program Files\uniTerm`, want: `C:\Program Files\uniTerm`},
		{name: "percent", in: `C:\Users\bob%2\AppData`, want: `C:\Users\bob%%2\AppData`},
		{name: "ampersand", in: `C:\A & B\uniTerm`, want: `C:\A ^& B\uniTerm`},
		{name: "caret", in: `C:\A^B\uniTerm`, want: `C:\A^^B\uniTerm`},
		{name: "pipe and redirection", in: `C:\a|b<c>d`, want: `C:\a^|b^<c^>d`},
		{name: "combined", in: `C:\x%&^\`, want: `C:\x%%^&^^\`},
		{name: "empty", in: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeBatchDArg(tc.in); got != tc.want {
				t.Fatalf("escapeBatchDArg(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEscapeBatchPath covers paths that stay inside double quotes: there
// cmd.exe treats & < > | ^ as literal text (a caret would become part of the
// path), while a percent sign still starts a variable expansion.
func TestEscapeBatchPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain path", in: `C:\Program Files\uniTerm`, want: `C:\Program Files\uniTerm`},
		{name: "percent", in: `C:\A%TEMP%B\uniTerm.exe`, want: `C:\A%%TEMP%%B\uniTerm.exe`},
		{name: "metacharacters stay literal", in: `C:\A & B^C\uniTerm.exe`, want: `C:\A & B^C\uniTerm.exe`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeBatchPath(tc.in); got != tc.want {
				t.Fatalf("escapeBatchPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// installerFixture builds the staged installer the detached script operates on.
func installerFixture() *PendingUpdate {
	return &PendingUpdate{
		Asset: UpdateAsset{
			Name:   "uniterm-windows-amd64-installer-v1.9.9.exe",
			SHA256: "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		},
		Kind:     "installer",
		StageDir: `C:\Users\bob\AppData\Local\uniTerm\updates\stage-1234`,
		FilePath: `C:\Users\bob\AppData\Local\uniTerm\updates\stage-1234\uniterm-windows-amd64-installer-v1.9.9.exe`,
	}
}

// TestInstallerScriptWaitsForOwnPID: the wait loop must poll this process's
// PID, not the image name — polling uniTerm.exe also matches a second copy or
// another user's session and waits forever.
func TestInstallerScriptWaitsForOwnPID(t *testing.T) {
	const pid = 4321
	script := installerScript(installerFixture(), `C:\Program Files\uniTerm\uniTerm.exe`, pid)

	if !strings.Contains(script, `tasklist /FI "PID eq 4321"`) {
		t.Errorf("script must filter tasklist by this process's PID:\n%s", script)
	}
	if !strings.Contains(script, `find /I "4321"`) {
		t.Errorf("script must test the PID line, not the image name:\n%s", script)
	}
	if strings.Contains(script, "IMAGENAME") {
		t.Errorf("script must not poll by image name any more:\n%s", script)
	}
	if !strings.Contains(script, "goto waitpid") {
		t.Errorf("script must loop until this PID exits:\n%s", script)
	}
}

// TestInstallerScriptRehashesBeforeLaunch: the installer sits in a
// user-writable staging dir while the app waits to exit, so the script must
// re-hash it immediately before starting it and abort unless it still matches
// the SHA256 verified at download time.
func TestInstallerScriptRehashesBeforeLaunch(t *testing.T) {
	pend := installerFixture()
	script := installerScript(pend, `C:\Program Files\uniTerm\uniTerm.exe`, 4321)

	hashCheck := strings.Index(script, "certutil -hashfile")
	if hashCheck < 0 {
		t.Fatalf("script must re-hash the installer before launch:\n%s", script)
	}
	launch := strings.Index(script, `start "" /wait`)
	if launch < 0 {
		t.Fatalf("script must launch the installer:\n%s", script)
	}
	if hashCheck > launch {
		t.Errorf("hash check must come before the installer launch:\n%s", script)
	}
	if !strings.Contains(script, `findstr /I /X /C:"`+pend.Asset.SHA256+`"`) {
		t.Errorf("hash check must compare against the verified SHA256 %s:\n%s", pend.Asset.SHA256, script)
	}
	if !strings.Contains(script, "installer sha256 mismatch") {
		t.Errorf("a swapped installer must abort the update:\n%s", script)
	}
}

// TestInstallerScriptEscapesPaths: every path interpolated into the script is
// escaped for its quoting context, so an install dir or staging dir containing
// % & or ^ cannot break out of the intended command.
func TestInstallerScriptEscapesPaths(t *testing.T) {
	pend := &PendingUpdate{
		Asset: UpdateAsset{
			Name:   "uniterm-windows-amd64-installer-v1.9.9.exe",
			SHA256: "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		},
		Kind:     "installer",
		StageDir: `C:\Users\a&b\AppData\Local\uniTerm\updates\stage-1234`,
		FilePath: `C:\Users\a&b\AppData\Local\uniTerm\updates\stage-1234\uniterm-%ARCH%-installer.exe`,
	}
	appExe := `C:\Program Files\uniTerm & Co^\uniTerm.exe`
	script := installerScript(pend, appExe, 4321)

	wantDArg := ` /S /D=C:\Program Files\uniTerm ^& Co^^` + "\n"
	if !strings.Contains(script, wantDArg) {
		t.Errorf("install dir must be escaped for the unquoted /D= argument, want %q in:\n%s", wantDArg, script)
	}
	if !strings.Contains(script, `start "" /wait "C:\Users\a&b\AppData\Local\uniTerm\updates\stage-1234\uniterm-%%ARCH%%-installer.exe"`) {
		t.Errorf("installer path must stay quoted with %% doubled:\n%s", script)
	}
	if !strings.Contains(script, `certutil -hashfile "C:\Users\a&b\AppData\Local\uniTerm\updates\stage-1234\uniterm-%%ARCH%%-installer.exe" SHA256`) {
		t.Errorf("hash-check path must stay quoted with %% doubled:\n%s", script)
	}
	if !strings.Contains(script, `start "" "C:\Program Files\uniTerm & Co^\uniTerm.exe"`) {
		t.Errorf("relaunch path must stay quoted and unescaped inside quotes:\n%s", script)
	}
	if !strings.Contains(script, `rmdir /s /q "C:\Users\a&b\AppData\Local\uniTerm\updates\stage-1234"`) {
		t.Errorf("stage dir must stay quoted:\n%s", script)
	}
}

// TestInstallerScriptSelfCleanup: both exit paths must end with a plain
// %~f0 self-delete. A %% that never renders would make cmd.exe look for a
// file literally named "%~f0" and leave the updater script behind in the
// staging dir.
func TestInstallerScriptSelfCleanup(t *testing.T) {
	script := installerScript(installerFixture(), `C:\Program Files\uniTerm\uniTerm.exe`, 4321)

	if got := strings.Count(script, `del "%~f0"`); got != 2 {
		t.Errorf("both exit paths must self-delete with %%~f0, found %d:\n%s", got, script)
	}
	if strings.Contains(script, "%%~f0") {
		t.Errorf("an unrendered %%~f0 must not leak into the batch file:\n%s", script)
	}
}
