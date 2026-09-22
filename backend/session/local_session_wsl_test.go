//go:build windows
// +build windows

package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildWSLStartArgs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		distro    string
		cwd       string
		startArgs []string
		want      []string
	}{
		{
			name:      "no cwd starts in linux home",
			distro:    "Ubuntu-24.04",
			cwd:       "",
			startArgs: nil,
			want:      []string{"-d", "Ubuntu-24.04", "--cd", "~"},
		},
		{
			name:      "explicit cwd skips home",
			distro:    "Ubuntu-24.04",
			cwd:       `C:\Users\me\project`,
			startArgs: nil,
			want:      []string{"-d", "Ubuntu-24.04"},
		},
		{
			name:      "shell integration args appended after cd",
			distro:    "Ubuntu-24.04",
			cwd:       "",
			startArgs: []string{"-e", "bash", "--rcfile", "/tmp/uniterm-abc123"},
			want:      []string{"-d", "Ubuntu-24.04", "--cd", "~", "-e", "bash", "--rcfile", "/tmp/uniterm-abc123"},
		},
		{
			name:      "explicit cwd with integration",
			distro:    "Debian",
			cwd:       `D:\work`,
			startArgs: []string{"-e", "env", "ZDOTDIR=/tmp/uniterm-xyz", "zsh"},
			want:      []string{"-d", "Debian", "-e", "env", "ZDOTDIR=/tmp/uniterm-xyz", "zsh"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildWSLStartArgs(tc.distro, tc.cwd, tc.startArgs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("buildWSLStartArgs(%q, %q, %v) = %v, want %v", tc.distro, tc.cwd, tc.startArgs, got, tc.want)
			}
		})
	}
}

// Distro names ride `wsl.exe -d <name>` argv, which wsl.exe re-quotes inside
// double quotes before handing it to the login shell — a name carrying shell
// metacharacters there would break out of that re-parse. parseWSLPath applies
// the same gate to wsl:// URIs, so both consumers are covered by one check.
func TestValidWSLDistroName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Ubuntu-22.04", true},
		{"AlmaLinux 9", true},
		{`Ubuntu" & calc`, false},
		{"x$(y)", false},
		{`a\b`, false},
		{"a`b", false},
		{"a\r\nb", false},
		{"", false},
	}
	for _, c := range cases {
		if got := validWSLDistroName(c.name); got != c.want {
			t.Errorf("validWSLDistroName(%q) = %v, want %v", c.name, got, c.want)
		}
		if _, ok := parseWSLPath("wsl://" + c.name); ok != c.want {
			t.Errorf("parseWSLPath(%q) ok = %v, want %v", "wsl://"+c.name, ok, c.want)
		}
	}
	if distro, ok := parseWSLPath("wsl://Ubuntu-22.04"); !ok || distro != "Ubuntu-22.04" {
		t.Errorf("parseWSLPath(wsl://Ubuntu-22.04) = %q, %v, want Ubuntu-22.04, true", distro, ok)
	}
	if _, ok := parseWSLPath(`C:\Windows\System32\bash.exe`); ok {
		t.Error("parseWSLPath(non-wsl path) should report ok=false")
	}
}

// splitCommandLine parses a command line into tokens per the documented
// CommandLineToArgvW escaping rules: a run of 2n backslashes before a quote
// yields n backslashes with the quote delimiting, 2n+1 yields n backslashes
// plus a literal quote, any other backslash is literal; a quote toggles
// quoting, an unquoted space/tab ends the token.
func splitCommandLine(s string) []string {
	var args []string
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		var tok strings.Builder
		inQuotes := false
		for i < len(s) {
			c := s[i]
			if c == '\\' {
				n := 0
				for i < len(s) && s[i] == '\\' {
					n++
					i++
				}
				if i < len(s) && s[i] == '"' {
					tok.WriteString(strings.Repeat(`\`, n/2))
					if n%2 == 1 {
						tok.WriteByte('"')
						i++
					}
					// even run: the quote left in place delimits below
				} else {
					tok.WriteString(strings.Repeat(`\`, n))
				}
				continue
			}
			if c == '"' {
				inQuotes = !inQuotes
				i++
				continue
			}
			if !inQuotes && (c == ' ' || c == '\t') {
				break
			}
			tok.WriteByte(c)
			i++
		}
		args = append(args, tok.String())
	}
	return args
}

// The ConPTY/elevated command line is re-parsed token-by-token, so a shell
// path containing a double quote must stay ONE first token — otherwise a
// value like `cmd.exe" /c calc & "` breaks out of the quoted token and
// injects arguments (H1). Benign paths keep the exact pre-fix shape.
func TestBuildCommandLineQuoteBreakout(t *testing.T) {
	cases := []struct {
		name  string
		shell string
		want  string   // exact command line
		toks  []string // tokens it must re-parse to
	}{
		{
			name:  "plain path",
			shell: `C:\pwsh.exe`,
			want:  `"C:\pwsh.exe"`,
			toks:  []string{`C:\pwsh.exe`},
		},
		{
			name:  "path with spaces keeps login flags",
			shell: `C:\Program Files\Git\bin\bash.exe`,
			want:  `"C:\Program Files\Git\bin\bash.exe" --login -i`,
			toks:  []string{`C:\Program Files\Git\bin\bash.exe`, `--login`, `-i`},
		},
		{
			name:  "system32 bash has no flags",
			shell: `C:\Windows\System32\bash.exe`,
			want:  `"C:\Windows\System32\bash.exe"`,
			toks:  []string{`C:\Windows\System32\bash.exe`},
		},
		{
			name:  "cmd exe keeps /k",
			shell: `C:\Windows\System32\cmd.exe`,
			want:  `"C:\Windows\System32\cmd.exe" /k`,
			toks:  []string{`C:\Windows\System32\cmd.exe`, `/k`},
		},
		{
			name:  "quote breakout stays one token",
			shell: `cmd.exe" /c calc & "`,
			want:  `"cmd.exe\" /c calc & \"" /k`,
			toks:  []string{`cmd.exe" /c calc & "`, `/k`},
		},
		{
			name:  "quote in mid path stays one token",
			shell: `C:\x"y\pwsh.exe`,
			want:  `"C:\x\"y\pwsh.exe"`,
			toks:  []string{`C:\x"y\pwsh.exe`},
		},
		{
			name:  "trailing backslash doubled before closing quote",
			shell: `C:\tools\pwsh.exe\`,
			want:  `"C:\tools\pwsh.exe\\"`,
			toks:  []string{`C:\tools\pwsh.exe\`},
		},
	}
	for _, c := range cases {
		got := buildCommandLine(c.shell)
		if got != c.want {
			t.Errorf("%s: buildCommandLine(%q) = %q, want %q", c.name, c.shell, got, c.want)
			continue
		}
		if toks := splitCommandLine(got); !reflect.DeepEqual(toks, c.toks) {
			t.Errorf("%s: tokens of %q = %q, want %q", c.name, got, toks, c.toks)
		}
	}
}
