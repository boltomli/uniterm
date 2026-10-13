package container

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// newSSHTestClient 起一个进程内假 SSH 服务器：每个 session 通道把 exec
// 请求交给 handle 处理。返回可直接构造 SSHRunner 的客户端。
func newSSHTestClient(t *testing.T, handle func(ch ssh.Channel, reqs <-chan *ssh.Request)) *ssh.Client {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			tcp, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sconn, chans, reqs, err := ssh.NewServerConn(tcp, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					if newCh.ChannelType() != "session" {
						_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					ch, chReqs, err := newCh.Accept()
					if err != nil {
						continue
					}
					go handle(ch, chReqs)
				}
			}()
		}
	}()
	client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("unused")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// serveExec 等 exec 请求，回包接受后调用 run。
func serveExec(t *testing.T, ch ssh.Channel, reqs <-chan *ssh.Request, run func()) {
	t.Helper()
	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		if req.WantReply {
			_ = req.Reply(true, nil)
		}
		run()
		return
	}
}

func exitStatus(ch ssh.Channel, code uint32) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
	_ = ch.Close()
}

// 回归：RunStdin 里 StdinPipe() 在 Start() 之后调用必失败
// （"ssh: StdinPipe after process started"），stdin 从未送达远端。
// 这里用进程内假 SSH 服务器验证 stdin 能完整回显。
func TestSSHRunnerRunStdinDeliversStdin(t *testing.T) {
	const payload = "password-for-registry-login"
	client := newSSHTestClient(t, func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		serveExec(t, ch, reqs, func() {
			_, _ = io.Copy(ch, ch) // stdin → stdout，EOF 后返回
			exitStatus(ch, 0)
		})
	})

	out, err := NewSSHRunner(client).RunStdin(context.Background(),
		[]string{"sh", "-c", "cat"}, []byte(payload))
	if err != nil {
		t.Fatalf("RunStdin failed: %v", err)
	}
	if string(out) != payload {
		t.Fatalf("stdin not delivered: got %q want %q", out, payload)
	}
}

// 回归：RunRaw 的 stderr 收集 goroutine 与 Wait 读取之间无同步，
// 既可能漏掉 stderr 内容（-race 会报）。Wait 必须等排干完成。
func TestSSHRunnerRunRawWaitIncludesStderr(t *testing.T) {
	stderrText := strings.Repeat("stderr-line-contents\n", 16384) // ~350KB，跨多包
	client := newSSHTestClient(t, func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		serveExec(t, ch, reqs, func() {
			_, _ = io.WriteString(ch.Stderr(), stderrText)
			_, _ = io.WriteString(ch, "stdout-data")
			exitStatus(ch, 3)
		})
	})

	s, err := NewSSHRunner(client).RunRaw(context.Background(), []string{"sh", "-c", "fail"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "stdout-data" {
		t.Fatalf("stdout wrong: %q", out)
	}
	waitErr := s.Wait()
	if waitErr == nil {
		t.Fatal("expected error for exit 3")
	}
	if !strings.Contains(waitErr.Error(), "stderr-line-contents") {
		t.Fatalf("Wait error missing stderr content: %v", waitErr)
	}
}

// 回归：RunStream 的 scanner 超过 256KiB 行停止后不排干 stdout，
// 远端写满 SSH 窗口阻塞，sess.Wait() 永远不返回。
func TestSSHRunnerRunStreamDrainsOverlongLine(t *testing.T) {
	bigLine := strings.Repeat("x", 3<<20) // 超过 x/crypto 默认 2MiB 窗口
	client := newSSHTestClient(t, func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		serveExec(t, ch, reqs, func() {
			_, _ = io.WriteString(ch, "good-line\n")
			_, _ = io.WriteString(ch, bigLine+"\n")
			exitStatus(ch, 0)
		})
	})

	s, err := NewSSHRunner(client).RunStream(context.Background(), []string{"sh", "-c", "big"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var lines []string
	for ln := range s.Lines() {
		lines = append(lines, ln)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- s.Wait() }()
	select {
	case err := <-waitCh:
		if err != nil {
			t.Fatalf("Wait failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunStream.Wait hung after overlong line")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "good-line") {
		t.Fatalf("lines before overlong line lost: %q", joined)
	}
}

// 本地 RunStream 同款回归：超长单行使 scanner 停止后子进程阻塞在写管道上，
// Wait 挂死。用测试二进制自身当 helper 输出 3MB 无换行数据。
func TestHelperLongLineOutput(t *testing.T) {
	if len(os.Args) < 3 || os.Args[2] != "longline-helper" {
		return
	}
	_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 3<<20))
	_, _ = os.Stdout.Write([]byte("\ntail-line\n"))
	os.Exit(0)
}

func TestLocalRunnerRunStreamDrainsOverlongLine(t *testing.T) {
	r := NewLocalRunner()
	s, err := r.RunStream(context.Background(),
		[]string{os.Args[0], "-test.run=TestHelperLongLineOutput", "longline-helper"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for range s.Lines() {
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- s.Wait() }()
	select {
	case <-waitCh:
	case <-time.After(10 * time.Second):
		t.Fatal("local RunStream.Wait hung after overlong line")
	}
}
