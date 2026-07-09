## 更新日志（CHANGELOG）

[English version](./CHANGELOG_EN.md)

本项目采用轻量更新日志格式（参考 Keep a Changelog），适合快速迭代的小工具。

---

## [Unreleased]（未发布）

> 说明：本项目当前尚未发布正式版本；本节描述“主分支已具备”的功能与特性。

### 功能 / 特性

- **OTLP gRPC 接收器**：通过 OTLP/gRPC 接收 Claude Code 遥测数据（logs / metrics / traces）。
- **TTFT（首 Token 时间 / Time To First Token）**：
  - 从 OTLP trace spans 中提取 `ttft_ms`。
  - 回填到 `api_requests.ttft_ms`，让 TTFT 可查询、可展示。
  - 增加 pending 队列，解决“trace 先到、`api_request` log 后到”导致的漏回填问题。
- **Request Log 增强**：
  - 按模型汇总表：**Avg Duration**、**Out tok/s**、**Avg TTFT**（有数据才显示）、**Min**、**Max**。
  - 汇总表支持点击排序。
  - 单条请求列表新增 **TTFT** 列。
  - Tooltip：TTFT 表头提示 + TTFT 单元格悬浮详情。
- **耗时统计**：
  - 新增 `/api/durations`，提供按模型的耗时/吞吐统计。
  - 输出吞吐：**Out tok/s**（由 output_tokens 与 duration 推导）。
- **实时刷新**：通过 SSE（`/api/events`）在新数据到达时自动刷新页面。
- **原始 OTLP 备份**：保存原始 OTLP 事件快照（`raw_otlp_events`），便于排查字段漂移与数据溯源。
- **本地存储**：SQLite 存储请求明细与聚合数据（Web UI 查询用）。
- **主题与日期范围**：深色/浅色主题、Today/7 Days/30 Days/All Time/自定义日期范围。

### 备注

- TTFT 依赖 Claude Code 开启 trace 导出（例如 `OTEL_TRACES_EXPORTER=otlp`），并确保 tracing 功能开关开启（Enhanced Telemetry Beta 相关开关/覆盖）。

---

## [0.1.0] - TBD

> 预告：首个公开发布版本将把上述 “Unreleased” 内容固化为 `0.1.0`，并补充安装/升级说明与发布包。

