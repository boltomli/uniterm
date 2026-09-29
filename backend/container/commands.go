package container

import (
	"fmt"
	"strconv"
	"strings"
)

// withNS 把 nerdctl 的全局 flag 插到子命令之前；其他运行时原样返回。
func withNS(rt Runtime, ns string, argv ...string) []string {
	out := []string{rt.Bin()}
	if rt == RuntimeNerdctl && ns != "" {
		out = append(out, "--namespace", ns)
	}
	return append(out, argv...)
}

const jsonFormat = "{{json .}}"

// psFormatDocker：显式字段模板，刻意不引用 .Size——{{json .}} 包含 Size，
// 会让 docker CLI 向 daemon 请求逐容器文件系统统计，容器多的节点上要几十秒；
// 不含 Size 的自定义格式与表格输出一样快。
const psFormatDocker = "{{.ID}}|{{.Image}}|{{.Names}}|{{.State}}|{{.Status}}|{{.Ports}}|{{.CreatedAt}}"

func psArgs(rt Runtime, ns string) []string {
	if rt == RuntimeDocker {
		return withNS(rt, ns, "ps", "-a", "--format", psFormatDocker)
	}
	if rt == RuntimeWSLC {
		argv := []string{"ps", "-a"}
		argv = append(argv, wslcFormat()...)
		return withNS(rt, ns, argv...)
	}
	return withNS(rt, ns, "ps", "-a", "--format", jsonFormat)
}

func inspectArgs(rt Runtime, ns, id string) []string {
	return withNS(rt, ns, "inspect", id)
}

func logsArgs(rt Runtime, ns, id string, tail int, follow, timestamps bool) []string {
	if rt == RuntimeWSLC {
		argv := []string{"logs", "-n", strconv.Itoa(tail)}
		if follow {
			argv = append(argv, "-f")
		}
		if timestamps {
			argv = append(argv, "-t")
		}
		return withNS(rt, ns, append(argv, id)...)
	}
	argv := []string{"logs", "--tail", strconv.Itoa(tail)}
	if timestamps {
		argv = append(argv, "--timestamps")
	}
	if follow {
		argv = append(argv, "-f")
	}
	return withNS(rt, ns, append(argv, id)...)
}

func execArgs(rt Runtime, ns, id, shell string) []string {
	return withNS(rt, ns, "exec", "-it", id, shell)
}

// execShellArgs 是文件会话的非交互 exec：无 TTY（脚本输出可安全解析），
// stdin 版加 -i 供 tar 流导入。脚本由调用方用 posixQuote 语义拼好。
func execShellArgs(rt Runtime, ns, cid, script string, stdin bool) []string {
	argv := []string{"exec"}
	if stdin {
		argv = append(argv, "-i")
	}
	return withNS(rt, ns, append(argv, cid, "sh", "-c", script)...)
}

// cpRawArgs 是 docker cp 的导出方向：把容器路径打成 tar 流写到 stdout，
// 路径前缀 "cid:" 由调用方拼好（本地/SSH runner 都不解析它）。
func cpRawArgs(rt Runtime, ns, cid, remotePath string) []string {
	return withNS(rt, ns, "cp", cid+":"+remotePath, "-")
}

// action ∈ start/stop/restart/rm/pause/unpause
func actionArgs(rt Runtime, ns, action, id string) ([]string, error) {
	if rt == RuntimeWSLC {
		switch action {
		case "start", "stop", "rm":
			return withNS(rt, ns, action, id), nil
		}
		return nil, fmt.Errorf("unsupported action %q for WSLC runtime", action)
	}
	switch action {
	case "start", "stop", "restart", "rm", "pause", "unpause":
		return withNS(rt, ns, action, id), nil
	}
	return nil, fmt.Errorf("unsupported action %q", action)
}

func renameArgs(rt Runtime, ns, id, newName string) ([]string, error) {
	if rt == RuntimeWSLC {
		return nil, fmt.Errorf("rename not supported for WSLC runtime")
	}
	return withNS(rt, ns, "rename", id, newName), nil
}

func statsArgs(rt Runtime, ns string) []string {
	if rt == RuntimeWSLC {
		argv := []string{"stats"}
		argv = append(argv, wslcFormat()...)
		return withNS(rt, ns, argv...)
	}
	return withNS(rt, ns, "stats", "--no-stream", "--format", jsonFormat)
}

func imagesArgs(rt Runtime, ns string) []string {
	if rt == RuntimeWSLC {
		argv := []string{"images"}
		argv = append(argv, wslcFormat()...)
		return withNS(rt, ns, argv...)
	}
	return withNS(rt, ns, "images", "--format", jsonFormat)
}

func pullArgs(rt Runtime, ns, image string, o TransferOptions) []string {
	return transferArgv(rt, ns, "pull", image, o)
}

func pushArgs(rt Runtime, ns, image string, o TransferOptions) []string {
	return transferArgv(rt, ns, "push", image, o)
}

// transferArgv 构造 pull/push 的 argv。insecure 参数按运行时注入：
// nerdctl 的 --insecure-registry 是全局 flag 且取 registry 地址为值，
// 放在子命令之前；podman 用命令级 --tls-verify=false；
// docker/wslc 的 CLI 不支持单命令级参数，直接忽略。
func transferArgv(rt Runtime, ns, sub, ref string, o TransferOptions) []string {
	out := withNS(rt, ns)
	if o.Insecure && rt == RuntimeNerdctl {
		if h := registryHost(ref); h != "" {
			out = append(out, "--insecure-registry", h)
		}
	}
	out = append(out, sub)
	if o.Platform != "" && rt != RuntimeWSLC {
		out = append(out, "--platform", o.Platform)
	}
	if sub == "pull" && o.AllTags && rt != RuntimeWSLC {
		out = append(out, "--all-tags")
	}
	if o.Insecure && rt == RuntimePodman {
		out = append(out, "--tls-verify=false")
	}
	return append(out, ref)
}

// registryHost 从镜像引用里提取 registry 主机段（第一个 / 之前、
// 含 . 或 : 或为 localhost 的部分）；没有 registry 段时返回空。
func registryHost(ref string) string {
	i := strings.Index(ref, "/")
	if i <= 0 {
		return ""
	}
	first := ref[:i]
	if first == "localhost" || strings.ContainsAny(first, ".:") {
		return first
	}
	return ""
}

func tagArgs(rt Runtime, ns, image, repoTag string) []string {
	return withNS(rt, ns, "tag", image, repoTag)
}

func imagePruneArgs(rt Runtime, ns string) []string {
	return withNS(rt, ns, "image", "prune", "-f")
}

func infoArgs(rt Runtime, ns string) []string {
	if rt == RuntimeWSLC {
		argv := []string{"info"}
		argv = append(argv, wslcFormat()...)
		return withNS(rt, ns, argv...)
	}
	return withNS(rt, ns, "info", "--format", jsonFormat)
}

func versionArgs(rt Runtime, ns string) []string {
	if rt == RuntimeWSLC {
		argv := []string{"version"}
		argv = append(argv, wslcFormat()...)
		return withNS(rt, ns, argv...)
	}
	return withNS(rt, ns, "version", "--format", jsonFormat)
}

// historyArgs 构造 history 的 argv；--no-trunc 避免 CreatedBy 被 CLI 截断。
// wslc 的 flag 支持度未知，按保守处理不传 --no-trunc。
func historyArgs(rt Runtime, ns, imageID string) []string {
	if rt == RuntimeWSLC {
		argv := []string{"history"}
		argv = append(argv, wslcFormat()...)
		return withNS(rt, ns, append(argv, imageID)...)
	}
	return withNS(rt, ns, "history", "--no-trunc", "--format", jsonFormat, imageID)
}

// loginArgs 构造 login 的 argv；密码由调用方通过 RunStdin 喂给 --password-stdin。
// registry 本身就是主机地址（区别于镜像引用），nerdctl 直接作为 --insecure-registry 的值。
func loginArgs(rt Runtime, registry, username string, insecure bool) []string {
	out := []string{rt.Bin()}
	if insecure && rt == RuntimeNerdctl && registry != "" {
		out = append(out, "--insecure-registry", registry)
	}
	out = append(out, "login", "--password-stdin", "-u", username)
	if insecure && rt == RuntimePodman {
		out = append(out, "--tls-verify=false")
	}
	if registry != "" {
		out = append(out, registry)
	}
	return out
}

func removeImageArgs(rt Runtime, ns, imageID string) []string {
	return withNS(rt, ns, "rmi", imageID)
}

func createArgs(rt Runtime, ns string, o CreateOptions) []string {
	argv := []string{"run", "-d"}
	if o.Name != "" {
		argv = append(argv, "--name", o.Name)
	}
	for _, p := range o.Ports {
		v := p.HostPort + ":" + p.ContainerPort
		if p.HostIP != "" {
			v = p.HostIP + ":" + v
		}
		if p.Protocol != "" && p.Protocol != "tcp" {
			v += "/" + p.Protocol
		}
		argv = append(argv, "-p", v)
	}
	for _, v := range o.Volumes {
		argv = append(argv, "-v", v)
	}
	for _, e := range o.Env {
		argv = append(argv, "-e", e)
	}
	// WSLC doesn't support --restart
	if rt != RuntimeWSLC && o.Restart != "" && o.Restart != "no" {
		argv = append(argv, "--restart", o.Restart)
	}
	argv = append(argv, o.Image)
	return withNS(rt, ns, append(argv, o.Command...)...)
}

// detectArgs: command -v 走 shell，两 runner 均支持（见各 runner 的特例处理）。
func detectArgs(rt Runtime) []string {
		return []string{"sh", "-c", "command -v " + rt.Bin()}
	}

// wslcFormat returns --format json for WSLC (vs Docker's --format "{{json .}}")
func wslcFormat() []string {
	return []string{"--format", "json"}
}

// posixQuote 供 SSHRunner 把 argv 拼成远端 sh 命令行；LocalRunner 不使用。
func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, `'`, `'\''`) + "'"
}

// JoinShellCommand 拼接 argv 为 POSIX shell 命令行。对纯安全字符的段不加引号，保持可读。
func JoinShellCommand(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" {
			parts[i] = "''"
			continue
		}
		safe := true
		for _, r := range a {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
				strings.ContainsRune("-._/:={}+", r)) {
				safe = false
				break
			}
		}
		if safe {
			parts[i] = a
		} else {
			parts[i] = posixQuote(a)
		}
	}
	return strings.Join(parts, " ")
}
