package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseShellHistoryPlainZsh(t *testing.T) {
	// The macOS default: EXTENDED_HISTORY off, one command per line.
	raw := "ls\ncd ~/dev\ngit status\n"
	got := parseShellHistory(raw, 10)
	want := []string{"git status", "cd ~/dev", "ls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistoryExtendedZsh(t *testing.T) {
	// oh-my-zsh and friends turn EXTENDED_HISTORY on.
	raw := ": 1730000000:0;claude\n: 1730000005:12;git push\n"
	got := parseShellHistory(raw, 10)
	want := []string{"git push", "claude"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistoryTrailingNewlineContinuation(t *testing.T) {
	// Seen in the wild: a pasted command keeps its trailing newline, which zsh
	// stores as a backslash plus an empty line. The command is still usable.
	raw := "ssh app@10.0.0.1\\\n\nls\n"
	got := parseShellHistory(raw, 10)
	want := []string{"ls", "ssh app@10.0.0.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistorySkipsMultiLineCommands(t *testing.T) {
	// A loop cannot be offered as a single-line completion.
	raw := "for i in 1 2 3; do\\\necho $i\\\ndone\nls\n"
	got := parseShellHistory(raw, 10)
	want := []string{"ls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistorySkipsBashTimestamps(t *testing.T) {
	raw := "#1730000000\nls\n#1730000005\ngit status\n"
	got := parseShellHistory(raw, 10)
	want := []string{"git status", "ls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistoryDedupesKeepingNewest(t *testing.T) {
	raw := "ls\ngit status\nls\n"
	got := parseShellHistory(raw, 10)
	want := []string{"ls", "git status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistoryRespectsLimit(t *testing.T) {
	raw := "a\nb\nc\nd\n"
	got := parseShellHistory(raw, 2)
	want := []string{"d", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseShellHistoryDropsInvalidUTF8(t *testing.T) {
	raw := "ls\n\xff\xfe bad\ngit status\n"
	got := parseShellHistory(raw, 10)
	want := []string{"git status", "bad", "ls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestShellHistoryPathPrefersHistfile(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom_history")
	t.Setenv("HISTFILE", want)
	if got := shellHistoryPath(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestShellHistoryPathFollowsShell(t *testing.T) {
	t.Setenv("HISTFILE", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("SHELL", "/bin/zsh")
	if got, want := shellHistoryPath(), filepath.Join(home, ".zsh_history"); got != want {
		t.Fatalf("zsh: got %q, want %q", got, want)
	}
	t.Setenv("SHELL", "/bin/bash")
	if got, want := shellHistoryPath(), filepath.Join(home, ".bash_history"); got != want {
		t.Fatalf("bash: got %q, want %q", got, want)
	}
}
