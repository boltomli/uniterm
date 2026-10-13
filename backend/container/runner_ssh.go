package container

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"strings"
	"sync"
	"time"
)

type SSHRunner struct {
	client *ssh.Client
}

func NewSSHRunner(client *ssh.Client) *SSHRunner { return &SSHRunner{client: client} }
func (r *SSHRunner) Client() *ssh.Client         { return r.client }

func (r *SSHRunner) Run(_ context.Context, argv []string) ([]byte, error) {
	return r.run(argv, nil)
}

func (r *SSHRunner) RunStdin(_ context.Context, argv []string, stdin []byte) ([]byte, error) {
	return r.run(argv, stdin)
}

// RunStdinStream 带流式标准输入执行：本机 tar 流经 SSH stdin 直达远端容器。
// 写完 stdin 并关闭后等退出，stdout 全量收集（导入类命令 stdout 近似为空）。
func (r *SSHRunner) RunStdinStream(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	sess, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	w, err := sess.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := sess.Start(JoinShellCommand(argv)); err != nil {
		return nil, err
	}
	if _, err := io.Copy(w, stdin); err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := sess.Wait(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// sshRawStream 是远端命令 stdout 的二进制流；Close 关会话，Wait 等退出。
// stderr 收进缓冲，非零退出时并入错误（nerdctl cp 失败原因在此）。
type sshRawStream struct {
	io.Reader // ssh.StdoutPipe 返回 io.Reader，Close 由 sess 承担
	sess      *ssh.Session
	stderr    *bytes.Buffer
	// stderrDone 在 stderr 排干 goroutine 结束后关闭；Wait 读缓冲前必须等它，
	// 否则与 io.Copy 并发读写 bytes.Buffer 是数据竞争，还会漏掉尾部内容。
	stderrDone chan struct{}
	once       sync.Once
}

func (s *sshRawStream) Wait() error {
	err := s.sess.Wait()
	if s.stderrDone != nil {
		// 等 stderr 排干 goroutine 收尾再读缓冲；远端发完 exit-status 但迟迟
		// 不关通道时设上限，避免 Wait 无界挂起。
		select {
		case <-s.stderrDone:
		case <-time.After(5 * time.Second):
		}
	}
	if err != nil && strings.Contains(err.Error(), "exited without exit status") {
		return nil // 远端被杀/会话关闭时的噪音退出态，不算错误
	}
	if err != nil {
		if msg := strings.TrimSpace(s.stderr.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
	}
	return err
}

func (s *sshRawStream) Close() error {
	s.once.Do(func() {
		_ = s.sess.Close()
	})
	return nil
}

func (r *SSHRunner) RunRaw(_ context.Context, argv []string) (RawStream, error) {
	sess, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stderrBuf := &bytes.Buffer{}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrBuf, stderr)
	}()
	if err := sess.Start(JoinShellCommand(argv)); err != nil {
		sess.Close()
		return nil, err
	}
	return &sshRawStream{Reader: stdout, sess: sess, stderr: stderrBuf, stderrDone: stderrDone}, nil
}

func (r *SSHRunner) run(argv []string, stdin []byte) ([]byte, error) {
	sess, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	if stdin == nil {
		if err := sess.Run(JoinShellCommand(argv)); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("%s", msg)
			}
			return nil, err
		}
		return stdout.Bytes(), nil
	}
	// 带标准输入：StdinPipe 必须在 Start 之前取（Start 后再取会得到
	// "ssh: StdinPipe after process started"，stdin 从未送达远端）。
	// 写入并关闭 stdin 后再等退出，避免管道死锁。
	w, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := sess.Start(JoinShellCommand(argv)); err != nil {
		_ = sess.Close()
		return nil, err
	}
	if _, err := w.Write(stdin); err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := sess.Wait(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

type sshLineStream struct {
	lines    chan string
	done     chan error
	sess     *ssh.Session
	once     sync.Once
	waitOnce sync.Once
	waitErr  error
}

func (s *sshLineStream) Lines() <-chan string { return s.lines }

// Wait 缓存结果：Close 与消费者可并发/重复调用，均拿到同一真实退出状态。
func (s *sshLineStream) Wait() error {
	s.waitOnce.Do(func() { s.waitErr = <-s.done })
	return s.waitErr
}

// Close 尽力而为：关闭会话（远端进程随之终止）并等回收，不传播错误。
func (s *sshLineStream) Close() error {
	s.once.Do(func() {
		_ = s.sess.Close()
		_ = s.Wait()
	})
	return nil
}

func (r *SSHRunner) RunStream(_ context.Context, argv []string) (LineStream, error) {
	sess, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	if err := sess.Start(JoinShellCommand(argv)); err != nil {
		sess.Close()
		return nil, err
	}
	s := &sshLineStream{lines: make(chan string, 64), done: make(chan error, 1), sess: sess}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 256*1024), 256*1024)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
		// scanner 因超长单行（>256KiB，ErrTooLong）提前停止后必须继续排干
		// stdout：否则远端阻塞在写 SSH 窗口上，sess.Wait() 永远不返回。
		_, _ = io.Copy(io.Discard, stdout)
		close(s.lines)
		s.done <- sess.Wait()
	}()
	return s, nil
}

type sshPTY struct {
	sess  *ssh.Session
	stdin io.Writer
	data  chan []byte
	mu    sync.Mutex
	once  sync.Once
}

func (r *SSHRunner) RunPTY(_ context.Context, argv []string, cols, rows int) (PTYStream, error) {
	sess, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		sess.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	if err := sess.Start(JoinShellCommand(argv)); err != nil {
		sess.Close()
		return nil, err
	}
	p := &sshPTY{sess: sess, stdin: stdin, data: make(chan []byte, 32)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				p.data <- cp
			}
			if err != nil {
				close(p.data)
				return
			}
		}
	}()
	return p, nil
}

func (p *sshPTY) Data() <-chan []byte { return p.data }
func (p *sshPTY) Write(b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.stdin.Write(b)
	return err
}
func (p *sshPTY) Resize(cols, rows int) error { return p.sess.WindowChange(rows, cols) }
func (p *sshPTY) Close() error {
	var err error
	p.once.Do(func() { err = p.sess.Close() })
	return err
}
