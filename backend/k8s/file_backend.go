package k8s

import (
	"bytes"
	"context"
	"io"
	"fmt"
	"path"
	"strings"

	"github.com/ys-ll/uniterm/backend/session"
)

// FileBackend 实现 session.ContainerFileBackend 接口，把 Pod 容器内的文件
// 操作接到 k8s exec WebSocket 通道，使 k8s Pod 复用 session.ContainerFileSession
// 的全部文件浏览/传输逻辑（浏览/上传/下载/任务管理零重复）。
type FileBackend struct {
	m         *Manager
	connID    string
	ns        string
	pod       string
	container string
}

// NewFileBackend：container 为空时使用 Pod 默认容器。
func NewFileBackend(m *Manager, connID, ns, pod, container string) *FileBackend {
	return &FileBackend{m: m, connID: connID, ns: ns, pod: pod, container: container}
}

func (b *FileBackend) ExecRun(ctx context.Context, script string) ([]byte, error) {
	return b.m.ExecRun(ctx, b.connID, b.ns, b.pod, b.container, script)
}

func (b *FileBackend) ExecStdin(ctx context.Context, script string, stdin []byte) ([]byte, error) {
	return b.m.ExecStdinStream(ctx, b.connID, b.ns, b.pod, b.container, script, bytes.NewReader(stdin))
}

func (b *FileBackend) ExecStdinStream(ctx context.Context, script string, stdin io.Reader) ([]byte, error) {
	return b.m.ExecStdinStream(ctx, b.connID, b.ns, b.pod, b.container, script, stdin)
}

// CpRaw 打开容器路径的 tar 导出流。k8s 无 cp-to-stdout，一律用容器内
// `tar cf - -C <dir> <base>`：根 entry 名为路径 basename，与
// ContainerFileSession 的解析约定一致。
func (b *FileBackend) CpRaw(ctx context.Context, containerPath string) (session.RawStream, error) {
	script := "tar cf - -C " + shQuote(path.Dir(containerPath)) + " " + shQuote(path.Base(containerPath))
	rs, err := b.m.ExecRaw(ctx, b.connID, b.ns, b.pod, b.container, script)
	if err != nil {
		return nil, err
	}
	// *execRawStream 的方法集与 session.RawStream 一致，可直接赋值
	return rs, nil
}

// CpToFile/HostRun/HostRaw 是 nerdctl 等"cp stdout 未实现"运行时的临时文件
// 中转通道；k8s 路径用容器内 tar 直接导出，不触发该回退，仅提供 stub。
func (b *FileBackend) CpToFile(ctx context.Context, cid, remotePath, destPath string) error {
	return fmt.Errorf("k8s file backend: host temp-file fallback not available")
}

func (b *FileBackend) HostRun(ctx context.Context, argv []string) ([]byte, error) {
	return nil, fmt.Errorf("k8s file backend: host commands not available")
}

func (b *FileBackend) HostRaw(ctx context.Context, argv []string) (session.RawStream, error) {
	return nil, fmt.Errorf("k8s file backend: host commands not available")
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
