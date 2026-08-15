package receiver

import (
	"encoding/hex"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Clients built on the Vercel AI SDK (opencode being the one this was written
// against) report token usage on GenAI *spans* rather than on log records the
// way Claude Code does. This file reshapes such a span into the same
// api_request log record Claude Code would have sent, so it flows through the
// one ingestion path in logsServiceServer.Export — same cost recompute, same
// service_name attribution, same dedup, same SSE notify.

// aiSDKFallbackService is the client attributed to an AI-SDK span whose resource
// carries no service.name. Anything that emits these spans is not Claude Code,
// and opencode is the known emitter, so guessing "claude-code" (the global
// default) would be actively wrong.
const aiSDKFallbackService = "opencode"

// aiSDKDebug turns on the per-span usage log (CC_OTEL_DEBUG_AISDK=1). Off by
// default — one line per request stops being informative once the mapping is
// trusted, and these clients are chatty.
var aiSDKDebug = os.Getenv("CC_OTEL_DEBUG_AISDK") == "1"

// aiSDKClampWarned keeps the cache-exclusive warning to one line per model.
var aiSDKClampWarned sync.Map

// isAISDKUsageSpan reports whether a span is the leaf call that carries token
// usage. The AI SDK emits usage on BOTH the parent (ai.streamText) and the leaf
// (ai.streamText.doStream); mapping both would double-count every request.
func isAISDKUsageSpan(name string) bool {
	switch name {
	case "ai.streamText.doStream", "ai.generateText.doGenerate":
		return true
	}
	return false
}

// mapAISDKSpan converts one AI-SDK usage span into an api_request log record.
// Returns nil when the span carries no usable token usage.
func mapAISDKSpan(sp *tracepb.Span, attrs, resAttrs map[string]string) *logspb.LogRecord {
	model := pickAttr(attrs, resAttrs,
		"ai.response.model", "ai.model.id", "gen_ai.request.model", "model")
	if model == "" {
		return nil
	}

	// ASSUMPTION: the AI SDK reports inputTokens INCLUSIVE of cached tokens
	// (OpenAI-compatible providers do), so uncached input is the remainder.
	// It holds for DeepSeek and friends but has NOT been confirmed for every
	// provider — Anthropic reports input_tokens exclusive of cache, and if the
	// SDK passes that through untouched the subtraction below under-counts.
	// The log line further down reports when that happens; see its comment.
	inputTotal := pickAttrInt(attrs, resAttrs, "ai.usage.inputTokens", "gen_ai.usage.input_tokens")
	cacheRead := pickAttrInt(attrs, resAttrs,
		"ai.usage.inputTokenDetails.cacheReadTokens",
		"ai.usage.cachedInputTokens",
		"gen_ai.usage.cache_read_input_tokens")
	cacheWrite := pickAttrInt(attrs, resAttrs,
		"ai.usage.inputTokenDetails.cacheWriteTokens",
		"ai.usage.inputTokenDetails.cacheCreationTokens",
		"ai.usage.cacheWriteInputTokens",
		"gen_ai.usage.cache_creation_input_tokens")
	output := pickAttrInt(attrs, resAttrs, "ai.usage.outputTokens", "gen_ai.usage.output_tokens")

	if inputTotal == 0 && output == 0 && cacheRead == 0 && cacheWrite == 0 {
		return nil // not a usage-bearing span after all
	}

	uncached := inputTotal - cacheRead - cacheWrite
	clamped := false
	if uncached < 0 {
		uncached = inputTotal
		clamped = true
	}

	// The clamp firing means this provider reports input EXCLUSIVE of cache, so
	// the subtraction above under-counts uncached input for it. That is a data
	// correctness problem, so it is reported even with debug logging off — but
	// only once per model, since it would otherwise repeat on every request.
	if clamped {
		if _, seen := aiSDKClampWarned.LoadOrStore(model, true); !seen {
			log.Printf("ai-sdk usage: model=%s reports cache-EXCLUSIVE input (inputTokens=%d < cacheRead=%d + cacheWrite=%d); "+
				"uncached input is being under-counted for this model. Set CC_OTEL_DEBUG_AISDK=1 for per-request detail.",
				model, inputTotal, cacheRead, cacheWrite)
		}
	}
	// Per-request detail is opt-in: useful when onboarding a new client or
	// confirming the inclusive/exclusive question above, pure noise afterwards.
	if aiSDKDebug {
		log.Printf("ai-sdk usage span: model=%s inputTokens=%d cacheRead=%d cacheWrite=%d outputTokens=%d => uncached=%d clamped=%t",
			model, inputTotal, cacheRead, cacheWrite, output, uncached, clamped)
	}

	// Prefer the model-measured duration; fall back to span wall clock.
	durationMs := int64(pickAttrFloat(attrs, resAttrs, "ai.response.msToFinish"))
	if durationMs == 0 && sp.EndTimeUnixNano > sp.StartTimeUnixNano {
		durationMs = int64((sp.EndTimeUnixNano - sp.StartTimeUnixNano) / 1e6)
	}
	ttftMs := int64(pickAttrFloat(attrs, resAttrs, "ai.response.msToFirstChunk"))

	ts := sp.EndTimeUnixNano
	if ts == 0 {
		ts = sp.StartTimeUnixNano
	}

	kvs := []*commontpb.KeyValue{
		kvStr("event.name", "api_request"),
		kvStr("model", model),
		kvInt("input_tokens", uncached),
		kvInt("output_tokens", output),
		kvInt("cache_read_tokens", cacheRead),
		kvInt("cache_creation_tokens", cacheWrite),
		kvInt("duration_ms", durationMs),
		kvInt("ttft_ms", ttftMs),
		// Span IDs are unique per span, so this makes ingestion idempotent via
		// the partial unique index on api_requests(request_id). Without it an
		// exporter retry would double-count tokens and cost.
		kvStr("request_id", hex.EncodeToString(sp.SpanId)),
	}
	// cost_usd is deliberately absent: these clients never report one. The
	// receiver recomputes it from the price table (see IsUpstreamAuthoritative).

	if v := pickAttr(attrs, resAttrs,
		"ai.telemetry.metadata.sessionId", "ai.request.headers.X-Session-Id",
		"session.id", "sessionId"); v != "" {
		kvs = append(kvs, kvStr("session.id", v))
	}
	if v := pickAttr(attrs, resAttrs, "ai.telemetry.metadata.userId", "user.id", "userId"); v != "" {
		kvs = append(kvs, kvStr("user.id", v))
	}
	if v := pickAttr(attrs, resAttrs, "prompt.id", "promptId"); v != "" {
		kvs = append(kvs, kvStr("prompt.id", v))
	}

	return &logspb.LogRecord{
		TimeUnixNano: ts,
		SeverityText: "INFO",
		Body:         &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "api_request"}},
		Attributes:   kvs,
	}
}

// DebugMapAISDKSpan reports how a span WOULD be ingested, without storing
// anything. Exported solely for tools/otlp_dump, so the debugging view is
// produced by the same mapping as real ingestion rather than a copy that drifts
// away from it. Returns ok=false for spans that would not become api_requests.
func DebugMapAISDKSpan(sp *tracepb.Span, res *resourcepb.Resource) (map[string]string, bool) {
	if sp == nil || !isAISDKUsageSpan(sp.Name) {
		return nil, false
	}
	resAttrs := map[string]string{}
	if res != nil {
		resAttrs = extractAttrs(res.Attributes)
	}
	rec := mapAISDKSpan(sp, extractAttrs(sp.Attributes), resAttrs)
	if rec == nil {
		return nil, false
	}
	out := make(map[string]string, len(rec.Attributes)+1)
	for _, kv := range rec.Attributes {
		out[kv.Key] = anyValueToString(kv.Value)
	}
	// Attribution is decided by the resource, not the record, so surface it too.
	out["service.name"] = serviceNameFromResource(aiSDKResource(res))
	return out, true
}

// aiSDKResource reuses the span's own resource so its service.name attribution
// survives, substituting a sensible default when it carries none.
func aiSDKResource(res *resourcepb.Resource) *resourcepb.Resource {
	if res != nil && serviceNameFromResource(res) != "" {
		return res
	}
	attrs := []*commontpb.KeyValue{kvStr("service.name", aiSDKFallbackService)}
	if res != nil {
		attrs = append(attrs, res.Attributes...)
	}
	return &resourcepb.Resource{Attributes: attrs}
}

// pickAttrInt / pickAttrFloat are the numeric companions of pickAttr: they walk
// the key list and return the first value that parses, skipping keys that are
// absent, empty, or zero. Stopping at the first *present* key would let an
// explicit 0 mask a later key that has the real number.
func pickAttrInt(attrs, resAttrs map[string]string, keys ...string) int64 {
	for _, k := range keys {
		for _, m := range []map[string]string{attrs, resAttrs} {
			raw := strings.TrimSpace(m[k])
			if raw == "" {
				continue
			}
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v != 0 {
				return v
			}
			// Some exporters serialize integer counts as floats ("1234.0").
			if f, err := strconv.ParseFloat(raw, 64); err == nil && int64(f) != 0 {
				return int64(f)
			}
		}
	}
	return 0
}

func pickAttrFloat(attrs, resAttrs map[string]string, keys ...string) float64 {
	for _, k := range keys {
		for _, m := range []map[string]string{attrs, resAttrs} {
			raw := strings.TrimSpace(m[k])
			if raw == "" {
				continue
			}
			if v, err := strconv.ParseFloat(raw, 64); err == nil && v != 0 {
				return v
			}
		}
	}
	return 0
}

func kvStr(k, v string) *commontpb.KeyValue {
	return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{
		Value: &commontpb.AnyValue_StringValue{StringValue: v},
	}}
}

func kvInt(k string, v int64) *commontpb.KeyValue {
	return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{
		Value: &commontpb.AnyValue_IntValue{IntValue: v},
	}}
}
