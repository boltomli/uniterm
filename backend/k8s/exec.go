package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// k8s exec v4.channel.k8s.io 帧协议：首字节为通道号。
const (
	wsChStdin  = 0
	wsChStdout = 1
	wsChStderr = 2
	wsChError  = 3
)

// dialExecWS 拨一条 exec WebSocket。tty 固定 false（输出可安全解析），
// command 为 /bin/sh -c script；container 为空时使用 Pod 默认容器。
func (m *Manager) dialExecWS(ctx context.Context, connID, ns, pod, container string, stdin bool, script string) (*websocket.Conn, error) {
	m.mu.RLock()
	conn, ok := m.conns[connID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("connection %q not found", connID)
	}
	u, err := url.Parse(conn.base)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/exec", ns, pod)
	q := url.Values{}
	if container != "" {
		q.Set("container", container)
	}
	q.Set("stdin", fmt.Sprintf("%t", stdin))
	q.Set("stdout", "true")
	q.Set("stderr", "true")
	q.Set("tty", "false")
	q.Add("command", "/bin/sh")
	q.Add("command", "-c")
	q.Add("command", script)
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{
		TLSClientConfig: conn.tlsConfig,
		Subprotocols:    []string{"v4.channel.k8s.io"},
	}
	if conn.dialOverride != nil {
		dialer.NetDialContext = conn.dialOverride
	}
	header := http.Header{}
	if conn.token != "" {
		header.Set("Authorization", "Bearer "+conn.token)
	}
	ws, _, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return nil, fmt.Errorf("exec dial: %w", err)
	}
	return ws, nil
}

// readExecFrames 读取 exec WS 直到关闭，stdout 聚合到 out，error 通道内容
// 记为执行错误（apiserver 在命令结束时于 error 通道发送 Status JSON，
// "Success" 忽略、"Failure" 提取 message）。
func readExecFrames(ws *websocket.Conn, out io.Writer) error {
	var execMsg string
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case wsChStdout:
			out.Write(msg[1:])
		case wsChError:
			if len(msg[1:]) > 0 {
				execMsg = string(msg[1:])
			}
		}
	}
	if execMsg == "" {
		return nil
	}
	if strings.Contains(execMsg, `"status":"Success"`) {
		return nil
	}
	// Failure：尽量提取人类可读的 message
	var st struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Details struct {
			Causes []struct {
				Message string `json:"message"`
			} `json:"causes"`
		} `json:"details"`
	}
	if json.Unmarshal([]byte(execMsg), &st) == nil && st.Status == "Failure" {
		msg := st.Message
		if msg == "" && len(st.Details.Causes) > 0 {
			msg = st.Details.Causes[0].Message
		}
		if msg != "" {
			return fmt.Errorf("%s", msg)
		}
	}
	return fmt.Errorf("%s", execMsg)
}

// ExecRun 在 Pod 容器内以 /bin/sh -c 执行脚本（非交互），返回 stdout；
// 非零退出/执行错误并入 error。命令结束后部分集群不关闭 WS，这里在
// stdout 里注入完成标记并在读到后收尾；30s 硬超时兜底。
func (m *Manager) ExecRun(ctx context.Context, connID, ns, pod, container, script string) ([]byte, error) {
	marker := fmt.Sprintf("__ubt_done_%d__", time.Now().UnixNano())
	ws, err := m.dialExecWS(ctx, connID, ns, pod, container, false, script+"; echo "+marker)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	var out bytes.Buffer
	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) {
			return out.Bytes(), fmt.Errorf("exec timeout (30s)")
		}
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, msg, err := ws.ReadMessage()
		if err != nil {
			// 超时读：命令可能已结束但服务器未关连接；标记未出现视为异常
			if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
				if strings.Contains(out.String(), marker) {
					break
				}
				continue
			}
			break
		}
		if len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case wsChStdout:
			out.Write(msg[1:])
		case wsChError:
			if len(msg[1:]) > 0 {
				execMsg := string(msg[1:])
				if !strings.Contains(execMsg, `"status":"Success"`) && strings.Contains(execMsg, `"status":"Failure"`) {
					return out.Bytes(), fmt.Errorf("%s", extractExecFailure(execMsg))
				}
			}
		}
		if strings.Contains(out.String(), marker) {
			break
		}
	}
	res := out.String()
	if i := strings.Index(res, marker); i >= 0 {
		res = res[:i]
	}
	return []byte(strings.TrimSuffix(res, "\n")), nil
}

// extractExecFailure 从 error 通道的 Status JSON 提取人类可读 message。
func extractExecFailure(execMsg string) string {
	var st struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Details struct {
			Causes []struct {
				Message string `json:"message"`
			} `json:"causes"`
		} `json:"details"`
	}
	if json.Unmarshal([]byte(execMsg), &st) == nil && st.Status == "Failure" {
		if st.Message != "" {
			return st.Message
		}
		if len(st.Details.Causes) > 0 && st.Details.Causes[0].Message != "" {
			return st.Details.Causes[0].Message
		}
	}
	return execMsg
}

// execRawStream 是 exec stdout 的二进制流（tar 导出），实现 session.RawStream。
type execRawStream struct {
	*io.PipeReader
	pw      *io.PipeWriter
	ws      *websocket.Conn
	mu      sync.Mutex
	done    chan struct{}
	execErr error
}

func (s *execRawStream) Wait() error {
	<-s.done
	return s.execErr
}

func (s *execRawStream) Close() error {
	// 先关管道写侧解除可能阻塞的 pw.Write，再关 WS，最后等 goroutine 收尾。
	// 顺序错了会死锁：WS 关闭不会唤醒阻塞中的 io.Pipe.Write。
	s.pw.Close()
	s.ws.Close()
	<-s.done
	return nil
}

// ExecRaw 打开一条 exec 的 stdout 二进制流（tar 导出回退通道也走这里）。
// 部分集群版本在命令结束后**不关闭**非 tty exec 的 WebSocket（tty 终端不受
// 影响），因此脚本末尾追加一行随机完成标记，客户端在 stdout 中检测到标记
// 即主动收尾，不依赖服务器关闭行为。
func (m *Manager) ExecRaw(ctx context.Context, connID, ns, pod, container, script string) (*execRawStream, error) {
	marker := fmt.Sprintf("__ubt_done_%d_%d__", time.Now().UnixNano(), os.Getpid())
	ws, err := m.dialExecWS(ctx, connID, ns, pod, container, false, script+"; echo "+marker)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	s := &execRawStream{PipeReader: pr, pw: pw, ws: ws, done: make(chan struct{})}
	go func() {
		var execErr error
		var tail string // 跨帧拼接：标记可能被拆到相邻两帧
		finalized := false
		finalize := func() {
			if finalized {
				return
			}
			finalized = true
			s.mu.Lock()
			s.execErr = execErr
			s.mu.Unlock()
			pw.Close()
			close(s.done)
		}
		defer func() {
			finalize()
		}()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if len(msg) == 0 {
				continue
			}
			switch msg[0] {
			case wsChStdout:
				// 标记检测：当前帧 + 上一帧尾部拼接扫描
				combined := tail + string(msg[1:])
				if strings.Contains(combined, marker) {
					finalize()
					return
				}
				tail = combined
				if len(tail) > len(marker) {
					tail = tail[len(tail)-len(marker)+1:]
				}
				if _, werr := pw.Write(msg[1:]); werr != nil {
					return
				}
			case wsChError:
				if len(msg[1:]) > 0 {
					execMsg := string(msg[1:])
					if !strings.Contains(execMsg, `"status":"Success"`) {
						execErr = fmt.Errorf("%s", execMsg)
						finalize()
						return
					}
				}
			}
		}
	}()
	return s, nil
}

// ExecStdinStream 流式标准输入（本机 tar 流导入）：写毕关闭 WS，kubelet
// 关闭容器内 stdin，`tar xf -` 得到 EOF 后收尾。stderr 经 error 通道并回。
func (m *Manager) ExecStdinStream(ctx context.Context, connID, ns, pod, container, script string, stdin io.Reader) ([]byte, error) {
	ws, err := m.dialExecWS(ctx, connID, ns, pod, container, true, script)
	if err != nil {
		return nil, err
	}
	defer ws.Close()

	stderr := &bytes.Buffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if len(msg) > 1 && msg[0] == wsChError {
				stderr.Write(msg[1:])
			}
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		n, rerr := stdin.Read(buf)
		if n > 0 {
			frame := make([]byte, n+1)
			frame[0] = wsChStdin
			copy(frame[1:], buf[:n])
			if werr := ws.WriteMessage(websocket.BinaryMessage, frame); werr != nil {
				return nil, werr
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	// 关闭 WS → kubelet 关闭容器内 stdin → tar 收到 EOF
	_ = ws.Close()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
	}
	if stderr.Len() > 0 {
		msg := strings.TrimSpace(stderr.String())
		if !strings.Contains(msg, `"status":"Success"`) {
			return nil, fmt.Errorf("%s", msg)
		}
	}
	return nil, nil
}
