## 更新日志（CHANGELOG）

[English version](./CHANGELOG_EN.md)

本项目采用轻量更新日志格式（参考 Keep a Changelog），适合快速迭代的小工具。

---

## [Unreleased] · 0.1.0 Preview

> **状态：Preview，尚未正式发布。** 本节描述主分支当前已具备的全部功能，
> 通过 `v0.1.0-preview.N` 标签逐步迭代（已发布到 `v0.1.0-preview.10`，对应
> GoReleaser 流水线自动出包）。等行为稳定后整体收敛为 `v0.1.0` 正式版本。

### 代理兼容性修复

- **`no_proxy` 自动注入**：`/cc-otel:setup` 现在会在 `settings.json` 的 `env` 中自动添加 `"no_proxy": "localhost,127.0.0.1"`。当用户设置了 `http_proxy` / `https_proxy`（如 Clash、V2Ray、企业代理）时，OTEL gRPC 流量会错误地走代理，导致遥测数据静默丢失。`no_proxy` 确保 OTEL exporter 直连 localhost，绕过代理。README 和 setup 文档已同步更新，重点标注代理用户必须配置此项。

### 数据源与接收

- **OTLP gRPC 接收器**：通过 OTLP/gRPC 接收 logs / metrics / traces。代码默认端口 `4317`（详见下面 Daemon / CLI 段的端口说明）。
- **Claude Code + Codex + Gemini CLI 三源**：按 OTLP Resource 的 `service.name` 路由 ——
  - 名称包含 `codex`（不区分大小写）→ 写入 `codex_*` 表，前端通过 `?source=codex` 切换视图，`/api/codex/*` 暴露。
  - 名称等于 `gemini-cli` → 写入 `gemini_*` 表（独立 schema：`thoughts_tokens` / `tool_tokens` / `total_tokens`），前端 `?source=gemini`，`/api/gemini/*` 暴露，配置见 `skills/otel-setup/gemini-setup.md`。
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
  - `panel-daily.js` / `panel-sessions.js` / `panel-requests.js` / `pagination.js`
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

## [0.1.0] - TBD

> 首个正式（非 preview）公开版本将把上述 Unreleased / Preview 内容固化为 `0.1.0`，
> 并补充安装 / 升级说明、二进制发布包与升级路径。
