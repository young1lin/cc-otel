## 更新日志（CHANGELOG）

[English version](./CHANGELOG_EN.md)

本项目采用轻量更新日志格式（参考 Keep a Changelog），适合快速迭代的小工具。

---

## 未发布

### 修复

- **dsh 插件对齐只升不降**：`@young1lin/dsh-cc-otel` 启动时把 `~/.cc-otel/` 对齐到捆绑二进制的判据，从"版本不相等就 `install`"改为方向比较（新增 `npm/dsh-cc-otel/lib/versions.js`）：仅当捆绑版本**严格更新**时才覆盖并重启守护进程；已装版本更新（如手工部署的 dev 构建）时跳过并提示；版本不可解析（`X.Y.Z` / `vX.Y.Z-N-g<sha>[-dirty]` 之外）时一律不碰。git-describe 后缀按"base tag 之后 N 提交"排序（`0.1.6 < v0.1.6-1-g… < 0.1.7`）。此前插件 @0.1.3 曾在 profile 启动时把生产 daemon"对齐"回 0.1.3（2026-11-06 事故；spec 见 `docs/internal/design/align-never-downgrade.md`）。
- **dsh 插件主动自更新**：每次启动最多做一次 npm 检查（默认 24 小时节流，标记文件在系统临时目录），发现新版 `@young1lin/dsh-cc-otel` 时自动执行 `dsh plugin --profile <p> update @young1lin/dsh-cc-otel`，下次 boot 生效；失败只记日志，不影响遥测与守护进程。新增插件行配置：`pluginAutoUpdate`（默认开启；`false` 时仅提示不执行）与 `updateCheckHours`（默认 24；`0` 关闭检查）。

## [v0.1.8] - 2026-11-11

### 修复

- **导入弹窗后台任务进度冻结（live 事故 2026-11-07）**：01952a5 给轮询加门后，点击按钮从未重启轮询链——弹窗在开机 tick 之后才打开时，卡片永远停在 POST 返回的 0% 快照（实测：后端 4 秒完成的 15807 行导入，UI 一直显示 "Importing 0 / 15,807 rows (0%)"）。Cursor 导入卡片与 Codex 迁移卡片同病（后者为 01952a5 起的存量隐患，本次一并修复）：POST 成功后立即重启轮询；弹窗重新打开时（MutationObserver 监听 `style`/`aria-hidden`）也恢复轮询，终态（Done / 失败信息）必达卡片。

### 新增

- **Clients 面板模型下钻**：点击 client 行展开该 client 下**每个模型**的消耗明细（Requests / Uncached / Cache Read / Cache Create / Output / Total / Share / Cache Hit / Cost），**按总 token 倒序**，Share 为该模型占此 client 的百分比（分母取本次响应的模型合计，恒为 100%）；再次点击收起。展开时按**当前范围选择器**懒加载最新数据（`GET /api/clients/models?service_name=&from=&to=`，`claude_pool_requests` 视图 GROUP BY model，与父行同源同口径——空/NULL service 折叠到 `claude-code`，cost int64→float 同款转换；实测对账：10 个 client × All Time/7 天两范围 Σtokens/Σrequests/Σcost 与父行全等）。SSE 推送/切范围重绘父表后，已展开的行自动按新范围重取并保持展开。chevron 按钮 `aria-expanded` + 90° 旋转（120-180ms）、键盘 Enter 可用；表格定义与父表逐列一致（pinned Input 侧语义）。顺带修正三处过时文案（面板提示与两处注释仍称"视图排除 codex"，与统一池现实相反）。

- **Cursor 账单导入（页面）**：数据库导入弹窗新增 **Cursor usage import** 卡片（Cursor 官方 2D 图标，内联 SVG、`currentColor` 随主题）。多选上传 Cursor `usage-events` CSV 导出（Settings → Usage → Export usage events），列映射四 token 分量：`Input (w/o Cache Write)→input_tokens`、`Input (w/ Cache Write)→cache_creation_tokens`、`Cache Read→cache_read_tokens`、`Output→output_tokens`。无 request id 的导出按内容哈希生成 `cursor-<sha256[:16]>`，落在 `api_requests(request_id)` partial unique index 上——重复下载、按月重叠、反复导入全部幂等折叠（重跑只计 duplicates）。模型名落库为**基础身份**（`NormalizeCursorModel`）：Cursor 档位后缀（`fast`=速度、`max`/`thinking`/`xhigh`/`high`/`medium`/`low`=思考程度）与 `cursor-` 前缀剥离（`gpt-5.1-codex-max`→`gpt-5.1-codex`），整段白名单匹配不误伤真实型号（`grok-code-fast-1`、`gemini-3-flash-preview` 原样）；原始 slug 仍参与去重哈希，映射调整不破坏幂等。行归因 `service_name='cursor'`（统一视图 / Clients 面板自动生效），云 Agent 行带 `cursor-agent-<id>` 会话；zero-token 行默认跳过（可选保留）。**成本口径（2026-11-08 用户指定）**：套餐内行（CSV `Included/Free/'-'` = $0）按价格表**牌价估算**——导入时即算（`cursorimport.Import` 注入 Pricer，按基础模型名 Calc），历史行由 ↻重算补价；CSV 为正的成本（User API Key 实付）原样保留、估算永不覆盖（recompute 对 `cost_usd>0` 的 cursor 行跳过）。为此 `pricing.IsUpstreamAuthoritative` 移除 cursor 全量分支。彩排（18899）：重算后 cursor 总成本 $1467.55（18890 行，35 模型，31 个有价）。接口 `GET/POST /api/cursor/import`（同源守卫；与数据库导入、Codex 迁移共享单写者闸，并发 409），写完自动聚合校平 + `import_ledger` 审计 + WAL TRUNCATE。彩排（18899，真实 11 份导出 20683 行）：首跑 +18890（= 20683 − 1793 零 token 行），token 四分量与 CSV 侧逐位一致，重跑 +0 新增 / 18890 duplicates，pool == agg == ledger 对账通过。**导入体验增量（2026-11-08）**：① 兼容新导出格式（新增 `Automation ID`、`Max Mode` 两列，按列名映射天然通过）；仅带 Automation ID 的行归 `cursor-automation-<id>` 会话（两种 ID 都不进哈希，不影响幂等）——当前导出该列全空，属前瞻能力。② 状态接口新增 `history`（每次 GET 从 `import_ledger` 实时汇总过往导入：文件、行数、时间，重载页面仍在）与 `superseded_hint`（同毫秒同模型的交互模型行数 = Cursor 重导修订痕迹的可见足迹；后台族 grok-bot-\*/Bugbot/agent_review 同毫秒真并发、占彩排池 333 中的 320，已排除；UI 措辞 "possibly revised"），卡片在 Done 行追加提示并渲染最近 5 条导入历史。③ 修复 `:memory:` 库的连接池隐患：`db.Init` 对内存库 `SetMaxOpenConns(1)`（文件库保持 4）——SQLite 内存态按连接存在，GET 查询与导入并发时池内第二个连接是空库，曾致导入报 `no such table`。**卡片打磨（2026-11-08 方案 A）**：整卡可拖拽 CSV（复用 DB 导入的虚线高亮语言，outline 实现、零布局位移）；新增只读预览 `POST /api/cursor/import/dry-run`（同解析同守卫、不写库、不占写者闸）——选文件即自动跑，卡片显示"N file(s) · +X new · Y duplicates · 日期范围 · 新模型"（无价错误静默，no_usable_rows 显式提示；与导入并发时计数近似，属确认提示性质）；Done 后出现 "View in Clients panel →" 链接（关弹窗 + 切面板）。实测（18899）：已导入文件预览 `+0 new / 2651 duplicates`，导入前 `+2 new / 0 duplicates`。**Tab 化改版（2026-11-08 用户指定）**：导入弹窗顶部改为 **Database / Cursor** 两个 tab（安静分段控件，与范围 tab 同语言；每次打开默认 Database）。Cursor tab 顶部是与 DB 导入**同款 `.dbi-drop` 大拖拽区**（"Drop Cursor usage CSVs here"，点击=选文件，拖入=高亮）——可拖性从"隐藏能力"变为显式入口；弹窗标题改 "Import data"，副题涵盖三类来源；DB tab 内容（db 合并 + Codex 迁移卡）原样保留。**修正**：DB 导入操作条（Cancel / Choose database）原误置于两 pane 之外、Cursor tab 下仍显示，已移入 Database pane 底部。同日另一修正（用户指定）：Cursor 导入运行态从纯文本行改为与 DB 合并**同款 `.dbi-progress` 填充进度条**（精确比例驱动宽度、取整百分比作文本；meta 行承载 `processed / total · inserted · duplicates`），状态行瘦身为 "Importing…"。同日窄屏修正：`@media (max-width:640px)` 里 `.dbi-primary/.dbi-secondary { flex:1 1 140px }` 原为 DB footer/Codex 单按钮的全宽设计，Cursor 卡片两颗同 class 按钮被波及拉长——已限定回 `.dbi-footer` 与 `#codex-migrate-start`，Cursor 操作按钮保持自然宽度并允许换行。续报修正（"导入前上下滚动有问题"）：Cursor tab 拖拽区原借用 DB 空状态的 190px 巨高，640×700 下内容 581px vs 可视 553px——只超 28px 却生出一根滚动条。已压缩为紧凑二级入口（96px、图标 20px），实测 640×700 **滚动需求 28px→0**（DB tab 同尺寸本为 0）；640×560 极矮窗下 Cursor 需 33px，反而小于 DB tab 的 116px。验证手段：无头 Edge + CDP 强制视口几何测量（`bin/tmp/geom2.mjs`，gitignored）。

- 注：本版的离线价格快照（`internal/pricing/embed/seed.json`）未刷新——发布时无法访问上游（离线）；沿用既有快照作为回退。

## [v0.1.7] - 2026-11-05

### 修复

- **Usage Calendar All Time 布局**：`calendarLayoutMode` 一直接收 range 参数却从未使用，仅按周数选布局——历史不足 18 周时 All Time 落入 medium（22px 大格、居中，窄屏出现横向滚动条）。现在 `range=all` 无条件使用 long（13px 紧凑小格、左对齐）；固定窗口（Today / 7 Days / 30 Days / Custom）维持原阶梯不变。`insights-calendar.test.mjs` 以 69 天 / 11 周的生产数据形状（2026-08-28 起）钉住该行为。
- **月份标签重叠**：月底开始的历史在紧凑格子里，首个与下个月的月份标签只相隔一列——11px 的三字符标签约两个轨距宽，视觉上互相压字。`calendarMonthLabels` 新增 `minColGap`（默认 2，GitHub 贡献图同款策略）丢弃过近的标签；30px 大格的 short 布局传 1，不受影响。

## [v0.1.6] - 2026-11-04

### 变更

- **Agent 生命周期技能套件**：`.claude/skills/`（junction 同步可见于 `.agents/skills/`）新增六个英文技能，全部针对本仓库定制——design（需求分诊 + spec 落盘 `docs/internal/design/` + 用户批准门）、implement（按 spec 逐 Task 执行 + 编码守卫）、document（双语文档/记忆门禁）、pre-commit（禁提交路径 + 隐私扫描 + 构建测试门禁，通过后代执行提交）、code-review（本仓 12 类缺陷模式库 DP-01..12、8 个热点不变式、机械检查表）、release（npm/GitHub 发布清单 + 生产实例升级 runbook：彩排 → 哈希校验替换 → 冒烟矩阵 → 回滚）。缺陷模式与热点全部来自本仓库真实事故，每条带 commit 锚点。
- **`.agents` 镜像布局**：`AGENTS.md` 硬链 `.claude/CLAUDE.md`，`rules/`、`skills/`、`harness/` 逐项 junction——任何认 `.agents` 约定的 agent 工具读到同一份规则。
- **仓库卫生**：gitignore 收紧（本地 settings、临时计划、评审材料不入场）。
- **文档**：新增 Clients 面板截图（`assets/images/clients.png`，统一视图下的全部客户端），双语 README 已引用。

## [v0.1.5] - 2026-11-03

### 修复

- **OTLP 交付契约**：一批记录**全部**落库失败时 Export 现在返回 gRPC Internal / HTTP 500，批次不再被误认为已送达（是否重试取决于导出端实现；由于全部失败时无任何落库，真发生的重试也不会复制行）；部分失败返回 PartialSuccess（被拒记录按 OTLP 语义为终态——可见、不重发，避免部分落库的行被复制）。
- **Codex completion 重投双计**：重投守卫原按时间戳精确匹配，但 tracker 终结行保留的是 api_request 的更早时间戳——真实重投漏过守卫插入了重复行。身份判定改为（会话、模型、完整 token 向量）+ 时间窗匹配，e2e 钉住重投 no-op。
- **迁移后 sanity 检查按保留窗口范围化**：`daily_model_agg` 会被保留清理而 `api_requests` 不会，全时段对比在首次重启清理后误报 `codex agg mismatch`，重跑迁移必挂。新增 `dbmerge.Options.RetentionDays`，handler 注入配置，配 prune→re-migrate 回归测试。
- **DSN 池化 PRAGMA**：`busy_timeout`/`cache_size`/`mmap_size`/`temp_store`/`journal_size_limit` 经 DSN 下发到**每个**连接（原只设在首个连接，池内其余连接裸奔）。
- **WAL 高水位回收**：迁移后 PASSIVE 检查点从不收缩 `-wal` 文件（生产实测冻结在 340MB）；追加 TRUNCATE 检查点 + `journal_size_limit(64MB)`。
- **recompute apply 0 变更也重建聚合**：apply 按钮是聚合漂移的唯一修复入口，原实现 0 变更时跳过重建导致漂移永久无法修复。
- **Repository.Prepare 硬失败**：原日志声称的 per-call 回退并不存在，schema 漂移会在首个请求 panic；现在启动即 panic 并指名语句。
- **配置守卫**：`db_path: :memory:`、`otel_port/web_port <= 0`、负数 `retention_days` 在 Load 时拒绝（port 0 原会绑定随机临时端口而 /api/status 仍报配置值；仅 `otlp_http_port: 0` 保留显式禁用语义）。
- **同源守卫**：变更类 `/api/*`（pricing、import、codex/migrate）拒绝跨源浏览器请求（Origin 校验；curl/CLI 不受影响）。
- **pricing POST 请求体上限 1MB**（MaxBytesReader，与 import 一致）。
- **`pending_ttft_spans` 清理**：processed/过期行随 raw TTL 清扫删除（原只增不删）；raw 归档未来时间戳钳制到当前时间，恶意/错钟不得逃逸 TTL。
- **Codex cache-hit 分母对齐**：分母补上 `cache_creation`，与 Claude 视图和前端 token-math 一致（Codex 大量上报 cache_creation，旧公式显著高估）。
- **DSH 路径补齐双重钳制**（R5 P0）：DSH 会话账本的 body JSON 整数不经 parseAttrInt，先前可原样落库 MaxInt64 计数与 2262 年时间戳，重开永久仪表盘溢出毒害；现 ±2^40 钳制 + 未来时间戳钳到到达时刻（与 codex/claude 路径一致，新增 TestDSHE2E_HostileLedgerClamped 钉住）。
- **recompute 聚合重建恒全量**（R5 P1 修正表述）: `daily_model_agg` 不再被保留清理（与 `api_requests` 一致永不修剪），INSERT-only 重建不可能复活已删行，All-time 视图与明细等宽；`RetentionDays` 对重建不生效，迁移后 sanity 检查仅作保守窗口。
- **`/api/requests` 补齐五个统一池字段**（R5 API P1）：`reasoning_tokens`/`total_tokens`/`http_status`/`endpoint`/`conversation_id` 先前在统一端点恒为 0/空，与 `/api/codex/requests` 对同一行相互矛盾。
- **OTLP/HTTP 接收器跨源防护**（R5）：浏览器跨源 POST（简单 content-type 也可驱动）现被 Origin 校验拒绝 403；真实导出端从不携带 Origin，零兼容性影响（originutil 单一实现，与仪表盘守卫共享端口感知比较）。
- **405 补 `Allow` 头**（RFC 9110）：`/api/pricing`、`/api/pricing/recompute`、`/api/pricing/suggest`。
- **文档纠偏**：raw 表写入方向纠正（`raw_otlp_events` 收 codex 未建模事件并受 TTL；`codex_raw_otlp_events` 为冻结存档、无写入者）、retention_days 注释不再误列 `api_requests`、“AllClient 页”幻影指针全部改为 DB 导入对话框中的实际按钮。
- **Codex 历史自动导入 + 30 天清理**：启动时自动导入 `codex_*` 归档（单写者窗口内、精确行号区间记录；已迁移库零成本快路径），30 天后自动清除导入行并重建聚合；归档冻结不变。钉 TestAutoImport_* 四态。
- **导入弹窗支持历史 Codex 备份**：legacy 文件（统一表结构过老）整体路由 codex→统一映射、现代文件追加归档映射；缺列类型化常量补齐 + `tool_tokens` 别名；归一化/幂等与在线一致。钉 TestImportLegacyCodexBackupLandsInUnifiedTables（活体 18899 全链路实测：500 含 cache→300 disjoint、tool_result 缺列默认、重传 0 新增）。
- **turn_ttft 竞态**：OTLP 导出乱序时 turn 事件先于锚点到达，一次性 UPDATE 落空——现暂存（10min TTL）并在锚点插入时重试；实测修复前 1/2 行丢 ttft，修复后 3/3 全捕获。钉 TestCodexE2E_TurnTTFTBeforeAnchorIsStashedAndRetried。
- **Codex TTFT 补齐**：`codex.turn_ttft`（回合级首输出时延）现在直接建模——completion 的 `ttft_ms` 是 Option（非流式/瞬时响应缺省，实测 30,735 行仅 27% 有值），turn 事件对同（conversation, model）事件时间前最新的零 TTFT 锚点行做只填不清回填；权威 completion 带 ttft 时仍以后者为准（四种时序钉 TestCodexE2E_TurnTTFTFillsAnchor）。
- **重复锚点重投 ErrTxDone**（R5 rollback P1）：`insertCodexRowInTx` 在重复分支内部 commit，调用方再 commit 一次导致 `sql.ErrTxDone` —— 例行的至少一次投递重发返回 500。helper 现永不 commit（钉 TestCodexE2E_DuplicateAnchorRedeliveryStaysOK：三次相同锚点 200/200/200）。
- **bin→global 合并 schema 升级接线**（R5 rollback P1）：`import_global` 现在导入前调用 `ensureTargetSchema`，AllClient 导出可并入仍为 pre-AllClient schema 的生产 global，不再因缺列中途失败.
- **启动 ANALYZE 换 `PRAGMA optimize`**（R4 启动性能）：全库 ANALYZE 实测占每次 boot 的 85-95%（535MB 库 1.4-1.85s）；stats 新鲜时 `PRAGMA optimize` 实测 1ms。boot 从 1.46s 降到 ~0.2s。
- **`temp_store` 改 FILE**（R4 内存）：TEMP b-tree 与迁移 staging 表原驻 wasm 线性堆且从不归还内存，每次大迁移永久抬高 RSS 地板；落盘后峰值不再滞留。
- **写路径 BUSY 重试补齐**（R4 并发）：7 处裸 `ExecContext` 统一走 `execWithBusyRetry`，与既有接收路径一致。
- **recompute 与导入/迁移互斥**（R4 并发）：recompute 与 import/codex-migrate 同受 `writerBusMu` 门控，重量级写者不再并发争抢 busy_timeout 窗口（冲突时 409 `writer_busy`）。
- **SSE 刷新合并 + durations 按需拉取**（R4 前端）：SSE 刷新轮次带 in-flight 守卫；Request Log 翻页不再重发全表 durations GROUP BY。
- **静态资源 ETag/304**（R4/R5 前端）：embed 资产带内容哈希 ETag，`If-None-Match` 回 304（echarts 1MB 包每次刷新省 ~500KB gzip）；`/` 入口同哈希；开发模式自动跳过。
- **自动导入进度日志 + 回滚探针按时间戳**（R4/R5 启动）：大归档导入每 2s 输出进度；回滚探针改比 timestamp，fast-path 库不再每次 boot 误刷 WARNING。
- **`/api/codex/intraday` 非法日期 400**（R5 一致性）：原在 from/to 任一缺省时静默回退 200+今天数据；现与 base 端点一致拒绝。
- **API 4xx/5xx 统一结构化 JSON**（R5 一致性）：dashboard 家族与参数校验的纯文本错误收敛为 `writeAPIError` 信封，错误码 snake_case。
- **Codex Sessions 补 User 列并对齐排序**（R5 显示）：SELECT 漏 `MAX(user_id)` 导致 User 列恒空；排序对齐池侧 `SUM(cost_usd) DESC`。
- **`/api/codex/requests` 补 8 字段**（R5 API）：event_name/event_sequence/terminal_type/service_version/host_arch/os_type/os_version/error_message 现由 SELECT 实取。
- **Codex durations 吞吐列实算**（R5 显示）：Out tok/s (avg/wt) 原硬编码 0.0；现按池侧公式实算并排除 duration_ms=0 行。
- **迁移卡片空态文案**（R5 UX）：daemon 重启后空闲态不再误报 No Codex archive tables found；改为提示未巡检、可幂等重跑。
- **SSE 刷新规则回归 main**（R5 回归）：claude 标签事件不再刷新 Codex 标签（codex 端点全为 service 过滤，原为纯过度刷新）。
- **TTFT 悬浮提示换行修复**（R5 显示）：模板串内字面反斜杠+n 改为真实换行。
- **文档双 tab 语义纠正**（R5 文档）：README 双语重写为 All Clients（不含 Codex）+ Codex 双标签；补 `bind_host` 配置节与升级须知；otel-events.md 存储映射更新为统一池口径。

### 变更
- **修复 Codex 每请求 TTFT 缺失**：HTTP-SSE 传输（codex exec）此前从不为单个请求重建 TTFT——轮级 `codex.turn_ttft` 只能填工具循环轮的第一个请求，后续请求永远为 0（线上实测每轮 6 行有 4 行为 0）。现在 `response.created` 观测时间减去 pending `api_request` 的 startNanos 锚点为每个请求精确重建，与 websocket 路径同构（修复后线上实测 6/6 行全部有值）。
- **单一统一视图（用户裁定的最终形态）**：`claude_pool_*` 视图不再排除 codex，KPI/图表/Sessions/Request Log/Clients/Hour of Day/Rate 全部覆盖含 Codex 在内的所有客户端；移除顶部 All Clients | Codex 标签页，`?source=` 旧深链加载时静默改写为普通 URL；Hour of Day 与 Rate、Clients 恒可用（此前在 Codex 标签下被隐藏）；SSE 任意来源事件均触发刷新。`/api/codex/*` 镜像端点保留（外部脚本用），仪表盘自身不再调用。
- **`bind_host` 回环默认（升级注意）**：三个监听器（4317/4318/8899）默认从 `0.0.0.0` 收紧为 `127.0.0.1`，并启用 Host 白名单（仅 127.0.0.1/localhost/[::1]）。跨机器上报或远程访问的部署升级后需在 yaml 显式设 `bind_host: 0.0.0.0`（此时白名单自动停用，注意无内建鉴权）。详见 README 监听地址一节。

- `/api/status` 新增 `version` 字段（ldflags 注入，本地构建为 `dev`）。
- Codex 迁移作业记录开始/完成/失败日志（含 scanned/inserted/duplicates/verified 计数）；no-op 重跑跳过 staging 后检视（省 ~3s）。
- 日志轮转在 serve 启动时急切打开：只读数据目录不再静默吞日志，回退 stderr。
- `tools/migrate_codex_data` 标记废弃，指向内置 AllClient 迁移。

---

## [v0.1.4] - 2026-11-02

### 修复

- **Codex environment 落库到 User 列**：Codex CLI 会把 `[otel] environment`（`~/.codex/config.toml`，取值 dev / company / staging / prod，默认 dev）作为 OTLP Resource 属性 `env` 上报（codex-rs `otel/src/provider.rs` 的 `ENV_ATTRIBUTE`），此前 cc-otel 直接丢弃、User 列永远显示 —。现映射到与 Claude Code `user.id` 相同的 `user_id` 列，Request Log 的 User 列即可回答「这条请求来自哪个环境」；若未来 Codex 直接上报 `user.id`，则以其为准。只影响新写入的行，历史行保持空值。

### 修复

- **OpenRouter 询价加固**：官方地址未变（`https://openrouter.ai/api/v1`，2026-10 的文档仍在引用），拿不到价格的真实诱因是请求被边缘节点误伤/路径错误。💡 一键询价现在带产品 `User-Agent` + `Accept: application/json`（裸 `Go-http-client` 会被部分 CDN/WAF 过滤或误路由）；非 200 时把 OpenRouter 返回的具体原因（如 `HTTP 404 (Not Found)`）带进前端 toast，不再只有裸状态码；新增 `openrouter_base_url` 配置项与 `CC_OTEL_OPENROUTER_BASE_URL` 环境变量，可指向镜像或自建网关，留空/默认值自动回落官方地址。

### 新增

- **Clients 面板**：按 OTLP Resource `service.name` 汇总各客户端的 tokens（未缓存输入 / 缓存读 / 缓存写 / 输出 / 合计）、请求数、成本、命中率和活跃区间，按消耗量排序并带占比条。直读**永不被 retention 清理**的 `api_requests` 台账，历史全量；数据源切到 Codex 时面板自动隐藏（Codex 是独立账本）。
- **Hour of Day 子视图（客户端 × 模型维度）**：Daily Detail 第三个子标签，把所选时间区间折叠成 24 个「几点钟」桶，并新增两个下拉：不选时按模型分柱（Top 8 + Other，分组柱不堆叠）；锁定客户端看它的模型分布；**锁定模型则轴翻转为按客户端分柱**（「1 点钟谁在烧 Claude Opus」）；两者都锁就是单序列（「Claude Code 的 Opus 在 13:00 用了多少」），此时 tooltip 展示完整 token 构成。锁定模型后整图沿用该模型在主图表的基准色、按名次深浅分档（模型颜色身份不随维度切换漂移）；tooltip 每行下附另一维度构成（如「glm-5.3 这根柱 @01:00 = DSH Plugin 78% · Claude Code 22%」），表头同时标注当前锁定的客户端/模型。跟随顶部 Tokens/Cost/Requests 切换；下拉切换用已缓存行本地重切、不重新请求。后端 `/api/hours` 改为返回 (hour, service_name, model) 明细单元。与 Intraday 定位互补不重叠：Intraday 看短区间的 chronological 曲线，Hour of Day 看全区间的时间段 × 客户端 × 模型分布，且任意跨度可用（Intraday 限 ≤7 天）；Codex 源无对应镜像端点，该子标签自动隐藏。图表遵守项目铁律：分组柱、`trigger: 'item'` 单柱 tooltip（悬停只显示本柱信息 + 其跨维度构成）。
- **分页跳页**：共享分页条（Daily/Sessions/Request Log/Pricing）新增页码输入框 + Go 按钮（回车同样生效），超界自动吸附到最近合法页。修复点击 Go 的 blur 竞态（浏览器先 blur 重置输入框、click 才读到旧值导致跳页静默失效——改为读取输入事件缓存的最后键入值），并新增 `pagination.test.mjs` 7 个用例复现并钉死该场景。
- **客户端名显示与 Request Log 完全一致**：徽章渲染收敛为共享的 `formatSourceTag`（utils.js），未知 `service_name` 一律原文显示（如 `deepseek-harness-plugin`），不再有任何面板自造别名。

### 开发

- 集成测试数据按铁律以在线快照 `VACUUM INTO` 从全局库导入 bin（全局 daemon 全程未停）；期间发现 bin 库 `daily_model_agg` 重建后会被 90 天 retention 裁剪，故 Clients/Hour of Day 均直读 `api_requests` 保证与明细一致。

---

## [v0.1.3] - 2026-09-10

### 新增

- **dsh 遥测 adapter（持续上报）**：`@young1lin/dsh-cc-otel` 运行时插件新增透明 `llm/stream` 监听（`npm/dsh-cc-otel/lib/telemetry.js`），每次 provider 调用 settle 后把用量、时长、TTFT 组一条 OTLP `api_request` 记录（Claude Code 词汇，cc-otel 通用 ingestion 零改动直收）实时推给本地 4318——dsh 0.1.5 移除 `FULL` 后持续统计的正式来源。Resource `service.name` 用 `deepseek-harness-plugin`（精确 `deepseek-harness` 会被路由进 session 账本解析器丢弃）；`request_id = dshp-<uuid>`；批量 3s/16 条冲刷，失败即丢、绝不影响模型调用。bundle patch 把原生遥测行钉在 `DISABLED`（防反馈前缀导出双计）；`CC_OTEL_OTLP_URL` / 插件行 `telemetry.{url,enabled}` 可配置。注意：adapter 行与 `tools/dsh_ingest` 的键命名空间不相交，adapter 上线后不要再对同一批会话跑 session-log 补录。

## [v0.1.2] - 2026-09-10

> 新增 npm 分发渠道：DeepSeek Harness 一条命令接入 cc-otel；Go 二进制按平台拆成 6 个 npm 包随主包自动分发。原有发布方式（GoReleaser / GitHub Releases / Claude Code 插件市场 / 源码编译）零改动。

### 新增

- **npm 分发**：主包 `@young1lin/dsh-cc-otel`（dsh profile bundle：遥测 patch + 守护进程保活插件 + `cc-otel` CLI shim）+ `@young1lin/cc-otel-{linux,darwin,win32}-{x64,arm64}` 六个平台二进制包。平台包用 `os`/`cpu` 字段 + 主包 `optionalDependencies` 声明，包管理器只安装与当前环境匹配的一个；无 install 脚本，不触发 pnpm 10+ 的构建脚本白名单。安装：`dsh plugin --profile web add @young1lin/dsh-cc-otel`（任意 profile 通用）。
- **dsh 插件运行时**（`npm/dsh-cc-otel/lib/index.js`）：profile 启动时解析平台二进制，与 `~/.cc-otel/` 做版本对齐——版本不一致才执行 `cc-otel install`（版本门控，稳定状态下是空操作，不会反复重启健康守护进程）；守护进程始终从 `~/.cc-otel/` 稳定路径运行，Windows 上 `dsh plugin update/remove` 不再因可执行文件被占用而失败；`cc-otel start` 幂等（"already running" 视为成功），默认不在 dsh 退出时停掉共享守护进程（`stopOnUnload` 可选）。支持 `autoStart` / `manageHomeBinary` / `binary`（`CC_OTEL_BIN`）配置覆盖。
- **遥测接入零配置**：bundle 的 `cordis.patch.yml` 按 `id` 整键覆盖 dsh-base 的 `session-telemetry-otel` 行，指向本地 `http://localhost:4318/v1/logs`（`mode` 默认 `FEEDBACK_ONLY`——dsh 0.1.5 起上游移除了 `FULL`，设了会在启动时校验失败；上报时机与隐私说明见 README）；`DSH_TELEMETRY_MODE` / `DSH_TELEMETRY_OTLP_URL` / `DSH_TELEMETRY_DISABLED` 环境变量、用户手写的 `cordis.patch.yml` patch 层全部照常生效（层级在插件之后），与既有手工接入方式完全兼容、可共存。
- **DSH 0.1.5 适配**：receiver 的 `request_id` 升级为 `dsh-v<session.format_version>-<session.id>-<event.seq>`（属性缺失时回退旧的无版本键形），v3 世代重编号后的 seq 不再与库存 v0 行碰撞互吞；新增 `tools/dsh_ingest`——0.1.5 移除 `FULL` 遥测后普通对话零上报，该工具直接读本地会话日志（`~/.dsh/sessions/**/session.v3.jsonl.zstd`）幂等补录用量，写入前先清掉同会话新旧两种键形的旧行避免双计；全仓 `mode: FULL` 示例（README 中英 / otel-setup skill / npm 包）统一改为 `FEEDBACK_ONLY`，排查清单补「启动报 `mode expected …`」与「对话了但没数据」两项。
- **发布流水线**：Release workflow 新增 `npm` job——下载 GoReleaser 刚上传的产物、`scripts/build-npm.mjs` 组装 7 个 npm 包（二进制与 GitHub Releases 字节一致；平台包内二进制放包根目录，刻意不放 `bin/` 以避开 cc-otel 的开发模式数据目录规则）、逐包 `npm publish --access public`（预发布版本走 `next` dist-tag）。认证用 **npm Trusted Publishing（GitHub Actions OIDC）**，全程无 `NPM_TOKEN` secret（与 dsh-cc 的 publish.yml 同方案）；每个包一次性引导：新包名首次手工带 OTP 发布，再到 npmjs.com 绑定 user/repo/workflow。仓库内版本号永远 `0.0.0` 占位，组装时注入 tag 版本。
- **文档**：README（中英）新增 npm 安装与更新章节、支持客户端表与 DSH 接入章节的"npm 插件（推荐）"入口；`npm/README.md` 记录包族矩阵、发布流程与设计依据。

### 验证

- `dsh --profile web --dump-config --patch` 确认 patch 合成语义：遥测行被整键覆盖、仅剩一行且指向本地端点，插件行正确插入。
- 真实 `dsh plugin --profile <scratch> add file:…` 端到端：pnpm 只装 1 个匹配平台包（其余 5 个被 `os`/`cpu` 跳过）、bundle 自动登记进 `dsh.profile.bundles`、宿主 boot 中插件经 loader 导入并完成 install/start 全流程。
- 隔离 HOME（`CC_OTEL_HOME` 全栈透传到子进程）验证版本对齐、幂等启动、真实 `~/.cc-otel` 不受影响；`npm pack --dry-run` 核对 7 个包的文件清单与体积（主包 7.6 kB，平台包 9–10 MB）。

---

## [v0.1.1] - 2026-08-23

> 补丁版本：修复 Web 面板切换后的陈旧数据、异步响应竞态，以及数据库繁忙时的写入稳定性。

### 修复

- **面板激活刷新**：Daily Detail、Sessions、Request Log、Rate 使用统一激活逻辑；从 Rate 或其他面板切回时会重新查询，不再展示停留期间积累的旧 DOM 数据。
- **异步竞态保护**：Dashboard、主图、Daily/Intraday、Sessions、Request Log、时长统计、模型列表、Rate 全部采用 latest-request 代次保护；日期、分页、来源、SSE 或手动刷新重叠时，旧响应不能覆盖新状态。
- **来源切换**：Claude/Codex 切换时同步清除旧模型选择和选项，避免用旧来源模型查询新来源；上下文变化会使隐藏面板的在途请求失效。
- **Codex Rate 语义**：Rate 目前仅有非 Codex 后端数据，因此 Codex 下隐藏 Rate；从 Rate 切到 Codex 会无重复请求地回到 Daily，底层加载器也拒绝误取 `/api/rate`。
- **SQLite 写入稳定性**：`InsertRequest` 遇到 `SQLITE_BUSY` 时重试，降低并发摄入时的瞬时写入失败；CI 测试关闭不必要的 fsync。

### 验证

- 前端 Node 测试 **102/102** 通过；Go 全量测试通过。
- 使用隔离开发端口完成嵌入式前端构建和真实 Chromium 自动验证，覆盖 Rate → Sessions / Request Log 重进、Codex Rate 隐藏/回退和 7 Days 视图。

---

## [v0.1.0] - 2026-08-17

> 首个正式版本。cc-otel 不再与 Claude Code 绑定：独立安装目录、四个客户端共用一个实例。
> 汇总 `v0.1.0-preview.16` 之后的主线：service_name 归因、OTLP/HTTP 接入与 opencode、
> 感知服务的定价、DeepSeek Harness 接入、代码审查修复批次。
> 作为首个正式版本，它同时收录自初始提交以来的全部 46 个提交（清单见本节末尾）。

### v0.1.0 能力总览

| 能力域 | 支持内容 |
|--------|----------|
| **客户端** | Claude Code、Codex CLI、opencode、DeepSeek Harness（DSH）四客户端共用一个实例，按 OTLP Resource `service.name` 自动路由与归因 |
| **遥测接入** | OTLP gRPC `:4317` + OTLP/HTTP `:4318` 双传输，logs / metrics / traces 三信号走同一条摄入路径；透明解压 `Content-Encoding: gzip`；导出方重试幂等去重 |
| **成本与定价** | 手动价目表（Web UI 增删改查、即时生效）+ OpenRouter 按需询价（默认直营 provider 价）+ 历史成本全量重算；上游 `cost_usd` 仅对 Claude Code × Claude 模型权威 |
| **Web 仪表盘** | KPI 卡片与分项弹窗、Tokens / Cost / Requests 主柱状图、Daily / Sessions / Request Log / Token Rate / Intraday 面板、GitHub 风格使用日历、SSE 实时刷新、深浅主题、浏览器历史导航、在线数据库导入 |
| **存储** | SQLite 单文件（WAL + busy_timeout）；`daily_model_agg` 实时预聚合（查询 < 3ms）；双层 TTL 清理；写入侧不再堆原始 OTLP 事件，体积长期可控 |
| **运维 CLI** | `start` / `stop` / `restart` / `status` / `serve` / `install` / `init` / `cleanup`；`/api/status` 健康面板；跨机 DB 合并九步流水线 + 在线快照 `VACUUM INTO` |
| **分发** | 单二进制（`go:embed` 打包前端，零运行时依赖）；Windows / macOS / Linux × amd64 / arm64（GoReleaser）；GitHub Actions 三平台测试 + race + 覆盖率；Claude Code 插件 slash 命令 |
| **升级** | 旧版 `~/.claude/cc-otel/` 安装在新二进制首次运行时整目录自动迁移到独立的 `~/.cc-otel/`，数据零丢失 |

### 独立安装目录 `~/.cc-otel/`（破坏性变更，自动迁移）

- 安装/数据目录从 `~/.claude/cc-otel/` 改为 **`~/.cc-otel/`**——cc-otel 服务多个客户端，不再寄居 Claude Code 的数据目录。二进制、配置、数据库、PID、日志仍同目录。
- **自动迁移**：新二进制首次运行发现 `~/.cc-otel/` 不存在且 `~/.claude/cc-otel/` 存在时，整目录 rename 过去（配置、数据库、日志一并迁移）。安全性质：迁移只有「整目录搬走」或「什么都不动」两种结局，任何路径下都不会产生空新库 + 遗弃旧数据。
  - 旧 daemon 还开着（文件被锁）→ 本次继续用旧目录并打日志，停掉后下次运行重试成功。
  - 从旧目录内运行新二进制 → 跳过迁移并提示（Windows 无法 rename 含运行中 exe 的目录）；`cc-otel install` 在停掉 daemon 后会二次解析目录，让迁移重试。
  - `~/.cc-otel` 路径上若有杂散文件 → 绝不覆盖（Windows rename 会静默替换文件，已显式防护并有测试钉住）。
- 升级步骤：旧二进制 `cc-otel stop` → 新二进制 `cc-otel install && cc-otel start`。

### service_name 归因（新维度）

- `api_requests` / `daily_model_agg` 增加 `service_name` 列（agg 主键 `(date, model, service_name)`），启动迁移自动从旧行重建并把空值回填为 `claude-code`；部分索引兜住回填，不再每次启动全表扫。
- `db.NormalizeServiceName` 是唯一归一化规则，接收器 / 聚合重建 / InsertRequest / dbmerge 全部走它，裸 `''` 永不入库。
- 前端：请求日志 **Source 列** + 按客户端的品牌色徽章。刻意**不做**按来源的查询过滤和图表维度——非 Codex 客户端都归一化进同一池，主标签改名 **All Clients**。

### OTLP/HTTP 接入 `:4318` + opencode（新客户端）

- `internal/receiver/http.go` 用同一套 Export 方法服务 OTLP/HTTP 三信号——协议 shim，不是第二条摄入路径。按客户端发来的编码应答（JSON 进 JSON 出），没存下的数据绝不返回 2xx。透明解压 `Content-Encoding: gzip`。配置 `otlp_http_port`（默认 4318，`0` = 显式禁用，`/api/status` 如实上报）。
- **opencode**（Vercel AI SDK）：usage span 重塑为 `api_request` 日志记录后回放 `logs.Export`，享受同样的成本重算、归因、去重、SSE。只取叶子 span（父 span 重复携带 usage，两边都算会翻倍）；`request_id` = hex span id，重试幂等；span 无 `service.name` 时归因为 `opencode` 而非全局默认。

### 感知服务的定价

- 上游 `cost_usd` 只对 **Claude Code + Claude 模型**权威（`pricing.IsUpstreamAuthoritative`）；Claude Code 走 GLM/DeepSeek 反代时上游价是错的，其他客户端干脆不报成本。接收器、Codex 路径、离线重算三处同一规则，回填不会改写接收器刻意保留的行。
- 价目表不再特判 Claude：Lookup/Upsert/`pricing:` YAML 都接受 Claude 条目——这是给「不报成本的客户端驱动 Claude 模型」定价的唯一途径。
- 内置离线种子（manual_seed）刷新至 **2026-08-16 OpenRouter 第一方报价**：新增 kimi-k3（$3/$15）、kimi-k2.7-code、glm-5.2（$1.4/$4.4）、glm-5.3（按 5.2 推断，官方未定价）、qwen3.8-max、minimax-m3、deepseek-v4 日期变体（flash-0731 / pro-0813）；**修正 deepseek-v4-pro 官方降价**（$1.74 → $0.435/Mtok，降 4 倍）。

### DeepSeek Harness (DSH) 接入（新客户端）

- DSH 经 OTLP/HTTP 上报整份 session 账本。`internal/receiver/dsh.go` 只把带 `usage` 的 `assistant/message` 转成 `api_requests`，其余记录一律丢弃（FULL 模式正文含提示词、工具输出、文件内容）；step 生命周期重建 `duration_ms` / `ttft_ms`；`request_id` = `dsh-<session>-<seq>`，重试幂等。
- 判定按 **ResourceLogs 粒度**（scope 前缀为主信号、`service.name` 兜底），collector 把多个客户端合批导出也不会误路由。

### 代码审查修复批次（max 级审查，12 项确认全修）

- **数据丢失**：混合批次 DSH 路由改为 per-ResourceLogs 分发（有测试钉死混合双入库）；`event.seq` 缺失时 request_id 以记录时间兜底，不再塌缩成 `-0` 吞掉整个 session。
- **工具链**：`tools/otlp_dump` 复用 `receiver.ReadOTLPBody`（gzip 解压 + 大小上限），DSH 调试工作流不再 400;`AGENTS.md` symlink 改相对路径（原绝对路径在任何 clone 上都是死链）；`import_global` 的 `daily_model_agg` DDL 与 `applyAggDeltas` 对齐。
- **行为如实**：ai-sdk clamp 警告文案更正（clamp 后即为正确值，提示供确认假设）；`otlp_http_port: 0` 显式禁用且状态如实显示。
- **效率/维护性**：DSH body 解码按需取键（跳过 MB 级 content）；`'claude-code'` 字面量统一走 `db.DefaultServiceName`；三份重复的聚合 SQL 收口为单一常量。
- **前端**：Source 徽章背景走 `var(--accent-dim)`（亮色主题不再用暗色 tint）；标签激活态统一 graphite 范式；清理死代码与格式损伤。

### v0.1.0 收录的全部提交（46 个）

> 自初始提交至打标签提交，主线共 46 个，下表按迭代分组、组内新→旧。
> 说明：preview.15 发布时主线历史做过一次重整，`v0.1.0-preview.1` – `.14`
> 的旧标签仍指向重整前的哈希；下表哈希为当前主线（重整后）的。各迭代的
> 功能详述见下方对应小节；`merge: bring zip snapshot …` 是当时跨机协作
> 的快照并入方式，保留原始主题以存历史。

#### v0.1.0 正式版（preview.16 之后，4 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `591959f` | chore(pricing): refresh manual seed from OpenRouter 2026-08-16 quotes | 离线价目种子刷新至 2026-08-16 第一方报价；修正 deepseek-v4-pro 官方降价（$1.74 → $0.435/Mtok） |
| `3d35df3` | feat(release): v0.1.0 — standalone ~/.cc-otel home, auto-migration, multi-client docs | 独立 `~/.cc-otel/` 安装目录 + 首次运行自动迁移 + 四客户端文档与 CHANGELOG |
| `4a2d195` | feat(receiver): ingest DeepSeek Harness session telemetry | DSH session 账本接入（第四个客户端），OTLP/HTTP + gzip 解压 |
| `4b754c4` | feat: service_name attribution, in-process OTLP/HTTP ingress, service-aware pricing | service_name 归因维度、`:4318` OTLP/HTTP 接入、opencode 接入、感知服务的定价规则 |

#### v0.1.0-preview.16（9 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `3023d18` | docs(release): preview.16 — changelog + README for online DB import, Codex accounting, intraday bar chart | 发布说明与 README 更新 |
| `829be30` | feat(ui): rebuild Intraday as a per-bucket bar chart | Intraday 重做为每 (桶, 模型) 一柱，5–60 分钟桶选择器 |
| `d1034d5` | feat(ui): graphite segmented control for chart granularity | Day / Month 粒度切换改为石墨分段控件（含无障碍修复） |
| `36f0c19` | docs: add mandatory frontend taste guide | 前端视觉规范 FRONTEND_TASTE.md |
| `b5d0661` | chore(db): clean legacy Codex events on startup | 启动后台分批清理遗留 `codex_events` |
| `0be4537` | feat(codex): add structured accounting correlation | Codex 流式事件关联为每请求一行结构化记账 |
| `1d7a8b4` | feat: add efficient online database import | 在线 DB 导入（`internal/dbmerge`，不停进程） |
| `dc664ec` | refactor(logrotate): extract closeFile helper to dedup close logic | 日志轮转关闭逻辑去重 |
| `b9daba9` | fix(pricing): guard redirectable URL vars to fix -race data race | 修复询价路径的数据竞争 |

#### v0.1.0-preview.15（4 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `498edfb` | docs(release): preview.15 — changelog summary + repo hygiene | 发布说明 + 仓库整理（.gitignore / .gitattributes） |
| `e1e8e5f` | feat(rate): Apple-style controls — custom rounded dropdown, drop Total tok/s, 10-min bucket | Rate 控件重设计：自绘圆角下拉、去掉 Total tok/s、新增 10 分钟桶 |
| `c4c12e2` | feat(pricing): manual pricing table with provider-aware OpenRouter suggest | 价目表改为手动管理 + 感知 provider 的 OpenRouter 询价 |
| `45da52d` | cleanup: remove gemini cli telemetry integration and pricing | 移除 Gemini CLI 集成 |

#### v0.1.0-preview.14（1 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `a9cfadf` | feat(rate): preview.14 — Token Rate panel + rate APIs + weighted tok/s | 新增 Rate 面板、速率 API 与加权 tok/s |

#### v0.1.0-preview.13（4 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `c5d5342` | docs(changelog): preview.13 | 发布说明 |
| `4d79a7b` | feat(pricing): split hand-maintained manual seed out of regenerable seed.json | 手动价目拆分 `embed/manual_seed.json`，重生成不再冲掉手动条目 |
| `bd4a8ac` | feat(pricing): prefer OpenRouter first-party provider price over blended | 询价默认取直营 provider 价而非混合促销价 |
| `71786b3` | feat(pricing): basename fallback for provider-prefixed model keys | 裸模型名按 basename 兜底匹配 provider 前缀 key |

#### v0.1.0-preview.12（2 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `9e2a733` | docs(changelog): preview.12 | 发布说明 |
| `fb13ae0` | fix(web): align usage-calendar weekday labels with cell rows | 使用日历星期标签与格子行对齐 |

#### v0.1.0-preview.11（2 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `9cd2351` | docs(changelog): preview.11 | 发布说明 |
| `44fda62` | merge: bring zip snapshot cc-otel-9e41afc into main | 快照并入主线 |

#### v0.1.0-preview.10（3 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `c4fb0a1` | docs(changelog): preview.10 — gemini source, merge data-loss fix, 24h time | 发布说明 |
| `82e1873` | fix(merge): stale import_ledger entries must not drop rows; verify per-row | 修复合并账本残留静默丢行；校验改为逐行 |
| `c4627ca` | merge: bring zip snapshot cc-otel.zip into main | 快照并入主线 |

#### v0.1.0-preview.9（1 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `09afbdb` | fix: add no_proxy to OTEL config for proxy compatibility | setup 自动注入 `no_proxy`，代理用户遥测不再静默丢失 |

#### v0.1.0-preview.8（3 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `4ee38c1` | merge: bring zip snapshot cc-otel-202f900 into main | 快照并入主线 |
| `aef2685` | docs(changelog): split Codex duration backfill into online + offline paths | 文档：Codex 耗时回填拆为在线 + 离线双路径 |
| `db85e14` | merge: overlay zip snapshot cc-otel-202f900 onto main | 快照覆盖并入 |

#### v0.1.0-preview.7（1 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `e9593d3` | feat: pricing table & non-Claude cost recompute | 价目表 + 非 Claude 模型成本重算 |

#### v0.1.0-preview.6（2 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `668ba1d` | fix: skip Windows-only merge test on non-Windows CI | CI 平台修复 |
| `64fbc52` | docs: fix Codex config keys (trace-exporter / metrics-exporter) | Codex 配置键文档修正 |

#### v0.1.0-preview.5（3 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `f778c57` | chore: remove one-shot migration doc with local paths | 清理含本机路径的一次性迁移文档 |
| `039f18c` | merge: integrate enhanced head from secondary workstation | 二号工作站分支并入 |
| `f31b691` | docs: update dashboard screenshots | 截图更新 |

#### v0.1.0-preview.4（1 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `69acbd0` | fix: remove legacy fallback in defaultDataDir | 移除数据目录解析的遗留回退 |

#### v0.1.0-preview.3（3 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `6510f04` | feat: add hourly view, model family colors, insights modal, URL sync | 小时视图、模型品牌色、Insights 弹窗、URL 状态同步 |
| `231f797` | docs: add dashboard screenshots | 截图 |
| `a546051` | docs: add dashboard screenshots to README | README 截图 |

#### v0.1.0-preview.1 – .2（3 个）

| 提交 | 主题 | 说明 |
|------|------|------|
| `7291b00` | fix(setup): query GitHub API for actual download URL | setup 改查 GitHub API 获取真实下载地址 |
| `70060ca` | docs: add /reload-plugins step before /cc-otel:setup | 文档补 /reload-plugins 步骤 |
| `23c25c3` | chore: initial commit | 初始提交：OTLP gRPC 接收 + Web 仪表盘雏形 |

## [v0.1.0-preview.16] - 2026-07-17

> 汇总 `v0.1.0-preview.15` 之后的 6 个提交：在线数据库导入、Codex 结构化计费关联、启动清理遗留 Codex 事件、前端视觉规范、粒度分段控件、Intraday 重做为按桶柱状图。

### 在线数据库导入（新）

- 在运行中的服务里直接合并一个 `.db` 文件到主库——**不停进程、不替换文件**。目标库保持 WAL 在线，上传的库以只读方式打开、绝不挂到可写连接上。
- 合并**只增不改**：既有行不删、不覆盖、不更新，仅插入目标库缺失的逻辑记录，重复行保留目标库版本；导入账本（ledger）使重传 / 断点续传安全、不双写。
- 新增 `internal/dbmerge` 作为 schema 识别、自然键去重、暂存、分批、聚合、校验的单一实现；`tools/merge_bin_global` 的离线 CLI 改为委托给它，离线与在线路径不再各写一套。
- 性能：基于暂存摘要的集合式处理，每批上限 1 万行或 ~128 MiB；端到端门槛——~55 万行预览 2 分钟内、导入 + 校验 5 分钟内。`codex_events` 被识别并跳过。
- 前端：`js/import-db.js` 驱动 上传 → 预检 → 预览 → 确认 → 进度 → 结果，对应 `/api/import{,/inspect,/start,/status}`。

### Codex 结构化计费关联

- 把 Codex 流式事件关联成**每个模型请求一行**结构化记账：分别记录未缓存输入、缓存读、缓存写、输出、推理 token，以及上游上报总量、TTFT、从请求开始到 `response.completed` 的完整模型调用耗时（不含本地工具执行）。费用按四个独立计价的 token 类别计算。
- 关联状态在内存中维护，结构化事务提交后即丢弃，30 分钟后清理被遗弃状态。
- 字段映射对照上游 Codex 源码钉死：注意 `tool_token_count` 实为总用量（不是工具 token 类别），不得单独计价；`cache_write_token_count` 来自 `input_tokens_details.cache_write_tokens`，旧载荷默认 0。

### 启动清理遗留 Codex 事件

- 每次 daemon 启动后，后台 goroutine 分批（每批 ≤ 1 万行）删除兼容专用的 `codex_events` 表里的遗留行，直到清空；出错即记日志并停止，剩余行留待下次启动。该表接收器已不再写入、导入也会跳过，故剩余行按定义都是遗留数据。
- 属临时措施，待部署库经过若干次启动自清后移除。

### 前端视觉规范（FRONTEND_TASTE）

- 新增 `.claude/harness/FRONTEND_TASTE.md` 作为前端视觉口径，要求在任何前端设计 / 实现 / 修改 / 审阅前先读。把项目既有视觉语言成文化：石墨 / 中性灰为默认、先靠间距与层次再考虑饱和色、紧凑控件优于浏览器原生、深色 / 浅色作为同一系统。

### 图表粒度分段控件

- 工具栏 Day / Month 粒度切换从浏览器原生按钮改为紧凑的 macOS 风格石墨分段控件，与应用整体一致；样式限定在 `#granularity-switch`，不影响 Token Rate 面板的 Weighted / Avg 控件。
- 无障碍：补 `type="button"`、`role="group"`、`aria-label`，并保持 `aria-pressed` 与 active 状态三路同步（init / URL 恢复 / 点击）。
- 抽出 `syncGranularityButtons` 到 `js/granularity.js`，filters.js 三处调用共用一套实现，杜绝此前 `aria-pressed` 与视觉态漂移。

### Intraday 重做为按桶柱状图

- Intraday 视图从「按模型折线图 + 明细表」改为**每个 (桶, 模型) 一根柱**，带 5 / 10 / 15 / 30 / 60 分钟选择器，跨度最长 7 天；明细表移除。
- 后端：四处 intraday 桶白名单（Claude repo + handler、Codex repo + handler）此前硬编码 15 / 30 / 60，会把 5 和 10 静默强转成 30；现统一复用 `db.ValidRateBucketMinutes`（SQL 本就支持任意桶宽，纯属校验层修复），并补上该路径首批测试。
- 前端：抽出三个此前各写一份的共用块——`bucket-dropdown.js`、`chart-main.js` 的 `makeTokenBarColor`、`legend-focus.js`；`intraday-slots.js` 为空桶补齐，避免类目轴把不连续时间点相邻错置。默认缩放窗改为按时长（16h，右锚），不再随桶宽漂移。
- 遵守 CLAUDE.md 图表硬规则：不堆叠、`trigger:'item'`、不 series 高亮、每个 (时间, 模型) 一根柱；x 轴升序（时间轴左→右，与 chart-main 的最新在前相反）。

## [v0.1.0-preview.15] - 2026-07-09

> 汇总 `v0.1.0-preview.14` 之后的 4 个提交：Gemini 移除、仓库整理、价目表手动管理 + 按 Provider 选价、Token Rate 控件重设计。

### 移除 Gemini CLI 数据源

- 删除 Gemini CLI 的完整集成：`gemini_*` 表与独立 schema、`/api/gemini/*` 路由、前端 `?source=gemini` 视图、`skills/otel-setup/gemini-setup.md` 及其专属定价。
- **为什么**：实际使用集中在 Claude Code 与 Codex。为一个很少用的来源维护独立 schema / API / 文档 / 定价分支，成本高于收益。移除后收敛为干净的两源模型（下方历史节残留的「三源」描述以本节为准）。

### 仓库整理

- `.gitignore` 忽略 `docs/superpowers/` 等工具临时目录；`.gitattributes` 强制 LF 换行。
- **为什么**：把工具生成的临时文件挡在仓库外，并消除 Windows / Linux 协作时的换行抖动。

### 价目表：手动管理 + 按 Provider 选价（增强）

- **手动管理**：移除 24h 自动拉取的 Refresher；状态弹窗 → Pricing Table 支持对 `model_pricing` 增删改查（即时生效）、「💡 从 OR 填」按需查价、「↻ 重算历史」服务端全量重算。`pricing_refresh:` 配置废弃。
- **默认取直营 Provider 价**：`/api/pricing/suggest` 默认返回模型第一方直营报价（如 `z-ai/glm-5.2` 取 Z.AI 直营，而非最便宜的混合促销价）；展开可见全部 provider（不限数量、直营置顶），含 `cache_create`。
- **Claude 只读参考价**：Claude 本不进本地价目表（重算短路），现从 OpenRouter 目录按需取参考价只读展示，避免整列空白。
- **为什么**：自动刷新不透明且不可控；手动管理把定价权交还用户（glm-5.x 等未收录模型本地是唯一真值）。默认取直营价，是因为 OpenRouter 混合价常是最便宜 provider 的促销 / 量化版，与直营标价不可比——既给靠谱默认，又允许在促销价与直营价之间自选。

### Token Rate 控件重设计（Apple 风格）

- **控件统一为应用风格**：Weighted/Avg 改为分段控件（对齐 `.pf-seg`）；时间桶改为自绘圆角下拉（Windows Chromium 下原生 `<select>` 展开列表无法圆角，故用 trigger + `.rate-menu` 浮层）；按钮对齐 `.pf-btn`。
- **去掉 Total tok/s**：Output/Total 切换意义不大（Total 混了缓存与未缓存），图表恒为 Output tok/s。
- **新增 10 分钟桶**：后端 `ValidRateBucketMinutes` 与前端 `BUCKET_CHOICES` / 下拉项同步加入 10（5 / 10 / 15 / 30 / 60）。
- **修复分段控件 active 丢失**：`filters.js` 的 `.gran-btn` 查询收窄到 `#granularity-switch`，否则每次切日期都会抹掉 Weighted/Avg 的 `.active`（该类与工具栏 Day/Month 共用）。
- **为什么**：原控件是未加样的原生 OS 部件，与应用整体风格脱节，展开下拉还是直角；自绘下拉让收起 / 展开都与应用一致。

## [v0.1.0 之前] · 0.1.0 Preview 阶段（历史记录）

> **状态：历史记录，已随 v0.1.0 收敛发布**（见顶部小节）。本节是 preview 阶段
> （`v0.1.0-preview.1` – `.15`）主分支功能的累积描述，保留作历史参考；与顶部
> 版本小节冲突时以顶部为准。本节提到的安装目录 `~/.claude/cc-otel/`、24h 自动
> 刷新价目、Gemini 三源等均为当时行为，已被后续版本逐一取代（v0.1.0 起目录为
> `~/.cc-otel/`，自动刷新已移除，Gemini 已删除）。

### 价目表改为手动管理

- **价目表改为手动管理**：移除 24h 自动从 LiteLLM/OpenRouter 拉取的 Refresher；Web UI（状态弹窗 → Pricing Table）支持增删改查 model_pricing，改完即时生效；新增「💡 从 OR 填」按需查 OpenRouter 单模型价格、「↻ 重算历史」服务端全量重算（状态留存、刷新页面不重算）。`pricing_refresh:` YAML 配置项随之废弃。

### 代理兼容性修复

- **`no_proxy` 自动注入**：`/cc-otel:setup` 现在会在 `settings.json` 的 `env` 中自动添加 `"no_proxy": "localhost,127.0.0.1"`。当用户设置了 `http_proxy` / `https_proxy`（如 Clash、V2Ray、企业代理）时，OTEL gRPC 流量会错误地走代理，导致遥测数据静默丢失。`no_proxy` 确保 OTEL exporter 直连 localhost，绕过代理。README 和 setup 文档已同步更新，重点标注代理用户必须配置此项。

### 数据源与接收

- **OTLP gRPC 接收器**：通过 OTLP/gRPC 接收 logs / metrics / traces。代码默认端口 `4317`（详见下面 Daemon / CLI 段的端口说明）。
- **Claude Code + Codex 两源**（preview.15 起移除 Gemini CLI，见顶部）：按 OTLP Resource 的 `service.name` 路由 ——
  - 名称包含 `codex`（不区分大小写）→ 写入 `codex_*` 表，前端通过 `?source=codex` 切换视图，`/api/codex/*` 暴露。
  - 其余（含缺省 `service.name`）→ 写入既有 Claude 表 / 走原有接口（向后兼容）。
- **TTFT（首 Token 时间）**：
  - 从 OTLP trace spans 提取 `ttft_ms` 并回填 `api_requests.ttft_ms` / `codex_api_requests.ttft_ms`。
  - 内置 pending 队列：trace 先到、`api_request` log 后到的乱序场景也能正确回填。
  - 历史数据回填工具：`tools/backfill_claude_ttft`。
- **Codex 耗时回填**（双路）：
  - **在线**（接收端）：log 入库时若没有匹配的 span ID，按 `(sessionID, model)` 在 10 分钟窗口内回退匹配最近的 `codex.websocket_request` span，直接补 `duration_ms`。
  - **离线**：`tools/backfill_codex_duration`，扫描 `codex_raw_otlp_events` 表里的 `codex.websocket_request` / `codex.websocket_event` span 配对，推导 `duration_ms` 写回 `codex_api_requests`。
- **实时推送**：`/api/events` SSE，接收器入库成功后调用 `Broker.Notify()` 通知所有浏览器自动刷新。

### 成本与定价（`internal/pricing`）

- **非 Claude 模型 cost 重算**：按单一规则覆盖 ——
  - `model` 以 `claude-` 开头（不区分大小写）→ 保留 Claude Code 上报的 `cost_usd`。
  - 其他模型 → 用本地价目表按 token 数重算并覆盖。
- **三层查找优先级**：
  1. `cc-otel.yaml` 的 `pricing:` 用户覆盖（最高优先）。
  2. SQLite `model_pricing` 表（长期存储）。
  3. 内嵌 `internal/pricing/embed/seed.json`（从 BerriAI/litellm 派生）。
- **价目表每日刷新**：从 LiteLLM + OpenRouter diff 写入 `model_pricing`，`Refresher` 在 `internal/pricing/refresher.go`；可通过 `pricing_refresh.enabled: false` 关闭。
- **修正的两类真实 Bug**：Codex 不上报 `cost_usd`；GLM/DeepSeek/Kimi 走 Anthropic 兼容反代时被错误计价为 Anthropic。
- **运维端点**：`GET /api/pricing/lookup?model=glm-4.6` 返回命中 key、来源、价格、是否 Claude。
- **状态面板**：右上 `live` → **Pricing Table** 行，按上次刷新时间显示绿（<48h）/ 黄（<7d）/ 红（>7d 或错误）。
- **历史回算**：`go run ./tools/recompute_cost --db <path> [--config <yaml>] --table both [--apply]`，默认 dry-run。
- **裸模型名 basename 兜底匹配**（preview.13）：反代上报的裸名（如 `glm-5.2`）与上游目录的 provider 前缀 key（`z-ai/glm-5.2`）对不上时，新增第 5 级匹配——按 basename（最后一个 `/` 之后）兜底命中。撞名择优：段数最少（直连 provider）→ 来源优先级（user>litellm>openrouter>seed）→ 字典序。
- **OpenRouter 取直营 provider 价**（preview.13）：`/api/v1/models` 返回的是最便宜 provider 的混合价，常是低精度量化版（fp4）——与直营 fp8 列表价不可比。现对 `z-ai/*` 额外拉 `/endpoints`，取 Z.AI 直营 provider 价覆盖混合价（`Z.AI`↔`z-ai` 归一化匹配）。glm-* 等不再需要手填 YAML 覆盖。
- **手动价目拆分 manual_seed**（preview.13）：无上游目录的模型（Xiaomi MiMo / StepFun / 未收录的 DeepSeek V4）的手动价从 `seed.json` 挪到 `embed/manual_seed.json`；`seed.go` 合并加载（手动覆盖自动），`dump_pricing_snapshot` 只重写 `seed.json` 不碰它——手动条目不再被发布前重生成冲掉。

### Token 速率与吞吐（preview.14）

- **Rate 面板（新）**：Web UI 新增 **Rate** 标签页，按模型绘制 **Token Rate over Time** 折线图（Claude 源；最长 7 天）。支持 **Weighted / Avg**、**Output / Total tok/s**、**5 / 15 / 30 / 60 min** 时间桶；切换日期范围时默认桶为 **Today → 5 min**、**多天 → 30 min**（下拉仍可手动改 5 / 15）。
- **速率 API**：`GET /api/rate?from=&to=&bucket=&model=` 返回按 `(bucket, model)` 聚合的吞吐桶；`duration_ms` 为 API 请求耗时（不含本地工具执行）。桶内同时返回 **加权吞吐** `SUM(tokens)×1000/SUM(duration_ms)` 与 **算术平均** `AVG(单请求 tok/s)`。
- **会话近期速率 API**：`GET /api/session/rate?session_id=` 返回该会话最近一个有数据的 **1 分钟** 桶（加权 + 平均 Out tok/s）；无数据 404。
- **Request Log 双列 tok/s**：按模型汇总表新增 **Out tok/s (avg)** 与 **Out tok/s (wt)** 两列（可排序），列头 tooltip 标明公式；与 Rate 图使用同一套速率语义。
- **Rate 图交互**：
  - **图例 solo**：点击图例只显示该模型；再点同一项或 **All models** 恢复全部。
  - **整线 hover**：光标沿折线移动即可看 tooltip（按时间桶匹配，避免跨桶错显模型）。
  - **连线策略**：单日日内连续（轻 smooth）；**多天按自然日断开**、日内直线相连，避免跨夜斜线横穿全图（对齐 Intraday 稀疏桶 + Grafana 式日界断线取舍）。
- **开发规则**：`.cursor/rules/sync-global-db-to-bin.mdc` — 说「挪 / 同步全局 db 到 bin」即走 `snapshot_db` → 停 bin → 替换库 → 重启（全局 daemon 不停；见 `.claude/rules/db-copy-no-stop.md`）。

### Web UI · 数据展示

- **KPI 卡片**：Cost / Input / Output / Cache Hit / Requests。其中 **Input = `input_tokens` + `cache_read_tokens` + `cache_creation_tokens`**（与 Anthropic 官方 prompt-caching 文档口径一致）。
- **KPI 分项弹窗**：点击任意 KPI 卡片，展示按模型的饼图明细。
- **主柱状图（Tokens / Cost / Requests）**：
  - 每个 `(date × model)` 一个独立柱子（不堆叠、不按 series 高亮、不用 axis trigger）。
  - Tokens 柱身为单 series 双段渐变：底段 = 输入侧合计，顶段 = Output；Output 占比极小时仍保证 ~6% 可见。
  - Tooltip 层级：**Total** → **Input**（粗体父行）→ Uncached / Cache Read / Cache Create（缩进子行）→ **Output** → Requests / Cost。
- **Daily Detail 表**：两行表头，Input 跨 3 列（Uncached / Cache Read / Cache Create）；其余列 `rowspan=2`。
- **使用日历热力图**（新）：顶栏 Insights 改为 GitHub 风格按日热力图，支持 tokens / cost / requests 三种指标，点击单元格直接跳到当天视图；后端 `/api/calendar` 与 `/api/codex/calendar`。
- **Intraday 折线视图**：1 天范围下渲染 30 分钟粒度的按模型折线图（替代柱状图），最长 7 天；hover 段触发 tooltip。
- **Sessions 面板**：按会话聚合 Cost / Token；含分页。
- **Request Log 面板**：
  - 按模型汇总表：**Avg Duration**、**Out tok/s**、**Avg TTFT**（有数据才显示）、**Min**、**Max**，列可点击排序。
  - 单条请求列表：含 **TTFT** 列与单元格悬浮详情；含分页。
- **耗时统计 API**：`/api/durations` 提供按模型 duration / 吞吐量统计；**Out tok/s** 由 `output_tokens` / `duration` 推导。
- **时间格式统一 24 小时制**（preview.10 修复）：新增 `fmtDate24` / `fmtDateTime`，替换 `toLocaleString()`，午夜不再按 12 小时制显示为 "12:xx AM"，全部渲染为本地时区 `YYYY-MM-DD HH:mm:ss`。
- **Cache Hit Rate 口径修正**（preview.11 修复）：缓存命中率从 `cache_read / (cache_read + cache_creation)` 改为 `cache_read / 输入侧合计`（`input_tokens + cache_read + cache_creation`）。旧口径对只上报 `cache_read`、从不上报 `cache_creation` 的反代提供商（GLM、mimo）会塌缩成恒定 100%；改用完整输入侧作分母后修复，并与 Codex / Gemini 口径（`cache_read / input_tokens`，其 input 已含缓存部分）保持一致。后端 `GetDashboardForRange` / `GetDailyStats` 与前端 `token-math.js` 同步调整，配套新增 `token-math.test.mjs` 及 GLM 无 `cache_creation` 回归用例。
- **使用日历对齐修正**（preview.12 修复）：多周视图下，左侧 `Sun`–`Sat` 行标签未计入上方月份标签行的高度（16px + 4px margin），整体比真正的格子行高出约 20px，导致星期标签落在两行之间、最底行溢出到 "Sat" 之下。改为用 `--usage-months-h` 变量把星期列下移一个月份头高度，使 7 个星期标签与 7 行格子精确对齐；strip（单日横条）模式不受影响。

### Web UI · 交互与控件

- **日期范围**：Today / Yesterday / 7 Days / 30 Days / All Time / 自定义（Flatpickr 双月、本地时区解析、避开 DST 夜半边界）。
- **日下拉**：最近 7 天快速切换（Today / Yesterday / Sun..Sat 标签）。
- **粒度切换（仅 All Time）**：day / month。
- **指标切换**：Tokens / Cost / Requests。
- **数据源 tab**：Claude / Codex 顶部切换（URL `?source=codex`）。
- **主题**：深色 / 浅色，自动跟随系统偏好；Flatpickr 跟随重绘。
- **浏览器历史导航**（新）：range / source / day-dropdown / 自定义范围 / 日历单元格点击均压入 `pushState`，浏览器返回 / 前进按预期工作；boot 与 popstate 的 URL 规范化用 `replaceState`，不污染历史。
- **跨天自动刷新**（新）：本地日切换时，若当前视图为 Today 或 single-day 钉在今天，自动 bump `customFrom/customTo`、刷新 URL（replace 模式）并重载数据。
- **分页**：Daily / Sessions / Requests 共用统一分页组件。

### Web UI · 状态与运维

- **右上 `live` 指示器**：绿点代表 SSE 推送连接正常。
- **Server Status 弹层**：SSE 客户端数、DB / API 健康、OTLP gRPC 监听状态（TCP dial 检测）、最近一次入库时间、OTLP gRPC + Web UI 端点（带复制按钮）、Pricing Table 新鲜度行。
- **`/api/status`**：后端汇总以上信息的 JSON 接口。

### Web UI · 架构

- **前端 ESM 模块化（无构建工具）**：`app.js` 是约 230 行的薄入口，按职责拆分到 `internal/web/static/js/` 下的独立模块：
  - `state.js` / `utils.js` / `theme.js` / `api.js` / `filters.js` / `sse.js`
  - `breakdown.js` / `insights.js` / `chart-main.js`
  - `panel-daily.js` / `panel-sessions.js` / `panel-requests.js` / `panel-rate.js` / `pagination.js`
- **纯函数单测**：`internal/web/static/tests/*.test.mjs`，通过 `node --test` 运行（Node ≥ 18 内置 runner，零依赖）。
- **前端开发免重编译**：设 `CC_OTEL_STATIC_DIR=internal/web/static`，Web UI 从磁盘读取静态资源；默认 `go:embed` 进二进制。
- **图表硬规则**（永不违反）：`stack: 'total'`、`trigger: 'axis'`、`emphasis: { focus: 'series' }` 全部禁用。

### 存储与生命周期

- **存储瘦身（写入侧）**：`raw_otlp_events` / `codex_raw_otlp_events` / `otel_metric_points` 表保留 schema 以兼容现有 backfill 工具，但不再写入新数据；DB 体积长期可控。
- **预聚合实时维护**：`daily_model_agg` / `codex_daily_model_agg` 在写入路径上同步刷新，Web UI 查询 < 3ms。
- **双层 TTL 清理**：
  - `raw_ttl_days`（新增；默认 5 天）—— 按小时清理 `*_raw_otlp_events` 及残留的 codex websocket event 行。
  - `retention_days`（已有）—— 控制 `cc-otel cleanup` 子命令与周期清理的整体留存阈值。
- **SQLite**：WAL 模式 + `busy_timeout`，单文件部署。

### Daemon / CLI

- **子命令**：`start`（后台）/ `stop` / `restart`（热替换二进制）/ `status`（PID + 端口 + 今日统计）/ `serve`（前台调试）/ `install`（拷贝二进制到 `~/.claude/cc-otel/`）/ `init`（生成默认配置）/ `cleanup` / `-v` / `-config <path>`。
- **数据目录解析**：若可执行文件位于名为 `bin` 的目录中（开发模式）→ 使用该 `bin/`；否则使用 `~/.claude/cc-otel/`（自动 mkdir）；最终回退到 `.`。`~/.claude/` 本身不参与中间查找。
- **环境变量覆盖**：`CC_OTEL_OTEL_PORT` / `CC_OTEL_WEB_PORT` / `CC_OTEL_DB_PATH` / `CC_OTEL_STATIC_DIR`。
- **同目录文件**：`cc-otel(.exe)` + `cc-otel.yaml` + `cc-otel.db` + `cc-otel.pid` + `cc-otel.log`。
- **端口默认值**：`otel_port = 4317`，`web_port = 8899`（代码常量 `DefaultOTELPort` / `DefaultWebPort`）。本仓库的 `bin/cc-otel.yaml` 把开发实例改为 `14317 / 18899` 以免和生产实例冲突，但这是 YAML 覆盖，不是代码默认值。

### Claude Code 插件

随 marketplace 提供 slash 命令：

| 命令 | 说明 |
|------|------|
| `/cc-otel:setup` | 下载二进制、配置 OTEL 环境变量、启动服务 |
| `/cc-otel:start` | 启动后台守护进程 |
| `/cc-otel:stop` | 停止守护进程 |
| `/cc-otel:status` | 状态 + 今日费用摘要 |
| `/cc-otel:open` | 浏览器打开 Web 仪表盘 |
| `/cc-otel:report [today\|7d\|30d\|all]` | 生成费用报告 |

### 跨机数据 & 运维工具

- **跨机 DB 合并**：`tools/merge_bin_global/run_merge` 编排 9 步：backup-bin-db-files → stop-bin-process → snapshot-local-db → snapshot-global-db → export-local-jsonl（`export_bin`）→ import-jsonl-into-global-copy（`import_global`）→ repair-daily-agg（`repair_daily_agg`）→ verify-merged-copy（`verify_merge`）→ replace-bin-db。`verify_union` 是配套但独立的人工核验工具，**不在 run_merge 调用链内**。方向固定：`~/.claude/cc-otel/` → `bin/cc-otel.db`，自动备份，PID 校验后安全停止 bin 守护进程。
- **历史回算工具**：`tools/recompute_cost`（按表回写 cost_usd）/ `tools/backfill_claude_ttft` / `tools/backfill_codex_duration` / `tools/prune_before`（按日期裁剪）/ `tools/migrate_codex_data`。
- **价目表快照工具**：`tools/dump_pricing_snapshot`（发布前从 BerriAI/litellm 重新生成 seed.json）。
- **流程文档**：`docs/MERGE_AND_RECOMPUTE.md`（合并 + 重算的标准动作，含 `--config` 必填这类易错点）。
- **合并防丢数据修复**（preview.10）：`import_global` 此前 ledger UUID 命中即跳过、不检查数据行是否真的存在 —— 目标库 ledger 有残留（行被 prune 后）会静默丢行。现在 ledger 只记账，按自然键（`api_requests` 优先 `request_id`；codex/gemini 排除会被 recompute 改写的 `cost_usd`）判存在性。`verify_merge` 同步改为三张 request 表逐行 NOT EXISTS 包含性校验：源库自身重复被去重打 NOTE，cost 总和不对称打 WARN，真缺行才 FAIL。
- **在线快照工具**：`tools/snapshot_db`（`VACUUM INTO`，不停进程复制 WAL 库）；`tools/otlp_dump`（OTLP 流量调试落盘）。

### 分发

- **单二进制**：`go:embed` 打包 Web UI，零运行时依赖。
- **跨平台**：Windows / macOS / Linux，amd64 + arm64（GoReleaser 自动构建）。
- **GitHub Actions**：`test.yml` 三平台矩阵 + race + 覆盖率上报 Codecov；`release.yml` 在推 `v*` tag 时触发 GoReleaser。

### 备注

- TTFT 依赖 Claude Code 开启 trace 导出（`OTEL_TRACES_EXPORTER=otlp`）+ tracing 开关（Enhanced Telemetry Beta 相关）。
- Codex 接入需在 `~/.codex/config.toml` 配置 OTLP gRPC endpoint，详见 README「Codex CLI 接入」。
- Codex CLI 不上报 `cost_usd`，cc-otel 通过价目表自动算出并写入 `codex_api_requests.cost_usd`。

---
