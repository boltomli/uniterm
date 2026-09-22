// SSH 认证类失败识别：后端会话连接失败时，用错误文本判断是否是“凭证被拒”，
// 决定是否立即弹出重新认证对话框（issue #949），而不是让用户按回车重连。
// 超时与用户主动取消不算认证失败——前者可能只是网络问题，重新弹框没有意义。

const AUTH_FAILURE_PATTERNS = [
  /unable to authenticate/i, // x/crypto 认证耗尽
  /permission denied/i, // 服务端明确拒绝（OpenSSH 风格）
  /authentication failed/i, // 通用认证失败文案
]

export function isAuthFailureError(message: string | null | undefined): boolean {
  if (!message) return false
  return AUTH_FAILURE_PATTERNS.some((p) => p.test(message))
}
