# Store 持久层统一改造记录

> 日期：2026-10-12　范围：`backend/store/*`　关联：issue #1115 前置工作（F-103/F-105、STORE-02/03/05/06/09/10/12/19/21）

## 一、起点：#1115 审计出的 4 个问题及修复方案

### 问题 1：换电脑后界面空白，也不提示输主密码（issue 的核心问题）

- **原因**：主密码派生出的钥匙会缓存一份在电脑系统里。程序启动时看到缓存里有钥匙就直接用，不检查它能不能打开数据。新电脑上如果残留着一把旧钥匙（跟迁移来的数据不匹配），程序就"假装解锁成功"——不弹输入密码的框，但每条密码都解不开，界面就空白了，还什么都不提示。
- **修法**：解密失败时，程序记一个"钥匙不对"的状态。启动时看到这个状态，重新弹出输入主密码的窗口。用户输入正确的主密码 → 用数据文件里自带的盐重新算出正确钥匙 → 数据正常显示，旧缓存也被覆盖。输错了就再弹一次，直到输对。
- **状态**：方案已定，未实施。详细计划见 `~/.claude/plans/linear-wobbling-wall.md`（`credentials.ErrAuthFailed` 哨兵 + `decryptFailed` 状态 + 前端重新弹解锁框 + 迁移提示文案）。

### 问题 2：点"重置"后，无法重新设置主密码

- **原因**："加密字段清单"在代码里被手抄了三份：真正加密用一份、重置时清空用一份、设置前检测用一份。重置用的是那份抄漏了的——只清了密码字段，漏了私钥字段（keyContent）和 ai.json，导致重置后目录里还残留密文。检测一看"还有密文"就拒绝设置新主密码，卡死。
- **修法**：把清单只写一份，重置清空和设置前检测都读这一份；再加一个测试，专门检查"加密逻辑写的字段"和"清单写的字段"完全一致，以后谁改了加密字段但忘改清单，测试直接报错。
- **状态**：清单已落地（`filestore.go` 的 `SecretFiles` manifest，加密/解密/清空/检测已从它派生）；**遗留**：app.go 的 `clearEncryptedFields` 与 `credentials.storeHasSecrets` 尚未改接 manifest，一致性测试未加。

### 问题 3：导出的文件在别的电脑导入时提示"解密失败"

- **原因**：个别连接身上残留着旧密文（比如连接从密码登录改成密钥登录后留下的）。导出时遇到这种残留原样跳过写进文件；导入时程序又拿你的导出密码去解这段残留密文，当然解不开，整个文件导入失败。
- **修法**：导出时把残留密文先用当前钥匙解成明文再正常加密进文件；解不开就直接报错拒绝导出，绝不生成一个必然导入失败的文件。
- **状态**：已实施，范围比原修法大（本轮讨论追加了三点）：
  1. **导出/导入的字段集合与保存一致**：`ExportUniterm`/`parseUniterm` 不再自己判断秘密字段，改为把导出密码包成 `store.PasswordStore` 适配器走 `SecretFiles` manifest walk（`connections.json` 的 `password` + `keyContent`）。修掉了"带密码导出的 .utm 里私钥 PEM 是明文"的漏洞。
  2. **导出只收明文**：store 的 `Load()` 已解密全部 `enc:v1:`（本轮 manifest 化解密顺带清掉旧版按 authType 条件加密留下的孤儿密文），导出器再兜底——输入里出现 `enc:v1:` 直接报错（原来是静默放行，正是"生成必然导入失败文件"的来源）。后端无密码导出分支已删除（UI 本就强制密码），`ExportUniterm` 现在要求导出密码。
  3. **身份库物化**：`.utm` 只导出连接，但连接可以 `authType: "identity"` 引用 identities.json 的可复用凭据——原样导出就是悬空引用，换机连不上。`ExportConnections` 现在先用 `MaterializeForExport`（复用运行时的 `MaterializeIdentity`）把每个身份引用物化成普通 password/key/keyText 连接（用户名/密码/私钥内嵌），导出文件里不再有任何 identity 引用；悬空引用降级为"无凭据连接"（保留连接自带的用户名覆盖，密码留空）。
  - 维持现状：`keyPath` 型外部私钥文件仍是路径引用，换机依赖本机文件（内嵌涉及把私钥写进导出文件的安全权衡，单独做）。identities 本身仍不导出。

### 问题 4：改配置目录时，数据被清空

- **原因**：改配置目录有"迁移模式"：把旧目录拷到新目录，然后删掉旧目录。但你选的新目录就是当前目录时，程序照样执行——自己拷贝给自己，然后把唯一这份数据删了。
- **修法**：加一个判断——目标目录和当前目录相同时，直接提示"已经是这个目录"，什么都不做。
- **状态**：已实施。`SetDataDir` 在解析出目标后、触碰任何文件前，用 `dataDirContains` 拒绝"目标 == 当前目录"和"目标在当前目录内部"两种情况（后者同样会被 copyDir 自遍历 + removeDataDir 连根删掉）。比较是 Clean 后的词法比较，Windows 大小写不敏感；守卫刻意不做符号链接解析，因为目标此时可能还不存在。测试：`TestSetDataDir_RejectsSameDir`（RED 阶段确认了旧代码真的把种子文件删了）、`TestSetDataDir_RejectsDirInsideCurrent`。

> 实施顺序建议：先修 1、2、4（都属于"数据恢复路径"，改动小），3 单独做。

## 二、改造前的状态

每个 store 各写一套持久化代码，行为漂移、难以维护：

1. **Save 各写各的**：`os.WriteFile` / `json.Encoder` / 手工 tmp+rename 混用；权限有 0644 也有 0600；是否加密、是否 hash-skip 由各 store 自己决定。
2. **Load 各写各的**：`os.ReadFile` + `json.Unmarshal` 手写一遍；损坏 JSON 的处理（静默用零值覆盖 / 报错 / 隔离）不一致。
3. **hash-skip（F-105）只在部分 store 生效**，且跳过写盘的前提 `lastSavedHash` 的维护逻辑分散。
4. **AISessionStore 每次保存全量重写**：F-103 改成 shard（`ai-sessions/<id>.json`）后，每次 Save 仍把所有 shard 重写一遍。
5. **加密字段清单可能漂移**：connections/identities/proxies/ai/settings 各自硬编码自己要加密的字段，与 credential reset、云同步、setup guard 里的路径清单是三份拷贝。
6. **Windows 上 `os.Rename` 替换会失败**：Go 的 `os.Open` 未带 `FILE_SHARE_DELETE`（golang/go#32088），并发读的瞬间持有句柄会让替换 rename 报 ACCESS_DENIED（settings 并发测试在 Windows 稳定复现）。

## 三、目标形态（用户确认的原则）

- **保存只有一条路**：`saveEncryptedFile` —— 算 hash → 相同则跳过 → 不同则加密 → 原子写。任何文件、任何时候都有加密器（fail-closed：无加密器则拒绝保存明文，`ErrNoCipher`）。
- **读取也只有一条路**：`loadEncryptedFile` —— 读文件 → 解析 → 解密 → 连同内容 hash 一起返回。缺文件/损坏/解密失败的**策略**留在各 store。
- **所有 JSON 落盘都走信封**：包括防抖 flush、迁移写、bootstrap 写。
- **hash 由 Load 信封直接返回**，store 不自己算。

## 四、改动内容

### 4.1 `backend/store/filestore.go`（唯一真相源）

- **SecretFiles manifest**：每个 JSON 文件的加密字段路径集中声明（`connections.*.password`、`ai.models.*.apiKey` 等），Encrypt/Decrypt/credential reset/云同步/SetupGuard 全部从 manifest 派生，杜绝清单漂移。
- **Save 信封** `saveEncryptedFile[T](file, path, data, ps, lastSavedHash) (string, error)`：
  1. `jsonHash`（sha256 of **pre-encryption** JSON——加密用随机 nonce，加密后的 hash 永不匹配）；
  2. hash 相同 → 跳过返回；
  3. `EncryptSecretFields`（无加密器 → `ErrNoCipher`，fail-closed）；
  4. `writeJSONFile`（MarshalIndent + `atomicWriteFile` 0600）。
  错误契约：**任何错误都原样返回调用方的 `lastSavedHash`**，所以调用方可无条件赋值：
  ```go
  h, err := saveEncryptedFile(...); s.lastSavedHash = h; return err
  ```
- **Load 信封** `loadEncryptedFile[T]` / `loadEncryptedFileBase[T]`（base 用于旧配置继承默认值），返回 5 个值：
  ```go
  out, res, hash, existed, err := loadEncryptedFile[T](file, path, ps)
  ```
  - `hash`：解密后明文内容的 `jsonHash`，只在解析+解密都成功时有值——**供 store 直接播种 `lastSavedHash`，重启后第一次内容未变的 Save 直接跳过**；
  - `existed`：文件是否存在。注意 unparseable 分支也返回 `existed=true`（文件在，只是坏的），否则调用方 `!existed` 分支会把隔离错误吞掉（本次开发中被 `TestProxyStoreCorrupt` 抓到的回归）；
  - 解析失败 → `quarantineCorrupt`（改名 `.corrupt-<ts>`，STORE-09）+ `errUnparseableConfig`；
  - 解密失败 → 返回已解析数据 + error（文件本身完好，换对 key 可恢复，不隔离）。
- **`atomicWriteFile`**：tmp 同目录 + fsync + rename；**Windows 专属重试**（20 次 × 5ms）绕开 golang/go#32088 的 rename ACCESS_DENIED。
- `atomic.go` 已合并进本文件，死代码 `copyFileWithoutSymlinks` 删除。

### 4.2 各 store

全部 12 个存储点统一：

| Store | Save | Load 策略（差异部分） |
|---|---|---|
| connection | 信封 + identity 连接清空 Password（#711，User 保留 #959） | 新旧格式探测（自读字节走 `loadEncryptedData`），解密错误上报不隔离，legacy keychain 回填 + 懒加密 |
| identity / proxy | 信封 | 缺文件→空；损坏→隔离+报错 |
| ai / settings | 信封 | 缺文件/损坏→默认值；明文字段或缺省字段回填→`needsSave` 重存 |
| quickCommands / tunnels | 信封（无密字段，ps=nil） | Version=0 回填 |
| localState | 信封 | `loadEncryptedFileBase` 带 `defaultLocalState()` 基底 |
| favorites / recent | 信封 | 缺文件/错误→重置空列表 |
| terminalHistory | 信封（在 flush/delete 路径，hash 读写都在锁内） | 缺文件/错误→空 |
| commands / skills prefs | 信封（saveMu 串行化） | Version 回填；`.md` 文件写走 `atomicWriteFile` 0644 |
| AI sessions（每 shard） | **每个 shard 独立过信封**，`shardHashes[name]` 缓存 hash → 未变的 shard 不再重写 | 每 shard 信封读取，hash 播种进 `shardHashes` |
| bootstrap | `writeJSONFile` | 读侧保留多位置探测（解析失败是预期分支，不隔离） |
| legacy_migrate | 两处写走 `writeJSONFile` | 明文解析路径保留 `json.Unmarshal` |

### 4.3 hash 播种的关键规则（本次踩过的坑）

**`needsSave`（懒加密/默认值回填）时不能播种 Load 的 hash**：

- ai/settings：明文密钥在解密时是**原样保留**的（等 Save 加密），此时 Save 的输入与文件内容完全一致 → hash 相同 → 会被信封跳过 → **懒加密永远不执行且每次 Load 重复触发**。因此：
  ```go
  if needsSave { _ = s.Save(cfg) } else { s.lastSavedHash = hash }
  ```
- connection `finishLoaded`：同型 if/else（needsSave 路径由懒加密自己记录 hash）。
- MaxTurns/Version 等**回填类** needsSave 本身内容已变不会被跳过，但统一走 else 播种，规则一致。
- 缺文件/加载失败 → hash 留空，首次 Save 必写（保守正确）。

其余约定：

- 信封错误契约让 Save 侧统一为三行简写，不做错误后再判断。
- recent/favorites 等并发 store：hash 的读/写在各自 mutex 保护下进行。
- 首次进程内 Save 总是写盘（`lastSavedHash` 零值为 `""`）——由 Load 播种解决重启后的多余重写。

## 五、测试与验证

- `go vet ./backend/store/` 通过；`go test ./backend/store/ ./backend/credentials/ -count=2` 全绿。
- `TestSettingsStore_ConcurrentSaveLoad`（4 写 + 8 读 × 20 次）曾稳定失败 → 定位为 Windows rename 句柄竞争 → `atomicWriteFile` 增加有界重试后连续通过。
- `TestProxyStoreCorrupt` 抓到 unparseable 分支 `existed=false` 吞错误的回归，已修复并加注释。
- `backend/session` 的 Kerberos / redis 测试失败为环境性（无 Kerberos 配置、本机无 redis），与本次改动无关。

## 六、遗留/后续

- credential reset（app.go `clearEncryptedFields`）与 `credentials.storeHasSecrets` 尚未改接 manifest（reset 时 keyContent 清不掉的 bug 属于 #1115 后续项）。
- 解密失败→重新弹解锁窗口的方案见 `~/.claude/plans/linear-wobbling-wall.md`。
- `lastSavedHash` 只在进程内有效；跨进程的 skip 依赖 Load 播种（本改造已完成）。
