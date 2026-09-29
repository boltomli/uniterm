package container

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/ys-ll/uniterm/backend/session"
)

type Provider struct {
	rt     Runtime
	ns     string
	runner Runner
}

func NewProvider(rt Runtime, ns string, r Runner) *Provider {
	if rt == RuntimeNerdctl && ns == "" {
		ns = "default"
	}
	return &Provider{rt: rt, ns: ns, runner: r}
}

func (p *Provider) Runtime() Runtime  { return p.rt }
func (p *Provider) Namespace() string { return p.ns }

func (p *Provider) List(ctx context.Context) ([]Container, error) {
	out, err := p.runner.Run(ctx, psArgs(p.rt, p.ns))
	if err != nil {
		return nil, err
	}
	return ParseContainers(p.rt, out)
}

func (p *Provider) Inspect(ctx context.Context, id string) (InspectResult, error) {
	out, err := p.runner.Run(ctx, inspectArgs(p.rt, p.ns, id))
	if err != nil {
		return InspectResult{}, err
	}
	d, err := ParseInspect(p.rt, out)
	if err != nil {
		return InspectResult{}, err
	}
	return InspectResult{Detail: d, Raw: string(out)}, nil
}

func (p *Provider) Action(ctx context.Context, id, action string) error {
	argv, err := actionArgs(p.rt, p.ns, action, id)
	if err != nil {
		return err
	}
	_, err = p.runner.Run(ctx, argv)
	return err
}

func (p *Provider) Rename(ctx context.Context, id, newName string) error {
	if strings.TrimSpace(newName) == "" {
		return fmt.Errorf("name required")
	}
	argv, err := renameArgs(p.rt, p.ns, id, newName)
	if err != nil {
		return err
	}
	_, err = p.runner.Run(ctx, argv)
	return err
}

func (p *Provider) Stats(ctx context.Context) ([]Stats, error) {
	out, err := p.runner.Run(ctx, statsArgs(p.rt, p.ns))
	if err != nil {
		return nil, err
	}
	return ParseStats(p.rt, out)
}

func (p *Provider) Images(ctx context.Context) ([]Image, error) {
	out, err := p.runner.Run(ctx, imagesArgs(p.rt, p.ns))
	if err != nil {
		return nil, err
	}
	return ParseImages(p.rt, out)
}

func (p *Provider) RemoveImage(ctx context.Context, imageID string) error {
	_, err := p.runner.Run(ctx, removeImageArgs(p.rt, p.ns, imageID))
	return err
}

func (p *Provider) Create(ctx context.Context, o CreateOptions) error {
	if strings.TrimSpace(o.Image) == "" {
		return fmt.Errorf("image required")
	}
	_, err := p.runner.Run(ctx, createArgs(p.rt, p.ns, o))
	return err
}

func (p *Provider) Logs(ctx context.Context, id string, tail int, follow, timestamps bool) (LineStream, error) {
	return p.runner.RunStream(ctx, logsArgs(p.rt, p.ns, id, tail, follow, timestamps))
}

func (p *Provider) Pull(ctx context.Context, image string, o TransferOptions) (LineStream, error) {
	if strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("image required")
	}
	return p.runner.RunStream(ctx, pullArgs(p.rt, p.ns, image, o))
}

func (p *Provider) Push(ctx context.Context, image string, o TransferOptions) (LineStream, error) {
	if strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("image ref required")
	}
	return p.runner.RunStream(ctx, pushArgs(p.rt, p.ns, image, o))
}

func (p *Provider) TagImage(ctx context.Context, image, repoTag string) error {
	if strings.TrimSpace(image) == "" || strings.TrimSpace(repoTag) == "" {
		return fmt.Errorf("image and tag required")
	}
	_, err := p.runner.Run(ctx, tagArgs(p.rt, p.ns, image, repoTag))
	return err
}

func (p *Provider) ImagePrune(ctx context.Context) error {
	_, err := p.runner.Run(ctx, imagePruneArgs(p.rt, p.ns))
	return err
}

// Info 汇总概览页运行时信息；info/version 任一失败都容忍（概览允许缺数据）。
// version 命令区分客户端/服务端版本；podman/nerdctl 客户端与服务端同体，
// version 命令缺 Client 段时以服务端版本兜底，避免客户端版本显示为空。
// 两个 CLI 调用并发执行（docker CLI 单次调用冷启动就要数秒，串行会翻倍）。
func (p *Provider) Info(ctx context.Context) (RuntimeInfo, error) {
	var (
		wg         sync.WaitGroup
		infoOut    []byte
		infoErr    error
		versionOut []byte
		versionErr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		infoOut, infoErr = p.runner.Run(ctx, infoArgs(p.rt, p.ns))
	}()
	go func() {
		defer wg.Done()
		versionOut, versionErr = p.runner.Run(ctx, versionArgs(p.rt, p.ns))
	}()
	wg.Wait()

	var info RuntimeInfo
	if infoErr == nil {
		info = ParseRuntimeInfo(p.rt, infoOut)
	}
	if versionErr == nil {
		client, server, serverComp := ParseVersions(p.rt, versionOut)
		info.ServerVersion = orDefault(info.ServerVersion, server)
		info.ClientVersion = orDefault(client, info.ServerVersion)
		info.ServerComponent = orDefault(serverComp, defaultServerComponent(p.rt))
	}
	if info.ClientComponent == "" {
		info.ClientComponent = string(p.rt)
	}
	if info.ServerComponent == "" {
		info.ServerComponent = defaultServerComponent(p.rt)
	}
	return info, nil
}

// defaultServerComponent 运行时默认的服务端组件名（version 输出没有 Components 时的兜底）。
func defaultServerComponent(rt Runtime) string {
	if rt == RuntimeNerdctl {
		return "containerd"
	}
	return string(rt)
}

// ImageHistory 返回镜像的分层信息（history 输出）。
func (p *Provider) ImageHistory(ctx context.Context, imageID string) ([]ImageLayer, error) {
	out, err := p.runner.Run(ctx, historyArgs(p.rt, p.ns, imageID))
	if err != nil {
		return nil, err
	}
	return ParseImageHistory(p.rt, out)
}

// ImageInspect 返回镜像 inspect 的原始 JSON（镜像结构与容器不同，不归一化）。
func (p *Provider) ImageInspect(ctx context.Context, imageID string) (string, error) {
	out, err := p.runner.Run(ctx, inspectArgs(p.rt, p.ns, imageID))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Login 走 --password-stdin：密码不进进程参数，不落 shell 历史。
func (p *Provider) Login(ctx context.Context, o LoginOptions) error {
	if strings.TrimSpace(o.Username) == "" {
		return fmt.Errorf("username required")
	}
	stdin := append([]byte(o.Password), '\n')
	_, err := p.runner.RunStdin(ctx, loginArgs(p.rt, o.Registry, o.Username, o.Insecure), stdin)
	return err
}

func (p *Provider) Exec(ctx context.Context, id, shell string, cols, rows int) (PTYStream, error) {
	if shell == "" {
		shell = "sh"
	}
	return p.runner.RunPTY(ctx, execArgs(p.rt, p.ns, id, shell), cols, rows)
}

// --- 容器文件会话通道（session.ContainerFileSession 的后端）----------------

// Runner 供外部会话层使用。
func (p *Provider) Runner() Runner { return p.runner }

// FileExecRun 在容器内以 sh -c 执行脚本，返回 stdout；stderr 并入错误。
func (p *Provider) FileExecRun(ctx context.Context, cid, script string) ([]byte, error) {
	return p.runner.Run(ctx, execShellArgs(p.rt, p.ns, cid, script, false))
}

// FileExecStdin 带标准输入在容器内执行脚本（tar 流导入）。
func (p *Provider) FileExecStdin(ctx context.Context, cid, script string, stdin []byte) ([]byte, error) {
	return p.runner.RunStdin(ctx, execShellArgs(p.rt, p.ns, cid, script, true), stdin)
}

// FileExecStdinStream 流式标准输入（本机 tar 流直达容器，大文件不落内存）。
func (p *Provider) FileExecStdinStream(ctx context.Context, cid, script string, stdin io.Reader) ([]byte, error) {
	return p.runner.RunStdinStream(ctx, execShellArgs(p.rt, p.ns, cid, script, true), stdin)
}

// FileCpToFile 把容器路径复制到宿主机文件（cp 的常规用法，nerdctl 等都支持；
// stdout 导出未实现的运行时用它中转）。
func (p *Provider) FileCpToFile(ctx context.Context, cid, remotePath, destPath string) error {
	_, err := p.runner.Run(ctx, withNS(p.rt, p.ns, "cp", cid+":"+remotePath, destPath))
	return err
}

// HostRun 在宿主机（本地或 SSH 远端）执行命令，返回 stdout。
func (p *Provider) HostRun(ctx context.Context, argv []string) ([]byte, error) {
	return p.runner.Run(ctx, argv)
}

// HostRaw 打开宿主机命令的 stdout 二进制流。
func (p *Provider) HostRaw(ctx context.Context, argv []string) (RawStream, error) {
	return p.runner.RunRaw(ctx, argv)
}

// FileCpRaw 打开容器路径的 tar 导出流（docker cp <cid>:<path> -）。
func (p *Provider) FileCpRaw(ctx context.Context, cid, remotePath string) (RawStream, error) {
	return p.runner.RunRaw(ctx, cpRawArgs(p.rt, p.ns, cid, remotePath))
}

// FileBackend 让 session 层的容器文件会话通过 Provider 通道操作容器文件系统。
// 容器包已依赖 session 包，因此由本包实现 session 侧定义的接口（结构化匹配）。
type FileBackend struct {
	p   *Provider
	cid string
}

func NewFileBackend(p *Provider, cid string) *FileBackend { return &FileBackend{p: p, cid: cid} }

func (b *FileBackend) ExecRun(ctx context.Context, script string) ([]byte, error) {
	return b.p.FileExecRun(ctx, b.cid, script)
}

func (b *FileBackend) ExecStdin(ctx context.Context, script string, stdin []byte) ([]byte, error) {
	return b.p.FileExecStdin(ctx, b.cid, script, stdin)
}

func (b *FileBackend) ExecStdinStream(ctx context.Context, script string, stdin io.Reader) ([]byte, error) {
	return b.p.FileExecStdinStream(ctx, b.cid, script, stdin)
}

func (b *FileBackend) CpToFile(ctx context.Context, cid, remotePath, destPath string) error {
	return b.p.FileCpToFile(ctx, cid, remotePath, destPath)
}

func (b *FileBackend) HostRun(ctx context.Context, argv []string) ([]byte, error) {
	return b.p.HostRun(ctx, argv)
}

func (b *FileBackend) HostRaw(ctx context.Context, argv []string) (session.RawStream, error) {
	rs, err := b.p.HostRaw(ctx, argv)
	if err != nil {
		return nil, err
	}
	return rs, nil
}

func (b *FileBackend) CpRaw(ctx context.Context, containerPath string) (session.RawStream, error) {
	rs, err := b.p.FileCpRaw(ctx, b.cid, containerPath)
	if err != nil {
		return nil, err
	}
	return rs, nil // container.RawStream 与 session.RawStream 方法集一致，可直接赋值
}

// Namespaces 仅 nerdctl 有意义。
func (p *Provider) Namespaces(ctx context.Context) ([]string, error) {
	if p.rt != RuntimeNerdctl {
		return nil, fmt.Errorf("namespaces only supported on nerdctl")
	}
	out, err := p.runner.Run(ctx, []string{p.rt.Bin(), "namespace", "ls"})
	if err != nil {
		return nil, err
	}
	return ParseNamespaces(out), nil
}

// DetectRuntimes 探测候选运行时中哪些可用（用于连接失败时的诊断）。
func DetectRuntimes(ctx context.Context, r Runner) []Runtime {
	var found []Runtime
	for _, rt := range []Runtime{RuntimeDocker, RuntimePodman, RuntimeNerdctl, RuntimeWSLC} {
		if _, err := r.Run(ctx, detectArgs(rt)); err == nil {
			found = append(found, rt)
		}
	}
	return found
}

// ValidateRuntime 校验所选运行时可用；不可用则探测其他并返回诊断错误。
func ValidateRuntime(ctx context.Context, rt Runtime, r Runner) error {
	if _, err := r.Run(ctx, detectArgs(rt)); err == nil {
		return nil
	}
	found := DetectRuntimes(ctx, r)
	if len(found) == 0 {
		return fmt.Errorf("container runtime %q not found; none of docker/podman/nerdctl/wslc detected", rt)
	}
	names := make([]string, len(found))
	for i, f := range found {
		names[i] = string(f)
	}
	return fmt.Errorf("container runtime %q not found; detected available: %s", rt, strings.Join(names, ", "))
}
