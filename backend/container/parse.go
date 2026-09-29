package container

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ys-ll/uniterm/backend/log"
)

// jsonLines 逐行解析 line-delimited JSON；坏行跳过并记日志。
func jsonLines(out []byte, fn func(map[string]any)) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return
	}
	// Some runtimes (e.g. WSLC) emit a JSON array instead of line-delimited JSON.
	if out[0] == '[' {
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err == nil {
			for _, m := range arr {
				fn(m)
			}
			return
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			log.Writef("[container] skip bad json line: %v", err)
			continue
		}
		fn(m)
	}
}

// pick 按别名表取第一个存在的字段。
func pick(m map[string]any, keys ...string) (any, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v, true
		}
	}
	return nil, false
}

func pickStr(m map[string]any, keys ...string) string {
	v, ok := pick(m, keys...)
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%v", t)
	default:
		return ""
	}
}

// pickStrList 处理 "a" 与 ["a","b"] 两种形态（docker vs podman 的 Names）。
func pickStrList(m map[string]any, keys ...string) []string {
	v, ok := pick(m, keys...)
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// formatPortObjects 把结构化端口对象数组格式化为 docker 风格的字符串，
// 如 0.0.0.0:9090->9090/tcp。支持 podman 和 WSLC (OCI) 两种格式。
func formatPortObjects(arr []any) string {
	var parts []string
	for _, e := range arr {
		p, ok := e.(map[string]any)
		if !ok {
			continue
		}
		hostPort := pickStr(p, "host_port", "HostPort", "HostPort")
		ctrPort := pickStr(p, "container_port", "ContainerPort")
		if ctrPort == "" {
			continue
		}
		proto := pickStr(p, "protocol", "Protocol")
		// WSLC emits numeric protocol (6 = TCP, 17 = UDP).
		if proto == "6" {
			proto = "tcp"
		} else if proto == "17" {
			proto = "udp"
		} else if proto == "" {
			proto = "tcp"
		}
		s := ""
		if hostPort != "" {
			// WSLC uses "BindingAddress" for the host IP.
			hostIP := pickStr(p, "host_ip", "HostIp", "BindingAddress")
			if hostIP == "" {
				hostIP = "0.0.0.0"
			}
			s = hostIP + ":" + hostPort + "->"
		}
		parts = append(parts, s+ctrPort+"/"+proto)
	}
	return strings.Join(parts, ", ")
}

// pickCreated 取创建时间并归一化为本地时间 "2006-01-02 15:04:05"。跨运行时字段差异：
//   - docker/nerdctl：CreatedAt 是带时区后缀的字符串（"2026-07-27 09:22:18 +0800 CST"）
//   - podman ps：CreatedAt 为空、Created 是 RFC3339
//   - podman images：Created 是 Unix 时间戳（数字）
func pickCreated(m map[string]any) string {
	const outLayout = "2006-01-02 15:04:05"
	// docker/nerdctl 的 Go 默认时间格式，含数字时区与时区名。
	layouts := []string{time.RFC3339, "2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05 -0700"}
	for _, k := range []string{"CreatedAt", "Created", "CreatedSince", "created", "createdAt", "createdSince"} {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if t == "" {
				continue
			}
			for _, l := range layouts {
				if ts, err := time.Parse(l, t); err == nil {
					return ts.Local().Format(outLayout)
				}
			}
			return t
		case float64:
			return time.Unix(int64(t), 0).Format(outLayout)
		}
	}
	return ""
}

// normalizeState converts OCI lowercase state names and numeric status codes
// to Docker-style strings ("running", "exited", "paused", etc.).
func normalizeState(state, status string) string {
	// WSLC numeric state codes (no status field available).
	switch state {
	case "0", "1":
		return "created"
	case "2":
		return "running"
	case "3":
		return "exited"
	case "4":
		return "paused"
	case "5":
		return "exited"
	}
	ls := strings.ToLower(state)
	switch {
	case ls == "running":
		return "running"
	case ls == "stopped" || ls == "terminated" || ls == "exited" || strings.HasPrefix(ls, "exited"):
		return "exited"
	case ls == "paused" || strings.HasPrefix(ls, "paused"):
		return "paused"
	case ls == "created" || ls == "restarting":
		return state
	case ls == "":
		// No state field; derive from status string.
	default:
		// Unrecognized — try status-based derivation.
	}
	// Derive from the Status field (docker/nerdctl/podman).
	switch {
	case strings.HasPrefix(status, "Up"), status == "Running", strings.ToLower(status) == "running":
		return "running"
	case strings.HasPrefix(status, "Exited"), strings.ToLower(status) == "exited",
		strings.ToLower(status) == "stopped", strings.ToLower(status) == "terminated":
		return "exited"
	case strings.HasPrefix(status, "Paused"), strings.ToLower(status) == "paused":
		return "paused"
	case status != "":
		return "unknown"
	}
	return state
}

func ParseContainers(rt Runtime, out []byte) ([]Container, error) {
	// docker 走竖线分隔的字段模板（见 psFormatDocker 的说明）
	if rt == RuntimeDocker {
		return parseDockerPS(out), nil
	}
	var list []Container
	jsonLines(out, func(m map[string]any) {
		name := ""
		if names := pickStrList(m, "Names", "Name", "names", "name"); len(names) > 0 {
			name = names[0]
		}
		ports := pickStr(m, "Ports", "ports")
		if ports == "" {
			// podman 把 Ports 输出成对象数组；docker/nerdctl 是字符串。
			if arr, ok := m["Ports"].([]any); ok && len(arr) > 0 {
				ports = formatPortObjects(arr)
			} else if ps := pickStrList(m, "Ports", "ports"); len(ps) > 0 {
				ports = strings.Join(ps, ", ")
			}
		}
		state := pickStr(m, "State", "state")
		// OCI schema uses lowercase and may emit numeric status codes;
		// normalize to Docker-style strings so the UI conditionals work.
		state = normalizeState(state, pickStr(m, "Status", "status"))
		list = append(list, Container{
			ID:        pickStr(m, "ID", "Id", "ContainerID", "id"),
			Name:      name,
			Image:     pickStr(m, "Image", "image"),
			State:     state,
			Status:    pickStr(m, "Status", "status"),
			Ports:     ports,
			CreatedAt: pickCreated(m),
		})
	})
	return list, nil
}

// parseDockerPS 解析 docker ps 的竖线分隔输出（ID|Image|Names|State|Status|Ports|CreatedAt）。
// docker 模板输出的字段值不含竖线（镜像名/状态/端口/时间均无此字符）。
func parseDockerPS(out []byte) []Container {
	var list []Container
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 7)
		if len(parts) != 7 {
			log.Writef("[container] skip bad ps line: %q", line)
			continue
		}
		list = append(list, Container{
			ID:        parts[0],
			Image:     parts[1],
			Name:      parts[2],
			State:     normalizeState(parts[3], parts[4]),
			Status:    parts[4],
			CreatedAt: parts[6],
			Ports:     parts[5],
		})
	}
	return list
}

// pickSize 取镜像大小。docker/nerdctl 是人类可读字符串（"192MB"）；
// podman images 的 Size 是字节数（数字），需转成人类可读。
func pickSize(m map[string]any) string {
	v, ok := pick(m, "Size", "size")
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return humanSize(int64(t))
	}
	return ""
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func ParseImages(rt Runtime, out []byte) ([]Image, error) {
	var list []Image
	jsonLines(out, func(m map[string]any) {
		repo := pickStr(m, "Repository", "repository")
		if names := pickStrList(m, "Repository", "repository", "Names", "names"); repo == "" && len(names) > 0 {
			repo = names[0]
		}
		tag := pickStr(m, "Tag", "tag")
		if repo == "" {
			repo = "<none>"
		}
		if tag == "" {
			tag = "<none>"
		}
		list = append(list, Image{
			ID:         pickStr(m, "ID", "Id", "id"),
			Repository: repo,
			Tag:        tag,
			Size:       pickSize(m),
			CreatedAt:  pickCreated(m),
		})
	})
	return list, nil
}

// nestedMap 取 m[k] 下第一个子对象（podman info 的 host/version/store 嵌套）。
func nestedMap(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if sub, ok := v.(map[string]any); ok {
				return sub
			}
		}
	}
	return nil
}

func orDefault(cur, alt string) string {
	if cur == "" {
		return alt
	}
	return cur
}

func bytesHuman(v any) string {
	if n, ok := v.(float64); ok {
		return humanSize(int64(n))
	}
	return ""
}

// ParseRuntimeInfo 归一化 info 输出：docker/nerdctl 是扁平键，
// podman 是 host/version/store 嵌套；取不到的字段留空。
// Os 是发行版名称（如 "Ubuntu 22.04"），OsType 是内核类型（linux/windows）。
func ParseRuntimeInfo(rt Runtime, out []byte) RuntimeInfo {
	var info RuntimeInfo
	jsonLines(out, func(m map[string]any) {
		if info.ServerVersion != "" {
			return
		}
		if host := nestedMap(m, "host"); host != nil {
			// podman：host.os 是内核类型，发行版在 Distribution 对象里
			info.OsType = pickStr(host, "osType", "os", "OS")
			if dist := nestedMap(host, "Distribution", "distribution"); dist != nil {
				name := pickStr(dist, "Distribution", "distribution", "Name", "name")
				ver := pickStr(dist, "Version", "version")
				info.Os = strings.TrimSpace(name + " " + ver)
			}
			info.Arch = pickStr(host, "Arch", "arch", "Architecture", "architecture")
			info.KernelVersion = pickStr(host, "Kernel", "kernel", "KernelVersion", "kernelVersion")
			info.CgroupDriver = pickStr(host, "CgroupsDriver", "cgroupsDriver", "cgroupManager", "CgroupDriver")
			info.CgroupVersion = pickStr(host, "CgroupsVersion", "cgroupsVersion", "CgroupVersion")
			info.NCPU = pickStr(host, "CPUs", "cpus", "NCPU", "ncpu")
			if v, ok := pick(host, "Memory", "memory"); ok {
				info.MemTotal = bytesHuman(v)
			}
			if st := nestedMap(m, "store"); st != nil {
				info.Driver = pickStr(st, "GraphDriverName", "graphDriverName", "Driver", "driver")
			}
			if ver := nestedMap(m, "version"); ver != nil {
				info.ServerVersion = pickStr(ver, "Version", "version")
			}
		}
		info.ServerVersion = orDefault(info.ServerVersion, pickStr(m, "ServerVersion", "serverVersion"))
		info.Os = orDefault(info.Os, pickStr(m, "OperatingSystem", "operatingSystem"))
		info.OsType = orDefault(info.OsType, pickStr(m, "OSType", "osType"))
		info.Arch = orDefault(info.Arch, pickStr(m, "Architecture", "architecture"))
		info.KernelVersion = orDefault(info.KernelVersion, pickStr(m, "KernelVersion", "kernelVersion"))
		info.Driver = orDefault(info.Driver, pickStr(m, "Driver", "driver"))
		info.CgroupDriver = orDefault(info.CgroupDriver, pickStr(m, "CgroupDriver", "cgroupDriver"))
		info.CgroupVersion = orDefault(info.CgroupVersion, pickStr(m, "CgroupVersion", "cgroupVersion"))
		info.NCPU = orDefault(info.NCPU, pickStr(m, "NCPU", "ncpu"))
		if info.MemTotal == "" {
			if v, ok := pick(m, "MemTotal", "memTotal"); ok {
				info.MemTotal = bytesHuman(v)
			}
		}
	})
	return info
}

// ParseVersions 从 version 输出提取客户端/服务端版本及服务端组件名。
// docker/podman 是 Client/Server 两个对象；nerdctl 的 Server 无 Version 字段，
// 版本在 Components 数组（containerd 组件）里，且 Server 可能是数组（多后端）。
func ParseVersions(rt Runtime, out []byte) (client, server, serverComp string) {
	jsonLines(out, func(m map[string]any) {
		if cli := nestedMap(m, "Client", "client"); cli != nil {
			client = orDefault(client, pickStr(cli, "Version", "version"))
		}
		switch srv := m["Server"].(type) {
		case map[string]any:
			server = orDefault(server, pickStr(srv, "Version", "version"))
			server = orDefault(server, componentVersion(srv))
			serverComp = orDefault(serverComp, componentName(srv))
		case []any:
			for _, e := range srv {
				if m0, ok := e.(map[string]any); ok {
					server = orDefault(server, pickStr(m0, "Version", "version"))
					server = orDefault(server, componentVersion(m0))
					serverComp = orDefault(serverComp, componentName(m0))
				}
			}
		}
		if client == "" {
			client = pickStr(m, "Version", "version")
		}
	})
	return client, server, serverComp
}

// componentName 取 Components 里的第一个组件名（docker 的 "Engine"、
// nerdctl 的 "containerd"）；没有 Components 时返回空。
func componentName(m map[string]any) string {
	comps, ok := m["Components"].([]any)
	if !ok || len(comps) == 0 {
		return ""
	}
	if cm, ok := comps[0].(map[string]any); ok {
		return pickStr(cm, "Name", "name")
	}
	return ""
}

// componentVersion 从 nerdctl 的 Server.Components 里取容器运行时（containerd）版本；
// 没有 containerd 项时取第一个组件。
func componentVersion(m map[string]any) string {
	comps, ok := m["Components"].([]any)
	if !ok {
		return ""
	}
	for _, c := range comps {
		if cm, ok := c.(map[string]any); ok {
			if pickStr(cm, "Name", "name") == "containerd" {
				return pickStr(cm, "Version", "version")
			}
		}
	}
	if len(comps) > 0 {
		if cm, ok := comps[0].(map[string]any); ok {
			return pickStr(cm, "Version", "version")
		}
	}
	return ""
}

// ParseImageHistory 解析 history --format json 的分层输出。
// CreatedSince（如 "3 weeks ago"）比绝对时间更适合展示，优先取它。
func ParseImageHistory(rt Runtime, out []byte) ([]ImageLayer, error) {
	var list []ImageLayer
	jsonLines(out, func(m map[string]any) {
		list = append(list, ImageLayer{
			ID:        pickStr(m, "ID", "Id", "id"),
			CreatedBy: pickStr(m, "CreatedBy", "createdBy"),
			Size:      pickSize(m),
			CreatedAt: pickStr(m, "CreatedSince", "createdSince", "CreatedAt", "created"),
		})
	})
	return list, nil
}

func ParseStats(rt Runtime, out []byte) ([]Stats, error) {
	var list []Stats
	jsonLines(out, func(m map[string]any) {
		name := pickStr(m, "Name", "name")
		if names := pickStrList(m, "Name", "Names", "names", "name"); name == "" && len(names) > 0 {
			name = names[0]
		}
		list = append(list, Stats{
			ID:         pickStr(m, "ID", "Container", "ContainerID", "id"),
			Name:       name,
			CPUPercent: pickStr(m, "CPUPerc", "CPUPercent", "cpu_percent"),
			MemUsage:   pickStr(m, "MemUsage", "mem_usage"),
			MemPercent: pickStr(m, "MemPerc", "MemPercent", "mem_percent"),
			NetIO:      pickStr(m, "NetIO", "net_io"),
			BlockIO:    pickStr(m, "BlockIO", "block_io"),
		})
	})
	return list, nil
}

// ParseInspect 解析 `inspect` 的 JSON 数组输出（取 [0]），归一化为 ContainerDetail。
// 支持 Docker/Podman/Nerdctl (Docker schema) 和 WSLC (OCI schema) 两种格式。
func ParseInspect(rt Runtime, raw []byte) (ContainerDetail, error) {
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return ContainerDetail{}, fmt.Errorf("inspect: %w", err)
	}
	if len(arr) == 0 {
		return ContainerDetail{}, fmt.Errorf("inspect: empty result")
	}
	m := arr[0]

	sub := func(key string) map[string]any {
		if v, ok := m[key].(map[string]any); ok {
			return v
		}
		return map[string]any{}
	}
	cfg, state, host, net := sub("Config"), sub("State"), sub("HostConfig"), sub("NetworkSettings")

	strList := func(v any) []string {
		arr, ok := v.([]any)
		if !ok {
			return nil
		}
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	join := func(v any) string { return strings.Join(strList(v), " ") }

	// OCI schema uses lowercase field names; Docker schema uses PascalCase
	// State: OCI uses "status" (lowercase), Docker uses "Status"
	stateStatus := pickStr(state, "Status", "status")
	// OCI uses "pid" (lowercase), Docker uses "Pid"
	statePid := func() int {
		if pid, ok := state["Pid"].(float64); ok {
			return int(pid)
		}
		if pid, ok := state["pid"].(float64); ok {
			return int(pid)
		}
		return 0
	}
	// OCI uses "exitCode" (lowercase), Docker uses "ExitCode"
	stateExitCode := func() *int {
		if code, ok := state["ExitCode"].(float64); ok {
			c := int(code)
			return &c
		}
		if code, ok := state["exitCode"].(float64); ok {
			c := int(code)
			return &c
		}
		return nil
	}
	// OCI uses "oomKilled" (lowercase), Docker uses "OOMKilled"
	stateOOMKilled := state["OOMKilled"] == true || state["oomKilled"] == true

	d := ContainerDetail{
		ID:            pickStr(m, "Id", "ID"),
		Name:          strings.TrimPrefix(pickStr(m, "Name"), "/"),
		Image:         pickStr(cfg, "Image"),
		State:         stateStatus,
		StartedAt:     pickStr(state, "StartedAt", "startedAt"),
		FinishedAt:    pickStr(state, "FinishedAt", "finishedAt"),
		OOMKilled:     stateOOMKilled,
		RestartPolicy: pickStr(host, "RestartPolicy", "RestartPolicyName"),
		Entrypoint:    join(cfg["Entrypoint"]),
		Command:       join(cfg["Cmd"]),
		WorkDir:       pickStr(cfg, "WorkingDir"),
		User:          pickStr(cfg, "User"),
		NetworkMode:   pickStr(host, "NetworkMode"),
		IP:            pickStr(net, "IPAddress", "IP"),
		Gateway:       pickStr(net, "Gateway"),
		Env:           strList(cfg["Env"]),
		ExitCode:      stateExitCode(),
		Pid:           statePid(),
	}

	if rp, ok := host["RestartPolicy"].(map[string]any); ok {
		d.RestartPolicy = pickStr(rp, "Name")
	}

	// OCI schema uses lowercase "mounts" (flat array), Docker uses "Mounts"
	mountsRaw, hasMounts := m["Mounts"]
	if !hasMounts {
		mountsRaw, hasMounts = m["mounts"]
	}
	if mounts, ok := mountsRaw.([]any); ok && hasMounts {
		for _, mv := range mounts {
			if mm, ok := mv.(map[string]any); ok {
				d.Mounts = append(d.Mounts, Mount{
					Source:      pickStr(mm, "Source", "source"),
					Destination: pickStr(mm, "Destination", "destination"),
					RW:          mm["RW"] == true || mm["rw"] == true,
				})
			}
		}
	}

	// OCI schema: port mappings may be in "networkSettings.networks" as a flat map
	// Docker schema: "NetworkSettings.Ports" as a map of port->bindings
	if ports, ok := net["Ports"].(map[string]any); ok {
		// Docker schema
		for key, bindings := range ports {
			port, proto := key, "tcp"
			if i := strings.LastIndex(key, "/"); i >= 0 {
				port, proto = key[:i], key[i+1:]
			}
			list, ok := bindings.([]any)
			if !ok {
				continue // null: exposed but not published
			}
			for _, bv := range list {
				if bm, ok := bv.(map[string]any); ok {
					d.Ports = append(d.Ports, PortMapping{
						HostIP:        pickStr(bm, "HostIp"),
						HostPort:      pickStr(bm, "HostPort"),
						ContainerPort: port,
						Protocol:      proto,
					})
				}
			}
		}
	} else if networks, ok := net["networks"].(map[string]any); ok {
		// OCI schema: networks is a flat map of network_name -> settings
		// Extract IP from first network that has an IP
		for _, netSettings := range networks {
			if netMap, ok := netSettings.(map[string]any); ok {
				if ip := pickStr(netMap, "IPAddress", "ip"); ip != "" && d.IP == "" {
					d.IP = ip
				}
				if gw := pickStr(netMap, "Gateway", "gateway"); gw != "" && d.Gateway == "" {
					d.Gateway = gw
				}
			}
		}
	}

	// OCI schema: port mappings may be in top-level "ports" array
	if portsArr, ok := m["ports"].([]any); ok && len(d.Ports) == 0 {
		for _, pv := range portsArr {
			if pm, ok := pv.(map[string]any); ok {
				proto := pickStr(pm, "protocol")
				if proto == "" {
					proto = "tcp"
				}
				d.Ports = append(d.Ports, PortMapping{
					HostIP:        pickStr(pm, "hostIP", "host_ip"),
					HostPort:      fmt.Sprintf("%v", pm["hostPort"]),
					ContainerPort: fmt.Sprintf("%v", pm["containerPort"]),
					Protocol:      proto,
				})
			}
		}
	}

	sort.Slice(d.Ports, func(i, j int) bool {
		pi, ei := strconv.Atoi(d.Ports[i].ContainerPort)
		pj, ej := strconv.Atoi(d.Ports[j].ContainerPort)
		if ei == nil && ej == nil {
			return pi < pj
		}
		return d.Ports[i].ContainerPort < d.Ports[j].ContainerPort
	})

	d.Status = d.State
	// 保证切片非 nil：Go nil slice 序列化为 JSON null，前端 .map() 会崩
	if d.Ports == nil {
		d.Ports = []PortMapping{}
	}
	if d.Mounts == nil {
		d.Mounts = []Mount{}
	}
	if d.Env == nil {
		d.Env = []string{}
	}
	return d, nil
}

// ParseNamespaces 解析 `nerdctl namespace ls` 表格输出（spike 校准格式）。
func ParseNamespaces(out []byte) []string {
	var ns []string
	for i, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if i == 0 && strings.EqualFold(fields[0], "NAME") {
			continue
		}
		ns = append(ns, fields[0])
	}
	return ns
}
