# Claude Code / Codex CLI OTEL Event Reference

Claude Code and OpenAI Codex CLI both send telemetry via OTLP (gRPC) to
`cc-otel`, but their event schemas and token semantics are different. `cc-otel`
routes them by OTLP Resource `service.name`: Claude data goes into the
`api_requests` family of tables; Codex data goes into the `codex_*` tables.

---

# Claude Code OTEL Event Reference / Claude Code OTEL 事件参考

> **English Summary**
>
> Claude Code sends telemetry via OTLP (gRPC) to `:4317`. There are **5 log event types** and **8 metric instruments**.
>
> **Log events:** `user_prompt` (user question), `api_request` (API call with token/cost data), `api_error` (API failure), `tool_decision` (tool accept/deny), `tool_result` (tool execution outcome).
>
> **Metrics:** `claude_code.token.usage` (by type: input/output/cacheRead/cacheCreation), `claude_code.cost.usage` (by model), `claude_code.session.count`, `claude_code.lines_of_code.count`, `claude_code.pull_request.count`, `claude_code.commit.count`, `claude_code.code_edit_tool.decision`, `claude_code.active_time.total`.
>
> All events share resource attributes: `host.arch`, `os.type`, `os.version`, `service.name`, `service.version`. Events are correlated via `prompt.id` (same user question) and `session.id`. Privacy: prompts are redacted by default; set `OTEL_LOG_USER_PROMPTS=1` to include content.

Claude Code 通过 OTLP (gRPC) 协议发送遥测数据到 `:4317`。
包含 **5 种 Log 事件** + **8 种 Metric 指标**。

---

## Log Events (5 types) / Log 事件（5 种）

### 1. `claude_code.user_prompt` — User Prompt / 用户提问

Fired each time the user sends a prompt (sequence=0). / 每次用户发送 prompt 时触发，sequence=0。

| 属性 | 类型 | 说明 |
|------|------|------|
| `event.name` | string | `"user_prompt"` |
| `event.sequence` | int | 序号，始终为 0 |
| `event.timestamp` | ISO8601 | 事件时间 |
| `prompt` | string | **提示内容**（默认 `<REDACTED>`，需设 `OTEL_LOG_USER_PROMPTS=1`） |
| `prompt.id` | UUID | 关联键：同一用户提问的所有事件共享此 ID |
| `prompt_length` | int | prompt 字符长度 |
| `session.id` | UUID | 会话 ID |
| `terminal.type` | string | 终端类型：`windows-terminal` / `vscode` / `iterm.app` 等 |
| `user.id` | string | 用户/设备标识 |

**Resource attributes (shared by all events) / Resource 属性（所有事件共用）：**

| 属性 | 示例值 |
|------|--------|
| `host.arch` | `amd64` |
| `os.type` | `windows` / `darwin` / `linux` |
| `os.version` | `10.0.19045` |
| `service.name` | `claude-code` |
| `service.version` | `2.1.96` |

---

### 2. `claude_code.api_request` — API Call / API 调用

Fired on every Claude API call. A single user prompt may trigger multiple calls (agent sub-calls, tool follow-ups). / 每次调用 Claude API 时触发。一条用户提问可能触发多条（Agent 子调用、工具后续请求）。

| 属性 | 类型 | 说明 |
|------|------|------|
| `event.name` | string | `"api_request"` |
| `event.sequence` | int | 会话内序号 |
| `event.timestamp` | ISO8601 | 事件时间 |
| `model` | **string** | 模型名（可能是代理名） |
| `input_tokens` | int | 输入 token 数 |
| `output_tokens` | int | 输出 token 数 |
| `cache_read_tokens` | int | 缓存读取 token 数 |
| `cache_creation_tokens` | int | 缓存创建 token 数 |
| `cost_usd` | float | 本次请求费用 (USD) |
| `duration_ms` | int | 请求耗时 (ms) |
| `ttft_ms` | int | 首字时间 (ms) |
| `request_id` | string | 请求 ID（可能为空） |
| `prompt.id` | UUID | 关联回 user_prompt |
| `session.id` | UUID | 会话 ID |
| `speed` | string | `normal` / `fast` |
| `user.id` | string | 用户/设备标识 |
| `terminal.type` | string | 终端类型 |

---

### 3. `claude_code.api_error` — API Error / API 错误

Fired when an API call fails. / API 调用失败时触发。

| 额外属性 | 类型 | 说明 |
|----------|------|------|
| `error.type` | string | 错误类型 |
| `error.message` | string | 错误信息 |
| `error.code` | int | HTTP 状态码或错误码 |
| `error.retryable` | bool | 是否可重试 |

其余字段同 api_request。

---

### 4. `claude_code.tool_decision` — Tool Decision / 工具决策

Fired each time Claude decides whether to invoke a tool. / 每次 Claude 决定是否调用工具时触发。

| 属性 | 类型 | 说明 |
|------|------|------|
| `event.name` | string | `"tool_decision"` |
| `tool_name` | string | 工具名：`Agent` / `Bash` / `Read` / `Write` / `Edit` / `Glob` / `Grep` 等 |
| `decision` | string | `accept` / `deny` |
| `source` | string | `config`（自动通过）/ `user`（用户确认） |
| `prompt.id` | UUID | 关联 prompt |
| `session.id` | UUID | 会话 ID |
| `event.sequence` | int | 序号 |
| `user.id` | string | 用户标识 |

---

### 5. `claude_code.tool_result` — Tool Result / 工具执行结果

Fired after a tool finishes execution. / 工具执行完成后触发。

| 属性 | 类型 | 说明 |
|------|------|------|
| `event.name` | string | `"tool_result"` |
| `tool_name` | string | 工具名 |
| `duration_ms` | int | 工具执行耗时 |
| `success` | bool/string | 是否成功 (`true`/`false`) |
| `tool_result_size_bytes` | int | 结果数据大小（字节） |
| `decision_source` | string | 决策来源 |
| `decision_type` | string | 决策类型 |
| `prompt.id` | UUID | 关联 prompt |
| `session.id` | UUID | 会话 ID |
| `event.sequence` | int | 序号 |
| `user.id` | string | 用户标识 |

> **Note:** Set `OTEL_LOG_TOOL_DETAILS=1` to enable full tool_decision and tool_result events.
> **注意**：需设置 `OTEL_LOG_TOOL_DETAILS=1` 才会发送 tool_decision 和 tool_result 事件的完整详情。

---

## Metrics (8 instruments) / Metric 指标（8 种）

| 指标名 | 类型 | 说明 | 属性 |
|---------|------|------|------|
| `claude_code.token.usage` | Sum | Token 用量 | `type`: input / output / cacheRead / cacheCreation |
| `claude_code.cost.usage` | Sum | 费用 (USD) | `model`: 模型名 |
| `claude_code.session.count` | Count | CLI 会话数 | - |
| `claude_code.lines_of_code.count` | Count | 修改代码行数 | - |
| `claude_code.pull_request.count` | Count | 创建 PR 数 | - |
| `claude_code.commit.count` | Count | 提交数 | - |
| `claude_code.code_edit_tool.decision` | Count | Code Edit 决策次数 | - |
| `claude_code.active_time.total` | Sum | 活跃时间(秒) | - |

---

## Codex CLI Log Events / Codex CLI Log 事件

Codex is detected by OTLP Resource `service.name` containing `codex`. Current
known values include `codex_cli_rs`, `codex_exec`, `codex-app-server`, and
`codex_mcp_server`.

Unlike Claude Code, Codex does not put complete request usage into a single
`api_request` log. `cc-otel` builds a request row by correlating several events:

```
codex.api_request
  -> insert codex_api_requests row with network/request metadata

codex.sse_event kind=response.completed
  -> backfill token fields into the newest pending row for same session+model

codex.websocket_event kind=response.completed
  -> best-effort backfill duration_ms when duration is not already known
```

### 1. `codex.api_request` — Request Metadata / 请求元数据

Fired for Codex API calls. This event is the first row anchor for
`codex_api_requests`; token fields are usually still zero until the completion
event arrives.

| 属性 | 类型 | 说明 |
|------|------|------|
| `event.name` | string | `"codex.api_request"` |
| `conversation.id` | string | Codex session/conversation key; stored as `session_id` |
| `model` | string | Model name |
| `duration_ms` | int | API request duration if Codex reports it |
| `http.response.status_code` | int | HTTP status |
| `endpoint` | string | API endpoint |
| `attempt` | int | Retry attempt |
| `error.message` | string | Error message, if any |
| `auth.request_id` | string | Upstream request id, if present |
| `auth.cf_ray` | string | Cloudflare ray id, if present |

Current storage mapping:

| Source event | Table | Purpose |
|--------------|-------|---------|
| `codex.api_request` | `codex_api_requests` | Creates the request row and increments request count |
| `codex.api_request` | `codex_daily_model_agg` | Adds request count; token deltas are added later |
| raw OTLP log | `codex_raw_otlp_events` | Debug/audit copy |

### 2. `codex.sse_event` — Streaming Events / 流式事件

Most SSE events are stored in `codex_events` for debugging. The important one
for usage accounting is `event.kind=response.completed`.

| 属性 | 类型 | 说明 |
|------|------|------|
| `event.name` | string | `"codex.sse_event"` |
| `event.kind` | string | e.g. `response.completed` |
| `conversation.id` | string | Correlation key |
| `model` | string | Model name |
| `input_token_count` | int | Total input tokens; **includes cached input** |
| `output_token_count` | int | Output tokens |
| `cached_token_count` | int | Cached input token subset |
| `reasoning_token_count` | int | Reasoning output tokens |
| `tool_token_count` | int | Codex currently sends `usage.total_tokens` here; not tool tokens |

Token semantics for Codex:

| UI / DB concept | Formula |
|-----------------|---------|
| Total input | `input_token_count` |
| Cached input | `cached_token_count` |
| Uncached input | `max(input_token_count - cached_token_count, 0)` |
| Output | `output_token_count` |
| Reported total | `tool_token_count` from Codex, stored as `total_tokens` |
| Fallback total | `input_token_count + output_token_count` |
| Cache hit rate | `cached_token_count / input_token_count` |

This differs from Claude Code. Claude input-side total is
`input_tokens + cache_read_tokens + cache_creation_tokens`; Codex must not add
cached input a second time.

Current backfill behavior:

| Source event | Table | Behavior |
|--------------|-------|----------|
| `codex.sse_event` + `response.completed` | `codex_api_requests` | Updates newest zero-token row within 5 minutes for same `conversation.id + model` |
| same | `codex_daily_model_agg` | Adds token deltas without incrementing request count |
| no pending request row | `codex_api_requests` | Inserts a token-only fallback row and counts it once |
| non-completion SSE events | `codex_events` | Stored as generic Codex events |

### 3. `codex.websocket_event` — WebSocket Timing / WebSocket 时序

WebSocket events are used for best-effort duration backfill. Individual
`duration_ms` values are per-event round-trip timings; `cc-otel` prefers the
timestamp span of stored websocket events for the same `conversation.id + model`
when it can compute one.

| 属性 | 类型 | 说明 |
|------|------|------|
| `event.name` | string | `"codex.websocket_event"` |
| `event.kind` | string | e.g. `response.completed` |
| `conversation.id` | string | Correlation key |
| `model` | string | Model name |
| `duration_ms` | int | Per-event duration fallback |
| `success` | bool | Whether the websocket event succeeded |
| `error.message` | string | Error message, if any |

Current duration backfill behavior:

| Source event | Table | Behavior |
|--------------|-------|----------|
| `codex.websocket_event` | `codex_events` | Stored for later span calculation |
| `codex.websocket_event` + `response.completed` | `codex_api_requests.duration_ms` | Updates newest tokenized row with zero duration |
| available websocket span | `duration_ms = (max(timestamp) - min(timestamp)) * 1000` |
| no span but event `duration_ms` exists | fallback to event `duration_ms` |

This is approximate. The stronger correlation key currently available to
`cc-otel` is `conversation.id + model + time window`; concurrent same-model
requests in the same conversation can still be ambiguous.

### 4. Other Codex Events / 其他 Codex 事件

| 事件 | Table | 说明 |
|------|-------|------|
| `codex.user_prompt` | `codex_user_prompt_events` | User prompt metadata/content when emitted |
| `codex.tool_decision` | `codex_tool_decision_events` | Tool approval/decision |
| `codex.tool_result` | `codex_tool_result_events` | Tool execution result |
| other Codex logs | `codex_events` | Generic fallback storage |
| raw OTLP logs | `codex_raw_otlp_events` | Debug/audit copy |

---

## Event Sequence Example / 完整事件时序示例

A typical event chain for a user prompt "fix a bug": / 一次用户提问 "帮我改个 bug" 的典型事件链：

```
seq 0  user_prompt              ← 你发了问题
seq 1  api_request   claude-opus-4-6   $0.003   63in/112out    ← 主模型回复
seq 2  api_request   glm-5v-turbo      $0.153  29272in/253out ← Agent 子调用
seq 3  tool_decision Agent           accept                  ← 调工具
seq 4  api_request   glm-5v-turbo      $0.062  11341in/138out   ← 处理结果
seq 5  tool_decision Bash            accept                  ← 执行命令
seq 6  tool_result   Bash             1568ms  ✅ 1248bytes     ← 命令完成
seq 7  tool_decision Bash            accept
seq 8  tool_result   Bash             140ms   ✅ 1094bytes
...
seq N  api_request   claude-opus-4-6   $0.024  final reply        ← 最终回复
```

---

## Privacy Controls / 隐私控制环境变量

| 变量 | 默认值 | 效果 |
|------|--------|------|
| `OTEL_LOG_USER_PROMPTS` | off (REDACTED) | 记录用户原始 prompt 内容 |
| `OTEL_LOG_TOOL_DETAILS` | off | 发送 tool_decision/tool_result 事件 |
| `OTEL_LOG_TOOL_CONTENT` | off | 记录工具的输入输出内容（截断 60KB） |

---

## Storage Mapping / 当前存储映射

| 事件类型 | events 表 | api_requests 表 | raw_otlp_events |
|----------|:----------:|:---------------:|:--------------:|
| user_prompt | ✅ 全字段 | ❌ 无 model | ✅ 原始 JSON |
| api_request | ✅ 全字段 | ✅ 全字段（金额统计） | ✅ 原始 JSON |
| api_error | ⚠️ 未单独处理 | ⚠️ 可能被丢弃 | ✅ 原始 JSON |
| tool_decision | ✅ 全字段 | ❌ 无 model | ✅ 原始 JSON |
| tool_result | ✅ 全字段 | ❌ 无 model | ✅ 原始 JSON |
