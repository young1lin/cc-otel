## 设计说明（为什么这样做 / 怎么改最合适）

[English version](./DESIGN_EN.md)

这份文档回答三个问题：

- **cc-otel 是什么**：它解决什么痛点，边界在哪里。
- **为什么这样设计**：关键权衡（单二进制 / SQLite / 预聚合 / SSE / TTFT 回填）。
- **怎么改最合适**：扩展一个指标/字段、改 UI 表格、调试数据缺失的推荐路径。

---

## 1. 目标与非目标

### 目标

- **零外部依赖**：不要求 Grafana/Prometheus/OTel Collector；单二进制可运行。
- **Claude Code 专用**：针对它的 OTEL 事件模型优化（`api_request` 为事实表）。
- **可追溯**：字段漂移/缺失时能回溯原始 OTLP payload（`raw_otlp_events`）。
- **交互体验好**：Web UI 实时刷新、范围切换快（百万行依然顺滑）。

### 非目标（刻意不做）

- 通用 OTLP 后端（不覆盖所有 OTLP 语义与协议变体）。
- 复杂多租户鉴权与远程部署场景（当前默认 localhost）。
- 试图 100% 精确把每个 trace span 与每个请求一一强关联（只做“足够可靠”的回填策略）。

---

## 2. 数据流：从 Claude Code 到 UI

**事实来源**分两类：

1. **Logs / Events（主事实）**：`claude_code.api_request` 事件携带 token/cost/duration 等明细 → 落 `api_requests`。
2. **Traces（补充事实）**：TTFT 大多只在 span attributes 中出现 → 接收 TraceService → 回填到 `api_requests.ttft_ms`。

UI 读库路径：

- **Dashboard / Daily / Sessions**：尽量走聚合表（或聚合查询）保证快。
- **Request Log**：上半部分读 `/api/durations`（按模型聚合），下半部分读 `/api/requests`（请求明细）。
- **实时刷新**：Receiver 入库新数据后 `Notify()` → SSE `/api/events` → 前端触发刷新。

相关事件字段参考：`docs/otel-events.md`

---

## 3. 存储模型（为什么是 SQLite + 预聚合）

### 为什么 SQLite

- 单文件、WAL 模式性能稳定，**适合本地工具**。
- 便于分发与备份（复制一个 `.db` 就够）。
- 配合索引和（必要时）预聚合，查询毫秒级。

### 为什么预聚合（daily_model_agg）

Claude Code 的请求明细可能快速增长，若每次 UI 刷新都扫 `api_requests` 会越来越慢。

因此在写入 `api_requests` 的同时，**事务内 upsert** 到 `daily_model_agg`：

- UI 图表与日维度表优先查 `daily_model_agg`（行数 ~ days×models）。
- 明细查询才查 `api_requests`（并有 timestamp/model/session 索引）。

---

## 4. TTFT 设计（为什么要 traces + 回填）

### TTFT 是什么

- **TTFT** = Time To First Token（ms）
- 表示“请求开始”到“收到第一个 token”之间的延迟
- 它不是整体 duration；通常 \(TTFT \ll duration\)

### 为什么不直接从 `api_request` log 取

Claude Code 的 `api_request` log 并不总是包含 `ttft_ms`。
实践中更稳定的来源是 **trace span attributes**（例如 `claude_code.llm_request` span）。

### 为什么要回填到 `api_requests`

目的不是完美追踪系统，而是让 UI/查询简单：

- 聚合统计（Avg TTFT）与明细展示都直接读 `api_requests.ttft_ms`
- 不需要 UI 再去 join trace 表或解析 raw JSON

### 回填策略（分层）

1. **严格匹配**：`(session_id + prompt_id + model) + 最近时间`
2. **降级匹配（常见）**：trace span 缺 `prompt_id` 时，用 `(session_id + model) + 时间窗口(±120s)` 最近匹配
3. **乱序补齐**：trace 先到、log 后到时，把 TTFT span 写入 `pending_ttft_spans`，等 `api_request` 插入后再补上

> 这三层的目标是“尽量补齐且降低误匹配风险”，不是追求绝对精确。

---

## 5. Web UI 设计（为什么是 SSE + 两段表）

### 为什么 SSE

- 实现成本低（单向推送足够）
- 浏览器原生支持 `EventSource`
- 避免轮询浪费（尤其在高频写入时）

### Request Log 两段表为什么这样拆

用户在 Request Log 里有两类任务：

- **先看整体**：各模型平均耗时、吞吐、TTFT
- **再看单条**：定位某条请求的 tokens/cost/duration/TTFT

因此上半表是**按模型聚合**（支持排序），下半表是**请求明细**（可加字段与 tooltip）。

---

## 6. 怎么改最合适（常见改动的“正确入口”）

### A) 新增一个“请求级字段”并展示到 UI

例：新增 `server_queue_ms`

1. **接收层**：在 `internal/receiver/receiver.go` 解析 OTLP attributes → 写入 `APIRequest`
2. **DB 层**：在 `internal/db/db.go` 给 `api_requests` 加列 + 索引（如需要）
3. **Repository**：`InsertRequest` 写入该字段；必要时更新聚合接口
4. **API**：`/api/requests` JSON 带上该字段
5. **UI**：`internal/web/static/index.html` 加列头；`app.js` 渲染每行

### B) 新增一个“按模型聚合统计列”（类似 Avg Duration/Out tok/s/Avg TTFT）

1. **Repository**：扩展 `GetDurationStatsByModel` 的 SELECT（AVG/MIN/MAX）
2. **API**：`/api/durations` 返回新增字段
3. **UI**：上半表 `<th data-sort-key=...>` + `renderDurationStatsTable()` 渲染 + 排序 key

### C) 为什么有数据缺失 / 指标为 0？

优先按这条路径排查：

1. **看接收日志**：是否收到对应 OTLP（例如 trace：`OTEL traces received`）
2. **看 raw 备份**：`raw_otlp_events` 是否已有该 span/log（字段名是否漂移）
3. **看回填日志**：trace backfill 是 updated / no match / missing keys
4. **再看 DB**：`api_requests` 对应行是否存在，timestamp 是否落在窗口内

---

## 7. 设计权衡（给未来维护者的“为什么”）

- **单二进制**：易分发，但前端改动要重新编译（开发时可用 `CC_OTEL_STATIC_DIR` 绕过）。
- **SQLite**：简单可靠，但并发写入要注意事务边界与索引（已启用 WAL + busy_timeout）。
- **TTFT 回填**：只做 best-effort；通过"pending + 时间窗口"降低顺序问题影响。
- **前端模块化（vanilla ESM，无构建工具）**：`internal/web/static/app.js` 拆为 `js/*.js` 一组 ES Modules，浏览器原生 `<script type="module">` 加载，**不引入** Node / Vite / 打包器。`go:embed static/*` 自动覆盖嵌套子目录，单二进制部署形态不变。每个模块单一职责（state / utils / theme / api / filters / sse / breakdown / insights / chart-main / panel-* / pagination），纯函数下沉到 `utils.js` / `theme.js` / `insights.js` 并配 `node --test` 单元测试。跨模块依赖通过 `initX({ ... })` 显式注入回调，避免循环 import。

---

## 8. 价格表与非 Claude 模型成本重算

### 问题

cc-otel 早期直接信任 OTLP 上报的 `cost_usd`，导致两类失真：

1. **Codex 不上报费用**：Codex CLI 的 `codex.api_request` 与 `codex.sse_event` 都不带 `cost_usd`，dashboard Cost KPI 永远显示 `—`。
2. **GLM/DeepSeek/Kimi 走 Anthropic 兼容反代**：Claude Code 仍按 Anthropic 官方价目算 `cost_usd`，cc-otel 接收的成本被按 Sonnet/Opus 价乘了 GLM 的 token，严重失真。

### 单一规则

```
if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude-")
    keep e.CostUSD as upstream-reported  // Claude 由 Anthropic 自己维护
else
    e.CostUSD = registry.Calc(...)       // 其他全部本地重算
```

派生效果：Codex 必然重算；GLM 反代必然重算；原生 Claude 不动。**没有 `cost_mode` 配置开关**，规则只此一条。

### 三层价格优先级

```
1. cfg.Pricing (cc-otel.yaml 用户覆盖) → 内存层，启动时加载，最高优先
2. model_pricing 表                    → SQLite 长期存储，跨重启
3. 远端定时刷新 (LiteLLM + OpenRouter)  → 24h 一次，diff 才写
```

查询路径只查 1+2；远端只在写入时参与，运行时绝不打远端。

### 价格表初始化

首次启动 `pricing.NewRegistry` 调用 `seedIfEmpty`，若 `model_pricing` 行数为 0，从 `internal/pricing/embed/seed.json`（669 条，由 `tools/dump_pricing_snapshot` 离线生成）灌入。embed 来自 BerriAI/litellm，**不包含** Anthropic / Claude 条目——查 Claude 时 registry 必返回 miss，receiver 早就在调用前用 `IsClaudeModel` 短路了。

### Refresher（diff 写）

`internal/pricing/refresher.go` 起一个 goroutine，启动立刻跑一次然后每 24h 跑一次：

1. 并发拉所有 Source（LiteLLM 优先级 10、OpenRouter 5）
2. 同 model 高优先级覆盖低优先级
3. 与 `model_pricing` 当前快照比 hash（`fmt.Sprintf("%.12g|%.12g|%.12g|%.12g", input, output, cacheRead, cacheCreate)`）
4. 仅 hash 变化的 UPDATE，未变化跳过（`updated_at` 不抖）
5. 全部源失败时保留旧快照；部分失败 → 状态置为 `partial`

刷新完成后调用 `Reloader.Reload(ctx)` 让 in-memory map 立即可见，并写入 `lastRefreshAt/Msg/Err/ChangedRows`，对外通过 `/api/status` 的 `pricing` 字段暴露。

### 模型名匹配（match.go）

跑通 "glm-4.6 反向解析到 openrouter/z-ai/glm-4.6" 靠四步级联：

1. 精确匹配
2. 去尾巴变体（`-20251028` 日期、`-preview/-latest` 标签）后再精确匹配
3. 别名反查（seed 写入的 alias 列）
4. 最长前缀匹配，要求边界字符是 `-`/`.`/`:`（避免 `gpt-5` 撞 `gpt-50`）

### 历史回填

`tools/recompute_cost` 默认 dry-run，`--apply` 才写：

1. 加载同一个 `pricing.Registry`
2. SELECT 范围内行，跳过 Claude 行与无 token 信号行
3. 重算并比 `cost_usd`，仅差异行进 batch UPDATE
4. UPDATE 完后 DELETE+INSERT 重建 `daily_model_agg` / `codex_daily_model_agg`

