import type { CredentialResult } from '../components/CredentialPrompt.vue'
import type { ConnectionConfig } from '../types/session'
import { useConnectionStore } from '../stores/connectionStore'
import { useI18n } from '../i18n'
import { inject } from 'vue'

type ShowCredentialDialog = (
  title: string,
  subtitle: string,
  fields: ('user' | 'password')[],
  initialUser?: string,
  initialPassword?: string
) => Promise<CredentialResult | null>

// 判断一条连接是否缺账密、需要弹凭据对话框。key/identity/kerberos/agent 认证
// 由后端解析凭据，无需补全提示（identity 连接的临时/旧密码若误弹窗，会压过后端
// 从密钥库解析出的正确值，因此必须排除）。
export function needsCredentialCheck(config: ConnectionConfig): boolean {
  const inScope = ['ssh', 'mosh', 'sftp', 'scp', 'ftp'].includes(config.type)
  if (!inScope) return false
  if ((config.type === 'ssh' || config.type === 'mosh' || config.type === 'scp' || config.type === 'sftp') && (config.authType === 'key' || config.authType === 'keyText')) return false
  if (config.authType === 'identity' || config.authType === 'kerberos' || config.authType === 'agent') return false
  return !config.user || !config.password
}

// 统一凭据补全入口。此前这段逻辑在 App.vue 与本文件各有一份复刻，而容器
// 连接（ContainerTabContent → ContainerConnect）哪份都没走，导致被引用的
// SSH 主机未保存密码时直接报认证错误。现在 App.vue 的终端连接与容器等
// tab 组件统一走这里的 ensureConnectionCredentials，新增连接入口也应调用
// 它而不是各自实现。
//
// App.vue 自身定义了 showCredentialDialog（provide 给子组件），但同组件内
// inject 不到，因此通过参数显式传入；tab 组件则走 inject。
export function useTunnelCredentials(showCredentialDialogOverride?: ShowCredentialDialog) {
  const connectionStore = useConnectionStore()
  const { t } = useI18n()
  const showCredentialDialog = showCredentialDialogOverride ?? inject<ShowCredentialDialog>(
    'showCredentialDialog',
    () => Promise.resolve(null)
  )

  // 完整流程：先补 config.tunnelSSHConnId 指向的隧道连接凭据（结果以内联
  // 字段 tunnelSSHUser/Password 随 config 传给后端，后端只填空不覆盖），
  // 再补主连接。save_and_connect 时通过 connectionStore 持久化。
  // 用户取消返回 null，调用方应中止连接。
  async function ensureConnectionCredentials(config: ConnectionConfig): Promise<ConnectionConfig | null> {
    // 1. Check SSH tunnel connection first
    if (config.tunnelSSHConnId) {
      const tunnelConn = connectionStore.connections.find(c => c.id === config.tunnelSSHConnId)
      if (tunnelConn && needsCredentialCheck(tunnelConn)) {
        const result = await showCredentialDialog(
          t('credential.tunnelTitle'),
          t('credential.tunnelSubtitle', { name: tunnelConn.name }),
          ['user', 'password'],
          tunnelConn.user,
          tunnelConn.password
        )
        if (!result) return null
        // Pass credentials inline so Go can apply them without reading the store
        config.tunnelSSHUser = result.user || tunnelConn.user
        config.tunnelSSHPassword = result.password || tunnelConn.password
        if (result.action === 'save_and_connect') {
          await connectionStore.update(tunnelConn.id, {
            user: config.tunnelSSHUser,
            password: config.tunnelSSHPassword
          })
        }
      }
    }

    // 2. Check main connection
    if (!needsCredentialCheck(config)) return config

    const result = await showCredentialDialog(
      t('credential.title'),
      [config.name, config.host].filter(Boolean).join(' · '),
      ['user', 'password'],
      config.user,
      config.password
    )
    if (!result) return null
    // Create new object instead of mutating the original (which may be
    // referenced by the Pinia store). For "save_and_connect" we explicitly
    // persist via connectionStore.update below.
    config = {
      ...config,
      user: result.user || config.user,
      password: result.password || config.password
    }
    if (result.action === 'save_and_connect') {
      await connectionStore.update(config.id, { user: config.user, password: config.password })
    }
    return config
  }

  // 只解析被引用隧道连接的凭据，供 K8s tab、隧道面板等场景使用。
  async function resolveTunnelCredentials(tunnelSSHConnId: string): Promise<{ user: string; password: string } | null> {
    if (!tunnelSSHConnId) return { user: '', password: '' }
    const tunnelConn = connectionStore.connections.find(c => c.id === tunnelSSHConnId)
    if (!tunnelConn) return { user: '', password: '' }
    if (!needsCredentialCheck(tunnelConn)) {
      return { user: tunnelConn.user || '', password: tunnelConn.password || '' }
    }
    const result = await showCredentialDialog(
      t('credential.tunnelTitle'),
      t('credential.tunnelSubtitle', { name: tunnelConn.name }),
      ['user', 'password'],
      tunnelConn.user,
      tunnelConn.password
    )
    if (!result) return null
    const user = result.user || tunnelConn.user || ''
    const password = result.password || tunnelConn.password || ''
    if (result.action === 'save_and_connect') {
      await connectionStore.update(tunnelConn.id, { user, password })
    }
    return { user, password }
  }

  return { ensureConnectionCredentials, resolveTunnelCredentials }
}
