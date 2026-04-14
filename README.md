[English Documentation](./README_en-US.md)

[![Claude Plugin](https://img.shields.io/badge/Claude_Code-Plugin-blueviolet)](https://github.com/young1lin/cc-otel)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://golang.org/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/young1lin/cc-otel)](https://github.com/young1lin/cc-otel/releases)
[![Coverage](https://codecov.io/gh/young1lin/cc-otel/branch/main/graph/badge.svg)](https://codecov.io/gh/young1lin/cc-otel)
[![Go Report Card](https://goreportcard.com/badge/github.com/young1lin/cc-otel)](https://goreportcard.com/report/github.com/young1lin/cc-otel)
[![Test](https://github.com/young1lin/cc-otel/actions/workflows/test.yml/badge.svg)](https://github.com/young1lin/cc-otel/actions/workflows/test.yml)
[![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-blue)](https://github.com/young1lin/cc-otel/releases)
[![Downloads](https://img.shields.io/github/downloads/young1lin/cc-otel/total)](https://github.com/young1lin/cc-otel/releases)

# CC-OTEL

Claude Code Token 用量监控服务。接收 OTEL 遥测数据，提供 Web 仪表盘查看 token 消耗和费用。

<!-- TODO: 截图占位 -->
<!-- ![Dashboard 截图](./images/dashboard.png) -->

## 为什么需要

Claude Code 内置了 OpenTelemetry 支持，但查看数据需要搭建 Grafana、Prometheus 或使用第三方 SaaS。**CC-OTEL** 是一个单二进制文件，接收 OTLP 遥测数据，存储到 SQLite，提供 Web 仪表盘 -- 无外部依赖，零配置即可运行。

## 架构

```
Claude Code ──OTLP gRPC(:4317)──> cc-otel ──> SQLite
                                      |
                                  Web UI <── Browser (localhost:8899)
```

## 功能

- **OTLP gRPC 接收器** -- 接收 Claude Code 的指标和日志事件
- **Web 仪表盘** -- Token 用量、费用明细、缓存命中率、按模型统计
- **KPI 分项** -- 点击任意 KPI 卡片查看模型级别明细
- **实时更新** -- SSE 推送，新数据到达时自动刷新
- **深色/浅色主题** -- 自动跟随系统偏好
- **日期范围** -- Today、7 Days、30 Days、All Time，或自定义日期
- **图表切换** -- Tokens、Cost、Requests 视图
- **会话追踪** -- 按会话聚合费用和 Token
- **预聚合表** -- 查询延迟 < 3ms，百万行无压力
- **单二进制** -- `go:embed` 打包 Web UI，零运行时依赖
- **跨平台** -- Windows、macOS、Linux

## 安装

### Claude Code 插件（推荐）

```bash
/plugin marketplace add young1lin/claude-token-monitor
/plugin install cc-otel@claude-token-monitor
/cc-otel:setup
```

### 可用命令

| 命令 | 说明 |
|------|------|
| `/cc-otel:setup` | 下载二进制、配置 OTEL 环境变量、启动服务 |
| `/cc-otel:start` | 启动后台守护进程 |
| `/cc-otel:stop` | 停止守护进程 |
| `/cc-otel:status` | 查看服务状态 + 今日费用摘要 |
| `/cc-otel:open` | 在浏览器中打开 Web 仪表盘 |
| `/cc-otel:report [today\|7d\|30d\|all]` | 生成费用报告 |

### 从源码编译

```bash
# Linux / macOS
go build -o cc-otel ./cmd/cc-otel/

# Windows
go build -o cc-otel.exe ./cmd/cc-otel/
```

### 从 Release 下载

前往 [Releases](https://github.com/young1lin/cc-otel/releases) 下载对应平台的二进制文件。

### 安装到 ~/.claude/cc-otel/

```bash
cc-otel install    # 复制二进制到 ~/.claude/cc-otel/（全平台通用）
cc-otel init       # 生成默认配置文件
```

## 运行

```bash
cc-otel start      # 后台启动
cc-otel status     # 查看状态（版本、PID、端口、今日统计）
cc-otel stop       # 停止
cc-otel serve      # 前台运行（调试用）
cc-otel -v         # 输出版本号
cc-otel cleanup    # 按 retention_days 清理旧数据
```

打开仪表盘: **http://localhost:8899/**

## 原理

### 什么是 OpenTelemetry？

[OpenTelemetry](https://opentelemetry.io/)（OTEL）是 CNCF 的可观测性标准，统一了三类遥测信号：

- **Metrics** -- 时序指标（token 数、费用、请求数等）
- **Logs / Events** -- 结构化事件（每次 API 请求、用户 prompt、工具调用结果）
- **Traces** -- 分布式追踪（Claude Code 0.2.x+ 的 beta 功能）

Claude Code 内置 OTEL SDK，通过 **OTLP**（OpenTelemetry Protocol，标准传输协议）把上述信号导出到任意兼容的后端。CC-OTEL 就是一个专门针对 Claude Code 定制的轻量 OTLP 后端。

### 数据流

```
┌─────────────────┐    OTLP/gRPC     ┌──────────────────────┐    ┌─────────────┐
│  Claude Code    │ ───────────────▶ │  cc-otel (:4317)     │───▶│  SQLite     │
│  (OTEL SDK)     │    :4317         │  · LogsService       │    │  · raw      │
│                 │                  │  · MetricsService    │    │  · requests │
│  metrics+logs   │                  │                      │    │  · daily    │
└─────────────────┘                  └──────────┬───────────┘    └──────┬──────┘
                                                │ Notify()              │
                                                ▼                       │
                                     ┌──────────────────────┐           │
                                     │  Web UI (:8899)      │◀──────────┘
                                     │  · REST API          │   query
                                     │  · SSE /api/events   │───┐
                                     └──────────────────────┘   │ push
                                                                ▼
                                                         ┌────────────┐
                                                         │  Browser   │
                                                         └────────────┘
```

### 三个阶段

**1. 接收（`internal/receiver/`）**

内嵌一个 gRPC 服务器，实现 OTLP 官方定义的 `LogsService` 和 `MetricsService`：

- 每个 `claude_code.api_request` 日志事件携带一次 API 调用的完整信息（model、tokens、cost、duration、session.id、user.id 等 resource + record attributes），被落库到 `api_requests` 表。
- `claude_code.token.usage`、`claude_code.cost.usage` 等 Metrics 也会入库，用于校验和补算。
- 所有 log record 的原始 protobuf → JSON 同时写入 `raw_events` 表，作为事后回放和排查字段缺失的依据。

**2. 存储（`internal/db/`）**

SQLite（WAL 模式 + `busy_timeout`）单文件数据库：

- `api_requests` -- 每次 API 调用一行，是最小粒度的原始事实表
- `events_daily` -- 按（日期 × 模型 × session）预聚合，Web UI 的图表和 Daily Detail 都查这张表，查询延迟 < 3 ms
- `raw_events` -- 原始事件回溯表，默认 90 天自动清理（`retention_days`）

Token 统计严格按照 Anthropic 的 [Prompt caching 官方口径](https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching)：输入侧总和 = `input_tokens` + `cache_read_tokens` + `cache_creation_tokens`，三者在 UI 上以「Uncached / Cache Read / Cache Create」三列分项展示。

**3. 展示（`internal/api/` + `internal/web/`）**

- REST API：`/api/dashboard`、`/api/daily`、`/api/sessions`、`/api/status` 等
- SSE：`/api/events` — 接收器每次成功入库新数据后调用 `Broker.Notify()`，通过 Server-Sent Events 推送到浏览器，前端自动刷新图表
- 静态资源：默认通过 `go:embed` 打进二进制；本地开发可用 `CC_OTEL_STATIC_DIR` 从磁盘读取，免重编译

### 为什么用 gRPC 而不是 HTTP？

Claude Code 支持 `grpc` / `http/json` / `http/protobuf` 三种 OTLP 传输。CC-OTEL 只实现 gRPC，因为：

- Claude Code 自身对 gRPC 路径做了最多的优化和测试
- protobuf 编码比 JSON 小 ~40%，在高频写 / 长 session 场景下更省
- 连接复用（HTTP/2 多路复用）减少导出延迟

如果你的网络环境只能走 HTTP，可在 Claude Code 和 cc-otel 之间架一层 `otel-collector` 做协议转换。

## 配置 Claude Code

Claude Code 需要通过 gRPC 导出 OTLP 数据到 CC-OTEL。在 `~/.claude/settings.json` 的 `"env"` 中添加以下环境变量：

```json
{
  "env": {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
    "OTEL_METRICS_EXPORTER": "otlp",
    "OTEL_LOGS_EXPORTER": "otlp"
  }
}
```

> **注意**: 只添加/更新以上 OTEL 相关的 key，不要覆盖已有配置。端口号应与 `cc-otel.yaml` 中的 `otel_port` 一致。

## 配置文件

CC-OTEL 按以下顺序查找配置和数据：

1. **`./bin/`** -- 可执行文件在 `bin/` 目录下时使用（开发模式）
2. **`~/.claude/`** -- 已有 `cc-otel.yaml` 或 `cc-otel.db` 时使用（旧版兼容）
3. **`~/.claude/cc-otel/`** -- 新安装的默认位置（全平台通用）

所有文件（二进制、配置、数据库、PID、日志）在同一目录：

```
~/.claude/cc-otel/
├── cc-otel(.exe)    # 可执行文件
├── cc-otel.yaml     # 配置
├── cc-otel.db       # SQLite 数据库
├── cc-otel.pid      # PID 文件
└── cc-otel.log      # 日志
```

环境变量覆盖（最高优先级）：

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `CC_OTEL_OTEL_PORT` | OTLP gRPC 接收端口 | `4317` |
| `CC_OTEL_WEB_PORT` | Web UI 端口 | `8899` |
| `CC_OTEL_DB_PATH` | SQLite 数据库路径 | `~/.claude/cc-otel/cc-otel.db` |

### 数据保留

默认 90 天自动清理旧的原始事件数据。在 `cc-otel.yaml` 中配置：

```yaml
retention_days: 90   # 0 = 永不清理
```

或手动执行: `cc-otel cleanup`

## Web UI

<!-- TODO: Web UI 截图占位 -->
<!-- ![Web UI 截图](./images/web-ui.png) -->

### 状态指示器

右上角绿色点 + `live` 表示 SSE 推送连接正常。点击打开 **Server Status** 面板，查看数据库健康状态、OTLP 接收器状态和端点信息。

### KPI 分项

点击任意 KPI 卡片（Cost、Input、Output、Cache Hit、Requests）查看按模型的分项数据。

## 更新

### 更新插件（命令和技能）

```bash
/plugin update cc-otel@claude-token-monitor
```

### 更新二进制

`/cc-otel:setup` 会检查已安装版本并自动更新到最新。

或手动：

```bash
# 查看当前版本
~/.claude/cc-otel/cc-otel -v

# 强制重新安装
/cc-otel:setup --force
```

## 开发

```bash
make build       # 编译（注入版本号）
make test        # 运行所有测试
make coverage    # 生成覆盖率报告
make vet         # go vet 检查
```

前端开发免重编译：

```bash
CC_OTEL_STATIC_DIR=internal/web/static cc-otel serve
```

## License

[MIT](LICENSE)
