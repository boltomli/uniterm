package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCompletionScannerEveryReadBoundary(t *testing.T) {
	const token = "fg-test"
	prefix := "__UT_" + token + ";"
	wire := "中文 output\r\n" + prefix + "23__$ next prompt"
	for split := 0; split <= len(wire); split++ {
		sc := completionScanner{prefix: []byte(prefix)}
		a, code, done := sc.feed([]byte(wire[:split]))
		if !done {
			b, status, finished := sc.feed([]byte(wire[split:]))
			a = append(a, b...)
			code, done = status, finished
		}
		if string(a) != "中文 output\r\n" || !done || code != 23 {
			t.Fatalf("split %d: %q %d %v", split, a, code, done)
		}
	}
}

func TestCompletionCommandsInRealShells(t *testing.T) {
	for _, shell := range []string{"bash", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			path, err := exec.LookPath(shell)
			if runtime.GOOS == "windows" && shell == "bash" {
				gitBash := `C:\Program Files\Git\bin\bash.exe`
				if _, statErr := os.Stat(gitBash); statErr == nil {
					path, err = gitBash, nil
				}
			}
			if err != nil {
				t.Skip("shell unavailable")
			}
			const token = "fg-test-real-shell"
			command := "printf '%s\\n' \"a'b\"; sleep 0.15; printf 'last line\\n'; (exit 7)"
			if shell == "powershell" {
				command = `Write-Output "a'b"; Start-Sleep -Milliseconds 150; Get-Item .; Write-Output 'last line'; cmd /c exit 7`
			}
			wrapped, ok := CompletionCommand(command, shell, token)
			if !ok {
				t.Fatal("supported shell rejected")
			}
			var cmd *exec.Cmd
			if shell == "bash" {
				cmd = exec.Command(path, "--noprofile", "--norc", "-c", wrapped)
			} else {
				cmd = exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", wrapped)
			}
			started := time.Now()
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("wrapper failed: %v %q", err, out)
			}
			sc := completionScanner{prefix: []byte("__UT_" + token + ";")}
			cleaned, code, done := sc.feed(out)
			if !done || code != 7 || !strings.Contains(string(cleaned), "a'b") || !strings.Contains(string(cleaned), "last line") {
				t.Fatalf("missing output/exit: %q code=%d done=%v", out, code, done)
			}
			if time.Since(started) < 150*time.Millisecond {
				t.Fatal("returned before command finished")
			}
		})
	}
}

func TestTimeoutKeepsPartialLineAndOriginalSessionLock(t *testing.T) {
	r := NewRegistry()
	p := &fakePty{alive: true}
	j, err := r.StartWithScreen(p, "s", "read", "\n", 40*time.Millisecond, 0, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	p.emit("partial")
	waitState(t, j, StateTimeout)
	out, _, _, _, _ := j.Snapshot()
	if string(out) != "partial" {
		t.Fatalf("partial output lost: %q", out)
	}
	if _, err := r.Start(p, "s", "next", "\n", time.Second, 0); err != ErrSessionBusy {
		t.Fatalf("running command unlocked: %v", err)
	}
	p.emit(" continued\r\nlate line\r\n")
	if resumed, ok := r.Collect("s", time.Second); !ok || resumed != j {
		t.Fatal("did not resume original job")
	}
	p.complete(j, 0)
	waitDone(t, j, time.Second)
	out, _, _, _, _ = j.Snapshot()
	if string(out) != "partial continued\nlate line\n" {
		t.Fatalf("lost/duplicated partial line: %q", out)
	}
	// Completion frees the lock immediately, without depending on watcher scheduling.
	next, err := r.Start(p, "s", "next", "\n", time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	p.complete(next, 0)
}

func TestUnknownShellWaitsForTimeoutDespitePromptAndForeignMarker(t *testing.T) {
	r := NewRegistry()
	p := &fakePty{alive: true}
	// The interface wrapper intentionally omits ForegroundShell.
	j, err := r.Start(struct{ Pty }{p}, "s", "cmd", "\n", 150*time.Millisecond, 0)
	if err != nil {
		t.Fatal(err)
	}
	p.emit(fmt.Sprintf("$ done?\r\n__UT_%s;0__", j.ID))
	time.Sleep(50 * time.Millisecond)
	_, _, _, state, _ := j.Snapshot()
	if state != StateRunning {
		t.Fatalf("unverified completion: %s", state)
	}
	waitState(t, j, StateTimeout)
	_ = r.Interrupt(j.ID)
}
