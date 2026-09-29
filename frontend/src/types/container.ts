export interface ContainerInfo {
  id: string
  name: string
  image: string
  state: string
  status: string
  ports: string
  createdAt: string
}

export interface PortMapping {
  hostIp?: string
  hostPort: string
  containerPort: string
  protocol: string
}

export interface ContainerMount {
  source: string
  destination: string
  rw: boolean
}

export interface ContainerDetail {
  id: string
  name: string
  image: string
  state: string
  status: string
  startedAt: string
  finishedAt: string
  exitCode?: number
  oomKilled: boolean
  pid: number
  restartPolicy: string
  entrypoint: string
  command: string
  workDir: string
  user: string
  networkMode: string
  ip: string
  gateway: string
  ports: PortMapping[]
  mounts: ContainerMount[]
  env: string[]
}

export interface InspectResult {
  detail: ContainerDetail
  raw: string
}

export interface ContainerImage {
  id: string
  repository: string
  tag: string
  size: string
  createdAt: string
}

// push/pull 共用的传输参数；insecure 仅 podman/nerdctl 生效
export interface ContainerTransferOptions {
  platform?: string
  allTags?: boolean
  insecure?: boolean
}

// registry 登录参数；密码由后端走 --password-stdin
export interface ContainerLoginOptions {
  registry?: string
  username: string
  password: string
  insecure?: boolean
}

// history 输出的单层信息；size 已由后端转为人类可读
export interface ImageLayer {
  id: string
  createdBy: string
  size: string
  createdAt: string
}

// volume ls 条目；size 仅 system df -v 有，列表不取
export interface ContainerVolume {
  name: string
  driver: string
  mountpoint: string
  createdAt: string
}

// 概览页运行时信息；取不到的字段为空。CLI 与 daemon 版本可能不同，分开返回
export interface RuntimeInfo {
  clientVersion: string
  serverVersion: string
  clientComponent: string
  serverComponent: string
  os: string
  arch: string
  kernelVersion: string
  driver: string
  cgroupDriver: string
  cgroupVersion: string
  ncpu: string
  memTotal: string
}

export interface ContainerStats {
  id: string
  name: string
  cpuPercent: string
  memUsage: string
  memPercent: string
  netIO: string
  blockIO: string
}

export interface ContainerCreateOptions {
  image: string
  name: string
  ports: PortMapping[]
  volumes: string[]
  env: string[]
  restart: string
  command: string[]
}

export interface ContainerTab {
  type: 'container'
  id: string
  panelId: string
  name: string
  connectionId: string // 保存的连接配置 ID（= 后端 conn key）
  runtime: 'docker' | 'podman' | 'nerdctl' | 'wslc'
  locked?: boolean
}
