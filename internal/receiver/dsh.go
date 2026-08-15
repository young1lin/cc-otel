package receiver

// DeepSeek Harness (DSH) session telemetry → Claude-Code-shaped api_requests.
//
// DSH ships session telemetry through the OTel JS SDK's OTLP/HTTP log exporter
// (@deepseek-ai/dsh-session-telemetry-otel, mode: FULL). The wire contract below
// is read off that package's source, NOT its README — the README documents a
// scope name ("dsh-session-sessionTelemetry-otel") that the code does not emit.
//
//	scope      "@deepseek-ai/dsh-session-telemetry-otel"      ledger records
//	           "@deepseek-ai/dsh-session-telemetry-otel/ops"  operational records
//	resource   service.name = "deepseek-harness", service.version, user.id
//	attributes session.id (string), event.type (string), event.seq (int),
//	           optional session.cwd / session.parent_id / session.seed_length
//	body       a deep copy of the session event's `data`, as a structured OTLP
//	           value (KvlistValue) — the event envelope is NOT in the body, so
//	           event.type is only ever readable from the attributes.
//
// Only "assistant/message" carries token accounting:
//
//	{ turn, step,
//	  message: { source: { kind: "model", provider, model } },
//	  usage?:  { inputTokens, outputTokens,
//	             cacheReadTokens?, cacheWriteTokens?, reasoningTokens? } }
//
// DSH's usage fields are disjoint the way Anthropic's are — inputTokens counts
// uncached input only — so they map 1:1 onto api_requests' input / cache_read /
// cache_creation columns with no re-derivation.
//
// Timing is not in the body. Claude Code reports duration_ms and ttft_ms per
// api_request, so this file reconstructs both from the surrounding lifecycle
// events the way DSH's own session-stats projection does: step/start opens the
// step, the first assistant/chunk marks first token, assistant/message closes it.

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/pricing"
	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

const (
	// dshScopeName is the ledger instrumentation scope; the operational channel
	// appends "/ops" to it, so a prefix test covers both.
	dshScopeName = "@deepseek-ai/dsh-session-telemetry-otel"
	// dshServiceName is APP_IDENTITY.product, the Resource service.name DSH sets.
	dshServiceName = "deepseek-harness"
	// dshStepTTL bounds how long an unclosed step keeps its timing entry.
	dshStepTTL = 30 * time.Minute
)

// isDSHScope reports whether an instrumentation scope belongs to DSH's
// session-telemetry backend (ledger or ops channel).
func isDSHScope(name string) bool {
	return strings.HasPrefix(name, dshScopeName)
}

// resourceLogsIsDSH reports whether one ResourceLogs came from DSH. The scope
// name is the precise signal; service.name is accepted as a fallback so a
// deployment that wraps the seam in its own backend still routes here.
//
// The test is per ResourceLogs, not per export request: a collector in front of
// cc-otel batches several clients into one request, and dispatching the whole
// request here would silently drop every non-DSH record in it.
func resourceLogsIsDSH(rl *logspb.ResourceLogs) bool {
	if serviceNameFromResource(rl.Resource) == dshServiceName {
		return true
	}
	for _, sl := range rl.ScopeLogs {
		if isDSHScope(sl.GetScope().GetName()) {
			return true
		}
	}
	return false
}

// dshStepKey identifies one model call within a session.
type dshStepKey struct {
	sessionID string
	turn      int64
	step      int64
}

// dshStepTiming holds the boundaries observed for an open step.
type dshStepTiming struct {
	startNanos      int64
	firstTokenNanos int64
	seen            time.Time
}

// dshStepTracker reconstructs per-request timing from DSH's step lifecycle
// events. Entries are deleted when their step closes; the TTL sweep is the
// backstop for sessions that crash mid-step.
type dshStepTracker struct {
	mu    sync.Mutex
	steps map[dshStepKey]*dshStepTiming
}

func newDSHStepTracker() *dshStepTracker {
	return &dshStepTracker{steps: make(map[dshStepKey]*dshStepTiming)}
}

// noteStart opens (or reopens) a step at the given record time.
func (t *dshStepTracker) noteStart(key dshStepKey, nanos int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.evictLocked()
	t.steps[key] = &dshStepTiming{startNanos: nanos, seen: time.Now()}
}

// noteFirstToken records the first streamed chunk of a step; later chunks of the
// same step are ignored, matching DSH's own first-token definition.
func (t *dshStepTracker) noteFirstToken(key dshStepKey, nanos int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.steps[key]
	if !ok || entry.firstTokenNanos != 0 {
		return
	}
	entry.firstTokenNanos = nanos
	entry.seen = time.Now()
}

// take removes a step's timing and returns it.
func (t *dshStepTracker) take(key dshStepKey) (dshStepTiming, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.steps[key]
	if !ok {
		return dshStepTiming{}, false
	}
	delete(t.steps, key)
	return *entry, true
}

// evictLocked drops entries whose step never closed. Callers hold t.mu.
func (t *dshStepTracker) evictLocked() {
	cutoff := time.Now().Add(-dshStepTTL)
	for key, entry := range t.steps {
		if entry.seen.Before(cutoff) {
			delete(t.steps, key)
		}
	}
}

// dshUsage is DSH's TokenUsage. Absent optional fields decode to zero, which is
// the correct count for a provider that reports no cache activity.
type dshUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"`
}

// dshAssistantMessage is the subset of an "assistant/message" body this receiver
// reads. The real body also carries the assembled message content; it is left
// undecoded on purpose — cc-otel stores token facts, not transcripts.
type dshAssistantMessage struct {
	Turn    int64 `json:"turn"`
	Step    int64 `json:"step"`
	Message struct {
		Source struct {
			Kind     string `json:"kind"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"source"`
	} `json:"message"`
	Usage *dshUsage `json:"usage"`
}

// dshStepRef is the (turn, step) pair every lifecycle event body carries.
type dshStepRef struct {
	Turn int64 `json:"turn"`
	Step int64 `json:"step"`
}

// dshBodyScalar renders one scalar OTLP AnyValue as the Go value json.Marshal
// writes identically (a Double holding an integral count marshals as "1200",
// so double-encoded integers still unmarshal into int64 fields).
func dshBodyScalar(v *commontpb.AnyValue) any {
	switch val := v.GetValue().(type) {
	case *commontpb.AnyValue_StringValue:
		return val.StringValue
	case *commontpb.AnyValue_IntValue:
		return val.IntValue
	case *commontpb.AnyValue_DoubleValue:
		return val.DoubleValue
	case *commontpb.AnyValue_BoolValue:
		return val.BoolValue
	default:
		return nil
	}
}

// dshScalars converts a KvlistValue's scalar entries to a map, skipping
// non-scalar children.
func dshScalars(kvs []*commontpb.KeyValue) map[string]any {
	out := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		if v := dshBodyScalar(kv.Value); v != nil {
			out[kv.Key] = v
		}
	}
	return out
}

// decodeDSHBody decodes the fields this file reads out of a log record body.
// Structured bodies (the SDK's own encoding) are walked key by key and ONLY
// the keys the reader structs use are converted: a FULL-mode body also carries
// the assembled message text, tool output and whole file contents — often
// megabytes — and converting those just to discard them would dominate the
// cost of ingestion. A body that arrived as a JSON string (a proxy that
// stringifies bodies) is decoded directly.
func decodeDSHBody(body *commontpb.AnyValue, out any) bool {
	if s, ok := body.GetValue().(*commontpb.AnyValue_StringValue); ok {
		return json.Unmarshal([]byte(s.StringValue), out) == nil
	}
	top := body.GetKvlistValue().GetValues()
	if top == nil {
		return false
	}
	converted := make(map[string]any, 4)
	for _, kv := range top {
		switch kv.Key {
		case "turn", "step":
			if v := dshBodyScalar(kv.Value); v != nil {
				converted[kv.Key] = v
			}
		case "usage":
			if kvs := kv.Value.GetKvlistValue().GetValues(); kvs != nil {
				converted["usage"] = dshScalars(kvs)
			}
		case "message":
			for _, mkv := range kv.Value.GetKvlistValue().GetValues() {
				if mkv.Key != "source" {
					continue
				}
				if skvs := mkv.Value.GetKvlistValue().GetValues(); skvs != nil {
					converted["message"] = map[string]any{"source": dshScalars(skvs)}
				}
			}
		}
	}
	raw, err := json.Marshal(converted)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

// dshRecordTime is the record's own timestamp, falling back to the observed
// timestamp and finally to arrival time.
func dshRecordTime(lr *logspb.LogRecord) int64 {
	if lr.TimeUnixNano > 0 {
		return int64(lr.TimeUnixNano)
	}
	if lr.ObservedTimeUnixNano > 0 {
		return int64(lr.ObservedTimeUnixNano)
	}
	return time.Now().UnixNano()
}

// dshAttrs merges record attributes with the Resource attributes they do not
// already define, matching what the Claude path reads per record.
func dshAttrs(lr *logspb.LogRecord, resource *resourcepb.Resource) map[string]string {
	attrs := extractAttrs(lr.Attributes)
	for _, kv := range resource.GetAttributes() {
		if _, exists := attrs[kv.Key]; !exists {
			attrs[kv.Key] = anyValueToString(kv.Value)
		}
	}
	return attrs
}

// dshAPIRequest maps one "assistant/message" record onto an api_requests row.
// Returns nil when the record reports no usage — DSH omits `usage` when the
// adapter reported none, and a row with no tokens has nothing to account.
func dshAPIRequest(lr *logspb.LogRecord, attrs map[string]string, timing dshStepTiming, body *dshAssistantMessage) *db.Event {
	if body.Usage == nil {
		return nil
	}
	nanos := dshRecordTime(lr)
	seqRaw := attrs["event.seq"]
	seq, seqErr := strconv.ParseInt(seqRaw, 10, 64)
	sessionID := attrs["session.id"]
	requestTail := strconv.FormatInt(nanos, 10)
	if seqErr == nil {
		requestTail = strconv.FormatInt(seq, 10)
	}
	requestID := "dsh-" + sessionID + "-" + requestTail

	event := &db.Event{
		Timestamp:           time.Unix(0, nanos),
		SessionID:           sessionID,
		UserID:              attrs["user.id"],
		EventName:           "api_request",
		EventSequence:       seq,
		Model:               body.Message.Source.Model,
		InputTokens:         body.Usage.InputTokens,
		OutputTokens:        body.Usage.OutputTokens,
		CacheReadTokens:     body.Usage.CacheReadTokens,
		CacheCreationTokens: body.Usage.CacheWriteTokens,
		// Cost stays zero here: DSH reports none, and the caller's pricing
		// recompute owns cost_usd for every non-Claude-Code service.
		CostUSD:        0,
		ServiceName:    db.NormalizeServiceName(attrs["service.name"]),
		ServiceVersion: attrs["service.version"],
		// The ledger's own identity, so exporter retries collapse onto the
		// partial unique index on api_requests(request_id). event.seq is pinned
		// by the wire contract; if a nonconforming exporter ever omitted it, the
		// record time keeps replays idempotent (a retry resends the same
		// timestamp) instead of collapsing every row in the session onto
		// "...-0", where the index would silently drop all but the first.
		RequestID: requestID,
	}
	if timing.startNanos > 0 && nanos > timing.startNanos {
		event.DurationMs = (nanos - timing.startNanos) / int64(time.Millisecond)
	}
	if timing.startNanos > 0 && timing.firstTokenNanos > timing.startNanos {
		event.TTFTMs = (timing.firstTokenNanos - timing.startNanos) / int64(time.Millisecond)
	}
	return event
}

// handleDSHResourceLogs ingests one DSH ResourceLogs. It returns whether any
// row was written, which drives the SSE refresh.
//
// Lifecycle events (step/start, assistant/chunk, step/end) are consumed for
// timing only. Every other DSH event — user messages, tool calls, tool results,
// compaction summaries — is deliberately dropped rather than stored: in FULL
// mode those bodies carry whole file contents, command output, and the system
// prompt, none of which cc-otel displays, and persisting them would grow the
// database without bound.
func (s *logsServiceServer) handleDSHResourceLogs(ctx context.Context, rl *logspb.ResourceLogs) bool {
	shouldNotify := false
	for _, sl := range rl.ScopeLogs {
		for _, lr := range sl.LogRecords {
			attrs := dshAttrs(lr, rl.Resource)
			sessionID := attrs["session.id"]
			if sessionID == "" {
				continue // ops records carry no ledger identity
			}
			switch attrs["event.type"] {
			case "step/start":
				var ref dshStepRef
				if decodeDSHBody(lr.Body, &ref) {
					s.dshTracker.noteStart(
						dshStepKey{sessionID, ref.Turn, ref.Step}, dshRecordTime(lr))
				}
			case "assistant/chunk":
				var ref dshStepRef
				if decodeDSHBody(lr.Body, &ref) {
					s.dshTracker.noteFirstToken(
						dshStepKey{sessionID, ref.Turn, ref.Step}, dshRecordTime(lr))
				}
			case "step/end":
				var ref dshStepRef
				if decodeDSHBody(lr.Body, &ref) {
					s.dshTracker.take(dshStepKey{sessionID, ref.Turn, ref.Step})
				}
			case "assistant/message":
				var body dshAssistantMessage
				if !decodeDSHBody(lr.Body, &body) {
					continue
				}
				timing, _ := s.dshTracker.take(dshStepKey{sessionID, body.Turn, body.Step})
				event := dshAPIRequest(lr, attrs, timing, &body)
				if event == nil {
					continue
				}
				if s.pricer != nil && !pricing.IsUpstreamAuthoritative(event.ServiceName, event.Model) {
					if c := s.pricer.Calc(ctx, event.Model,
						event.InputTokens, event.OutputTokens,
						event.CacheReadTokens, event.CacheCreationTokens); c > 0 {
						event.CostUSD = c
					}
				}
				apiReq := apiRequestFromEvent(event)
				apiReq.ActualModel = s.cfg.ResolveActualModel(apiReq.Model)
				inserted, err := s.repo.InsertRequest(ctx, apiReq)
				if err != nil {
					log.Printf("dsh: insert api_request: %v", err)
				} else if inserted {
					shouldNotify = true
				}
			}
		}
	}
	return shouldNotify
}
