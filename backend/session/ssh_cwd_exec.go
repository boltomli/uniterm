package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SCP-only hosts need no SFTP subsystem or server binary. All queries run in
// separate, non-interactive exec channels AFTER the login shell starts. They
// never touch terminal stdin, shell options, prompts, or interactive history.
type cwdExecFS struct {
	ctx     context.Context
	execute func(context.Context, string) ([]byte, error)
}

func newCwdExecFS(client *ssh.Client) *cwdExecFS {
	return &cwdExecFS{context.Background(), func(ctx context.Context, script string) ([]byte, error) {
		return runCwdExec(ctx, client, script)
	}}
}

func (f *cwdExecFS) withContext(ctx context.Context) cwdProcFS {
	return &cwdExecFS{ctx, f.execute}
}

func (f *cwdExecFS) command(script string) ([]byte, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	return f.execute(f.ctx, script)
}

func (f *cwdExecFS) ReadFile(name string) ([]byte, error) {
	if name == "/proc/self/stat" {
		return f.command(`cat /proc/$$/stat`)
	}
	return f.command("cat " + shellEscape(name))
}

type cwdProcEntry string

func (e cwdProcEntry) Name() string       { return string(e) }
func (e cwdProcEntry) Size() int64        { return 0 }
func (e cwdProcEntry) Mode() os.FileMode  { return os.ModeDir | 0555 }
func (e cwdProcEntry) ModTime() time.Time { return time.Time{} }
func (e cwdProcEntry) IsDir() bool        { return true }
func (e cwdProcEntry) Sys() any           { return nil }

func (f *cwdExecFS) ReadDir(name string) ([]os.FileInfo, error) {
	if name != "/proc" {
		return nil, os.ErrInvalid
	}
	data, err := f.command(`printf '%s\000' /proc/[0-9]*`)
	if err != nil {
		return nil, err
	}
	var entries []os.FileInfo
	for _, p := range bytes.Split(data, []byte{0}) {
		name := strings.TrimPrefix(string(p), "/proc/")
		if n, err := strconv.Atoi(name); err == nil && n > 0 {
			entries = append(entries, cwdProcEntry(name))
		}
	}
	return entries, nil
}

func (f *cwdExecFS) ReadLink(name string) (string, error) {
	data, err := f.command("readlink " + shellEscape(name))
	return strings.TrimSuffix(string(data), "\n"), err
}

// Snapshot discovery in one exec, avoiding a remote command per /proc entry.
// Only potential sibling PTYs are returned; the client validates full stat
// records, rejects ambiguity, and excludes identities already bound to clones.
const cwdExecDiscoverCommand = `cat /proc/$$/stat || exit 1
printf '\000'
for f in /proc/[0-9]*/stat; do
  IFS= read -r info < "$f" 2>/dev/null || continue
  set -f
  set -- ${info##*) }
  [ "${2-}" = "$PPID" ] && [ "${5-0}" != 0 ] || continue
  cat "$f" 2>/dev/null || continue
  printf '\000'
done
:`

func (f *cwdExecFS) FindTerminal(assigned map[cwdProcessID]bool) (cwdProcess, error) {
	data, err := f.command(cwdExecDiscoverCommand)
	if err != nil {
		return cwdProcess{}, err
	}
	records := bytes.Split(data, []byte{0})
	self, err := parseCwdProcess(records[0])
	if err != nil || self.parent <= 1 {
		return cwdProcess{}, errors.New("invalid exec probe process")
	}
	var candidate cwdProcess
	for _, record := range records[1:] {
		p, err := parseCwdProcess(record)
		if err != nil || !isCwdTerminal(p, self.parent, assigned) {
			continue
		}
		if candidate.pid != 0 {
			return cwdProcess{}, errors.New("ambiguous terminal processes")
		}
		candidate = p
	}
	if candidate.pid == 0 {
		return cwdProcess{}, errNoCwdTerminal
	}
	return candidate, nil
}

func (f *cwdExecFS) ReadTerminalCwd(want cwdProcess) (string, error) {
	base := "/proc/" + strconv.Itoa(want.pid)
	stat, cwd := shellEscape(base+"/stat"), shellEscape(base+"/cwd")
	data, err := f.command("cat " + stat + ` && printf '\000' && readlink ` + cwd + ` && printf '\000' && cat ` + stat)
	if err != nil {
		return "", err
	}
	records := bytes.Split(data, []byte{0})
	if len(records) != 3 {
		return "", errors.New("invalid exec cwd snapshot")
	}
	for _, record := range [][]byte{records[0], records[2]} {
		p, err := parseCwdProcess(record)
		if err != nil {
			return "", err
		}
		if !sameCwdTerminal(p, want) {
			return "", errCwdProcessChanged
		}
	}
	// Remove only readlink's own newline, preserving newlines in path names.
	path := strings.TrimSuffix(string(records[1]), "\n")
	if !validTerminalCwd(path) {
		return "", errors.New("terminal cwd is unavailable")
	}
	return path, nil
}

// Bound banner/output memory independently of the operation deadline.
type cwdExecOutput struct{ buffer bytes.Buffer }

func (b *cwdExecOutput) Write(data []byte) (int, error) {
	if b.buffer.Len()+len(data) > 256*1024 {
		return 0, errors.New("cwd probe output too large")
	}
	return b.buffer.Write(data)
}

func decodeCwdExecOutput(data []byte, marker string) ([]byte, error) {
	start := bytes.Index(data, []byte(marker+"\n"))
	end := bytes.LastIndex(data, []byte("\x00"+marker+"\x00"))
	if start < 0 || end < start+len(marker)+1 {
		return nil, errors.New("missing cwd probe markers")
	}
	status := data[end+len(marker)+2:]
	if !bytes.Equal(status, []byte("0\x00")) {
		return nil, errors.New("cwd probe command failed")
	}
	return data[start+len(marker)+1 : end], nil
}

func runCwdExec(ctx context.Context, client *ssh.Client, script string) ([]byte, error) {
	type opened struct {
		session *ssh.Session
		err     error
	}
	ready := make(chan opened)
	go func() {
		sess, err := client.NewSession()
		select {
		case ready <- opened{sess, err}:
		case <-ctx.Done():
			closeSSHSessionAsync(sess)
		}
	}()
	var sess *ssh.Session
	select {
	case r := <-ready:
		if r.err != nil {
			return nil, r.err
		}
		sess = r.session
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer closeSSHSessionAsync(sess)
	marker := "__UNITERM_CWD_" + rand.Text() + "__"
	wrapped := "printf '%s\\n' " + shellEscape(marker) + "\n" + script +
		"\n__uniterm_cwd_rc=$?\nprintf '\\000%s\\000%d\\000' " + shellEscape(marker) +
		" \"$__uniterm_cwd_rc\"\nexit \"$__uniterm_cwd_rc\""
	var output cwdExecOutput
	sess.Stdout, sess.Stderr = &output, io.Discard
	done := make(chan error, 1)
	go func() { done <- sess.Run("exec /bin/sh -c " + shellEscape(wrapped)) }()
	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("cwd exec: %w", err)
		}
		return decodeCwdExecOutput(output.buffer.Bytes(), marker)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
