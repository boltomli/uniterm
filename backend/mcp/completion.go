package mcp

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"strings"
)

// CompletionCommand adds an in-band acknowledgement issued by the shell only
// after the submitted command returns. eval runs in the current shell, so cd,
// exports and shell variables survive. The input contains escaped control
// characters: terminal echo cannot impersonate the acknowledgement.
// Unsupported shells deliberately get no heuristic completion detection.
func CompletionCommand(command, shell, token string) (string, bool) {
	quoted := "'" + strings.ReplaceAll(command, "'", "'\"'\"'") + "'"
	// Use concealed screen text rather than OSC: ConPTY passes OSC through
	// ahead of pending screen updates, which would acknowledge completion
	// before the last output frame. Text shares the renderer's output order.
	format := `\n\033[8m__UT_` + token + `;%d__\033[0m\n`
	switch shell {
	case "bash", "zsh", "sh", "dash", "ksh":
		return " eval " + quoted + "; printf '" + format + `' "$?"`, true
	case "powershell", "pwsh":
		// $LASTEXITCODE is sticky across PowerShell commands. Reset it before
		// evaluation. Capture $? INSIDE the script block: Windows PowerShell
		// 5 resets it when an invocation/expression returns to its caller.
		name := "__ut_" + strings.ReplaceAll(token, "-", "_")
		// Keep terminal input on one line: PSReadLine does not treat a bulk
		// write containing literal LF inside a quoted string as a script paste.
		encoded := base64.StdEncoding.EncodeToString([]byte(command + "\n; $" + name + "=@($?, $global:LASTEXITCODE)"))
		psQuoted := "[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + encoded + "'))"
		return ". { $global:LASTEXITCODE=0; try { . ([scriptblock]::Create(" + psQuoted + ")); if ($" + name + "[0]) { $" + name + "=0 } elseif ($" + name + "[1]) { $" + name + "=$" + name + "[1] } else { $" + name + "=1 } } catch { Write-Error $_ -ErrorAction Continue; $" + name + "=1 } } | Out-Default; [Console]::Write([Environment]::NewLine+([string][char]27)+'[8m__UT_" + token + ";'+$" + name + "+'__'+([string][char]27)+'[0m'+[Environment]::NewLine); Remove-Variable " + name, true
	default:
		return command, false
	}
}

// completionScanner tolerates arbitrary read boundaries, including a split
// UTF-8 character next to the control sequence. Only this job's nonce matches.
type completionScanner struct {
	prefix  []byte
	pending []byte
}

func (s *completionScanner) feed(data []byte) (cleaned []byte, code int, complete bool) {
	buf := append(s.pending, data...)
	s.pending = nil
	for {
		i := bytes.Index(buf, s.prefix)
		if i < 0 {
			keep := 0
			for n := min(len(buf), len(s.prefix)-1); n > 0; n-- {
				if bytes.Equal(buf[len(buf)-n:], s.prefix[:n]) {
					keep = n
					break
				}
			}
			cleaned = append(cleaned, buf[:len(buf)-keep]...)
			s.pending = append([]byte(nil), buf[len(buf)-keep:]...)
			return
		}
		cleaned = append(cleaned, buf[:i]...)
		rest := buf[i+len(s.prefix):]
		end := bytes.Index(rest, []byte("__"))
		if end < 0 && len(rest) <= 16 {
			s.pending = append([]byte(nil), buf[i:]...)
			return
		}
		if end >= 0 {
			if status, err := strconv.Atoi(string(rest[:end])); err == nil {
				// Do not include the next prompt (or later shell output).
				return cleaned, status, true
			}
		}
		cleaned = append(cleaned, buf[i])
		buf = buf[i+1:]
	}
}
