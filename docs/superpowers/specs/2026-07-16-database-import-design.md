# 在线导入 cc-otel SQLite 数据库：设计规格

日期：2026-07-16

## 1. 背景与目标

cc-otel 已有 tools/merge_bin_global 下的离线合并工具，但它面向固定目录和文件替换流程。新功能要把相同的数据合并语义带进正在运行的 Go 服务：

- 当前服务正在使用的 SQLite 数据库是主库，也是唯一写入目标。
- 用户上传的 .db 文件是只读导入库。
- 主库持续在线并保持 WAL 模式，不停止进程、不替换数据库文件。
- 合并后保留主库原有数据，只补充导入库中缺失的逻辑记录。
- 重复记录保留主库版本。
- 重复上传同一数据库，或在部分批次成功后重试，不会重复写入。
- 前端提供上传、预检、预览、确认、进度和结果反馈。
- 新的正式 Go 包与现有 merge tool 共享 schema、去重、写入、聚合和验证规则，避免维护两套合并语义。

## 2. 非目标

本功能不做以下事情：

- 不删除、覆盖或更新主库中的既有明细记录。
- 不停止 cc-otel 服务，不复制或替换主库文件。
- 不创建自动备份；这是只增不删的合并流程。
- 不导入上传库的聚合表、价格表、配置或导入账本。
- 不自动重算 cost_usd；导入值按原值保存。
- 不支持任意 SQLite 数据库，只支持当前版本和明确列出的历史 cc-otel schema。
- 不在服务重启后自动恢复未完成任务。
- 不增加多任务队列；同一时间只允许一个上传、预检或导入任务。

## 3. 已确定的核心语义

主库是权威数据源。对任一逻辑身份相同的记录：

1. 主库已有记录时，跳过导入库记录，不比较或覆盖非身份字段。
2. 主库没有记录时，插入导入库记录，但不沿用源库自增 id。
3. 导入库自身有重复记录时，只插入第一条逻辑记录。
4. import_ledger 只记录处理事实，不能单独决定跳过；每一行仍必须检查主库中的实际记录。

所有明细写入按同表、最多 10,000 个源记录或约 128 MiB 估算 payload 组成一个批次，任一上限先到即刷新；单行超过 128 MiB 时独立成批。每个批次的明细、由新增请求产生的聚合增量和 ledger 记录在同一个主库事务中提交。批次失败时只回滚当前批次；之前已经提交的批次保留。

## 4. 总体架构

采用共享引擎方案，在 internal/dbmerge 中建立正式合并包。HTTP API 和 merge tool 都是适配层，不复制业务规则。

~~~
浏览器
  │ multipart 上传 / 状态轮询
  ▼
internal/api
  ├─ 上传文件与任务生命周期
  └─ HTTP 校验和 JSON 契约
          │
          ▼
internal/dbmerge
  ├─ Schema Registry
  ├─ SQLite Inspector
  ├─ Natural-key Deduper
  ├─ Batch Importer
  ├─ Aggregate Updater
  ├─ Ledger Writer
  └─ Containment Verifier
      │                 │
      ▼                 ▼
上传库，只读       当前运行主库，在线写入
~~~

### 4.1 组件边界

internal/dbmerge 负责：

- 定义受支持表、字段、历史字段映射和自然键。
- 只读检查上传库，生成逐表预览。
- 把不同来源适配成统一的 Row 流。
- 在主库事务内去重、插入、更新聚合和写 ledger。
- 导入后按同一身份规则验证源数据被主库包含。
- 通过进度回调报告阶段和计数，不依赖 HTTP 或 DOM。

internal/api 负责：

- multipart 流式接收、大小限制、随机临时文件名和权限。
- 单任务状态、任务取消、过期清理和后台 goroutine。
- 把 dbmerge 的进度、警告和错误映射为稳定的 JSON。
- 导入结束或部分失败后通知 SSE，使仪表盘刷新。

前端负责：

- 顶部入口、SVG 图标、模态框和 Apple 风格视觉。
- 上传进度、任务轮询、预览确认、结果与重试。
- 页面刷新时通过状态接口恢复同进程内的当前任务。

merge tool 负责：

- 保留现有命令行参数、快照、备份、进程控制和文件替换编排。
- import_global、verify_merge 和 run_merge 中涉及数据身份、插入、聚合、验证的部分改为调用 internal/dbmerge。
- JSONL 仍可通过 RowSource 适配器输入；Web 导入使用 SQLiteSource。两者必须使用同一 Schema Registry 和 Deduper。

### 4.2 建议文件布局

- internal/dbmerge/types.go：公共类型、结果、进度和 RowSource 接口。
- internal/dbmerge/schema.go：表注册表、字段映射、严格 schema 校验。
- internal/dbmerge/inspect.go：SQLite header、quick_check、逐表预览。
- internal/dbmerge/dedupe.go：身份键、SQLite IS 查询和稳定指纹。
- internal/dbmerge/import.go：10,000 行 / 128 MiB 有界批次、事务、重试和进度。
- internal/dbmerge/aggregate.go：Claude/Codex 聚合 delta UPSERT。
- internal/dbmerge/ledger.go：import_ledger 建表和记录。
- internal/dbmerge/verify.go：导入后包含性验证。
- internal/api/import_handler.go：四个 HTTP 端点。
- internal/api/import_jobs.go：单任务管理器和文件清理。
- internal/web/static/js/import-db.js：导入弹窗状态机和 DOM 行为。
- internal/web/static/js/api.js：上传 XHR 与其余导入 API 调用。
- internal/web/static/tests/import-db.test.mjs：纯状态与格式化测试。

实际实现可以在不改变这些边界的前提下合并很小的 Go 文件，但不能把 dbmerge 规则放回 HTTP handler 或前端。

## 5. 数据范围

### 5.1 导入表和规范字段

id 永远不导入。下列字段按所列顺序组成每张表的规范输入：

- api_requests：timestamp, session_id, user_id, prompt_id, prompt_length, model, actual_model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, cost_usd, duration_ms, ttft_ms, request_id, event_name, event_sequence, speed, terminal_type, tool_name, decision, source, service_name, service_version, host_arch, os_type, os_version, error_type, error_message, error_code, error_retryable。
- user_prompt_events：timestamp, session_id, user_id, prompt_id, prompt_text, prompt_length, event_sequence, terminal_type, service_name, service_version, host_arch, os_type, os_version。
- tool_decision_events：timestamp, session_id, user_id, prompt_id, event_sequence, tool_name, decision, source, terminal_type。
- tool_result_events：timestamp, session_id, user_id, prompt_id, event_sequence, tool_name, success, duration_ms, tool_result_size_bytes, decision_source, decision_type, terminal_type。
- api_error_events：timestamp, session_id, user_id, prompt_id, event_sequence, model, duration_ms, terminal_type, error_type, error_message, error_code, error_retryable, service_name, service_version。
- otel_metric_points：timestamp, metric_name, value, session_id, user_id, terminal_type, model, attr_type。
- events：timestamp, session_id, user_id, prompt_id, prompt_length, event_name, event_sequence, model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, cost_usd, duration_ms, speed, terminal_type, tool_name, decision, source, success, tool_result_size_bytes, service_name, service_version, host_arch, os_type, os_version, error_type, error_message, error_code, error_retryable。
- raw_otlp_events：timestamp, event_type, raw_json。
- pending_ttft_spans：created_unix, session_id, model, span_end_unix, ttft_ms, raw_json, processed, request_id。
- codex_api_requests：timestamp, session_id, user_id, model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, cost_usd, duration_ms, ttft_ms, http_status, endpoint, conversation_id, event_name, event_sequence, terminal_type, service_name, service_version, host_arch, os_type, os_version, error_message。
- codex_user_prompt_events：timestamp, session_id, conversation_id, prompt_text, prompt_length, event_sequence, terminal_type, service_name, service_version。
- codex_tool_decision_events：timestamp, session_id, conversation_id, tool_name, call_id, decision, source, event_sequence, terminal_type。
- codex_tool_result_events：timestamp, session_id, conversation_id, tool_name, call_id, success, duration_ms, arguments_length, output_length, tool_origin, mcp_server, error_message, event_sequence, terminal_type。
- codex_raw_otlp_events：timestamp, event_type, raw_json。

NULL 值按原值保留。比较时使用 SQLite IS 语义，不能把 NULL 隐式改成空字符串或零后再判重。

### 5.2 明确不导入的表

- daily_model_agg
- codex_daily_model_agg
- model_pricing
- import_ledger
- sqlite_sequence
- codex_events
- gemini_api_requests
- gemini_daily_model_agg

前两个聚合表由实际新增请求产生的 delta 更新。model_pricing 和配置属于当前主库。import_ledger 不能从外部继承。sqlite_sequence 由主库维护。

codex_events 只作为兼容表保留：当前接收器不再写入，SQLite/JSONL 导入和旧 CLI 均识别但跳过。严格校验字段固定为 id, timestamp, session_id, conversation_id, event_name, event_kind, model, duration_ms, error_message, raw_attrs_json；出现其他字段仍视为未知未来 schema。

历史 Gemini 两张表属于已知旧版 cc-otel，但当前产品已删除 Gemini 集成。它们被忽略并在预检中显示警告，不会被当成未知未来 schema 拒绝。

严格校验时允许的 Gemini 历史字段也固定如下；出现其他 Gemini 字段仍视为未知未来 schema：

- gemini_api_requests：id, timestamp, session_id, model, input_tokens, output_tokens, cache_read_tokens, thoughts_tokens, tool_tokens, total_tokens, duration_ms, cost_usd, http_status_code, prompt_id, event_name, service_name, service_version。
- gemini_daily_model_agg：date, model, total_requests, input_tokens, output_tokens, cache_read_tokens, thoughts_tokens, tool_tokens, total_tokens, cost_usd, duration_ms_sum。

## 6. Schema 兼容策略

上传文件必须满足以下全部条件：

1. 文件名扩展名为 .db，大小不超过 2 GiB。
2. 前 16 字节为 SQLite format 3 加结尾 NUL。
3. 使用只读 immutable 连接成功打开。
4. PRAGMA quick_check 返回且只返回 ok。
5. 至少存在完整的 Claude 核心表组：
   api_requests、user_prompt_events、tool_decision_events、tool_result_events、api_error_events、otel_metric_points、events、raw_otlp_events、daily_model_agg。
6. 表字段匹配当前 schema 或下面明确支持的历史变体。
7. 不存在未登记的用户表或已登记表中的未知字段。

### 6.1 支持矩阵

| 变体 | 接受条件 | 处理 |
|---|---|---|
| Claude 初始版 | 只有完整 Claude 核心表组 | 导入 Claude 表 |
| TTFT 历史版 | 有 pending_ttft_spans，但没有 request_id | request_id 映射为空字符串 |
| 当前 TTFT | pending_ttft_spans 有 request_id | 原值导入 |
| Codex 历史版 | 完整 Codex 表组使用 tool_tokens | 映射到 total_tokens |
| Codex 迁移后版本 | 同时存在 tool_tokens 和 total_tokens | total_tokens 非零时优先，否则使用正数 tool_tokens |
| 当前 Codex | 完整 Codex 表组使用 total_tokens | 原值导入 |
| Gemini 历史残留 | 额外存在两张 gemini_* 表 | 忽略并警告 |
| 未来未知版本 | 出现未知表、未知字段或不完整表组 | 拒绝，提示升级当前服务 |

完整 Codex 表组是：

- codex_api_requests
- codex_daily_model_agg
- codex_user_prompt_events
- codex_tool_decision_events
- codex_tool_result_events
- codex_events
- codex_raw_otlp_events

Codex 表组可以整体不存在，但不能只出现一部分。pending_ttft_spans、model_pricing、import_ledger 和 Gemini 历史表可以按其年代独立出现。

Schema Registry 同时登记导入表和已知忽略表的当前字段。除 tool_tokens 和缺失的 pending request_id 外，不做猜测式补列、重命名或类型转换。发现未来字段时返回 unsupported_schema，并列出表名和字段名。

## 7. 去重与主库优先规则

### 7.1 请求表

api_requests：

- request_id 非空时，只用 request_id 判定身份。
- request_id 为空时，使用：
  timestamp + session_id + prompt_id + event_sequence + model + input_tokens + output_tokens + duration_ms。

codex_api_requests 使用：

timestamp + session_id + model + input_tokens + output_tokens + duration_ms。

两个请求键都刻意排除 cost_usd。相同请求即使在另一数据库中被重新定价，也只保留主库版本。

非空的定义是 SQLite 文本不等于空字符串；不额外 trim。主库中的 api_requests 唯一 request_id 索引继续作为最后一道并发保护。

### 7.2 普通事件表

以下表以第 5.1 节列出的全部规范字段作为自然键，id 除外：

- user_prompt_events
- tool_decision_events
- tool_result_events
- api_error_events
- otel_metric_points
- events
- codex_user_prompt_events
- codex_tool_decision_events
- codex_tool_result_events

每个字段使用 IS 比较，因此两个 NULL 相等，NULL 与空字符串或零不相等。

### 7.3 Raw OTLP

raw_otlp_events 和 codex_raw_otlp_events 使用：

timestamp + event_type + raw_json。

raw_json 按原始文本逐字比较，不做 JSON 解析、重排或空白规范化。

### 7.4 Pending TTFT

pending_ttft_spans：

- request_id 非空时，只用 request_id。
- 否则使用 session_id + model + span_end_unix + ttft_ms。

身份相同时保留主库整行，包括主库 processed 状态；不使用导入库状态覆盖。新插入时保留导入库 processed。processed、created_unix 和 raw_json 不参与身份判断，以便 TTFT 行在处理状态变化后仍可识别。

### 7.5 稳定指纹与 ledger

dbmerge 为每个逻辑身份生成 SHA-256 指纹。编码必须包含表名、字段名、SQLite 值类型、NULL 标记和长度前缀，避免简单字符串拼接歧义。

主库建立或复用：

~~~
CREATE TABLE IF NOT EXISTS import_ledger (
    uuid TEXT PRIMARY KEY,
    imported_at INTEGER NOT NULL,
    source_db TEXT NOT NULL,
    table_name TEXT NOT NULL
);
~~~

- uuid 是表名加自然身份的稳定指纹。
- source_db 保存 upload: 加上传文件 SHA-256，不保存原始或临时路径。
- 每个扫描到且通过字段读取的源身份都执行 INSERT OR IGNORE ledger，包括实际插入和因主库已存在而跳过的身份；ledger 记录与对应批次同事务提交。
- ledger 命中仅用于审计和统计，仍必须运行实际 recordExists 检查。

## 8. 在线事务、一致性和聚合

### 8.1 主库在线写入

- 使用当前 Repository 暴露的同一个 sql.DB；不重新迁移或替换主库。
- 主库继续使用 WAL、busy_timeout 和现有连接池。
- 上传库使用 file URI 的 mode=ro 与 immutable=1，绝不执行 migration、ATTACH 写入或 PRAGMA journal_mode 修改。
- 不执行 VACUUM INTO、文件复制、daemon stop 或数据库 rename。

上传协议只接收一个自包含 .db 文件，不接收客户端的 .db-wal 或 .db-shm。API 无法判断用户是否遗漏了尚未 checkpoint 的 WAL 内容；调用方负责用 SQLite 在线快照或完成 checkpoint 后再上传。本功能只保证对收到的 .db 文件做 quick_check 和一致读取。

每批先把同表、最多 10,000 个源行或约 128 MiB 估算 payload 装入连接本地 TEMP staging，再对主库取得短时 writer lock。去重查询和集合插入必须在同一个 BEGIN IMMEDIATE 事务中完成，防止两个导入操作在检查和插入之间穿插。当前功能只有一个导入任务，但仍需与 OTLP 接收器的正常写入协调。

遇到 SQLITE_BUSY 或 SQLITE_BUSY_SNAPSHOT 时，整批回滚并使用 100 ms、250 ms、500 ms 三次有界退避重试。仍失败则任务进入 failed；已提交批次不回滚。

### 8.2 聚合增量

仅 api_requests 和 codex_api_requests 的实际新增行产生聚合 delta。对每个新增请求：

- date 使用当前服务进程 time.Local，把 timestamp 转成 YYYY-MM-DD。
- model 和所有 token、cost 字段使用插入行的最终值。
- request_count 增加 1。

Claude UPSERT 字段：

- input_tokens
- output_tokens
- cache_read_tokens
- cache_creation_tokens
- cost_usd
- request_count

Codex UPSERT 还包括：

- reasoning_tokens
- total_tokens

每一批的明细 INSERT、daily_model_agg 或 codex_daily_model_agg UPSERT、ledger INSERT 必须在同一事务提交。不能像旧工具那样在所有明细提交后再用独立事务补聚合。

不读取导入库聚合表，不自动重算 cost_usd。导入请求的 cost_usd 原样进入明细和该行产生的聚合 delta。

### 8.3 失败与重试

任务失败后保留已经提交的批次。对同一上传文件重试时从第一张表重新扫描，依靠自然键跳过已有行，因此可以安全续导。重试不是依赖 ledger 游标恢复。

若错误发生在导入或验证阶段且临时文件仍在，retryable 为 true；预检的文件损坏或 schema 不支持错误不可重试，必须重新上传。

### 8.4 完成验证

每个批次提交后立即使用同一 TEMP staging 做集合包含性验证；导入主流程不再为验证第三次扫描上传库。任务保持 state=importing，并在验证进度中使用 phase=verifying：

- 使用与导入完全相同的自然键和 NULL 语义。
- 验证集合包含性，不比较原始 id、源库行数或 cost 总和。
- 导入库自身的重复行只需在主库存在一次。
- pending_ttft_spans 不比较 processed。
- 缺失任一身份时任务以 verification_failed 结束，已提交数据仍保留且可重试。

验证成功后才进入 succeeded。

## 9. 后台任务与文件生命周期

### 9.1 单任务状态机

外部状态只有：

~~~
inspecting → ready → importing → succeeded
     │                    └──────→ failed
     └───────────────────────────→ failed

failed(retryable) → importing
~~~

uploading 是 inspecting 下的 phase；verifying 是 importing 下的 phase。DELETE 直接移除尚未开始导入的任务，不引入 canceled 状态。

活跃任务定义为上传中、inspecting、ready 或 importing。存在活跃任务时再次上传返回 409 import_busy。succeeded 或 failed 不阻止新上传；新上传会清理并替换旧终态任务。

### 9.2 临时文件

- 目录：主库所在目录下的 .imports。
- 目录权限：0700；上传文件权限：0600。Windows 上按平台能力尽力应用。
- 文件名：服务端生成的随机 128-bit 标识加 .db.upload，不使用用户文件名。
- 原文件名仅作为经过 JSON 和 HTML 转义的显示元数据。
- 上传时同步计算 SHA-256，不做第二次整文件 hash。

清理规则：

- succeeded：验证成功后立即删除上传文件。
- DELETE：导入开始前取消时立即删除；若检查 goroutine 正在使用文件，先取消并在其退出后删除。
- failed：保留 1 小时供同进程重试，之后删除文件并清除任务。
- ready：最多保留 24 小时，之后过期并删除。
- 进程启动：删除修改时间超过 24 小时的孤儿文件。
- 进程运行中：每 15 分钟清扫一次过期或孤儿文件。
- 服务重启：任务内存状态不恢复；未过 24 小时的文件暂时作为孤儿保留，到期后清理，不能通过 API 续跑。

关闭模态框或浏览器不会取消后台任务。

## 10. HTTP API

所有接口返回 application/json，204 除外。通用错误格式：

~~~json
{
  "error": {
    "code": "import_busy",
    "message": "Another database import is already active",
    "details": {}
  }
}
~~~

message 可直接显示给用户，但不能包含主库路径、临时路径、SQL 文本或堆栈。

### 10.1 POST /api/import/inspect

请求：

- multipart/form-data
- 唯一文件字段名：file
- 文件内容上限：2,147,483,648 字节
- 服务端以 2 GiB + 1 MiB 限制整个 multipart 请求，并对 file part 单独以 2 GiB + 1 字节检测越界。
- 通过流式 copy 写文件；禁止 ParseMultipartForm 把大文件加载到内存或默认临时表单目录。

服务端在开始接收时预占单任务槽，state=inspecting、phase=uploading。完整写盘后启动后台预检并返回：

~~~json
{
  "job_id": "a random opaque id",
  "state": "inspecting"
}
~~~

HTTP 状态为 202。扩展名、multipart 或大小错误同步返回 4xx 并清理部分文件。SQLite header、quick_check 和 schema 错误通过异步任务的 failed 状态返回。

### 10.2 GET /api/import/status

可选查询参数 job_id。不传时返回当前内存任务，便于页面刷新恢复：

~~~json
{
  "job": {
    "job_id": "opaque id",
    "state": "ready",
    "phase": "preview",
    "created_at": 1784191563,
    "updated_at": 1784191588,
    "expires_at": 1784277988,
    "retryable": false,
    "file": {
      "name": "history.db",
      "size_bytes": 12345678,
      "sha256": "hex digest"
    },
    "progress": {
      "current_table": "api_requests",
      "processed_rows": 1200,
      "total_rows": 8400,
      "inserted_rows": 900,
      "duplicate_rows": 300,
      "percent": 14.29
    },
    "preview": {
      "source_rows": 8400,
      "new_rows": 6200,
      "duplicate_rows": 2200,
      "tables": [
        {
          "name": "api_requests",
          "source_rows": 1000,
          "new_rows": 700,
          "duplicate_rows": 300
        }
      ],
      "ignored_tables": [
        "daily_model_agg"
      ],
      "warnings": []
    },
    "result": null,
    "error": null
  }
}
~~~

没有当前任务时返回 200 和 {"job":null}。显式 job_id 不存在时返回 404 job_not_found。

预检的 new_rows 是扫描时刻的确定性预览，并计入导入库自身重复。主库仍在线变化，因此真正导入时必须重新判重，最终 result 是权威计数。

importing 时 preview 保留，progress 持续更新。succeeded 的 result 至少包含 scanned_rows、inserted_rows、duplicate_rows、verified_identities、started_at、finished_at 和逐表计数。failed 的 error 包含 code、message、table 和可选 row_number，不包含原始敏感行内容。

### 10.3 POST /api/import/start

请求：

~~~json
{
  "job_id": "opaque id"
}
~~~

只允许：

- ready 任务开始第一次导入。
- retryable=true 且上传文件未过期的 failed 任务重新导入。

成功返回 202 和最新 job 摘要。不存在返回 404；状态不允许返回 409 invalid_job_state。

### 10.4 DELETE /api/import?job_id=...

允许删除 uploading、inspecting、ready 和 failed 任务。成功返回 204。

importing，包括 verifying，返回 409 import_in_progress，不能取消已经开始的合并。succeeded 没有文件，可删除内存中的结果状态并返回 204。

### 10.5 HTTP 与任务错误码

| code | HTTP 或任务 | 含义 |
|---|---|---|
| invalid_multipart | 400 | multipart 格式或字段错误 |
| missing_file | 400 | 缺少 file part |
| invalid_file_extension | 400 | 文件名不是 .db |
| job_not_found | 404 | 指定任务不存在 |
| method_not_allowed | 405 | HTTP 方法不支持 |
| import_busy | 409 | 已有活跃任务 |
| invalid_job_state | 409 | 当前状态不能执行 start |
| import_in_progress | 409 | 导入中不能删除 |
| upload_too_large | 413 | 文件内容超过 2 GiB |
| invalid_sqlite | 任务 failed | header 或只读打开失败 |
| integrity_check_failed | 任务 failed | quick_check 不为 ok |
| unsupported_schema | 任务 failed | 不是受支持的 cc-otel schema |
| inspection_failed | 任务 failed | 预检 I/O 或内部错误 |
| import_failed | 任务 failed | 批次写入失败 |
| verification_failed | 任务 failed | 导入后包含性验证失败 |
| storage_error | 500 | 无法创建或写入上传临时文件 |

## 11. 前端交互

### 11.1 顶部入口与 SVG

在 toolbar-right 中新增独立的数据库导入按钮，与 Pricing、Theme 图标并排。顺序为 Server Status、Database Import、Pricing、Theme。

图标采用已确认的方案 A：

- 20 × 20 viewBox，stroke=currentColor，fill=none。
- 下部是两到三层轻量数据库圆柱轮廓。
- 上部是向下进入数据库的箭头。
- 线宽约 1.6，round linecap 和 round linejoin。
- 不使用 emoji、位图、渐变或实心蓝色。
- title 和 aria-label 为 Import database。

按钮尺寸、hover、focus 和相邻间距复用 pricing-toggle 与 theme-toggle 的现有规则。

### 11.2 单模态框状态递进

同一个模态框显示以下状态：

1. Empty：拖放区和 Choose database。
2. Uploading：文件名、字节进度、Cancel；由 api.js 中 XHR upload progress 提供进度。Cancel 中止 XHR，服务端从 request context 感知断开、删除部分文件并释放任务槽。
3. Inspecting：灰阶 spinner、当前表和扫描计数。
4. Ready：显示新增、重复、总数、逐表明细和警告；主操作 Start merge。
5. Importing：进度条、当前表、Inserted 与 Skipped；关闭按钮仍可用，但没有取消合并按钮。
6. Verifying：沿用进度布局，文案改为 Verifying imported data。
7. Succeeded：结果摘要和 Done。
8. Failed：错误摘要；retryable 时显示 Retry merge，否则显示 Choose another database。

关闭再打开或刷新页面时，先调用 GET /api/import/status 恢复状态。导入后台运行时，顶部图标显示一个不改变布局的小灰色状态点或环，不使用蓝色徽标。

### 11.3 Apple 灰阶视觉

视觉遵循项目现有 macOS Activity Monitor 风格和 Apple 设计参考：

- 使用现有 SF 系统字体栈、surface、surface2、surface3、border、text 和 text-muted token。
- 模态框使用克制圆角、hairline 边框、柔和阴影和充足留白。
- 主按钮采用已确认的 Graphite Fill：background 使用当前主题的 text，文字使用 bg，hover 只做轻微明度变化。
- 次按钮使用 surface3 和 hairline。
- 大面积、主操作和进度条都不使用纯蓝色。蓝色只允许沿用项目现有的小范围键盘 focus 或选中反馈。
- 进度轨道使用 surface3，进度填充使用中性 graphite。
- 成功和错误颜色只用于小图标或短状态文字，不铺满背景。
- 浅色和深色主题均需可读；不写死只适合一种主题的颜色。
- 遵守 prefers-reduced-motion；减少动画时 spinner 改为静态状态图形或极弱过渡。

模态框宽度以 680 px 左右为桌面基准，最大不超过视口减 32 px；移动端变为单列，逐表数据允许横向滚动或折叠，不造成页面横向溢出。

### 11.4 前端模块规则

- 只有 js/api.js 发起网络请求；它可用 XMLHttpRequest 实现上传进度，其余请求继续使用 fetch。
- js/import-db.js 通过 initDatabaseImport 形式注入 openPopover、closePopover 和数据刷新回调。
- app.js 只负责初始化和依赖连接。
- 所有用户文件名通过 textContent 写入，不能拼接为 innerHTML。
- 成功或部分失败后重新加载当前 source 的 dashboard、daily 和当前可见 panel；同时后端发送一次 SSE 通知。

## 12. 安全与资源控制

- 不信任 MIME、文件名、表名、字段名和上传库中的 SQL 对象。
- 所有动态 SQL 标识符只来自编译期 Schema Registry，不能直接拼接上传库提供的名称。
- 只查询白名单中的真实 table，不执行上传库 view、trigger 或 extension。
- 不 ATTACH 上传库到可写主连接，避免跨库触发器或意外写入。
- 上传内容流式落盘；逻辑批次同时受同表最多 10,000 行和约 128 MiB 估算 payload 限制。超过 128 MiB 的单个 raw_json 行独立成批，仍受文件总大小约束；读取器需要明确的单行错误处理，不能使用默认 64 KiB Scanner 限制。
- API 仍沿用 cc-otel 当前部署边界，不在此功能中新增远程认证模型；错误响应不泄露本地路径。
- 任务状态的所有读写受 mutex 保护；后台清理不能删除正在使用的文件。

## 13. 测试策略

### 13.1 internal/dbmerge

- 每张表的当前 schema 导入测试。
- Claude-only、pending 无 request_id、Codex tool_tokens、tool_tokens 与 total_tokens 同时存在、Gemini 残留的兼容测试。
- 未知表、未知字段、不完整 Codex 组、非 SQLite、quick_check 失败的拒绝测试。
- api_requests 有/无 request_id、Codex 请求、普通事件 NULL、raw JSON、pending TTFT 的身份测试。
- 主库冲突时主库整行不变。
- 导入库自身重复、连续导入两次、失败后重试均不产生重复。
- ledger 已存在但实际主行被删除时，行仍会重新插入。
- Claude/Codex 聚合只包含实际新增请求，且与明细同事务回滚或提交。
- cost_usd 原样保留，不调用 pricing 或 recompute。
- 在第 N 批注入失败，验证前 N-1 批保留、当前批明细和聚合同时回滚，重试后完成。
- 导入期间并发正常 Repository 写入，验证 WAL 在线可用和 busy 重试。
- Verifier 对源库重复、repriced 请求和 pending processed 变化使用正确包含性。
- go test -race 覆盖进度回调和任务并发使用。

### 13.2 internal/api

- httptest 覆盖四个路由、方法限制和通用错误 envelope。
- multipart 流式上传、文件字段、扩展名、可配置的小测试上限和部分文件清理。
- 同时上传返回 409。
- inspecting、ready、importing、succeeded、failed 和 retry 的状态转换。
- importing DELETE 返回 409；ready DELETE 取消并清理。
- 成功立即删文件、failed 一小时、ready 24 小时、启动孤儿清理。
- GET 不传 job_id 恢复当前任务，未知显式 id 返回 404。
- shutdown context 终止后台 goroutine，但不把未完成任务伪报为成功。

### 13.3 前端

- Node 测试覆盖状态到视图模型、百分比和字节格式、retry 按钮选择、错误文案。
- DOM 行为验证拖放只接受单个 .db、文件名安全写入、关闭不取消导入、刷新恢复。
- 浅色和深色主题检查，无大面积蓝色。
- 键盘 Tab、Escape、focus、aria-live 和 reduced-motion 检查。

### 13.4 集成与 UI 验证

- go test ./...
- go test -race ./...
- node --test internal/web/static/tests/*.test.mjs
- 使用开发端口 14317/18899 构建并启动独立实例，绝不触碰生产端口或生产数据库。
- 准备包含新增、重复、旧 schema 和 raw OTLP 的测试 .db，通过浏览器完成上传到成功全过程。
- 在导入进行时持续写一条正常 OTLP 数据，确认服务在线且最终两类数据都存在。
- 再次上传同一文件，预览和最终结果均显示零新增。
- 浏览器分别检查浅色、深色、桌面和窄屏，保存至少一张包含导入模态框和日期范围的截图。

## 14. 验收标准

功能只有在以下条件全部满足时才算完成：

1. 正在运行的主库不停止、不替换，WAL 接收写入在导入期间保持可用。
2. 支持的 cc-otel .db 可以从前端上传、预检、确认并完成后台合并。
3. 14 张指定明细/事件表按范围导入；codex_events 被识别但忽略，聚合、价格、配置、ledger 和 Gemini 数据不从上传库导入。
4. 所有身份规则与本规格一致；同一文件连续导入两次不会增加第二份逻辑记录。
5. 冲突时主库数据完全保留，cost_usd 不被自动重算。
6. 每批为同表最多 10,000 个源行或约 128 MiB 估算 payload；明细、聚合增量和 ledger 同事务。
7. 中途失败保留已提交批次，重试安全完成，验证失败不会声称成功。
8. 2 GiB 限制、只读 quick_check、严格 schema、随机 0600 临时文件和清理策略生效。
9. 页面刷新和关闭模态框不影响同进程后台任务；服务重启不自动续跑。
10. 顶部数据库导入 SVG 与 Pricing、Theme 并排，整体是项目现有 Apple 灰阶风格，主操作使用 Graphite Fill，无大面积纯蓝。
11. 现有 merge tool 的数据合并规则由 internal/dbmerge 共享实现，Web 与 CLI 不再各自维护自然键和验证逻辑。
12. Go、race、前端测试和独立开发实例的浏览器验证全部通过，并有截图证据。

## 15. 实施范围说明

该需求横跨共享引擎、HTTP 任务、前端弹窗和既有 CLI 适配，但四部分依赖同一份 schema 与自然键，拆成独立规格会造成临时重复实现。因此保留为一个实施计划，并按以下依赖顺序分阶段落地：

1. Schema Registry、Inspector、Deduper 和测试。
2. Batch Importer、聚合、ledger、Verifier 和故障注入测试。
3. API 任务管理、上传和生命周期测试。
4. 前端 SVG、模态框、状态恢复和前端测试。
5. merge tool 适配共享引擎。
6. 全量验证、开发实例浏览器检查和截图。

每一阶段必须保持可测试边界，不能在后续阶段重新定义前面已经实现的身份规则。
