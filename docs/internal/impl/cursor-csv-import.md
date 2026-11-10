# cursor-csv-import — 实施记录

- Date: 2026-11-07（增量至 2026-11-08）· Spec: docs/internal/design/cursor-csv-import.md
- Status: task 1–7 完成；task 8（生产发布）待 release 流程

## Tasks（全部完成）
cursorimport 包（解析/映射/哈希/Cost）→ 导入执行器 + agg + ledger → pricing 谓词 → `/api/cursor/import` handler + 互斥闸（与 db import / codex migrate 共享单写者，并发 409）→ 前端卡片 + 官方图标 → 彩排对账 → 双语文档 + CLAUDE.md。
测试锚点：`TestImport_IdempotentWithAggAndLedger`、`TestCursorImportLifecycle_IdempotentReupload`、`TestCursorImportDryRunCountsAgainstPool`、`TestNormalizeCursorModel`、`TestImport_ZeroCostRowsEstimatedPaidRowsKept`、`TestRun_CursorZeroEstimatedPaidKept`。

## 彩排证据（bin 18899，真实 11 份导出）
- 首跑 inserted=18890 / dup=0 / skipped_zero=1793 / violations=0（~15s）；幂等重传 0 / 18890。
- 四 token 分量与 cost：CSV==pool==agg 逐位一致（input 146,239,185 / cache_create 40,065,448 / cache_read 2,496,427,571 / output 19,351,902 / $0）；RECONCILE pool==agg==ledger true；cloud-agent 行 42。
- 交叉验证：`go test ./...` 全绿；web 套件全过（钉版本断言随每次 bump 同步）。

## 偏差
- D2 执行器改走 `Repository.InsertRequest` + 末尾 RebuildDailyAggregates（去重/agg upsert/BUSY 重试免费复用；dsh_ingest 同款先例，spec 原定单连接 BEGIN IMMEDIATE 批次）。
- D3 ledger 按行 INSERT OR IGNORE（uuid=request_id），重跑天然 no-op。
- D7 不可解析 Cost 按 $0 落库并计 identity_violations（真实数据未触发）。
- D4 初期无浏览器自动化按 CLAUDE.md 降级验证；后经无头 Edge+CDP 补齐（见增量）。

## 增量时间线（根因 + 教训浓缩）
- **model-normalize**（11-07）：NormalizeCursorModel 落库前剥档位后缀；模型 49→35；哈希仍用原始 slug。
- **live 事故：进度冻结 0%**（11-07 21:43）：cursor-import.js 照抄 codex-migrate.js 的轮询门（01952a5），开机首 tick 后链不再重启——POST 的 0% 快照成最后一帧（后端 4s 已完成、数据无损、SSE 已推）。修复：POST 成功即 schedule() 重启 + MutationObserver 监听弹窗重开即恢复轮询；codex 卡同款存量隐患一并修。
- **Tab 化**（11-08，用户："谁知道能拖动进来"）：弹窗顶部 Database/Cursor tab（.dbi-tabs 分段控件，import-db.js 持 setImportTab，open() 重置回 Database）+ Cursor tab 顶部同款 .dbi-drop 拖拽区。教训：给既有弹窗加 pane 包裹时必须审计 modal 层级的**既有兄弟元素**——dbi-footer 曾留在两 pane 之外，Cursor tab 下仍显示。
- **滚动修正**：drop 区误用 .dbi-drop 的 190px 空状态主角高度，640×700 下 28px 溢出生出滚动条 → 压到 96px 后需求 0。验证手段由此确立：无头 Edge + CDP 盒模型测量（bin/tmp/geom*.mjs，gitignored），是截图证据的可行替代。
- **窄屏按钮拉伸**：媒体块 `.dbi-primary,.dbi-secondary{flex:1 1 140px}` 是 DB footer 的移动端全宽设计，波及 Cursor 同 class 按钮 → 限定回 `.dbi-footer` 内 + #codex-migrate-start，Cursor 操作行 flex-wrap 兜底。教训：给既有类家族加按钮要审计**全部**作用于该 class 的选择器（含媒体块）——全局规则的继承面随 DOM 增长悄悄扩大。
- **进度条**（11-08 用户指定）：运行态对齐 DB 合并的 .dbi-progress 三件套（fraction 精确宽度、aria-valuenow 同步、计数移入 meta 行）。
- **打磨方案 A**（11-08）：整卡拖拽（outline 实现，零布局位移）+ `POST /api/cursor/import/dry-run` 预览（复用 parseCursorUpload、只读、不占写者闸、request_id 分块 IN 探测 500/批；并发导入时计数近似）+ Done 态 "View in Clients panel →" 链接（关弹窗+切面板）。偏差：per-file 零跳过未做（Parse 统计是全局的，保持签名不动）。
- **体验三项 + :memory: 修复**（11-08）：① Automation ID → `cursor-automation-<id>` 会话（当前导出全空，前瞻能力）② superseded_hint（同 timestamp+model 交互模型行计数；排除 grok-bot-*/Bugbot/agent_review 同毫秒真并发 320/333 后剩 11）③ history（import_ledger 按 source_db LIKE 'cursor-csv:%' 聚合，LIMIT 20，重载仍在）。**:memory: 事故**：②④ 使 GET 首次查库，与导入并发时池（MaxOpenConns=4）扩出第二连接=空库，报 no such table → `db.Init` 对内存库 SetMaxOpenConns(1)（文件库保持 4）。
- **成本牌价估算**（11-08 用户指定）：$0 行导入时即按基础名 Calc（handler 注入 Pricer），正值 CSV 成本永不覆盖。价格来源：OpenRouter suggest 21 条（其中 6 条原 seed 过期 2-2.5× 已刷新；-1000000 占位识别为毒数据跳过）+ Cursor 官方牌价 + xAI 官方 + 社区；35 模型 31 个有价。彩排：cursor 总成本 $1467.55（/api/clients == SQL 直查）。副作用（已获批准）：gpt-5.1/5.2/5.5/5.6-* 刷新价使其他客户端历史行重算（共改写 97608 行）。

## Follow-ups
- 生产发布（task 8）走 release 流程；深/浅主题视觉目检（18899）。
