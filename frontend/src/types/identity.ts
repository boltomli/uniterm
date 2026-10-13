export interface Identity {
  id: string
  name: string
  username: string
  authType: 'password' | 'key' | 'keyText'
  // LoadIdentities 脱敏后密码/密钥字段恒为空：hasPassword/hasKeyContent
  // 标志表示"已保存"（编辑弹窗显示"已保存，留空保持"占位）。留空保存时
  // 后端（SaveIdentities）会保留原值；明文按需通过 ResolveIdentitySecret
  // 按 ID 获取，连接时由 MaterializeIdentity 在 Go 侧解析。
  password?: string
  keyPath?: string
  keyContent?: string // inline private-key text (authType === 'keyText')
  hasPassword?: boolean
  hasKeyContent?: boolean
}

export interface IdentityStoreData {
  identities: Identity[]
}
