export interface Proxy {
  id: string
  name: string
  kind: 'socks5' | 'http'
  host: string
  port: number
  user?: string
  // LoadProxies 脱敏后密码恒为空：hasPass 标志表示"已保存"（编辑弹窗显示
  // "已保存，留空保持"占位）。留空保存时后端（SaveProxies）保留原值；拨号
  // 时由 materializeProxy 在 Go 侧解析明文。
  pass?: string
  hasPass?: boolean
  enabled?: boolean
}

export interface ProxyStoreData {
  proxies: Proxy[]
}
