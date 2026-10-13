package k8s

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newExecTestManager 起一个假 kubelet exec WebSocket 服务并注册连接，
// 返回可直接 dialExecWS 的 Manager。
func newExecTestManager(t *testing.T, h http.HandlerFunc) *Manager {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Manager{
		conns: map[string]*connection{
			"conn": {id: "conn", base: srv.URL, watches: make(map[string]struct{})},
		},
	}
}

var execTestUpgrader = websocket.Upgrader{
	Subprotocols: []string{"v5.channel.k8s.io", "v4.channel.k8s.io"},
}

// 服务器侧哨兵错误：区分假服务端没能按协议收到关闭帧的场景。
var (
	errProto        = errStr("server did not negotiate v5.channel.k8s.io")
	errNoCloseFrame = errStr("client closed WS without v5 stdin close frame")
	errBadFrame     = errStr("server received unexpected frame")
)

type errStr string

func (e errStr) Error() string { return string(e) }

// markerFromExecQuery 从 exec 命令 query 里取出脚本末尾的完成标记。
func markerFromExecQuery(r *http.Request) string {
	cmds := r.URL.Query()["command"]
	if len(cmds) == 0 {
		return ""
	}
	fields := strings.Fields(cmds[len(cmds)-1])
	return fields[len(fields)-1]
}

// holdUntilClientCloses 升级后让连接保持打开（模拟命令结束后不关 WS 的集群），
// 直到客户端断开，避免 httptest.Server.Close 卡在活跃 handler 上。
func holdUntilClientCloses(conn *websocket.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// 回归：完成标记与 tar 数据同帧时，标记之前的数据必须写入输出再收尾。
// 旧实现在命中帧直接 finalize，丢掉整帧数据。
func TestExecRawSameFrameMarkerKeepsData(t *testing.T) {
	data := strings.Repeat("tar-bytes-", 64)
	m := newExecTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := execTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		marker := markerFromExecQuery(r)
		if marker == "" {
			return
		}
		frame := append([]byte{wsChStdout}, data...)
		frame = append(frame, marker...)
		frame = append(frame, '\n')
		if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			return
		}
		holdUntilClientCloses(conn)
	})

	s, err := m.ExecRaw(t.Context(), "conn", "ns", "pod", "", "tar -c /x")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != data {
		t.Fatalf("same-frame data truncated: got %q want %q", out, data)
	}
}

// 回归：完成标记跨帧拆分时，标记前缀不能泄漏进输出，
// 标记前同一帧内的数据也不能丢。
func TestExecRawSplitMarkerNoLeakNoLoss(t *testing.T) {
	data := strings.Repeat("blob", 32)
	m := newExecTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := execTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		marker := markerFromExecQuery(r)
		if marker == "" {
			return
		}
		half := len(marker) / 2
		frame1 := append([]byte{wsChStdout}, data...)
		frame1 = append(frame1, marker[:half]...)
		if err := conn.WriteMessage(websocket.BinaryMessage, frame1); err != nil {
			return
		}
		frame2 := append([]byte{wsChStdout}, marker[half:]...)
		frame2 = append(frame2, '\n')
		if err := conn.WriteMessage(websocket.BinaryMessage, frame2); err != nil {
			return
		}
		holdUntilClientCloses(conn)
	})

	s, err := m.ExecRaw(t.Context(), "conn", "ns", "pod", "", "tar -c /x")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != data {
		t.Fatalf("split-frame output wrong: got %q want %q", out, data)
	}
}

// 回归（上传误报成功）：客户端必须用 v5 的 stdin 关闭帧 [255,0] 通知 EOF，
// 服务器（模拟 tar 失败）随后在 channel 3 回 Status Failure，
// 客户端必须把它变成 error 返回。旧实现直接 ws.Close()，永远看不到 Status。
func TestExecStdinStreamFailureStatusReturnsError(t *testing.T) {
	const payload = "tar-payload-bytes"
	const wantMsg = "tar: cannot write: read-only file system"
	stdinCh := make(chan []byte, 1)
	got := make(chan error, 1)

	m := newExecTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := execTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if conn.Subprotocol() != "v5.channel.k8s.io" {
			got <- errProto
			return
		}
		var stdin []byte
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				got <- errNoCloseFrame
				return
			}
			if len(msg) == 2 && msg[0] == 255 && msg[1] == wsChStdin {
				// v5 stdin 关闭信号：tar 收到 EOF 后失败，回 Status Failure
				stdinCh <- stdin
				st := `{"metadata":{},"status":"Failure","message":"` + wantMsg +
					`","details":{"causes":[{"message":"` + wantMsg + `"}]}}`
				_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte{wsChError}, st...))
				got <- nil
				return
			}
			if len(msg) > 0 && msg[0] == wsChStdin {
				stdin = append(stdin, msg[1:]...)
				continue
			}
			got <- errBadFrame
			return
		}
	})

	_, err := m.ExecStdinStream(t.Context(), "conn", "ns", "pod", "", "tar xf -",
		strings.NewReader(payload))
	select {
	case serr := <-got:
		if serr != nil {
			t.Fatalf("server side: %v", serr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server never saw stdin close frame")
	}
	if err == nil {
		t.Fatal("upload failure (Status Failure) must return error, got nil")
	}
	if !strings.Contains(err.Error(), wantMsg) {
		t.Fatalf("error should carry tar failure message, got %v", err)
	}
	select {
	case stdin := <-stdinCh:
		if string(stdin) != payload {
			t.Fatalf("stdin payload corrupted: got %q", stdin)
		}
	default:
		t.Fatal("stdin payload not delivered")
	}
}

// 正向路径：Status Success 时上传成功（err == nil）。
func TestExecStdinStreamSuccessStatusReturnsNil(t *testing.T) {
	got := make(chan error, 1)
	m := newExecTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := execTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				got <- errNoCloseFrame
				return
			}
			if len(msg) == 2 && msg[0] == 255 && msg[1] == wsChStdin {
				st := `{"metadata":{},"status":"Success"}`
				_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte{wsChError}, st...))
				got <- nil
				return
			}
		}
	})

	if _, err := m.ExecStdinStream(t.Context(), "conn", "ns", "pod", "", "tar xf -",
		strings.NewReader("x")); err != nil {
		t.Fatalf("success status should not error, got %v", err)
	}
	select {
	case serr := <-got:
		if serr != nil {
			t.Fatalf("server side: %v", serr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server never saw stdin close frame")
	}
}
