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
