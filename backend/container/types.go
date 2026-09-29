package container

type Runtime string

const (
	RuntimeDocker  Runtime = "docker"
	RuntimePodman  Runtime = "podman"
	RuntimeNerdctl Runtime = "nerdctl"
	RuntimeWSLC    Runtime = "wslc"
)

func (r Runtime) Bin() string { return string(r) }
func (r Runtime) Valid() bool {
	return r == RuntimeDocker || r == RuntimePodman || r == RuntimeNerdctl || r == RuntimeWSLC
}

type Container struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Image     string `json:"image"`
	State     string `json:"state"`  // running / exited / paused / ...
	Status    string `json:"status"` // 人类可读，如 "Up 5 days"
	Ports     string `json:"ports"`
	CreatedAt string `json:"createdAt"`
}

type PortMapping struct {
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      string `json:"hostPort"`
	ContainerPort string `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type Mount struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	RW          bool   `json:"rw"`
}

type ContainerDetail struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Image         string        `json:"image"`
	State         string        `json:"state"`
	Status        string        `json:"status"`
	StartedAt     string        `json:"startedAt"`
	FinishedAt    string        `json:"finishedAt"`
	ExitCode      *int          `json:"exitCode"`
	OOMKilled     bool          `json:"oomKilled"`
	Pid           int           `json:"pid"`
	RestartPolicy string        `json:"restartPolicy"`
	Entrypoint    string        `json:"entrypoint"`
	Command       string        `json:"command"`
	WorkDir       string        `json:"workDir"`
	User          string        `json:"user"`
	NetworkMode   string        `json:"networkMode"`
	IP            string        `json:"ip"`
	Gateway       string        `json:"gateway"`
	Ports         []PortMapping `json:"ports"`
	Mounts        []Mount       `json:"mounts"`
	Env           []string      `json:"env"`
}

// InspectResult: 归一化详情 + inspect 原文（前端 JSON tab 展示用）
type InspectResult struct {
	Detail ContainerDetail `json:"detail"`
	Raw    string          `json:"raw"`
}

type Image struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Size       string `json:"size"`
	CreatedAt  string `json:"createdAt"`
}

// ImageLayer 是 history 输出的单层信息；Size 已转为人类可读。
type ImageLayer struct {
	ID        string `json:"id"`
	CreatedBy string `json:"createdBy"`
	Size      string `json:"size"`
	CreatedAt string `json:"createdAt"`
}

// RuntimeInfo 是概览页的运行时信息；取不到的字段留空（前端降级展示）。
// CLI 在本机或 SSH 远端执行，客户端与服务端版本可能不同，分开返回。
// Client/ServerComponent 是版本对应的组件名（如 nerdctl/containerd）。
type RuntimeInfo struct {
	ClientVersion   string `json:"clientVersion"`
	ServerVersion   string `json:"serverVersion"`
	ClientComponent string `json:"clientComponent"`
	ServerComponent string `json:"serverComponent"`
	Os              string `json:"os"`
	OsType          string `json:"osType"`
	Arch            string `json:"arch"`
	KernelVersion   string `json:"kernelVersion"`
	Driver          string `json:"driver"`
	CgroupDriver    string `json:"cgroupDriver"`
	CgroupVersion   string `json:"cgroupVersion"`
	NCPU            string `json:"ncpu"`
	MemTotal        string `json:"memTotal"`
}

type Stats struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CPUPercent string `json:"cpuPercent"`
	MemUsage   string `json:"memUsage"`
	MemPercent string `json:"memPercent"`
	NetIO      string `json:"netIO"`
	BlockIO    string `json:"blockIO"`
}

// TransferOptions 是 push/pull 共用的镜像传输参数。
// Insecure 在 docker/wslc 的 CLI 上没有单命令级参数，后端直接忽略，
// 由前端置灰并提示走 daemon 配置。
type TransferOptions struct {
	Platform string `json:"platform"` // --platform，空则不传
	AllTags  bool   `json:"allTags"`  // 仅 pull：--all-tags
	Insecure bool   `json:"insecure"` // 跳过 TLS 验证（podman/nerdctl 支持）
}

// LoginOptions 是 registry 登录参数；密码走 --password-stdin，不进进程参数。
type LoginOptions struct {
	Registry string `json:"registry"` // 空 = 运行时默认（docker.io）
	Username string `json:"username"`
	Password string `json:"password"`
	Insecure bool   `json:"insecure"`
}

type CreateOptions struct {
	Image   string        `json:"image"`
	Name    string        `json:"name"`
	Ports   []PortMapping `json:"ports"`
	Volumes []string      `json:"volumes"` // "host:container" 原文
	Env     []string      `json:"env"`     // "KEY=VAL" 原文
	Restart string        `json:"restart"` // no/always/unless-stopped/on-failure
	Command []string      `json:"command"`
}
