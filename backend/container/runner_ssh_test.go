package container

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// 编译期接口合规断言：SSHRunner 必须实现冻结的 Runner 接口。
var _ Runner = (*SSHRunner)(nil)

// JoinShellCommand 已在 Task 2 测；这里测 detectArgs 经 shell 的形态。
func TestDetectArgs(t *testing.T) {
	got := JoinShellCommand(detectArgs(RuntimePodman))
	want := "sh -c 'command -v podman'"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// hangExecServer 起一个假 SSH 服务器：接受 exec 请求但永不退出（远端进程
// "挂起"），用来验证调用方的 context 能打断阻塞。pty-req 一并接受，
// 这样 RunPTY 也能用同一个服务器。
func hangExecServer(t *testing.T) *ssh.Client {
	t.Helper()
	return newSSHTestClient(t, func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		for req := range reqs {
			if req.Type == "exec" || req.Type == "pty-req" {
				if req.WantReply {
					_ = req.Reply(true, nil)
				}
				if req.Type == "exec" {
					// Never send exit-status and never close: the client's
					// Wait blocks until the session is closed from underneath.
					<-t.Context().Done()
					return
				}
				continue
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	})
}

// 回归：SSHRunner 所有方法都忽略 ctx（签名是 `_ context.Context`），调用方
// 的超时（例如 PutContent 的 30s）毫无作用，远端 hang 时 Wails IPC 调用
// 永久阻塞。修复后 ctx 取消必须关闭 session 让阻塞的方法立即返回。
func TestSSHRunnerRunHonorsContextCancel(t *testing.T) {
	r := NewSSHRunner(hangExecServer(t))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, []string{"sleep", "infinity"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil error after ctx cancel, want ctx error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx was cancelled")
	}
}

// 同上，但带 stdin：RunStdin 也不能在远端挂起时忽略 ctx。
func TestSSHRunnerRunStdinHonorsContextCancel(t *testing.T) {
	r := NewSSHRunner(hangExecServer(t))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := r.RunStdin(ctx, []string{"cat"}, []byte("payload"))
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunStdin did not return after ctx was cancelled")
	}
}

// RunStream 是流式方法：ctx 取消必须关会话，让 scanner 排干、Wait 返回。
func TestSSHRunnerRunStreamHonorsContextCancel(t *testing.T) {
	r := NewSSHRunner(hangExecServer(t))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	s, err := r.RunStream(ctx, []string{"tail", "-f", "/dev/null"})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	defer s.Close()

	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunStream Wait did not return after ctx was cancelled")
	}
}

// RunPTY 是交互终端：ctx 取消必须关会话，让 Data 通道关闭。
func TestSSHRunnerRunPTYHonorsContextCancel(t *testing.T) {
	r := NewSSHRunner(hangExecServer(t))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	p, err := r.RunPTY(ctx, []string{"sh"}, 80, 24)
	if err != nil {
		t.Fatalf("RunPTY: %v", err)
	}
	defer p.Close()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-p.Data():
			if !ok {
				return // data channel closed → session torn down
			}
		case <-deadline:
			t.Fatal("PTY data channel did not close after ctx was cancelled")
		}
	}
}
