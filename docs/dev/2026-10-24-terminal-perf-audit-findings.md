# 终端输出性能 / 正确性审查清单与修复指引（供后续修复使用）

> 日期：2026-10-24
> 审查基线：`ab5227e3`（分支 `sec/high-fixes`，含 `41489267 perf(terminal): cut the per-chunk cost of high-volume output`）
> 范围：终端输出全链路 —— 后端 readLoop → `emitLoop` → Wails 事件传输 → 前端 `session:data` 订阅者 → xterm 解析 / 渲染 / 高亮 / gutter；附带少量安全低危项。
> 现象：终端里文本刷新"一卡一卡"（大量输出时掉帧、Ctrl+C 和回显延迟）。
> 性质：**只做审查，未修改任何代码**。结论来自逐行阅读，未在运行中的应用里 profiling；每项的"验证"小节给出修复后如何确认收益。

---

## 0. 执行须知

1. 遵守 `AGENTS.md` 和 `docs/dev/2026-10-18-security-audit-findings.md` §0 的约束（i18n、严格 TS、不改 `frontend/wailsjs/go/models.ts`、不削弱已有加固）。
2. **xterm 是自有 fork**（`frontend/vendor/xterm-xterm-6.0.0-uniterm.1.tgz`）。需要改 fork 的方案（P-04 的方案 B、P-06 的方案 B）必须先找到 fork 的源仓库并重新打包，**不要直接改 `node_modules`**；能不改 fork 的，优先用不改 fork 的方案。
3. 一次只修一项，每项单独提交（`perf(terminal): …` / `fix(session): …`），附回归测试或基准测试。
4. 行号基于 `ab5227e3`，以函数名定位为准。修完后更新文末状态表。
5. 已有的不变量不能破坏：
   - **单一应答方规则**（`terminalManager.ts` `createMirror` 注释）：任一时刻只能有一个解析器应答 DA/CPR/DSR 设备查询，否则会出现 "0R" 残留。
   - zmodem 的各个闸门（`zmodemCancellingUntil`、`IsZmodemMode` 同步路径、`getCancelUntil`）。
   - KeepAlive 补放逻辑（`writtenChunks` / `sessionStore.getDataFromChunk`）。
   - `emitLoop` 的"先阻塞后丢旧"流控语义（issue #1053）。

### 验证命令

```bash
go vet ./... && go test ./backend/session ./backend/mcp ./...
cd frontend && npx vue-tsc --noEmit && npx vitest run && npm run build
```

性能对比的统一做法：`wails3 dev` 打开 DevTools → Performance，在 SSH 和本地会话里分别跑：
- `seq 1 2000000`（纯文本洪水）
- `cat` 一个约 20MB、含大量中文的日志
- `yes | head -c 50M`
- `htop` / `vim` 滚动（TUI 重绘）

记录：主线程 Long Task 占比、FPS、按 Ctrl+C 到输出停止的延迟。

---

## 1. 问题总表

| ID | 等级 | 一句话 | 主位置 |
|---|---|---|---|
| P-01 | **高** | 每个会话的输出在前端被完整解析两次（隐藏的 mirror Terminal 常驻），而 mirror 的读接口已经没有调用方 | `frontend/src/services/terminalManager.ts:115-185, 475` |
| P-02 | **高** | 前后端之间没有流控：前端积压最多 50MB，造成延迟和成段跳动 | 前端 `writeStamped` / 后端 `app_terminal.go` 会话 API |
| P-03 | **高（正确性）** | UTF-8 多字节字符在读取边界被切开后变成 U+FFFD，前端再删掉 → 中文丢字 | `ssh_session.go:878`、`local_session_*.go`、`session.go:flushEmitChunks` |
| P-04 | 中 | 只用 DOM 渲染器；`minimumContrastRatio` 默认 4.5 | `terminalManager.ts:380-391` |
| P-05 | 中 | 关键字高亮每约 10ms 对整屏做一次同步重扫（屏内行不缓存） | `frontend/src/composables/overlayHighlight.ts:382, 463` |
| P-06 | 中 | scrollback 满了以后，每次刷新都要整表重建 override Map 和行缓存，复杂度 O(N) | `overlayHighlight.ts:189 shiftOverrideStore`、`:211 shiftLineCache` |
| P-07 | 中 | 行号/时间戳登记一直在跑，`before` 在入队时取值，积压时扫描区间越来越大 | `BaseTerminal.vue:256 writeStamped`、`terminalTimestamps.ts:132` |
| P-08 | 中低 | 超过 8KB 的事件走 `fetch` 并串行排在 `__eq` 上；合并后的单个事件可达约 1MB | `backend/session/session.go:738 flushEmitChunks` |
| P-09 | 低 | 后端每次读取的额外开销：`lastRecv` 整块拷贝；ConPTY 读缓冲只有 4KB；MCP 屏幕模拟器常驻 | `ssh_session.go:625`、`local_session_windows.go:686`、`app_mcp.go:1272` |
| P-10 | 低 | 后台标签每收到一块数据都线性遍历 panels / tabs | `BaseTerminal.vue:1547-1570` |
| S-01 | 低（安全） | `ReadFileBase64` / `ReadFileChunkBase64` 读取长度无上限 | `app_terminal.go:692, 717` |
| S-02 | 低（安全） | `AppendFileBase64` 不限制解码后的大小，和 `WriteFileBase64` 不一致 | `app_terminal.go:809` |
| S-03 | 低（规范） | WebLinks 悬停文案 `'Ctrl + Click to open'` 硬编码，没走 i18n | `BaseTerminal.vue:1205` |

推荐修复顺序：P-03 → P-01 → P-07 → P-05/P-06 → P-02 → P-09/P-10 → P-04 → P-08；S-* 可以随时穿插。

---

## 2. 背景：一块输出在主线程上要做哪些事

一个 `session:data` 事件到达后，主线程上依次发生：

1. Wails 注入的 JS 对 payload 求值（超过 8KB 时先 `fetch` 再 `r.json()`）
2. **所有** `session:data` 监听者都会被调用：`sessionStore`、每个会话的 mirror、每个 BaseTerminal 实例（包括被 KeepAlive 缓存的）、FileSidebar、FileTabContent、Monitor*
3. 可见 BaseTerminal 执行 `renderTerminalData`：几次正则守卫 → `dcsReassembler` → `terminal.write`
4. xterm 的 `WriteBuffer` 用 `setTimeout` 分片解析，**每片最多 12ms**（`WRITE_TIMEOUT_MS`）
5. mirror Terminal 有自己的 `WriteBuffer`，再解析一遍，每片同样最多 12ms
6. 解析完成后：`recordWrite`（行登记）→ `onWriteParsed` → 高亮同步扫描（≤3ms，超出部分放到下一帧继续）+ gutter 的 rAF
7. DOM 渲染器重建变化的行

第 4、5 两步在大量输出时各自都能占满 12ms，一帧只有 16.7ms，掉帧不可避免。第 6 步每 10ms 还要再加 3ms 左右。这就是"一卡一卡"的主要来源。

---

## 3. 详细问题与方案

### P-01（高）mirror Terminal 让每块输出被解析两次

**现状**
- `acquireTerminal` 为每个会话都调用 `createMirror(...)`（`terminalManager.ts:475`；会话复用时在 `:618`）。mirror 是一个从不 `open()` 的完整 `Terminal`，它订阅 `session:data`，无论标签是否在前台都会 `mirror.terminal.write(payload.data)`（`:168-177`）。
- mirror 的三个读接口 `getMirrorPromptSnapshot` / `readMirrorScreenFromRow` / `readMirrorTail` **已经没有任何调用方**：`6a55b491 feat(mcp): unify foreground terminal tools with built-in AI` 把 AI 的读屏改成走后端 `mcp.Screen`（`app_mcp.go sessionCaptureFor`），删掉了 `agent.ts` 里对这三个函数的调用。
- mirror 剩下的唯一作用，是**标签在后台时代替可见终端应答设备查询**（`replyDispose`，`:181-184`）。CPR（`ESC[6n`）的应答需要准确的光标位置，所以只要 mirror 还负责应答，它的状态就必须跟上输出流。

**影响**：前台会话的每块输出都要额外解析一次，主线程的解析开销翻倍。打开 N 个会话时，后台会话也在持续解析，并和前台抢主线程。

**方案 A（推荐，改动小）：标签在前台时 mirror 只排队，不解析**
1. 删掉三个没有调用方的读接口及对应测试（先 `grep -rn` 确认没有调用方）。
2. `TerminalMirror` 增加字段 `pending: string[]`、`pendingBytes: number`、`catchingUp: boolean`。
3. 在 `session:data` 回调里：如果 `isTerminalActive(sessionId)` 为真，只把数据推进 `pending`，不调用 `write`；否则先把 `pending` 刷完，再 `write`。
4. 补解析（catch-up）的时机：
   - 在 `markTerminalActive(sid, ref, false)` 之后，如果 `activeRefs.size === 0`，立即刷一次 `pending`。
   - 前台期间用 `requestIdleCallback`（超时约 1000ms；没有该 API 时降级为 `setTimeout(…, 200)`）在空闲时分批刷，避免切到后台的那一刻集中解析一大段。
5. **补解析期间不能应答**：设置 `catchingUp = true`，在 `replyDispose` 里遇到 `catchingUp` 直接丢弃应答。最后一块补解析的 `write(data, cb)` 回调里再把它置回 `false`。否则 mirror 会应答早已过期的查询，重新出现 "0R" 问题。
6. 给队列设上限（比如 `pendingBytes > 2MB`）：超过就清空队列，执行 `mirror.terminal.reset()`，再用 `useSessionStore().getData(sid)`（最近约 256KB）重放一遍。理由是：应答只依赖当前光标和模式状态，最近 256KB 足以恢复 shell 提示符和 TUI 的状态。

**方案 B（彻底，改动大）：把后台应答搬到后端**
- 后端 `mcp.Screen` 本来就解析所有输出、知道光标位置。前端在标签进入或离开后台时调用一个新的绑定（比如 `SessionSetFrontendActive(id, bool)`）。后端在"前端不活跃"期间直接应答 DA1/DA2/CPR/DSR，然后彻底删掉前端的 mirror。
- 需要复核单一应答方规则的切换竞态（前端恢复活跃和后端停止应答之间的空窗），并补充后端测试。

**验证**
- vitest：模拟活跃/不活跃切换，断言活跃期间 `mirror.terminal.write` 不被调用；切到后台后补解析，且补解析期间 `onData` 产生的应答被丢弃。
- 手工：在后台打开一个 SSH 标签跑 `printf '\e[6n'; read -t2 -d R x; echo "[$x]"`，切回前台后不能看到 `0R` 残留。
- Performance 面板：前台跑 `seq 1 2000000`，主线程脚本耗时应该下降约 40%。

---

### P-02（高）没有端到端流控

**现状**
- 后端 `queueEmit` 在队列满时会阻塞 readLoop，从而背压到 SSH/PTY。但它的消费者是 Wails 的 `enqueueEventJS`：只要 UI 线程还能执行 `ExecJS`，消费就很快。
- 前端 xterm 的 `WriteBuffer` 不设上限地缓存（直到 `DISCARD_WATERMARK = 50MB` 才开始丢弃）。所以真正的瓶颈（JS 解析和渲染）的压力传不回远端。

**影响**：`cat` 大文件时，前端积压好几秒的数据：画面成段跳动，Ctrl+C 要等积压消化完才"生效"，按键回显被排在积压后面。

**方案**（参照 xterm 官方文档 *Flow Control* 的 watermark 做法）
1. 后端为 `baseSession` 增加 `pauseOutput` 状态（`sync.Cond` 或 `chan struct{}`），新增绑定 `SessionPauseOutput(id)` / `SessionResumeOutput(id)`。暂停期间让 readLoop 在 `emitData` 之前阻塞（或者让 `queueEmit` 把"暂停"当作"队列已满"处理），复用现有的阻塞和看门狗语义。**zmodem 模式下必须忽略暂停**（二进制同步路径不能被卡住）。
2. 前端在 BaseTerminal 的 `writeStamped` 外层统计 `pendingBytes += data.length`，在 `write` 回调里减回去。超过 `HIGH = 512KB` 时调用 `SessionPauseOutput`，降到 `LOW = 128KB` 以下时调用 `SessionResumeOutput`（加一个状态位，避免重复调用）。
3. 标签不活跃时可见终端不再写入，前端无法确认消费进度，这时必须 resume（由 P-01 的 mirror 或 sessionStore 接手）。组件卸载、断线时也要 resume，防止会话永久卡在暂停状态。
4. 后端给暂停也加一个看门狗（比如 5s 没收到 resume 就自动恢复），防止前端崩溃后会话被锁死。

**验证**
- Go：新增测试，暂停后 readLoop 不再调用 `emitData`，resume 后数据完整、顺序不变；zmodem 模式下暂停不生效；看门狗会自动恢复。
- 手工：`yes` 运行 10s 后按 Ctrl+C，输出应该在约 100ms 内停止（修复前通常要好几秒）。

---

### P-03（高，正确性）UTF-8 多字节字符在读取边界丢失

**现状**
- UTF-8 会话的 `decoder == nil`，`decodeOutputLocked` 直接返回原始字节（`ssh_session.go:885`），没有处理被切开的多字节字符。本地、telnet、serial 等会话类型也一样。
- `flushEmitChunks` 合并后通过 `a.emitSessionData(id, string(data))` 发出（`app_terminal.go:157, 441`），Wails 的 `CustomEvent.ToJSON` 使用 `json.Marshal`，**会把非法 UTF-8 的每个字节替换成 U+FFFD**。
- 前端 `sanitizeLiveTerminalOutput` 会删除 U+FFFD（`terminalSanitize.ts:49`），于是这个字被悄悄吞掉。
- 已复现：`"ab中文"` 在第 4 字节处切开，两段的 JSON 分别是 `"ab��"` 和 `"�文"`，前端最终显示 `ab文`。

**影响**：大量中日韩文本输出时随机丢字；终端会话日志（`OutputLogger`）收到的是原始字节，不受影响，所以屏幕和日志会对不上。

**方案**
1. 在 `baseSession` 里增加一个 UTF-8 尾部缓存工具（新文件 `backend/session/utf8_carry.go`）：

   ```go
   // splitIncompleteUTF8 returns data minus any trailing bytes that start a
   // multi-byte rune not yet complete, plus that tail (≤3 bytes).
   func splitIncompleteUTF8(data []byte) (complete, tail []byte) {
       // Look back at most utf8.UTFMax-1 bytes for a rune start.
       for i := len(data) - 1; i >= 0 && i >= len(data)-(utf8.UTFMax-1); i-- {
           if utf8.RuneStart(data[i]) {
               if !utf8.FullRune(data[i:]) {
                   return data[:i], data[i:]
               }
               break
           }
       }
       return data, nil
   }
   ```

2. 放在**文本路径的唯一出口**：在 `emitData` 调用 `queueEmit` / 同步回调之前，拼上一次留下的 `utf8Tail`，再切出新的尾部（需要加锁，或者只在单个 readLoop goroutine 里使用）。注意：
   - `emitBinary` 和 zmodem 路径**不能**走这里。
   - `EndZmodem(trailing)` 和进入 zmodem 时，要先把未发出的尾部清空（或原样发出）。
   - `outputLogWriter` 和 data taps 继续接收原始字节，保持现状。
   - 设置了 `decoder` 的会话（GBK 等）已经有 `decodeLeftover`，输出本身是完整的 UTF-8，这一步对它们是无操作。
   - 断线时如果尾部还有残留字节，直接丢弃（本来就不完整）。
3. 只在 `emitData` 这一处收口，不要每个会话类型各改一遍。

**验证**
- Go 表驱动测试：`"中"` 在第 1、2 字节处切开；4 字节 emoji 切开；连续多块拼接；ASCII 原样通过；`EndZmodem` 前后尾部被清空。
- 手工：SSH 会话里 `cat` 一个 20MB 的中文文本，用 `md5sum` 对照截图或日志，屏幕上不再出现缺字。

---

### P-04（中）只用 DOM 渲染器 + 对比度增强默认开启

**现状**
- 项目从未接入 `@xterm/addon-webgl`。关键字高亮依赖 fork 里的 `CellColorOverrideStore`，而这个补丁只做在 DOM 渲染器（`DomRendererRowFactory.ts`）里。
- `minimumContrastRatio` 默认 4.5（`terminalManager.ts:391`，xterm 自己的默认值是 1）。DOM 渲染器每重建一个格子都要做一次对比度缓存查询，未命中时还要计算。

**方案**
- **A（短期，不改 fork）**：
  - 设置里新增"渲染器：自动 / DOM / WebGL"选项（新的 i18n 键要加到全部 9 个语言文件）。"自动"的含义是：关闭高亮时用 WebGL，开启高亮时用 DOM。
  - 处理 WebGL 上下文丢失（`onContextLoss` 时 dispose 并回退到 DOM）。
  - 背景图透明（`allowTransparency`）和 WebGL 一起用时要实测。
  - 重新评估 4.5 这个默认值：或者降到 1 并在设置里说明，或者保持不变但在文档里写明它的代价。
- **B（长期，需要改 fork）**：给 WebGL 渲染器加上 override 支持，让 WebGL 渲染器在构建每个格子的模型时读取同一个 `CellColorOverrideStore`，之后默认使用 WebGL。

**验证**：同一个洪水用例下，对比 DOM 和 WebGL 的 FPS 与 Long Task；对比 `minimumContrastRatio` 为 1 和 4.5 时渲染耗时的差异。

---

### P-05（中）高亮每约 10ms 对整屏做一次同步重扫

**现状**：`triggerWriteRefresh`（`overlayHighlight.ts:382`）在每次 `onWriteParsed` 时，只要距离上次超过 10ms 就同步执行 `refreshViewport`。屏幕区（`lineY >= baseY`）的行**从不进入缓存**，每次都要重新执行 `translateToString` + `matchTextSpans`。预算是 3ms，用完后放到下一帧（rAF）继续。持续输出时，这一项就能占去主线程约 30% 的时间。

**方案**
1. 为每一行建立"上次扫描的文本 → spans"缓存：`screenRowCache: Map<number /*abs y*/, { text: string; spans }>`。`translateToString` 之后，如果文本和上次相同，就直接复用 spans，跳过正则。逻辑行（跨行折叠）用拼接后的整段文本做 key。
2. 洪水模式下降频：如果连续 N 次扫描都发生在 `WRITE_MAX_WAIT_MS` 窗口内，就把同步扫描的最小间隔从 10ms 动态提高到 33ms，输出停下来后再恢复。
3. 可选：如果 `buffer.viewportY !== buffer.baseY`（用户正在往回翻 scrollback），屏幕区的变化不影响视口，就跳过本次写入触发的扫描。

**验证**：
- vitest：同一行内容不变时 `matchTextSpans` 只被调用一次。
- 基准测试：`seq` 洪水期间，`refreshViewportInner` 的总耗时下降。
- 回归：现有的 `overlayHighlight.test.ts` 和 `highlightRules.test.ts` 全部通过。

---

### P-06（中）scrollback 满后每次刷新整表重建 Map

**现状**：
- scrollback 满了以后，每输出一行就会 trim 一行。`refreshViewportInner` 每次都要调用：
  - `shiftLineCache`：重建最多 3000 项的 `lineMatchCache`；
  - `shiftOverrideStore`：重建 fork 里的 `_overrides`，最多 scrollback 行数。
- 两者都是 O(N) 分配，每约 10ms 执行一次。

**方案**
- **A（不改 fork，推荐先做）**：
  1. `lineMatchCache` 改用"不随 trim 变化"的 key：`key = y + totalTrimmed`（和 `terminalManager` 的 `lineOffset` 一样，由 `onTrim` 累加）。这样就不需要平移，被 trim 掉的项由 LRU 自然淘汰。
  2. 限制 override store 的规模：每次刷新后删除 `y < viewportY - 2*rows` 的 override（回滚时由缓存重新应用）。store 大小稳定在约 3 屏，`shiftOverrideStore` 的开销变成 O(3*rows)。
- **B（改 fork）**：`CellColorOverrideStore` 内部维护一个 `_base` 偏移，`shift(n)` 只需 `_base += n` 并懒惰清理，是 O(1)。

**验证**：vitest 覆盖 trim 前后 override 和缓存的颜色仍然对得上（现有 `shiftOverrideStore`、`shiftLineCache` 相关测试需要同步更新）；基准测试中 scrollback 已满时的刷新耗时下降。

---

### P-07（中）行登记在积压时扫描区间不断变大，而且默认也在跑

**现状**
- `writeStamped`（`BaseTerminal.vue:256`）在**调用 `write()` 时**就读取 `before = currentAbsoluteLine(sid)`，但解析要等到之后才异步进行。积压时，排队中的很多写入拿到的是同一个旧 `before`，每个回调都要从旧位置扫到当前光标（`recordWrittenLines` 的 1b 和第 2 步，`terminalTimestamps.ts:156-168`）。总开销接近"积压块数 × 扫描行数"。
- gutter 默认关闭（`gutterVisible() === false`），但 `recordWrittenLines` 仍然每块都执行；`41489267` 只去掉了 notify。

**方案**
1. 在 `ManagedTerminal` 里增加 `lastParsedAbs`。`write` 回调中用 `before = managed.lastParsedAbs ?? 回调时的光标`，处理完后把 `lastParsedAbs` 更新为 `after`。写入是按顺序解析的，上一次的 `after` 就是本次的真实 `before`。
2. 对 `to - from` 设上限（比如 `rows * 4`）。超出部分只登记，不做第 2 步的完成态回填（只影响时间戳精度，可以接受）。
3. 是否在 gutter 关闭时完全跳过登记，需要产品决策：跳过的话，用户中途打开 gutter 后行号会从那一刻开始计数。建议跳过，并在打开 gutter 时用 `realignRegistry` 按当前 buffer 补登记，只有时间戳会缺失。

**验证**：vitest 模拟 50 次写入入队后再依次解析，断言 `getLine` 调用总数是线性的；现有 `terminalTimestamps` 测试全部通过。

---

### P-08（中低）大事件走 fetch，并阻塞后续的小事件

**现状**：Wails beta.27 中 `maxInlineEventPayload = 8192`（`wails/v3/pkg/application/event_payload_store.go:41`）。超过这个大小的事件会改走 `fetch('/wails/eventpayload/<id>')`，一旦创建了 `window._wails.__eq`，后续**所有**事件都串行排在这条 Promise 链上。`emitLoop` 合并后的单个事件最大约为 64 块 × 16KB ≈ 1MB。于是持续输出期间，按键回显这类小事件要等前面的大块 fetch 和解析完成。

**方案**：在 `flushEmitChunks` 里限制单次回调的大小（比如每个事件 ≤ 64KB，超出就拆成多次 `cb`，**只能在完整 UTF-8 边界拆分**，依赖 P-03 的工具函数）。

64KB 是折中值：太小会让 `ExecJS` 次数暴涨；太大则排队延迟高。Wails 的注释说明 WebView2 上走 fetch 的吞吐反而更高，因此**不要**把上限压到 8KB 以下。按 §0 的用例在 16KB、64KB、256KB 三档上实测后再定。

**验证**：Go 测试断言拆分后字节完整、顺序不变、不切断多字节字符；手工用 `seq` 洪水时测按键回显延迟。

---

### P-09（低）后端读路径上的额外开销

1. `ssh_session.go:625` 的 `s.lastRecv.Store(append([]byte(nil), data...))` 每次读取都完整拷贝最多 16KB，但只有 `tailHex(last, 64)` 会用到。改为只保留最后 64 字节（在固定大小的数组里复制，或者用 `bytes.Clone(data[max(0,n-64):])`）。
2. `local_session_windows.go:686` 的 ConPTY 读缓冲只有 4KB，事件和队列条目数因此偏多（64 个队列槽只能装 256KB）。改成 16KB，与 SSH 一致；非 Windows 的 `local_session_unix.go` 也一并检查。
3. `app_mcp.go:1272 sessionCaptureFor` 为每个会话在 readLoop 上同步运行 `mcp.Screen.Feed`（持锁，约 47MB/s）。可选优化：只有在内置 AI 或 MCP 服务启用时才挂上 tap，首次读屏时再用 sessionStore 或后端缓冲回填。需要先确认 `uniterm_send_key` / job 等功能对"从会话开始就有完整历史"的依赖，改不改由维护者决定。

**验证**：`go test ./backend/session ./backend/mcp`；为 SSH readLoop 写一个基准（`testing.B`，模拟 100MB 输出）对比内存分配次数。

---

### P-10（低）后台标签每块数据都线性遍历 panels / tabs

**现状**：`BaseTerminal.vue:1547-1570` 中，不活跃实例只要收到本会话的数据，就要遍历一遍 `panelStore.panels` 和 `tabStore.tabs` 去找对应标签，再执行 `markTabNotification`。

**方案**：增加一个本地标志 `notifiedWhileInactive`。第一次打上通知点后置为 true，之后直接 return；`onActivated` 时复位。也可以把 `tabId` 缓存在 deactivate 时计算好。

**验证**：vitest 断言不活跃期间连续收到 100 块数据，`markTabNotification` 只被调用一次。

---

### S-01（低，安全）读文件接口没有长度上限

**现状**：`ReadFileBase64` 一次读入整个文件；`ReadFileChunkBase64` 的 `length` 只被文件剩余大小截断（`app_terminal.go:692, 717`）。虽然有 `requireFileGrant`，但渲染层一旦被攻破（或出现 bug），可以对一个已授权的大文件请求几 GB → `make([]byte, length)` 导致内存耗尽。

**方案**：`ReadFileChunkBase64` 的 `length` 上限设为比如 16MiB；`ReadFileBase64` 先 `Stat`，超过上限（比如 64MiB，与 `maxWriteFileBase64Bytes` 一致）就返回错误，并检查前端调用方在大文件时是否已经改用分块接口。

### S-02（低，安全）`AppendFileBase64` 不限制解码后的大小

**方案**：和 `WriteFileBase64` 一样检查 `len(data) > maxWriteFileBase64Bytes`；最好在解码前就用 `base64.StdEncoding.DecodedLen(len(base64Data))` 预检，避免先分配内存。补一个测试（参照现有 `WriteFileBase64` 的测试，把上限调小）。

### S-03（低，规范）硬编码的悬停文案

**方案**：`BaseTerminal.vue` 中 WebLinks 的 `hover` 回调改用 `t('terminal.ctrlClickToOpen')`（键名自定），9 个语言文件全部补齐；macOS 下显示 `⌘ + Click`。

---

## 4. 状态跟踪表

> 修复完成于 2026-10-25，按推荐顺序逐项提交，每项附回归测试。全量验证：
> `go vet ./... && go test ./...`（1762 passed）、
> `npx vue-tsc --noEmit && npx vitest run`（1182 passed）、`npm run build` 通过。

| ID | 状态 | 提交 | 备注 |
|---|---|---|---|
| P-01 | 已修复 | `b4d98eb0` | 方案 A：前台只排队（2MiB 上限 + reset/重放），空闲分批补解析（64KiB/requestIdleCallback），补解析期间丢弃应答；三个无调用方的读接口已删；vitest 覆盖活跃/不活跃切换与应答抑制 |
| P-02 | 已修复 | `6a9d95d8` | 后端 queueEmit 暂停等待 + `SessionPauseOutput/ResumeOutput` 绑定 + 5s 看门狗（generation 防陈旧回调），zmodem 忽略/解除暂停；前端 writeStamped 计数，512KiB/128KiB 水位，deactivate/unmount/id 换时防御性 resume；6 个 Go 测试 + 源码接线测试 |
| P-03 | 已修复 | `7d0063e9` | `splitIncompleteUTF8` + emitData 单点收口；zmodem/binary 不走缓存；进入二进制模式、EndZmodem、重连时清尾；表驱动测试 |
| P-04 | 已修复（方案 A） | `382d3985` | 新设置「渲染器」（自动/DOM/WebGL，11 语言）；自动 = 高亮开启时 DOM、关闭时 WebGL；上下文丢失回退 DOM；addon-webgl 以 vendor tgz 引入（peer 依赖与 fork 版本号冲突，剥离后重打包）；4.5 默认保留，代价已写入设置描述；**WebGL + 背景图透明仍需手工实测** |
| P-05 | 已修复 | `d9d8a29d` | 屏内行文本校验缓存（文本不变则跳过正则/字符映射）+ 洪水降频（连续 5 次后 10ms→33ms）；可选项 3（用户回翻 scrollback 时跳过写扫描）**未做**：trim 会让 override 索引错位，延迟刷新会把颜色贴错到用户正在看的视图 |
| P-06 | 已修复（方案 A） | `d6f5fd3e` | 缓存改绝对 key（index + totalTrim），trim 不再重建；override store 每次刷新裁剪到视口上方 2 屏，trim 重键 O(屏数)；shiftLineCache 删除；测试覆盖 trim 后缓存命中 |
| P-07 | 已修复 | `ff770105` | 按维护者决策：不跳过登记，仅限界——recordWrite 用 lastParsedAbs（上次解析结束行）作扫描起点，to-from > rows*4 时跳过完成态回填（登记保留）；测试断言 50 次积压解析的 getLine 探测数线性 |
| P-08 | 已修复 | `dcf35fe0` | flushEmitChunks 单回调 ≤64KiB，仅在完整 UTF-8 边界拆分（复用 P-03 工具）；字节/顺序保持；**64KiB 为折中值，建议按 §0 用例在 16/64/256KiB 三档实测调优** |
| P-09 | 部分修复 | `9b260450` | 1、2 已修：lastRecv 只留 64 字节（keepTail + 测试），ConPTY 读缓冲 4KiB→16KiB（unix 本已是 16KiB）；第 3 项（mcp.Screen tap 按需挂载）**留维护者决定**（需先分析 uniterm_send_key/job 对完整历史的依赖）；readLoop 基准测试未做 |
| P-10 | 已修复 | `5b787884` | notifiedWhileInactive 标志：每个后台周期只标一次通知点；onActivated 与会话 id 切换时复位；源码接线测试 |
| S-01 | 已修复 | `2d6c0364` | ReadFileBase64 先 Stat，>64MiB 拒绝并指向分块接口；ReadFileChunkBase64 限 16MiB/次（截断不拒绝，调用方按 offset 续读）；前端大文件调用方（zmodem 上传）本已分块 |
| S-02 | 已修复 | `69fe3f8b` | AppendFileBase64 用 DecodedLen 预检（解码前），与 WriteFileBase64 同上限；测试确认超限不落盘 |
| S-03 | 已修复 | `0903cd8e` | 悬停文案走 i18n（terminal.ctrlClickToOpen，11 语言），macOS 显示 ⌘ |

### 已知遗留

- 基线上 `GOOS=linux/darwin go build ./backend/session` 因 `copyFile` 只定义在 Windows 专属文件而失败，已随后修复（`f5d11b3b`，移到跨平台 `file_copy.go`）。注：从 Windows 交叉编译完整应用（`go build ./...`）仍会因 wails 依赖自身的 Linux 构建要求（gtk/webkit）失败，这属于构建环境限制，非本仓库代码问题。
- P-04 的 WebGL 渲染器在「背景图透明」组合下的视觉效果、P-08 的事件上限档位、P-02 的手工延迟对比（`yes` 后 Ctrl+C ≤100ms）需要在 `wails3 dev` + DevTools Performance 中实测确认。
