package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/ys-ll/uniterm/backend/log"
	"golang.org/x/crypto/ssh"
)

const (
	sshCwdTimeout  = 5 * time.Second
	sshCwdInterval = time.Second
)

var (
	errNoCwdTerminal       = errors.New("no terminal process found")
	errCwdProcessChanged   = errors.New("terminal process changed or exited")
	errCwdProbeUnavailable = errors.New("SFTP cwd probe unavailable")
)

// A PID alone is not an identity: Linux can reuse it after the terminal exits.
type cwdProcessID struct {
	pid   int
	start uint64
}

type cwdProcess struct {
	cwdProcessID
	parent  int
	session int
	tty     int64
	state   string
}

func parseCwdProcess(data []byte) (cwdProcess, error) {
	var p cwdProcess
	s := strings.TrimSpace(string(data))
	// comm is parenthesized and may itself contain spaces and parentheses.
	first, last := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if first < 1 || last < first {
		return p, errors.New("invalid proc stat")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:first]))
	if err != nil || pid <= 0 {
		return p, errors.New("invalid proc pid")
	}
	f := strings.Fields(s[last+1:]) // starts at field 3 (state)
	if len(f) < 20 {
		return p, errors.New("truncated proc stat")
	}
	parent, e1 := strconv.Atoi(f[1])
	sid, e2 := strconv.Atoi(f[3])
	tty, e3 := strconv.ParseInt(f[4], 10, 64)
	start, e4 := strconv.ParseUint(f[19], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || parent < 0 || sid < 0 || start == 0 {
		return p, errors.New("invalid proc identity")
	}
	return cwdProcess{cwdProcessID{pid, start}, parent, sid, tty, f[0]}, nil
}

type cwdProcFS interface {
	ReadFile(string) ([]byte, error)
	ReadDir(string) ([]os.FileInfo, error)
	ReadLink(string) (string, error)
}

type cwdSFTPFS struct{ *sftp.Client }

func (f cwdSFTPFS) ReadFile(name string) ([]byte, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// proc files report size zero; read until EOF rather than using their size.
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if len(data) > 64*1024 {
		return nil, errors.New("proc file too large")
	}
	return data, err
}

func readCwdProcess(fs cwdProcFS, name string) (cwdProcess, error) {
	data, err := fs.ReadFile(name)
	if err != nil {
		return cwdProcess{}, err
	}
	return parseCwdProcess(data)
}

// On OpenSSH, the PTY session and SFTP subsystem on the SAME transport are
// children of the same connection process. A separate SFTP connection cannot
// be used here. Serializing terminal creation and remembering every assigned
// identity makes duplicate channels unambiguous without typing into any shell.
func findCwdTerminal(fs cwdProcFS, assigned map[cwdProcessID]bool) (cwdProcess, error) {
	self, err := readCwdProcess(fs, "/proc/self/stat")
	if err != nil {
		return cwdProcess{}, fmt.Errorf("read SFTP process: %w", err)
	}
	if self.parent <= 1 {
		return cwdProcess{}, errors.New("unsupported SFTP process tree")
	}

	parent := strconv.Itoa(self.parent)
	data, err := fs.ReadFile("/proc/" + parent + "/task/" + parent + "/children")
	pids := strings.Fields(string(data))
	if err != nil {
		entries, err := fs.ReadDir("/proc")
		if err != nil {
			return cwdProcess{}, err
		}
		pids = nil
		for _, entry := range entries {
			if n, err := strconv.Atoi(entry.Name()); err == nil && n > 0 {
				pids = append(pids, entry.Name())
			}
		}
	}
	var candidate cwdProcess
	for _, pid := range pids {
		n, err := strconv.Atoi(pid)
		if err != nil || n <= 0 || n == self.pid {
			continue
		}
		p, err := readCwdProcess(fs, "/proc/"+strconv.Itoa(n)+"/stat")
		if err != nil {
			continue
		} // unrelated processes can exit or be unreadable
		if p.pid != n || p.parent != self.parent || p.session != p.pid || p.tty == 0 || p.state == "Z" || assigned[p.cwdProcessID] {
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

func readTerminalCwd(fs cwdProcFS, want cwdProcess) (string, error) {
	base := "/proc/" + strconv.Itoa(want.pid)
	check := func() error {
		p, err := readCwdProcess(fs, base+"/stat")
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%w: %v", errCwdProcessChanged, err)
			}
			return err
		}
		if p.cwdProcessID != want.cwdProcessID || p.parent != want.parent || p.tty != want.tty || p.state == "Z" {
			return errCwdProcessChanged
		}
		return nil
	}
	if err := check(); err != nil {
		return "", err
	}
	cwd, err := fs.ReadLink(base + "/cwd")
	if err != nil {
		return "", err
	}
	// Check again to exclude PID reuse between stat and readlink.
	if err := check(); err != nil {
		return "", err
	}
	if !strings.HasPrefix(cwd, "/") || strings.HasSuffix(cwd, " (deleted)") {
		return "", errors.New("terminal cwd is unavailable")
	}
	return cwd, nil
}

// One read-only probe channel per authenticated transport, shared by clones.
// All I/O is bounded; timeout closes ONLY this auxiliary channel, never the
// interactive terminal or its transport. Unsupported hosts never get a hook.
type sshCwdProbe struct {
	mu            sync.Mutex
	fs            cwdProcFS
	channel       *ssh.Session
	client        *sftp.Client
	unavailable   bool
	bindingFailed bool
	assigned      map[cwdProcessID]bool
}

type cwdProbeResult struct {
	process cwdProcess
	cwd     string
	shell   string
	err     error
}

func (p *sshCwdProbe) open(client *ssh.Client) error {
	if p.fs != nil {
		return nil
	}
	type result struct {
		session *ssh.Session
		client  *sftp.Client
		err     error
	}
	done := make(chan result)
	cancel := make(chan struct{})
	defer close(cancel)
	go func() {
		sess, err := client.NewSession()
		if err != nil {
			select {
			case done <- result{err: err}:
			case <-cancel:
			}
			return
		}
		// Also interrupt a subsystem request/handshake that never replies.
		finished := make(chan struct{})
		watcherDone := make(chan struct{})
		go func() {
			defer close(watcherDone)
			select {
			case <-cancel:
				_ = sess.Close()
			case <-finished:
			}
		}()
		sess.Stderr = io.Discard
		stdin, err := sess.StdinPipe()
		var stdout io.Reader
		if err == nil {
			stdout, err = sess.StdoutPipe()
		}
		if err == nil {
			err = sess.RequestSubsystem("sftp")
		}
		var sc *sftp.Client
		if err == nil {
			sc, err = sftp.NewClientPipe(stdout, stdin)
		}
		close(finished)
		// The cancellation watcher must be gone before ownership is handed
		// over; otherwise cancelling this completed open could close the probe.
		<-watcherDone
		select {
		case done <- result{sess, sc, err}:
		case <-cancel:
			if sc != nil {
				_ = sc.Close()
			}
			_ = sess.Close()
		}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			if r.session != nil {
				go r.session.Close()
			}
			return r.err
		}
		p.channel, p.client, p.fs = r.session, r.client, cwdSFTPFS{r.client}
		return nil
	case <-time.After(sshCwdTimeout):
		return errors.New("SFTP probe open timed out")
	}
}

// Caller holds mu. Operations only read fs/assigned; on timeout no later bind
// is allowed to mutate assigned while the timed-out operation is unwinding.
func (p *sshCwdProbe) run(fn func(cwdProcFS) cwdProbeResult) cwdProbeResult {
	if p.unavailable {
		return cwdProbeResult{err: errCwdProbeUnavailable}
	}
	done := make(chan cwdProbeResult, 1)
	fs := p.fs
	go func() { done <- fn(fs) }()
	select {
	case result := <-done:
		return result
	case <-time.After(sshCwdTimeout):
		p.unavailable = true
		if p.channel != nil {
			go p.channel.Close()
		}
		if p.client != nil {
			go p.client.Close()
		}
		return cwdProbeResult{err: fmt.Errorf("%w: request timed out", errCwdProbeUnavailable)}
	}
}

func (p *sshCwdProbe) bind(client *ssh.Client) cwdProbeResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unavailable || p.bindingFailed {
		return cwdProbeResult{err: errors.New("SFTP cwd discovery unavailable")}
	}
	if err := p.open(client); err != nil {
		p.unavailable = true
		return cwdProbeResult{err: err}
	}
	if p.assigned == nil {
		p.assigned = make(map[cwdProcessID]bool)
	}
	r := p.run(func(fs cwdProcFS) cwdProbeResult {
		process, err := findCwdTerminal(fs, p.assigned)
		// The shell request can be acknowledged just before the forked child
		// acquires its controlling tty. Retry that short initialization window.
		for attempt := 0; errors.Is(err, errNoCwdTerminal) && attempt < 3; attempt++ {
			time.Sleep(50 * time.Millisecond)
			process, err = findCwdTerminal(fs, p.assigned)
		}
		if err != nil {
			return cwdProbeResult{err: err}
		}
		cwd, err := readTerminalCwd(fs, process)
		// exe is optional (e.g. restricted procfs); cwd following still works.
		shell, _ := fs.ReadLink("/proc/" + strconv.Itoa(process.pid) + "/exe")
		return cwdProbeResult{process: process, cwd: cwd, shell: shell, err: err}
	})
	if r.err != nil {
		// An unbound PTY must never be mistaken for the next clone's terminal.
		p.bindingFailed = true
		if len(p.assigned) == 0 {
			p.unavailable = true
			if p.channel != nil {
				go p.channel.Close()
			}
			if p.client != nil {
				go p.client.Close()
			}
		}
	} else {
		p.assigned[r.process.cwdProcessID] = true
	}
	return r
}

func (p *sshCwdProbe) cwd(process cwdProcess) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.run(func(fs cwdProcFS) cwdProbeResult {
		cwd, err := readTerminalCwd(fs, process)
		return cwdProbeResult{cwd: cwd, err: err}
	})
	return r.cwd, r.err
}

// CurrentCwd queries the bound terminal on demand, including when follow is
// disabled. File tabs can therefore open at its current directory without
// keeping every background terminal polling continuously.
func (s *SSHSession) CurrentCwd() (string, error) {
	select {
	case <-s.quit:
		return "", errors.New("SSH session disconnected")
	default:
	}
	s.mu.RLock()
	ref, target := s.clientRef, s.cwdTarget
	s.mu.RUnlock()
	if ref == nil || target.pid == 0 {
		return GetSessionCwd(s.id), nil
	}
	cwd, err := ref.cwd.cwd(target)
	if err != nil {
		return "", err
	}
	select {
	case <-s.quit:
		return "", errors.New("SSH session disconnected")
	default:
	}
	recordSessionCwd(s.id, cwd)
	if TerminalCwdSink != nil {
		TerminalCwdSink(s.id, cwd)
	}
	return cwd, nil
}

// SetCwdTracking controls polling only. It never writes terminal input.
func (s *SSHSession) SetCwdTracking(enabled bool) error {
	s.cwdTracking.Store(enabled)
	select {
	case s.cwdWake <- struct{}{}:
	default:
	}
	return nil
}

func (s *SSHSession) trackCwd(probe *sshCwdProbe, process cwdProcess) {
	ticker := time.NewTicker(sshCwdInterval)
	defer ticker.Stop()
	last := ""
	failed := false
	for {
		select {
		case <-s.quit:
			return
		case <-ticker.C:
		case <-s.cwdWake:
			last = "" // enabling follow must report even an unchanged cwd
		}
		if !s.cwdTracking.Load() {
			last = ""
			continue
		}
		cwd, err := probe.cwd(process)
		if err != nil {
			if !failed {
				log.Writef("ssh: cwd tracking unavailable (session=%s): %v", s.id, err)
			}
			failed = true
			if errors.Is(err, errCwdProcessChanged) || errors.Is(err, errCwdProbeUnavailable) {
				return
			}
			// A deleted cwd or temporary permission error can recover after cd.
			continue
		}
		failed = false
		select {
		case <-s.quit:
			return
		default:
		}
		if !s.cwdTracking.Load() || cwd == last {
			continue
		}
		last = cwd
		recordSessionCwd(s.id, cwd)
		if TerminalCwdSink != nil {
			TerminalCwdSink(s.id, cwd)
		}
	}
}
