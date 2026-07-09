# Gemini CLI 遥测数据集成指南

本文档基于对 Gemini CLI 实际发出的 OTLP Payload 数据的分析，说明如何将 Gemini CLI 的 Token 消耗等遥测数据集成到 `cc-otel` 项目中。

## 1. 开启 Gemini CLI 遥测

默认情况下遥测是关闭的。你可以通过配置让 Gemini CLI 将 OTLP 数据发送到 `cc-otel`（假设运行在 `localhost:4317`）。

**全局配置 (~/.gemini/settings.json)**
```json
{
  "telemetry": {
    "enabled": true,
    "useCollector": true,
    "otlpEndpoint": "http://localhost:4317",
    "otlpProtocol": "grpc"
  }
}
```

或者使用环境变量（适合临时测试）：
```powershell
$env:GEMINI_TELEMETRY_ENABLED="true"
$env:GEMINI_TELEMETRY_USE_COLLECTOR="true"
$env:GEMINI_TELEMETRY_OTLP_ENDPOINT="http://localhost:4317"
$env:GEMINI_TELEMETRY_OTLP_PROTOCOL="grpc"
gemini "Hello"
```

## 2. 真实上报数据分析 (Payload)

Gemini CLI 使用标准的 OTLP gRPC 协议。关键的统计数据存放在 `LogsService` 中。以下是从本地抓包获取的真实结构摘录：

### 2.1 资源属性 (Resource Attributes)
用于区分数据来源的标识。
- **`service.name`**: 始终为 `"gemini-cli"`。这是在 `cc-otel` 中路由数据的最佳标识。
- **`session.id`**: 会话的唯一 UUID。

### 2.2 日志属性 (LogRecord Attributes)
Token 相关的指标在 `event.name` 为 `"gemini_cli.api_response"` 的日志记录中产生。

关键字段映射分析：
| 字段含义 | Gemini CLI 实际字段名 | 数据类型 |
| :--- | :--- | :--- |
| **事件名称** | `event.name` = `"gemini_cli.api_response"` | String |
| **模型名称** | `model` | String |
| **请求耗时** | `duration_ms` | Int |
| **输入 Token** | `input_token_count` | Int |
| **输出 Token** | `output_token_count` | Int |
| **缓存 Token** | `cached_content_token_count` | Int |
| **思考 Token** | `thoughts_token_count` | Int |
| **工具 Token** | `tool_token_count` | Int |
| **时间戳** | `event.timestamp` | String (ISO8601) / 也可以用 log 本身的 unixnano |

## 3. cc-otel 代码适配方案

基于上述数据结构，建议在 `cc-otel` 中按以下方式扩展解析逻辑：

### 3.1 增加入口路由 (`internal/receiver/receiver.go`)

在 `logsServiceServer.Export` 中，通过 `service.name` 识别 Gemini 的请求：

```go
func (s *logsServiceServer) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
    shouldNotify := false
    for _, rl := range req.ResourceLogs {
        svc := serviceNameFromResource(rl.Resource)
        
        // 现有 Codex 逻辑
        if isCodexService(svc) {
            dispatchCodexLog(...)
            continue
        }

        // 新增：Gemini CLI 逻辑
        if svc == "gemini-cli" {
            // 需要实现 dispatchGeminiLog
            dispatchGeminiLog(ctx, s.repo, lr, rl.Resource, s.notifier, s.pricer)
            continue
        }
        
        // ... 原有的 Claude 逻辑 ...
```

### 3.2 实现解析器 (`internal/receiver/gemini_parser.go`)

在 `internal/receiver/` 下新建 `gemini_parser.go` 文件，直接提取对应的 Attributes：

```go
package receiver

import (
	"context"
	"time"

	"github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/pricing"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func dispatchGeminiLog(ctx context.Context, repo *db.Repository, lr *logspb.LogRecord, res *resourcepb.Resource, notifier Notifier, pricer Pricer) bool {
	if lr == nil || repo == nil {
		return false
	}
	notify := func() {
		if notifier != nil {
			notifier.NotifySource("gemini")
		}
	}

	attrs := extractAttrs(lr.Attributes)
	if res != nil {
		for _, kv := range res.Attributes {
			if _, exists := attrs[kv.Key]; !exists {
				attrs[kv.Key] = anyValueToString(kv.Value)
			}
		}
	}

	eventName := attrs["event.name"]
	if eventName != "gemini_cli.api_response" {
		return false
	}

	ts := time.Now()
	if nanos := codexLogUnixNanos(lr); nanos > 0 { // 复用已有的时间提取函数
		ts = time.Unix(0, nanos)
	}

	req := &db.APIRequest{
		Timestamp:           ts,
		SessionID:           attrs["session.id"],
		Model:               attrs["model"],
		DurationMs:          parseAttrInt(attrs, "duration_ms"),
		InputTokens:         parseAttrInt(attrs, "input_token_count"),
		OutputTokens:        parseAttrInt(attrs, "output_token_count"),
		CacheReadTokens:     parseAttrInt(attrs, "cached_content_token_count"),
		EventName:           "api_request", // 映射到 db.Event 中统一的 api_request 事件名
		ServiceName:         attrs["service.name"],
		ServiceVersion:      attrs["service.version"],
	}

	// 计算价格逻辑
	if pricer != nil && !pricing.IsClaudeModel(req.Model) {
		req.CostUSD = pricer.Calc(ctx, req.Model,
			req.InputTokens, req.OutputTokens, req.CacheReadTokens, 0)
	}

	if inserted, _ := repo.InsertRequest(ctx, req); inserted {
		notify()
		return true
	}

	return false
}
```