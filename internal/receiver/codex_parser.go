package receiver

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"time"

	"github.com/young1lin/cc-otel/internal/db"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// eventNameFromCodexLog returns the dotted event name (e.g. codex.api_request).
// Falls back to the LogRecord body when event.name attribute is absent.
func eventNameFromCodexLog(lr *logspb.LogRecord, attrs map[string]string) string {
	if v := attrs["event.name"]; v != "" {
		return v
	}
	if lr != nil && lr.Body != nil {
		s := anyValueToString(lr.Body)
		if strings.HasPrefix(s, "codex.") {
			return s
		}
		if strings.HasPrefix(s, "claude_code.") {
			return strings.TrimPrefix(s, "claude_code.")
		}
	}
	return ""
}

// eventKindFromLog extracts event.kind from attributes first, then falls back
// to the LogRecord body (Codex CLI puts event.kind in the body for websocket events).
func eventKindFromLog(lr *logspb.LogRecord, attrs map[string]string) string {
	if v := attrs["event.kind"]; v != "" {
		return v
	}
	if lr != nil && lr.Body != nil {
		s := anyValueToString(lr.Body)
		if strings.HasPrefix(s, "response.") || s == "error" {
			return s
		}
	}
	return ""
}

// spanIDFromLog extracts the span_id from the OTLP LogRecord as a base64 string.
func spanIDFromLog(lr *logspb.LogRecord) string {
	if lr == nil || len(lr.SpanId) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(lr.SpanId)
}

func codexLogUnixNanos(lr *logspb.LogRecord) int64 {
	if lr == nil {
		return 0
	}
	if lr.TimeUnixNano > 0 {
		return int64(lr.TimeUnixNano)
	}
	if lr.ObservedTimeUnixNano > 0 {
		return int64(lr.ObservedTimeUnixNano)
	}
	return 0
}

// codexSpanTracker maps span_id to the observed_time of the corresponding
// websocket_request event. Used to compute precise per-request duration
// when the matching response.created/response.completed websocket events arrive.
type codexSpanTracker struct {
	mu    sync.Mutex
	spans map[string]spanInfo // span_id → info
}

type spanInfo struct {
	sessionID  string
	model      string
	startNanos int64 // observed_time_unix_nano from websocket_request
}

func newCodexSpanTracker() *codexSpanTracker {
	return &codexSpanTracker{spans: make(map[string]spanInfo)}
}

func (t *codexSpanTracker) recordRequest(spanID, sessionID, model string, observedNanos int64) {
	if spanID == "" {
		return
	}
	t.mu.Lock()
	t.spans[spanID] = spanInfo{sessionID: sessionID, model: model, startNanos: observedNanos}
	// Evict entries older than 30 minutes to prevent unbounded growth.
	cutoff := observedNanos - int64(30*time.Minute)
	if cutoff > 0 && len(t.spans) > 100 {
		for k, v := range t.spans {
			if v.startNanos < cutoff {
				delete(t.spans, k)
			}
		}
	}
	t.mu.Unlock()
}

func (t *codexSpanTracker) peekRequest(spanID string) (spanInfo, bool) {
	t.mu.Lock()
	info, ok := t.spans[spanID]
	t.mu.Unlock()
	return info, ok
}

func (t *codexSpanTracker) popRequest(spanID string) (spanInfo, bool) {
	t.mu.Lock()
	info, ok := t.spans[spanID]
	if ok {
		delete(t.spans, spanID)
	}
	t.mu.Unlock()
	return info, ok
}

// dispatchCodexLog handles a single Codex log record. Returns true when the
// record produced a side effect that should trigger SSE notification.
func dispatchCodexLog(ctx context.Context, repo *db.Repository, lr *logspb.LogRecord, res *resourcepb.Resource, notifier Notifier, tracker *codexSpanTracker) bool {
	if lr == nil || repo == nil {
		return false
	}
	notify := func() {
		if notifier != nil {
			notifier.NotifySource("codex")
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

	ts := time.Now()
	if nanos := codexLogUnixNanos(lr); nanos > 0 {
		ts = time.Unix(0, nanos)
	}


	eventName := eventNameFromCodexLog(lr, attrs)
	switch eventName {
	case "codex.api_request":
		if attrs["model"] == "" {
			return false
		}
		req := &db.CodexAPIRequest{
			Timestamp:      ts,
			SessionID:      attrs["conversation.id"],
			ConversationID: attrs["conversation.id"],
			Model:          attrs["model"],
			DurationMs:     parseAttrInt(attrs, "duration_ms"),
			HTTPStatus:     parseAttrInt(attrs, "http.response.status_code"),
			Endpoint:       attrs["endpoint"],
			EventName:      "codex.api_request",
			TerminalType:   attrs["terminal.type"],
			ServiceName:    attrs["service.name"],
			ServiceVersion: attrs["service.version"],
			HostArch:       attrs["host.arch"],
			OSType:         attrs["os.type"],
			OSVersion:      attrs["os.version"],
			ErrorMessage:   attrs["error.message"],
		}
		if _, err := repo.InsertCodexAPIRequest(ctx, req); err == nil {
			notify()
			return true
		}
		return false

	case "codex.sse_event":
		if eventKindFromLog(lr, attrs) == "response.completed" {
			upd := &db.CodexTokenUpdate{
				SessionID:       attrs["conversation.id"],
				Model:           attrs["model"],
				Timestamp:       ts,
				InputTokens:     parseAttrInt(attrs, "input_token_count"),
				OutputTokens:    parseAttrInt(attrs, "output_token_count"),
				CacheReadTokens: parseAttrInt(attrs, "cached_token_count"),
				ReasoningTokens: parseAttrInt(attrs, "reasoning_token_count"),
				TotalTokens:     parseAttrInt(attrs, "tool_token_count"),
			}
			if _, err := repo.UpdateCodexAPIRequestTokens(ctx, upd); err == nil {
				notify()
				return true
			}
			return false
		}
		ev := &db.CodexEvent{
			Timestamp:      ts,
			SessionID:      attrs["conversation.id"],
			ConversationID: attrs["conversation.id"],
			EventName:      "codex.sse_event",
			EventKind:      eventKindFromLog(lr, attrs),
			Model:          attrs["model"],
			DurationMs:     parseAttrInt(attrs, "duration_ms"),
			ErrorMessage:   attrs["error.message"],
		}
		_ = repo.InsertCodexEvent(ctx, ev)
		return false

	case "codex.user_prompt":
		ev := &db.CodexEvent{
			Timestamp:      ts,
			SessionID:      attrs["conversation.id"],
			ConversationID: attrs["conversation.id"],
			EventName:      "codex.user_prompt",
			PromptText:     attrs["prompt"],
			PromptLength:   parseAttrInt(attrs, "prompt_length"),
			TerminalType:   attrs["terminal.type"],
			ServiceName:    attrs["service.name"],
			ServiceVersion: attrs["service_version"],
		}
		if err := repo.InsertCodexUserPrompt(ctx, ev); err == nil {
			notify()
			return true
		}
		return false

	case "codex.tool_decision":
		ev := &db.CodexEvent{
			Timestamp:      ts,
			SessionID:      attrs["conversation.id"],
			ConversationID: attrs["conversation.id"],
			ToolName:       attrs["tool_name"],
			CallID:         attrs["call_id"],
			Decision:       attrs["decision"],
			Source:         attrs["source"],
			TerminalType:   attrs["terminal.type"],
		}
		if err := repo.InsertCodexToolDecision(ctx, ev); err == nil {
			notify()
			return true
		}
		return false

	case "codex.tool_result":
		success := 0
		if attrs["success"] == "true" || attrs["success"] == "1" {
			success = 1
		}
		ev := &db.CodexEvent{
			Timestamp:       ts,
			SessionID:       attrs["conversation.id"],
			ConversationID:  attrs["conversation.id"],
			ToolName:        attrs["tool_name"],
			CallID:          attrs["call_id"],
			DurationMs:      parseAttrInt(attrs, "duration_ms"),
			Success:         success,
			ArgumentsLength: parseAttrInt(attrs, "arguments_length"),
			OutputLength:    parseAttrInt(attrs, "output_length"),
			ToolOrigin:      attrs["tool_origin"],
			MCPServer:       attrs["mcp_server"],
			ErrorMessage:    attrs["error.message"],
			TerminalType:    attrs["terminal.type"],
		}
		if err := repo.InsertCodexToolResult(ctx, ev); err == nil {
			notify()
			return true
		}
		return false

	case "codex.websocket_request":
		if tracker != nil {
			spanID := spanIDFromLog(lr)
			obsNano := codexLogUnixNanos(lr)
			if obsNano == 0 {
				obsNano = ts.UnixNano()
			}
			tracker.recordRequest(spanID, attrs["conversation.id"], attrs["model"], obsNano)
		}
		ev := &db.CodexEvent{
			Timestamp:      ts,
			SessionID:      attrs["conversation.id"],
			ConversationID: attrs["conversation.id"],
			EventName:      "codex.websocket_request",
			EventKind:      eventKindFromLog(lr, attrs),
			Model:          attrs["model"],
		}
		_ = repo.InsertCodexEvent(ctx, ev)
		return false

	case "codex.websocket_event":
		ek := eventKindFromLog(lr, attrs)
		spanID := spanIDFromLog(lr)
		obsNano := codexLogUnixNanos(lr)
		if obsNano == 0 {
			obsNano = ts.UnixNano()
		}

		if tracker != nil && spanID != "" {
			if ek == "response.created" {
				if info, ok := tracker.peekRequest(spanID); ok {
					ttftMs := (obsNano - info.startNanos) / 1e6
					if ttftMs > 0 {
						_ = repo.UpdateCodexRequestTTFT(ctx, info.sessionID, info.model, ts, ttftMs)
					}
				}
			}
			if ek == "response.completed" {
				if info, ok := tracker.popRequest(spanID); ok {
					durationMs := (obsNano - info.startNanos) / 1e6
					if durationMs < 0 {
						durationMs = 0
					}
					if updated, err := repo.UpdateCodexRequestDurationBySession(ctx, info.sessionID, info.model, ts, durationMs); err == nil {
						if updated {
							notify()
							return true
						}
						if durationMs > 0 && info.sessionID != "" && info.model != "" {
							_, err := repo.InsertCodexAPIRequest(ctx, &db.CodexAPIRequest{
								Timestamp:      ts,
								SessionID:      info.sessionID,
								ConversationID: info.sessionID,
								Model:          info.model,
								DurationMs:     durationMs,
								EventName:      "codex.websocket_event:response.completed",
							})
							if err == nil {
								notify()
								return true
							}
						}
					}
				}
			}
		}

		// websocket_event consumed in real-time by tracker above (TTFT + duration).
		// No need to persist to codex_events; result already in codex_api_requests.
		return false

	default:
		if strings.HasPrefix(eventName, "codex.") {
			ev := &db.CodexEvent{
				Timestamp:      ts,
				SessionID:      attrs["conversation.id"],
				ConversationID: attrs["conversation.id"],
				EventName:      eventName,
				Model:          attrs["model"],
				DurationMs:     parseAttrInt(attrs, "duration_ms"),
				ErrorMessage:   attrs["error.message"],
			}
			_ = repo.InsertCodexEvent(ctx, ev)
		}
		return false
	}
}
