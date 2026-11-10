# cursor-csv-import — Cursor usage-events CSV 导入（页面卡片 + 内容哈希去重）

- Date: 2026-11-07 · Status: **implemented**（实施记录 docs/internal/impl/cursor-csv-import.md）
- 演进：v1 曾推荐离线工具 tools/cursor_ingest；v2 用户改为页面操作（Import 弹窗内 Cursor 卡片 + 官方图标），去重内核不变。
- 本文是**唯一设计文档**：合并原 cursor-model-normalize.md（11-07）与 cursor-import-card-polish.md（11-08 方案 A）两个增量 spec。

## Problem
11 份 Cursor usage-events CSV（20683 行，2025-12-01→2026-11-06）需去重导入，与 Claude/Codex/DSH 同一仪表盘。难点：导出**无 request id**（幂等键须由行内容构造）；文件间可能重叠（重跑必须幂等，不能依赖当前恰好互不重叠）；入口须在页面。

## 决策
内容哈希 `cursor-<sha256[:16]>` 作 request_id，搭 `api_requests(request_id)` partial unique index（db.go:259）——INSERT OR IGNORE 天然幂等，DSH 适配器同款先例。

## 关键事实（实测/读码，实施后仍承重）
- **列映射**（恒等式 Total = Input(w/o) + Input(w/ Cache Write) + Cache Read + Output，实测 20683/20683 零违例）：`input_tokens`=Input(w/o)、`cache_creation_tokens`=Input(w/ Cache Write)、`cache_read_tokens`=Cache Read、`output_tokens`=Output。**input 不含 cache read，否则恒等式双计**。
- Cost 形态：Included×17206 / Free×3443 / 0.00×31 / `-`×3 均为真实 $0；正成本（User API Key 实付）原样保留。后续口径（2026-11-08 用户指定）：$0 行按价格表**牌价估算**（Pricer 注入、按基础名 Calc）、估算永不覆盖正值（recompute 对 cost_usd>0 的 cursor 行跳过），`IsUpstreamAuthoritative` 因此移除 cursor 全量分支。
- zero-token 行 1793 条默认跳过（可选保留）；42 行 Cloud Agent ID → `cursor-agent-<id>` 会话；Automation ID → `cursor-automation-<id>`（两个 ID 都不进哈希，幂等不受影响）。
- `service_name='cursor'` 原样保留 → 统一视图 / Clients 面板自动生效（HS-08），零查询改动。
- 写路径走 `Repository.InsertRequest`（request_id 去重 + daily_model_agg 同事务 upsert + BUSY 重试免费复用）；cost_usd 整数微美元（costToInt64）。
- 范式参照：codex_migrate_handler（GET status / POST start、单任务 409、sameOriginGuard 包写端点）；前端 codex-migrate.js（弹窗打开才轮询、交叉禁用、aria-live）；图标规范 FRONTEND_TASTE：currentColor 单色 SVG、嵌入式资源改动 bump `?v=`。

## 增量设计：模型名基础化（原 cursor-model-normalize，方案 A）
Cursor 的 Model 列是「基础模型 × 模式档位」slug（`gpt-5.1-codex-max`、`claude-4.5-sonnet-thinking`，词序还不一致），直接入库会让档位变成模型身份（实测 49 条）。
- `NormalizeCursorModel(raw)` 纯函数：循环剥离**尾部整段**模式段——白名单 `fast / thinking / xhigh / extra-high / high / medium / low / max`；`cursor-` 前缀剥离。
- 白名单外一律不动：`grok-code-fast-1`（尾段是 1）、`gemini-3-flash-preview` / `mimo-v2-flash`（flash 是型号）、`auto`、`composer-*`、`grok-bot-*`、`github_bugbot`、`agent_review`。
- 落库存 base；`RequestID` 继续哈希**原始 slug**（幂等与映射解耦）。跨服务同名 slug 仅 5 个，并入全局同名模型是统一视图期望行为。
- 弃选：B（+mode 维度，schema 全链路改动收益存疑）、C（仅规整显示名，49 条问题未解）。
- 验证：49 个真实 slug → base 全表钉死单测 + 白名单负例；清库重导四 token 逐位一致；模型 49→35。

## 增量设计：卡片打磨（原 cursor-import-card-polish，方案 A）
范围仅 internal/web/static + 一个只读 dry-run API，不动导入写路径与哈希。
1. **整卡拖拽**：卡片可拖入 CSV（复用 DB 导入的虚线高亮语言；outline 实现，零布局位移），drop 与文件选择器走同一 setChosenFiles 漏斗。
2. **dry-run 预览**：`POST /api/cursor/import/dry-run`（复用 parseCursorUpload、只算不写、不占写者闸；返回每文件 new/dup、日期范围、新模型数）；选文件即自动跑，卡片显示一行摘要再确认导入。
3. **Done 直达**：干净 Done 态显示 "View in Clients panel →"（关弹窗 + 切面板）。
- 不做：修改去重哈希/写路径；Max Mode 计价倍率（等用户给数，属定价不是 UI）。
- 弃选 B：弹窗信息架构整体重排（动三个卡片共用骨架，回归面大）。

## 验证（bin 18899 实测）
首跑 inserted=18890 / dup=0 / skipped_zero=1793（=20683−1793，~15s）；重跑 0 新增 / 18890 duplicates；四 token 分量与 cost CSV==pool==agg 逐位一致；RECONCILE pool==agg==ledger true。

## Rollback
`DELETE FROM api_requests WHERE request_id LIKE 'cursor-%'` → RebuildDailyAggregates → 必要时恢复 VACUUM INTO 备份；代码 git revert（谓词改动无存量依赖）。
（环境操作先例：全局库经 tools/snapshot_db `VACUUM INTO` 在线快照，生产 daemon 不停。）
