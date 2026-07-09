package receiver

import (
	"context"
	"time"

	"github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/pricing"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// dispatchGeminiLog handles a single Gemini CLI log record.
// Only processes gemini_cli.api_response events (token/cost data).
// All other Gemini events are silently ignored.
// Returns true when the record triggered an SSE notification.
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
	if nanos := codexLogUnixNanos(lr); nanos > 0 {
		ts = time.Unix(0, nanos)
	}

	req := &db.GeminiAPIRequest{
		Timestamp:       ts,
		SessionID:       attrs["session.id"],
		Model:           attrs["model"],
		InputTokens:     parseAttrInt(attrs, "input_token_count"),
		OutputTokens:    parseAttrInt(attrs, "output_token_count"),
		CacheReadTokens: parseAttrInt(attrs, "cached_content_token_count"),
		ThoughtsTokens:  parseAttrInt(attrs, "thoughts_token_count"),
		ToolTokens:      parseAttrInt(attrs, "tool_token_count"),
		TotalTokens:     parseAttrInt(attrs, "total_token_count"),
		DurationMs:      parseAttrInt(attrs, "duration_ms"),
		HTTPStatusCode:  parseAttrInt(attrs, "http.status_code"),
		PromptID:        attrs["prompt_id"],
		EventName:       "api_response",
		ServiceName:     attrs["service.name"],
		ServiceVersion:  attrs["service.version"],
	}

	if pricer != nil && !pricing.IsClaudeModel(req.Model) {
		req.CostUSD = pricer.Calc(ctx, req.Model,
			req.InputTokens, req.OutputTokens, req.CacheReadTokens, 0)
	}

	if inserted, _ := repo.InsertGeminiAPIRequest(ctx, req); inserted > 0 {
		notify()
		return true
	}

	return false
}
