# 数据库功能回归复盘与修复指引（供后续排期）

> 日期：2026-10-25
> 审查基线：`9f61eb7a`（分支 `sec/high-fixes`）
> 范围：`backend/database/*`、`backend/session/database_session.go`、`app_database.go`、
> `frontend/src/components/DB*.vue`（Tree / TabContent / QueryEditor / ResultGrid / TableStructure / ObjectList）、
> `frontend/src/utils/connectionTypes.ts`、`frontend/src/components/ConnectionForm.vue`
> 触发问题：「数据库相关功能越修越坏」——同类问题反复出现，修好 A 又坏 B
> 性质：**只做审查，未修改任何代码**。结论来自逐行阅读 + 提交历史考古，未在真机上逐条复现每个数据库方言；
> 每条都给出了可执行的复现步骤，请在开工前用 10 分钟验证自己那条。

---

## 0. 执行须知

1. 遵守 `AGENTS.md` 与 `docs/dev/2026-10-18-security-audit-findings.md` §0 的约束（i18n、严格 TS、
   不改 `frontend/wailsjs/go/models.ts`、不削弱已有加固）。
2. **一次只修一项**，每项单独提交（`fix(db): …`），附一个能复现原问题的测试或至少一段可复现步骤。
3. 行号基于 `9f61eb7a`，以函数名定位为准。修完后更新 §7 状态表。
4. 修数据库层时**不要**顺手重构 UI；修 UI 时**不要**顺手改 Provider。本次事故的根源就是改动跨层时没有契约。
5. 已有的不变量不能破坏：
   - `ExecuteQuery` 返回**位置化** `[][]any`（重名列合法，`SELECT a.id, b.id` 两列都要保留）。
   - `PrepareExec` 的语义：在**同一条物理连接**上先切换库/模式再执行下一条语句。
   - 每个 Provider 的 `Quote` 必须对任意输入返回非空引用（`provider_quote_test.go` 的约束，但目前有漏网）。
   - `ExecuteScript` 失败时返回 `FailedLine/FailedSQL/Error` 而不是 Go error（前端据此展示行号）。

### 验证命令

```bash
go build ./...
go vet ./...
go test ./backend/database ./backend/session ./...
cd frontend && npx vue-tsc --noEmit && npx vitest run && npm run build
```

---

## 1. 结论

**不是「越修越坏」，而是「修复一直没有落到契约层」。**

28 项可复现缺陷里，有 10 项左右属于同一类：*某项修复在 A 路径生效，但 B 路径（兄弟 provider / 另一条调用链 /
另一个功能）没有同步，或者被后来的改动覆盖*。用户感受到的就是「上次修好的问题，这次又回来了，而且换了个位置」。

真正让用户觉得「数据库功能坏了」的只有三类，而且前两类都不会报错：

1. **编辑静默丢失/写错**（P0，4 项：DB-01~04）：单元格改完看起来已保存，实际数据库没变；
   或者改的是主键/JOIN 结果，UPDATE 影响 0 行却提示成功。
2. **拿错对象**（P0/P1，4 项）：同名表在多个 schema 下，列结构、索引、甚至 DDL 落到错误的 schema；
   改了表结构后界面还显示旧结构。
3. **SQL 被静默截断**（P0，DB-15）：含分号的字符串会让语句变成语法错误。

两条判据：

- 如果一个问题**只在某一个数据库类型上出现**，多半是 §2 的 R1（修复未横向铺开）。
- 如果一个问题**在所有数据库类型上都出现**，多半是 §2 的 R2/R4/R5（前端状态机、缓存、竞态）。

---

## 2. 六个系统性原因

### R1　修复只落在「被发现的那一条路径」

| 原修复 | 实际状态 | 证据 |
|---|---|---|
| F-116 MySQL 连接池调优 | 被 session 层覆盖，从未生效 | `backend/database/engine.go:49-50`（20/5）→ `backend/session/database_session.go:62-63`（5/2）在 `Connect` 里又设了一遍 |
| `Quote` 吞错返回空串 | MySQL/PG/rqlite 修了，**SQLite 没修**；回归测试也漏了 SQLite | `backend/database/provider_sqlite.go:88-91`；`provider_quote_test.go:39-46` 只遍历 mysql/postgres/rqlite |
| F-12.4 查询超时 | 只加在 `queryStrings/queryAny/ExecuteQuery/ExecuteStatement/ExecuteScript`；**所有 DDL 与导出路径仍用 `context.Background()`** | `executor.go:42,107,161`；`provider_mysql.go:302-320`；`provider_oracle.go:347`；`provider_sqlserver.go:530+` |
| F-113 `QueryRowsStream`（大结果流式） | **没有调用方**，死代码 | 全仓 grep 只在 `engine.go:135` 定义 |
| 分页 API `PagedTableQuery` / `DBPagedTableQuery` | **前端没用**，`DBDefaultTableQuery` 一职两用 | `app_database.go:139-158`；前端只 import `DBDefaultTableQuery` |
| `2b358b63` SQL Server 命名实例 | 修复条件 `port <= 0` 在 UI 默认路径永远不成立（切类型必填 1433） | `provider_sqlserver.go:24-40` + `ConnectionForm.vue:1462-1464` |
| `4aa8d5bf` Dump 按 schema 限定 | 只修了元数据查询；导出的 DROP/INSERT 仍是未限定名，且各 provider 的对象类型处理不一致（见 DB-23） | `provider_postgres.go:566-590`、`provider_oracle.go:667,687`、`provider_sqlserver.go:574` |

**后果**：每位贡献者都在自己的用例上"修好了"，但没人保证其他方言、其他入口一致。数据库层没有一份
「Provider 契约测试」，`Provider` 接口的注释就是唯一规范。

### R2　性能优化没有配套的失效策略

```
07-28  69695073  F-115 给 Postgres GetTableSchema 加同步缓存   → 永久缓存，无失效、无 TTL、key 不含连接
08-03  38b54d61  给结果网格加过滤/排序（displayRows 派生数组）  → 过滤/排序会更换 rows 数组引用
08-07  536787df  给网格加 watch(props.rows) 用来重置单元格选中
08-23  a7991e84  「显式保存」改造，把 pending.clear() 塞进这个 watch → 过滤/排序静默丢弃未保存编辑
10-22  dd62dff5  单元格复制又接入同一个 watch
```

这是本次**最典型的回归链**：三个功能各自都正确，叠加后产生数据丢失，而且四个月内没人发现——
因为前端没有组件挂载测试（`DBResultGrid.duplicateColumns.test.ts` 明确写了
`The repo has no component-mount tests`，只能在 SFC 源码上做字符串断言）。

Postgres 缓存那条同理：`F-115` 是为了解决「结构树每刷新一次要 3×N 次串行往返」，但它让
`DBTableStructure` 在 DDL 成功后调用 `loadSchema()` 拿到**旧结构**——用户看到"没生效"，再改一次就撞
"column already exists"。缓存本身没错，错在没有失效钩子、没有按连接隔离。

### R3　Provider 契约里没有「schema」这个概念

`Provider` 的全部方法签名都是 `(db *sql.DB, dbName, tableName string)`（`backend/database/provider.go:44-105`）。
`dbName` 在不同方言里其实是三种东西：

| 方言 | `dbName` 的实际含义 | 定位机制 |
|---|---|---|
| MySQL | database | 连接级 `USE`（`PrepareExec`） |
| PostgreSQL | database | 连接 DSN 已固定；**schema 只能靠 search_path** |
| Oracle | schema/owner（当前已改为列出 schema） | `ALTER SESSION SET CURRENT_SCHEMA` |
| SQL Server | database | 连接级 `USE`（`PrepareExec` / `withUse`） |
| SQLite | 无（恒为 `"main"`） | 文件即库，路径塞在 `Host` |

于是：

- `TableInfo` 只有 `Name`，没有 `Schema`（`backend/database/schema.go:5-10`），前端 doc 模型也只有 `dbName`
  → **PG/MSSQL 里两个 schema 的同名表在 UI 上不可区分**。
- PG 的 `GetTables` 会列出所有非系统 schema 的表（`provider_postgres.go:163-190`），但
  `GetTableSchema`/DDL 用的是未限定名，最终落到 `search_path` 的第一个命中 → 点 `audit.users`
  看到的是 `public.users`。
- MSSQL 的 `INFORMATION_SCHEMA.COLUMNS WHERE TABLE_NAME=@p1`（`provider_sqlserver.go:236-244`）
  同样不区分 schema。

这是"点了 A 表、改了 B 表"这类玄学问题的总根因，也是每次都要按方言打补丁的原因。

### R4　前端把「结果数据」直接当「编辑草稿」

`DBResultGrid` 的做法是：编辑确认时**直接写回 `props.rows` 里的行对象**
（`DBResultGrid.vue:387` `row[field] = newVal`），再用一个 `pending: Map<rowObject, StagedRow>`
记录"哪些格是脏的"。这带来三个连带问题：

1. 行对象被就地修改，`queryResult`（`shallowRef`）感知不到，"数据已变"与"已保存"在界面上无法区分。
2. pending 以**行对象身份**为 key，而父组件传给网格的是 `displayRows`（过滤/排序后的派生数组）
   → 任何派生都会换引用并触发 `watch(props.rows)` 清空 pending（见 R2）。
3. `snapshotPk()` 在写入新值**之后**才拍快照（`DBResultGrid.vue:387-390`）→ 修改主键格时 WHERE 条件用的是新值。

### R5　异步流程没有请求令牌，也没有真取消

- `DBQueryEditor`：`onExecute` 用组件级 `let cancelled`，没有 run id。连点两次执行时，第二次会把
  `cancelled` 重置为 `false`，第一次的旧结果返回后被当作最新结果渲染（`DBQueryEditor.vue:569-651`）。
  "取消"按钮只是 `cancelled = true`（`:653-656`），后端查询仍在跑满 30s 并占着连接。
- `DBTreePanel.loadTree`：`GetDatabases` → `GetTables` 两段 await，没有序号校验。快速切换会话/重连时，
  先发出的旧请求后返回，会把新会话的树覆盖掉（`:240-296`）。
- `DBObjectList.load`、`DBTableStructure.loadSchema` 同样只有 `cancelled`/无校验。

### R6　一个组件承担了太多职责，且没有可测的中间层

`DBQueryEditor.vue`（1.1k 行）同时负责：SQL 编辑、浏览模式、分页、AI 生成、脚本文件、结果导出、
单元格内联编辑、整行编辑弹窗、插入行弹窗、执行历史。所有状态（`page`、`browseMode`、`queryResult`、
`pendingCount`、`sortProp`、`resultFilter`…）平铺在同一层，任何新功能都要动到相邻功能的路径。

`DBTabContent.vue` 的 `docs` 也是本地 `ref`，不随 `sessionId`/`dbType` 重置——重连或改连接类型后，
旧 doc 继续用旧库名发查询。

---

## 3. 问题总表

| ID | 等级 | 类别 | 一句话 | 主位置 |
|---|---|---|---|---|
| DB-01 | **P0 数据** | 编辑 | 主键快照晚于赋值，改主键 WHERE 用新值，UPDATE 影响 0 行仍报成功 | `DBResultGrid.vue:387-390`、`DBQueryEditor.vue:740-772` |
| DB-02 | **P0 数据** | 编辑 | 过滤/排序/重跑会清空 pending，但行里已是新值 → 幽灵保存 | `DBResultGrid.vue:488-495`、`DBQueryEditor.vue:286-308` |
| DB-03 | **P0 数据** | 编辑 | 把 SQL 改成 JOIN 后仍可内联编辑，UPDATE 打到基础表同名列 | `DBQueryEditor.vue:272-284` |
| DB-04 | **P0 数据** | 编辑 | 行弹窗/删除用当前行值构造 WHERE，含未保存的脏值 | `DBQueryEditor.vue:778-800,844-851` |
| DB-05 | P1 数据 | 编辑 | 逐条 UPDATE、无事务、无 affected 校验，失败即 break 留下半提交 | `DBQueryEditor.vue:740-772` |
| DB-06 | **P0 对象** | schema | PG schema 未限定、`TableInfo` 无 schema，同名表混用 | `provider_postgres.go:163-268`、`schema.go:5-10` |
| DB-07 | P1 对象 | schema | SQL Server 同库跨 schema 混用（列/索引/PK） | `provider_sqlserver.go:236-280` |
| DB-08 | **P0 对象** | 缓存 | PG schema 缓存永不过期、跨连接共享 → DDL 后读到旧结构 | `provider_postgres.go:18-21,223-226,331` |
| DB-09 | P1 架构 | schema | `dbName` 一个字段承载 database/schema/文件路径三义 | `provider.go:44-105`、`app_database.go` 全量 |
| DB-10 | P1 资源 | 超时 | MySQL `readTimeout=10s` 与 `queryTimeout=30s` 冲突，>10s 查询必挂 | `provider_mysql.go:31-40`、`executor.go:18` |
| DB-11 | P1 资源 | 超时 | DDL/Dump 全部无 ctx 超时，卡死只能杀进程 | `provider_mysql.go:302-320` 等 |
| DB-12 | P1 资源 | 大对象 | `DumpTable` 无行数上限、无流式，大表把整份 SQL 拼进内存再传一次 | `provider_mysql.go:654`、各 provider `DumpTable` |
| DB-13 | P1 资源 | 池 | F-116 连接池参数被 session 层覆盖 | `engine.go:49-50` ↔ `database_session.go:62-63` |
| DB-14 | P2 资源 | 冗余 | 打开"结构"页也会跑一次 browse SELECT | `DBTabContent.vue:130-136` + `DBQueryEditor.vue:504-510` |
| DB-15 | **P0 SQL** | 切分 | `firstStatement` 裸 `split(';')`，字符串里的分号会截断 SQL | `DBQueryEditor.vue:401-406,613` |
| DB-16 | P1 竞态 | 执行 | 无请求令牌 + 假取消，旧结果可覆盖新结果 | `DBQueryEditor.vue:569-576,653-656` |
| DB-17 | P1 竞态 | 树 | `loadTree` 无时序保护；每次刷新都 `emit('openDatabase')` 抢焦点 | `DBTreePanel.vue:240-296` |
| DB-18 | P1 状态 | 会话 | `docs` 不随 `sessionId`/`dbType` 重置 | `DBTabContent.vue:252` |
| DB-19 | P2 状态 | 交互 | 先 `await` schema 再开 tab：点击无反馈、失败静默只读 | `DBTabContent.vue:448-480` |
| DB-20 | P2 交互 | 分页 | 总数靠估算，可翻到空页；过滤/排序只作用于当前页 | `DBQueryEditor.vue:286-316` |
| DB-21 | P2 一致 | 引用 | SQLite `Quote` 仍可能返回空串；回归测试漏 SQLite | `provider_sqlite.go:88-91`、`provider_quote_test.go:39` |
| DB-22 | P1 一致 | 默认值 | MySQL 把 `CURRENT_TIMESTAMP`/表达式当字符串、空串默认丢失；Oracle 原样拼接；MSSQL 不处理 | `provider_mysql.go:211-226,510-515`；`provider_oracle.go:590-599`；`provider_sqlserver.go:378-395` |
| DB-23 | P2 一致 | 导出 | 导出脚本不可移植：PG/MSSQL 把视图导成表、Oracle 的 `DROP … IF EXISTS` 老版本非法、DROP/INSERT 均未限定 schema | `provider_postgres.go:583-608`、`provider_sqlserver.go:574-599`、`provider_oracle.go:667,687` |
| DB-24 | P2 一致 | 池 | SQL Server `withUse` 把 `USE` 施加到池化连接，归还后 current db 被污染 | `provider_sqlserver.go:486-491` |
| DB-25 | P2 一致 | 连接 | 命名实例修复在 UI 默认路径不可达 | `provider_sqlserver.go:24-40` + `ConnectionForm.vue:1462` |
| DB-26 | P2 一致 | 死代码 | `QueryRowsStream` / `QueryResultToJSON` / `PagedTableQuery` 无调用方 | `engine.go:135`、`executor.go:136`、`app_database.go:153` |
| DB-27 | P2 功能 | 脚本 | 多语句脚本里的 SELECT 走 `ExecContext`，结果被丢弃 | `executor.go:171-186` |
| DB-28 | P2 一致 | 重复 | 建/删表在树、对象列表、结构页三处各写一遍，行为分叉 | `DBTreePanel.vue` / `DBObjectList.vue` / `DBTableStructure.vue` |

**推荐修复顺序**：DB-01 → DB-02 → DB-15 → DB-08 → DB-03/04 → DB-06/07 → DB-10/11 → 其余。
理由：前四项是"用户已经在丢数据/看错数据"，且修复面积小；DB-06/07/09 是结构性改动，需要单独排期。

---

## 4. 明细

### 4.1 A 组：会静默丢数据 / 写错数据

#### DB-01　主键快照晚于赋值：改主键后 UPDATE 影响 0 行，却显示保存成功

**现象**：浏览表 → 双击主键列改值 → 点保存 → 保存条消失、单元格显示新值 → 刷新后回到旧值（或行丢失）。
触发条件是「这一行的**第一次**暂存编辑就是主键列」；先改其他列再改主键时，PK 快照是旧的，反而是对的——
这种“时对时错”正是它难被发现的原因。

**证据**（`frontend/src/components/DBResultGrid.vue`）：

```ts
// :387-391  先写入新值
row[field] = newVal
let rec = pending.get(row)
if (!rec) {
  rec = { pk: snapshotPk(row), fields: new Map() }   // ← 此时 row 已是新值
```

`DBQueryEditor.onSaveEdits` 拿 `ed.where`（= 快照）作为 WHERE，`DBUpdateRow` 返回 nil 就 `markCommitted`
（`:746-755`），前端没有任何 `RowsAffected` 校验（后端 `execPrepared` 也丢弃了 `sql.Result`，
`provider.go:108-122`）。

**修法**：
1. 先 `snapshotPk` 再写 `row[field]`（一行顺序调整即可）。
2. `UpdateRow/DeleteRow` 返回受影响行数（改 `Provider` 签名或新增 `ExecRow` 返回 `(int64, error)`），
   前端 `affected === 0` 时提示"数据可能已被修改，请刷新"，不要 `markCommitted`。
3. 同一行多条 pending 合并成一条 UPDATE（见 DB-05），WHERE 始终用**快照**。

**验收**：`DBResultGrid` 新增纯函数单测（不挂载组件）：给定 row/columns/PK，先暂存 PK 编辑，
`getPendingEdits()[0].where` 必须等于原始值。手工：改 PK → 保存 → 数据库里应真的变，刷新后仍是新值。

#### DB-02　过滤/排序/重新执行会清空 pending，但行对象已被改写 → 幽灵保存

**现象**：改一个格（出现"1 处未保存"）→ 在结果过滤框里输入任意字符（或点列头排序）→
保存条消失，格子里仍是新值，数据库没变。

**证据**：

```ts
// DBQueryEditor.vue:286-308 过滤/排序返回新数组
rows = rows.filter(...)      // 或 rows = [...rows].sort(...)
// DBResultGrid.vue:488-495
watch(() => props.rows, () => {
  if (pending.size) { pending.clear(); bumpPending() }   // ← 草稿被丢
  selectedCell.value = null
})
```

而 `onCellEditConfirm` 已经把新值写进行对象（`:387`），所以 `displayRows` 重新计算后**仍然显示新值**。
`onExecute` 开头 `queryResult.value = null`（`:572-576`）和 `resultFilter.value = ''`（`:591`）也会触发同一条
watch——即"点执行 = 丢弃未保存编辑"，没有任何提示。

**修法**（推荐顺序）：
1. 给 `props.rows` 的 watch 加来源判断：只有**结果集变化**（`queryResult` 身份变化）才清 pending；
   过滤/排序不应触发。最省事的做法是把 watch 改成 `watch(() => props.resultKey, …)`，
   由父组件传一个随结果集递增的 key。
2. 或者把 pending 从网格内部提升到 `DBQueryEditor`，以「结果集 key + 行序号」为索引，与显示派生解耦。
3. 行值不要就地改 `queryResult.rows`；保留 `baseline` 副本，渲染时叠加草稿。
4. 若确实要清空，先弹确认（"有 N 处未保存修改，执行将丢弃"）。

**验收**：暂存 1 条编辑 → 输入过滤词 → 保存条仍显示 1；清空过滤 → 仍在；排序 → 仍在；
执行新查询 → 弹确认（或明确丢弃并回滚显示）。

#### DB-03　自定义/JOIN SQL 完成后仍可内联编辑，写入基础表的同名列

**证据**（`DBQueryEditor.vue:272-284`）：

```ts
const resolvedPrimaryKeys = computed(() => {
  if (!props.tableName || props.isView || !props.primaryKeys?.length || !queryResult.value) return []
  const colMap = new Map(queryResult.value.columns.map(c => [c.name.toLowerCase(), c.name]))
  …
})
const canEditRows = computed(() => resolvedPrimaryKeys.value.length > 0)
```

判定条件里没有"当前 SQL 仍是浏览该表"这一条。表格 doc 的编辑器允许用户改 SQL；改成
`SELECT a.id, b.name FROM a JOIN b …` 后，只要结果里出现同名的 PK 列，`canEditRows` 仍为 true。
用户双击 `b.name` 修改后，保存发出的是 `UPDATE a SET name = … WHERE id = …`——
要么改错表，要么因为 `a` 没有 `name` 列而报错。

**修法**：把可编辑状态绑定到"浏览模式 + SQL 与 `DefaultTableQuery` 规范化后一致 + 结果列集合等于表列集合"；
`onExecute` 里已有一次 `DBDefaultTableQuery` 比对（`:583-593`），复用它决定 `browseMode`，
并让 `canEditRows` 依赖 `browseMode === true`；用户手工执行后立即转为只读。

**验收**：浏览表 → 把 SQL 改成 JOIN → 执行 → 双击单元格不应进入编辑态；改回默认 SQL → 可编辑。

#### DB-04　行编辑弹窗 / 删除行使用「当前行值」构造 WHERE

`onEditRowConfirm` 的 `where` 取自 `row[pkIdx]`（`:778-800`），`onDeleteRow` 同理（`:844-851`），
而 `row` 可能带着未保存的单元格编辑（`DBResultGrid` 已就地改过它）。
另外 `rowIndexOf` 的兜底是"逐值相等"匹配（`:844-851`），在重复行（无 PK 的视图/去重结果）上会命中错行。

**修法**：删除/整行编辑前先 `clearEdit()` 并拒绝在有 pending 的行上操作；
`rowIndexOf` 的兜底按"结果集 key + 行序号"解决，不要按值比较。

#### DB-05　保存无事务、无 affected 校验、失败即 break

`onSaveEdits`（`:740-772`）逐条 `DBUpdateRow`：第一条失败就 `break`，已成功的几条已落库，
但 `emit('cellUpdated')` 不触发（树/其他页签不刷新），`revertRow` 还会把该行其它未提交的编辑一起回滚。
所有 provider 的 `UpdateRow/DeleteRow` 都不返回受影响行数。

**修法**：`Provider` 增加批量更新入口（单事务、按行分组，`WHERE` 用快照）；返回每行 affected；
前端逐行标记成功/失败，失败的保留 pending 并高亮，而不是整行 revert。

---

### 4.2 B 组：跨库 / 跨 schema 拿错对象

#### DB-06　PostgreSQL：schema 完全不参与定位

```sql
-- provider_postgres.go:236-241 列：只按表名过滤，跨所有 schema
WHERE c.table_name = $1
-- :250-252 主键：$1::regclass 走 search_path
WHERE i.indrelid = $1::regclass AND i.indisprimary
-- :260-262 索引：只按 relname
WHERE t.relname = $1
```

`GetTables`（`:163-190`）却会返回 `public` 之外所有 schema 的表。结果：
- 同名表存在两个 schema 时，结构页把两份列拼在一起（重复列、类型错乱）；
- 点 `audit` 下的表：若 `public` 有同名表，数据/DDL/导出会静默命中 `public`；若只在 `audit` 下，则报 `relation does not exist`。

**修法**（与 DB-09 一起做）：
1. `TableInfo` 增加 `Schema string`；前端 doc 模型增加 `schema`，树节点显示 `schema.table`。
2. `GetTableSchema` / `DefaultTableQuery` / DDL 全部使用 `schema.table` 限定名（`pgQuoteIdent(schema)+"."+pgQuoteIdent(table)`）。
3. `GetTables` 对同名表给出可区分名称，或在 `TableInfo` 里带上 `Schema` 让前端自己拼。
4. 回归测试：建两个 schema 的同名表，断言 `GetTableSchema(schemaB)` 的列不含 schemaA 的。

#### DB-07　SQL Server：同库内跨 schema 混用

`INFORMATION_SCHEMA.COLUMNS WHERE TABLE_NAME = @p1`（`provider_sqlserver.go:236-244`）、
主键（`:247-258`）、索引 `sys.tables t WHERE t.name = @p1`（`:268-280`）都不带 `TABLE_SCHEMA`。
`dbo.users` 与 `sales.users` 同时存在时会出现 DB-06 同样的症状。

**修法**：同上，统一 `schema.table`；SQL Server 的默认 schema 是 `dbo`，不要依赖用户的默认 schema。

#### DB-08　PostgreSQL schema 缓存：永不失效 + 跨连接共享（F-115 引入）

```go
var postgresSchemaCache sync.Map                 // provider_postgres.go:21
cacheKey := dbName + "." + tableName             // :223  不含连接/主机/端口
if cached, ok := postgresSchemaCache.Load(cacheKey); ok { return cached.(*SchemaResult), nil }
…
postgresSchemaCache.Store(cacheKey, result)      // :331  之后再也没有 Delete
```

两个后果：
1. DDL（`ALTER TABLE`/`DROP COLUMN`）成功后，前端 `DBTableStructure` 的 `loadSchema()` 拿到旧结构，
   用户以为没生效 → 再改一次 → `Duplicate column`。**这是「越修越坏」最直观的一条**。
2. 同一个 `dbName.tableName` 出现在两个连接（比如测试库/生产库都叫 `app.users`）时，缓存串用，
   显示的列来自另一台服务器。

**修法**：
1. 删除缓存，或改为 `key = providerInstance + dbName + schema + table` 且带 TTL（例如 30s）。
2. 增加 `InvalidateTableSchema(p Provider, db *sql.DB, ref TableRef)`，在 `AddColumn/ModifyColumn/DropColumn/AddIndex/DropIndex/DropTable/CopyTable` 成功后调用
   （DDL 方法内部知道 `db` 指针，可以作为 key 的一部分）。
3. 前端 DDL 成功后改用 `force=true` 的查询（或先 `Invalidate` 再 `loadSchema`）。
4. 单测：store → 调 DDL 的 invalidate → load 必须 miss。

#### DB-09　`dbName` 一字段三义（架构级）

见 §2 R3。这一条不改，DB-06/07 只能一个方言一个方言地补，且每补一次都要重新验证另外三个方言。

**建议的契约变更**（一次性、可渐进）：

```go
type TableRef struct {
    Database string // MySQL/MSSQL/SQLite 概念，PG 可空
    Schema   string // PG/MSSQL/Oracle；MySQL 为 db
    Name     string
}
func (t TableRef) Qualified(p Provider) string
```

- `Provider` 方法从 `(db, dbName, tableName)` 迁移到 `(db, TableRef)`；
- 迁移期保留旧签名作为包装，避免一次性改 6 个 provider + 10 个前端组件；
- 前端 doc 模型同步加 `schema`，树节点标题显示 `schema.table`（i18n 不需要新 key）。

**验收**：6 个方言各跑一遍「两个 schema 同名表」矩阵；MySQL/SQLite 行为不变（schema 为空）。

---

### 4.3 C 组：超时、卡死、大对象

#### DB-10　MySQL 驱动 10s 读超时与 30s 查询超时冲突

```go
// provider_mysql.go:31-40
"timeout": "10s", "readTimeout": "10s", "writeTimeout": "10s"
```

`go-sql-driver` 的 `readTimeout` 是**每次读 socket** 的 deadline。一条 15s 才返回第一行的大查询会直接
报 `invalid connection` / `i/o timeout`，连接被标记坏掉；`queryTimeout=30s`（`executor.go:18`）
永远不会生效。用户/贡献者把 `queryTimeout` 调大也修不好，于是"又坏了一个地方"。

**修法**：`readTimeout/writeTimeout` 至少不低于 `queryTimeout`（或用 `0` 交给 ctx 控制）；
`timeout`（拨号）保持 10s。写一条注释说明三者关系，避免下次又被"加固"回去。

**验收**：对 MySQL 执行 `SELECT SLEEP(12)`：应返回结果（或 ctx 超时），不应出现 `invalid connection`。

#### DB-11　DDL / 导出路径没有超时

`mysqlUseDB`、MySQL/Oracle/SQL Server 的所有 DDL、全部 `DumpTable` 都用 `context.Background()`。
在锁等待（`ALTER TABLE` 等 MDL）或大表导出场景下，UI 永远转圈，只能杀进程——而 `executor.go:14-19`
的注释明确写着加超时就是为了"不让 UI 卡死"，说明这是漏改，不是设计。

**修法**：抽一个 `withTimeout(ctx, ddlTimeout)` 统一包装；DDL 超时可以比查询长（例如 60s），
导出单独一个更长或可取消的 ctx（并接前端取消，见 DB-16）。

#### DB-12　`DumpTable` 无行数上限、无流式

`SELECT <all cols> FROM <table>` 把全部行读进 `strings.Builder`（`provider_mysql.go:654-676` 等 6 处），
然后整份字符串经 Wails 传前端，前端再 `encodeBase64`（`DBTreePanel.vue:615-620`）→ 至少 2~3 份内存副本。
百万行表会让应用直接无响应。

**修法**：分批（`LIMIT/OFFSET` 或游标）+ 边读边写文件（后端直接落盘，不经前端）；
`DumpOptions` 增加 `MaxRows` 和取消 ctx；前端改为"后端导出到文件"的 API。

#### DB-13　F-116 连接池参数被 session 层覆盖

```go
// backend/database/engine.go:49-50   NewDB()
db.SetMaxOpenConns(20); db.SetMaxIdleConns(5)
// backend/session/database_session.go:62-63  Connect() 随后覆盖
db.SetMaxOpenConns(5);  db.SetMaxIdleConns(2)
```

`Connect` 在 `NewDB` 之后执行，所以 20/5 从未生效。并发多页签查询会互相等连接，
表现为"偶尔很慢"，且和 F-116 的预期（修复 MySQL 连接池）完全相反。

**修法**：删掉 session 层的两行，或把参数提到 `ConnectionConfig`；在 `engine.go` 里保留唯一出处。

#### DB-14　打开「结构」页也会跑一次 browse SELECT

`DBTabContent.vue:131-137` 用 `v-show` 控制 data/structure 子页，`DBQueryEditor` 始终被挂载；
它 `onMounted` 里 `autoRun` 会执行 `SELECT * … LIMIT 100`（`DBQueryEditor.vue:468-475`）。
用户只是想看表结构，却对表发了查询（大表/加锁表上代价不小，视图上可能很慢）。

**修法**：`subTab === 'structure'` 时不挂载数据面板（`v-if`），或在 `autoRun` 前置一个"首次激活"判断。

#### DB-15　`firstStatement` 用裸 `split(';')`，字符串里的分号截断 SQL（P0）

```ts
// DBQueryEditor.vue:401-406
const parts = trimmed.split(';').map(s => s.trim()).filter(Boolean)
return parts[0] || trimmed
```

`hasMultipleStatements` 用的是能识别引号的 `SplitScriptLike`（`:413-451`），两者判定不一致：

| 输入 | 多语句判定 | 实际发送 |
|---|---|---|
| `SELECT ';' AS x` | 1 条（引号内） | `SELECT '` → 语法错误 |
| `SELECT 1; -- tail` | 2 条 | 走脚本分支（结果丢失，见 DB-27） |
| `SELECT 'a;b' AS x, 2` | 1 条 | `SELECT 'a` → 语法错误 |

**修法**：删除 `firstStatement` 的自研切分，统一用 `SplitScriptLike(text)[0]`（并补上块注释/
Postgres dollar-quote 处理），或者干脆把"是否查询"改为看后端返回（尝试 Query，失败再 Statement）。
单测：上表三行作为用例。

---

### 4.4 D 组：状态与竞态

#### DB-16　`onExecute` 没有请求令牌，"取消"是假的

```ts
let cancelled = false            // 每个组件实例一个
async function onExecute() {
  …
  loading.value = true
  cancelled = false              // ← 第二次执行会把第一次的"取消"复位
```

连点执行：第一次的响应回来时 `cancelled === false`，直接 `queryResult.value = result` 覆盖第二次的结果。
取消按钮（`:653-656`）只改标志位，后端查询继续跑到 30s。`scriptResult` / `loading` 同理。

**修法**：`let runSeq = 0`；每次执行 `const myRun = ++runSeq`，所有赋值前判 `if (myRun !== runSeq) return`。
取消时**同时**调用后端取消（见下）；没有后端取消就先隐藏按钮或标注"仅隐藏结果"。
后端取消：`ExecuteQuery` 的 ctx 需要与 session 绑定，提供 `CancelDBQuery(sessionID, runID)`，
用 `context.WithCancel` 存在 map 里（`app_database.go`）。

#### DB-17　树刷新无时序保护，且每次刷新都切换前台页签

```ts
// DBTreePanel.vue:240-296
async function loadTree() {
  const dbs = await GetDatabases(props.sessionId)   // 无序号校验
  …
  if (autoOpen) {
    selectedDb.value = autoOpen; selectedTable.value = ''
    emit('openDatabase', autoOpen, 'objects')       // ← 每次刷新都发
  }
}
```

`refreshAll` / `onCtxRefreshDatabases` / 建删库表后都会走这里，父组件 `onOpenDatabase` 会
`closeTree()`（窄屏收起抽屉）并 `activateDoc()`（把用户从当前表/查询切到 objects 页签）。
快速切换会话时，旧请求后到会覆盖新树。

**修法**：
1. `let treeSeq = 0`，每次 `loadTree` 递增，返回后校验；`sessionId` 变化时先清空 `databases/selected/expanded`。
2. `autoOpen` 只在**首次加载**（`docs` 为空或 `treeLoaded === false`）时 emit；刷新不要抢焦点。
3. 刷新失败要有可见错误（现在只 `console.error`）。

#### DB-18　`docs` 不随会话重置

`DBTabContent.vue:252` 的 `docs` 是本地 `ref`，没有 `watch(sessionId)`。
重连（同一 tab id、新 session）或把连接改成另一种 `dbType` 后，旧 doc 的 `dbName/tableName/primaryKeys`
继续被使用：查询可能落到同名但不同的库，编辑按钮的 PK 判断也可能来自旧表。

**修法**：`watch([sessionId, dbType], () => { docs.value = []; activeDocId.value = '' })`（或保留 doc 但清空
`queryResult/primaryKeys` 并强制重载）。

#### DB-19　先取 schema 再开页签：点击无反馈、失败静默只读

```ts
// DBTabContent.vue:456-480
const doc = { … }
await loadSchema(doc)     // 两次 Wails 往返，期间界面无变化
docs.value.push(doc)
```

`loadSchema` 的 `catch {}` 吞掉错误（`:448-455`），于是 `primaryKeys` 为空 →
`canEditRows === false` → 网格只读，且**没有任何提示**；用户再点一次同一个表才会重试
（`existing.primaryKeys.length` 为 0 的分支）。

**修法**：立即 push doc 并显示 loading；schema 到达后补 `primaryKeys`；失败时 toast 并保留"重试"入口。

#### DB-20　分页总数估算与"只过滤当前页"

`browsePageTotal`（`:310-316`）用 `(page+1)*pageSize+1` 推算"还有下一页"，最后一页刚好满页时会多出一个
空页；`resultFilter`/`sortProp` 只作用于**当前页**（`:286-308`），用户很容易以为是在过滤整张表。

**修法**：后端返回真实 `COUNT(*)`（可选，慢表可跳过）或把"还有更多"改成"下一页可点"而非页码；
过滤框文案标明"过滤当前页"，或把过滤下推到 SQL（带用户确认）。

---

### 4.5 E 组：一致性 / 可用性

#### DB-21　SQLite `Quote` 仍会返回空串

```go
// provider_sqlite.go:88-91
func (p *sqliteProvider) Quote(name string) string {
    q, _ := SafePgIdent(name)   // ← 错误被丢弃
    return q                    // 非法名（含 .. / \ / 长度>128）→ ""
}
```

`provider_quote_test.go` 本来就是为这个回归写的，但 `TestQuoteNeverReturnsEmpty`（`:39-46`）只遍历
`mysql/postgres/rqlite`——漏了 `sqlite`（也漏了 oracle/sqlserver，不过它们已是纯 doubling 实现）。
SQLite 允许 `CREATE TABLE "a/b"`，此时整张表在 UI 上不可浏览（生成 `SELECT * FROM `）。

**修法**：改为与 rqlite 一致的 `"` + 双写 `"`；测试矩阵改成遍历 `providers` 注册表全部键，
并加一条 `for name in ["", "odd..name", "a/b", "a\"b"]` 的通用断言。

#### DB-22　默认值 round-trip 不安全：只改注释也会把默认值改坏

- MySQL 读取时 `SHOW FULL COLUMNS` 的 `Default` 是原样字符串（`provider_mysql.go:211-226`），
  写回时统一加引号（`:510-515`）：
  - `CURRENT_TIMESTAMP` → `DEFAULT 'CURRENT_TIMESTAMP'`（从时间戳默认变成常量字符串）；
  - `(uuid())` 等 MySQL 8 表达式默认 → 被引号包成字面量；
  - 空串默认（`Default` 返回 `""`）与"无默认"不可区分 → `defaultType` 判定为 `none` → `MODIFY COLUMN`
    不带 DEFAULT，**默认值被移除**。
- Oracle `oracleDefaultValue`（`provider_oracle.go:590-599`）：只要值里含 `(` 或前缀 `TO_` 就**原样拼接**进 DDL
  — 用户填的任意片段都能绕过 F-11 的校验（`SafeColumnType` 只管类型）。Oracle 不支持单次调用多语句，
  所以更可能的结果是生成错误 SQL/意外 DDL，而不是直接 RCE；但这段逻辑一旦被其他方言或新入口复制，
  就会变成真注入（且合法表达式如 `SYS_GUID()` 也确实需要白名单而不是“含括号就放行”）。
- SQL Server `ModifyColumn`（`:378-395`）只做 `ALTER COLUMN … type NULL/NOT NULL`，完全不处理 `defaultVal`。

**修法**：为每个方言实现显式的 `renderDefault(col) (string, error)`，用白名单（数字 / `'text'` /
`CURRENT_TIMESTAMP` 及带精度形式 / `NULL`）而不是"含 `(` 就放行"；读路径同时返回 `defaultIsExpression bool`，
避免把表达式当字符串。测试：`ddl_safety_test.go` 增加各 provider 的 `renderDefault` 表驱动用例。

#### DB-23　导出脚本跨方言 / 跨对象类型不可移植

同一个 `DumpTable` 在四个 provider 里的产物质量不一致：

| 方言 | 结构段 | 数据段 | 问题 |
|---|---|---|---|
| MySQL | `tableKind()` → `DROP TABLE/VIEW IF EXISTS` + `SHOW CREATE` | ``INSERT INTO `t` `` | 基本正确；未限定库名，靠连接级 `USE` 重放 |
| PostgreSQL | 固定 `DROP TABLE IF EXISTS … CASCADE` + `CREATE TABLE`（`provider_postgres.go:583,653`） | `INSERT INTO "t"` | **视图会被导成表**；`DROP TABLE` 遇到视图直接报错；未限定 schema |
| SQL Server | 固定 `DROP TABLE IF EXISTS` + `CREATE TABLE`（`provider_sqlserver.go:574`） | `INSERT INTO [t]` | 视图同样被导成表；未限定 schema |
| Oracle | `DROP <kind> IF EXISTS`（`provider_oracle.go:667`） | `INSERT INTO "T"` | `IF EXISTS` 仅在 23ai+ 合法；未限定 owner |

**修法**：`DumpOptions` 增加对象类型；结构段按类型生成 `DROP VIEW` / `DROP TABLE`；
所有方言输出 `schema.table` 限定名（与 DB-09 的 `TableRef` 一起做）；
Oracle 用 PL/SQL 包装或 `BEGIN EXECUTE IMMEDIATE … EXCEPTION WHEN OTHERS THEN NULL; END;`。
验收：对表和对视图各导一次，导出的 .sql 在空 schema 里能直接跑通。

#### DB-24　SQL Server `withUse` 污染池化连接

`withUse` 把 `USE [db];\n<stmt>` 拼成一条 SQL（`provider_sqlserver.go:486-491`），在**池里的任意连接**上执行。
语句返回后，该连接的 current database 已改变并被归还池中；之后任何未显式 `USE` 的调用（例如
`dbName == ""`、或将来新增的方法）都会落在"上一次碰巧用过的库"。

**修法**：与 MySQL 的 `mysqlUseDB` 一样，用 `db.Conn(ctx)` 拿专用连接 → `PrepareExec` → 执行 → `Close`；
DDL/元数据查询全部走这条路径。删除字符串拼 `USE` 的做法。

#### DB-25　命名实例修复在 UI 默认路径不可达

`DSN` 只有在 `port <= 0` 时才省略端口并走 SQL Browser（`provider_sqlserver.go:32-40`），
但 `ConnectionForm` 在类型切换时总会把端口填成 1433（`:1462-1464`），新建连接同理。
用户输入 `server\instance` 后直接保存，DSN 会变成 `sqlserver://…@server:1433/instance`，
SQL Browser 发现被绕过，连接失败。

**修法**：`DSN` 检测到 `\` 时**忽略**显式端口（或仅当端口非默认 1433 时使用），并在表单里
把端口置灰/加提示"命名实例由 SQL Browser 解析端口，留空即可"。验收：不手动清空端口也能连上命名实例。

#### DB-26　死代码与半成品 API

- `QueryRowsStream`（`engine.go:135`）——大结果流式的修复，无调用方。
- `QueryResultToJSON`（`executor.go:136`）——无调用方。
- `PagedTableQuery` / `App.DBPagedTableQuery`（`provider.go:64`、`app_database.go:153`）——无调用方，
  `DefaultTableQuery` 同时承担首屏和翻页（Oracle 两处甚至用了完全不同的分页写法）。

**修法**：要么接入（分页用 `PagedTableQuery`，导出/大结果用 `QueryRowsStream`），要么删除。
留着半成品会让下一位贡献者以为功能已存在，从而在 `DefaultTableQuery` 上再改一次。

#### DB-27　多语句脚本里的 SELECT 结果被丢弃

`ExecuteScript` 对所有语句都用 `conn.ExecContext`（`executor.go:171-186`）。MySQL/PG 不会报错，
但结果集被静默丢弃；前端只显示 `scriptExecuted: n` + `affectedTotal`。
用户写 `SELECT 1; SELECT 2;` 点执行，会看到"脚本执行了 2 条"而不是两段结果。

**修法**：明确产品行为——要么脚本模式只允许非查询语句（前端提示"多语句模式不返回结果，请逐条执行"），
要么后端按语句判断：查询语句走 `QueryContext` 并把结果附在 `ScriptResult` 里。二选一，别留"看起来执行了"。

#### DB-28　同一操作在三处各写一遍

建库/建表/删表/删视图/清空表在 `DBTreePanel`（右键菜单）、`DBObjectList`（行内按钮）、
`DBTableStructure`（结构页）各有一份调用与后处理：有的 `loadTree()`（会抢焦点，见 DB-17），
有的 `refreshDb()`，有的 `load()` + `emit`。错误提示与确认文案也不统一。

**修法**：抽一个 `useDbObjectActions(sessionId)` composable，统一「执行 → 刷新 → 通知」，
三个组件只负责触发；`objectRemoved` 事件统一由 composable 发。

---

## 5. 排期建议

### 批次一：止血（P0，建议 3~4 天）

目标：不再静默丢数据、不再因为分号截断 SQL。

| 顺序 | 项 | 改动量 | 说明 |
|---|---|---|---|
| 1 | DB-01 | XS | 调换两行顺序 + Provider 返回 affected |
| 2 | DB-02 | S | watch 条件收紧 / pending 与结果集解耦 |
| 3 | DB-15 | XS | 复用 `SplitScriptLike`，补 3 个单测 |
| 4 | DB-03 | S | `canEditRows` 依赖 `browseMode` |
| 5 | DB-04 | S | 有 pending 时禁止删除/整行编辑 |
| 6 | DB-08 | S | 删缓存或加失效 + 连接维度 key |

验收（批次一）：§6 的「编辑冒烟清单」全部通过；`vitest` 新增 ≥4 个不依赖浏览器的纯函数用例。

### 批次二：契约与超时（P1，建议 5~7 天）

| 顺序 | 项 | 改动量 | 说明 |
|---|---|---|---|
| 1 | DB-09（设计）+ DB-06/07 | L | `TableRef`/`TableInfo.Schema`，6 个 provider + doc 模型 |
| 2 | DB-10/11 | M | MySQL 超时对齐；DDL/Dump 统一 ctx |
| 3 | DB-13 | XS | 删掉 session 层覆盖 |
| 4 | DB-16 | M | runSeq + 后端取消 |
| 5 | DB-17/18/19 | M | 树/页签状态机收拾干净 |
| 6 | DB-22 | M | 各方言 `renderDefault` 白名单 |

DB-09 需要先写一份半页的接口草案在 PR 里讨论，**不要**直接开改 6 个 provider。

### 批次三：一致性与体验（P2，建议 3~5 天）

DB-05、DB-12、DB-14、DB-20、DB-21、DB-23、DB-24、DB-25、DB-26、DB-27、DB-28。

---

## 6. 防回归护栏

### 6.1 后端：Provider 契约测试矩阵（本次最该补的东西）

新增 `backend/database/provider_contract_test.go`，对 `providers` 注册表**全量**遍历（不再手写列表），
每个方言至少覆盖：

| 用例 | 断言 |
|---|---|
| `Quote` 边界名（空串、`a"b`、`a\`b`、`a]b`、`a..b`、`a/b`、129 字符） | 永不返回空串；引号成对；内嵌引号被双写 |
| `DefaultTableQuery/PagedTableQuery` | 含 LIMIT/OFFSET 或方言等价物；表名被引用 |
| `renderDefault`（新增） | `CURRENT_TIMESTAMP`/表达式/数字/字符串/NULL 白名单；危险输入返回 error |
| `GetTableSchema` 多 schema 同名表 | 只返回目标 schema 的列 |
| `GetTableSchema` 缓存 | DDL 后必须失效 |
| `UpdateRow/DeleteRow` | 返回 affected；WHERE 用原值 |
| `DumpTable` | 输出可被 `SplitScript` 重新切分并在空库执行（可用 sqlite/rqlite 做闭环） |

没有真实数据库时，至少保证「SQL 形状」用表驱动断言（现有 `ddl_safety_test.go`/`provider_quote_test.go`
就是这个路子，只是覆盖率不够）。

### 6.2 前端：把编辑状态机抽成纯函数

现在组件不可挂载（无 jsdom/@vue/test-utils），但**状态迁移可以抽出来**：

```ts
// frontend/src/utils/dbEditState.ts（示意）
createEditState(rows, columns, pks)
stage(state, rowIdx, fieldIdx, newValue)   // 返回新 state
pendingEdits(state)                        // [{ where, field, value }]
applyResult(state, newRows)                // 只有这里允许清空 pending
applyDerivedView(state, filter, sort)      // 不得清空 pending
revert(state, rowIdx)
```

`DBResultGrid` 只做渲染与事件转发。这样 §4.1 的每条都能用 vitest 复现，
也不用再靠"读 SFC 源码正则"来防回归。

### 6.3 手工冒烟清单（每次动数据库代码都跑）

对 **MySQL / PostgreSQL / Oracle / SQL Server / SQLite** 各跑一遍（可只跑改到的方言 + 至少一个其他方言）：

1. 打开树 → 展开库 → 打开表（数据页应出结果，结构页不应额外发 SELECT）。
2. 双击非主键格改值 → 过滤框输入字符 → 保存条仍在 → 清空过滤 → 保存 → 刷新后是新值。
3. 双击主键格改值 → 保存 → 刷新后是新值（且旧值行不存在）。
4. 把 SQL 改成 `SELECT 1 AS id` 执行 → 单元格不可编辑。
5. 执行 `SELECT ';' AS x` → 有 1 行结果（不是语法错误）。
6. 连点两次执行（第一次慢查询）→ 最终显示的是最后一次的结果。
7. 结构页加一列 → 列表立刻出现新列 → 再加同名列应报"已存在"（而不是"没生效"）。
8. 刷新树的按钮 → 当前页签/选中项不应跳走。
9. 导出 10 万行表 → 有进度/可取消，内存不飙。
10. 断开重连同一连接 → 旧页签不残留旧库的查询结果。

### 6.4 Review checklist（改数据库代码必查）

- [ ] 这个改动是否需要在**所有 6 个 provider** 同步？有没有用注册表遍历测试？
- [ ] 是否新增了缓存？失效路径在哪、key 是否包含连接/schema？
- [ ] 是否新增了 `context.Background()`？能不能用带超时的 ctx？
- [ ] 写操作是否检查 affected rows？是否用「原值」构造 WHERE？
- [ ] 前端是否又直接改 `queryResult` 的行对象？pending 的 key 是否会随过滤/排序变化？
- [ ] 是否新增了异步调用？失败/乱序/取消路径是否都有明确行为？
- [ ] 新文案是否走了 i18n（9 个 locale）？
- [ ] 是否在 `docs/dev/` 或 PR 描述里更新了本次审查的状态表？

### 6.5 明令禁止

- 不要再用「连接级 `USE` / `CURRENT_SCHEMA` + 未限定表名」实现跨库/跨 schema 访问（DB-09 的根源）。
- 不要把 `pending` 之类跨结果集的状态挂在"行对象身份"上。
- 不要在没有取消语义的情况下提供"取消"按钮。
- 不要用 SFC 源码字符串断言来替代交互测试（可以保留，但不能作为唯一防线）。

---

## 7. 状态表

| ID | 状态 | 负责 | PR | 备注 |
|---|---|---|---|---|
| DB-01 | 已修 | | | 2026-10-25：网格先 snapshotPk 再写行；Provider UpdateRow/DeleteRow 返回 affected（execPreparedAffected，-1=驱动不支持）；MySQL DSN 加 clientFoundRows；前端 0 行不 markCommitted；测试 backend/database/row_affected_test.go + DBResultGrid.pkSnapshot.test.ts |
| DB-02 | 已修 | | | 2026-10-25：pending 清理从 watch(props.rows) 移到 watch(props.resultKey)（父层 resultSeq 随新结果集递增）；过滤/排序不再丢草稿；执行新查询前弹 db.discardEditsConfirm 确认；测试 DBResultGrid.pendingLifecycle.test.ts |
| DB-03 | 已修 | | | 2026-10-25：canEditRows = browseMode && PK 存在；onExecute 用 DefaultTableQuery 比对决定 browseMode，手工执行立即只读；测试 DBQueryEditor.editability.test.ts |
| DB-04 | 已修 | | | 2026-10-25：rowIndexOf 只按行对象身份定位；有 pending 的行拒绝删除/整行编辑（db.saveOrDiscardFirst）；网格暴露 isRowPending；测试 DBQueryEditor.rowActions.test.ts |
| DB-05 | 待修 | | | 依赖 DB-01 的 affected 返回值 |
| DB-06 | 已修 | | | 2026-10-25：TableInfo.Schema + TableRef/SchemaDDL（tableref.go）；PG/MSSQL 元数据探测按 schema 钉死；浏览 SQL/行级 CRUD/全部 DDL 带 schema 限定；树/对象列表/结构页/编辑器全线透传；测试 schema_qualification_test.go + schemaThreading.test.ts |
| DB-07 | 已修 | | | 2026-10-25：MSSQL 同上（TABLE_SCHEMA 过滤 + [schema].[table] + GetTables 发布 SCHEMA_NAME）；随 DB-06 一并验证 |
| DB-08 | 已修 | | | 2026-10-25：缓存抽到 postgres_schema_cache.go；key 含连接池指针 + 30s TTL；全部 PG DDL 成功后 pgSchemaCacheInvalidate；测试重写（跨池隔离/失效/过期/并发） |
| DB-09 | 已修（第一阶段） | | | 2026-10-25：TableRef + SchemaDDL 能力接口（backend/database/tableref.go），旧签名保留为包装；PG/MSSQL 实现已覆盖元数据/浏览/行 CRUD/DDL；MySQL/SQLite/Oracle 不实现（USE/文件/owner 约定不变）。遗留：Dump 导出限定名归 DB-23，剩余方言随需求推进 |
| DB-10 | 已修 | | | 2026-10-25：MySQL DSN readTimeout/writeTimeout=0（交给 ctx），dial 保持10s；预算顺序 query<ddl<dump 由测试守护 |
| DB-11 | 已修 | | | 2026-10-25：新增 timeout.go（ddlCtx/dumpCtx/execDDL，60s/10m）；六方言全部 DDL/导出/PrepareExec/行 CRUD 均带预算；守护测试 timeout_contract_test.go |
| DB-12 | 待修 | | | |
| DB-13 | 已修 | | | 2026-10-25：删除 session 层 SetMaxOpenConns/SetMaxIdleConns/ConnMaxLifetime 覆盖，engine.go 为唯一出处；守卫测试 database_session_pool_test.go |
| DB-14 | 已修 | | | 2026-10-25：编辑器新增 active prop；子页为结构页时不 autoRun，首次切到数据页才跑且已有结果不重跑；测试 activation.test.ts |
| DB-15 | 已修 | | | 2026-10-25：抽 `utils/sqlStatements.ts`（引号/行注释/块注释/dollar-quote 感知），firstStatement 与多语句判定共用；注释片段不计为语句；纯函数单测 14 例 |
| DB-16 | 已修 | | | 2026-10-25：前端 runSeq 令牌 + activeCall.cancel()；App.ExecuteQuery/Statement/SQLScript 接收请求 ctx（Wails 注入），database 层新增 Ctx 变体；CancelError 不报错；测试 executor_cancel_test.go + executionToken.test.ts |
| DB-17 | 已修 | | | 2026-10-25：loadTree 加 treeSeq 令牌（每次 await 后校验）；sessionId 切换清空树状态并作废在飞请求；autoOpen 仅首次加载 emit；失败 msg.error(db.treeLoadFailed)；测试 DBTreePanel.loadOrder.test.ts |
| DB-18 | 已修 | | | 2026-10-25：watch([sessionId, dbType]) 清空 docs/activeDocId；树在 sessionId 变化时同步清状态（DB-17） |
| DB-19 | 已修 | | | 2026-10-25：onSelectTable 先 push doc 再后台 loadSchema；失败 toast（db.schemaLoadFailed）且可重试；测试 DBTabContent.sessionReset.test.ts |
| DB-20 | 已修 | | | 2026-10-25：空尾页自动回退到真实最后一页（不入历史）；过滤框文案改 db.filterResults 为“本页”范围（11 locale）；测试 pageScoping.test.ts |
| DB-21 | 已修 | | | 2026-10-25：sqlite Quote 改为 `"`+双写；测试矩阵改遍历 providers 注册表（长度哨兵防新方言漏测）+ 包裹/转义双重断言 |
| DB-22 | 已修 | | | 2026-10-25：共享 RenderDefaultLiteral 白名单（数字/NULL/CURRENT_* 带精度/带参函数白名单/PG cast/括号表达式，其余转义为字符串）；MySQL 读路径改 information_schema（空串默认不再丢失）；MSSQL ModifyColumn 会替换默认值约束；Oracle 括号直通规则移除；测试 coldefault_test.go |
| DB-23 | 待修 | | | |
| DB-24 | 已修 | | | 2026-10-25：删除 withUse 字符串批处理；全部元数据/DDL 走专用连接（onDB/execDDLOnDB/queryStringsOnDB/queryRowsOnDB）；queryStrings 重构出 queryer 接口；守卫测试 TestSqlServerNoPooledUseBatch |
| DB-25 | 已修 | | | 2026-10-25：命名实例 DSN 无条件忽略显式端口（走 SQL Browser）；表单端口置灰 + db.namedInstancePortHint 提示；测试 TestSqlserverDSN + namedInstance.test.ts |
| DB-26 | 已决（删除） | | | 2026-10-25：删除无调用方的 QueryRowsStream / QueryResultToJSON / PagedTableQuery / DBPagedTableQuery；分页统一走 DefaultTableQuery |
| DB-27 | 已决（告知） | | | 2026-10-25：脚本模式保留全量执行（导入场景）；含查询语句时结果面板显示 db.scriptNoResults 明确告知不返回结果集；测试 scriptNotice.test.ts |
| DB-28 | 待修 | | | |

---

## 附录 A：本篇结论的可验证点（5 分钟）

```bash
# 1) 连接池修复被覆盖
grep -n "SetMaxOpenConns" backend/database/engine.go backend/session/database_session.go

# 2) PG 缓存无失效
grep -rn "postgresSchemaCache" backend/database/
grep -rn "postgresSchemaCache.Delete" backend/database/ | grep -v _test.go   # 无输出

# 3) Quote 回归测试漏掉的方言
grep -n "range \[\]string{\"mysql\"" -A 2 backend/database/provider_quote_test.go

# 4) DDL 仍然没有超时
grep -n "context.Background()" backend/database/provider_*.go | wc -l

# 5) 分号截断
grep -n "function firstStatement" -A 5 frontend/src/components/DBQueryEditor.vue

# 6) 前端网格就地改行 + 晚拍 PK 快照
grep -n "row\[field\] = newVal" -A 4 frontend/src/components/DBResultGrid.vue
```

## 附录 B：这条回归链的提交考古

| 日期 | 提交 | 内容 | 与本文的关系 |
|---|---|---|---|
| 2026-07-28 | `69695073` | F-115 PG `GetTableSchema` 并行 + 缓存 | 引入 DB-08 |
| 2026-08-03 | `38b54d61` | 分页/过滤/排序、`firstStatement`、大量 DB UI 重构 | 引入 DB-02 的前提、DB-15 |
| 2026-08-07 | `536787df` | VXE 集成，网格加 `watch(props.rows)` 重置选中 | DB-02 的触发器 |
| 2026-08-23 | `a7991e84` | 结果网格改回 el-table + 显式保存 | 把 `pending.clear()` 塞进上面的 watch → DB-02 成型 |
| 2026-10-22 | `dd62dff5` | 单元格复制（选中/右键） | 继续复用同一个 watch，问题更难被发现 |
| 2026-10-22 | `df94e236` | 去掉 el-table-v2 只读分支 | 单一 el-table 同时承担只读/编辑，DB-03 更易触发 |
| — | `374fcf2f` | F-116 MySQL 连接池调优 | 被 `database_session.go` 覆盖 → DB-13 |
| — | `2b358b63` | SQL Server 命名实例 | UI 默认端口使其不可达 → DB-25 |
| — | `4aa8d5bf` | Dump 查询按 schema 限定 | 只修了元数据查询，导出的 DROP/INSERT 仍未限定 → DB-23 |
| — | `e592cb1d` | Oracle DDL 始终引用表名（F-11） | 默认值路径仍未加固 → DB-22 |
