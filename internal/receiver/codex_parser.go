package receiver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/pricing"

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
		if strings.HasPrefix(s, "response.") || s == "error" {
			return "codex.websocket_event"
		}
		if strings.HasPrefix(s, "claude_code.") {
			return strings.TrimPrefix(s, "claude_code.")
		}
	}
	return ""
}

// eventKindFromLog extracts event.kind from the record attributes. The body
// fallback below only matches websocket-era shapes from old builds; every
// inspected codex build emits event.kind as an attribute.
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

// codexLogObservedUnixNanos returns the event observed time used for timing
// math (request-start reconstruction, TTFT fallback, full duration). It prefers
// ObservedTimeUnixNano and falls back to TimeUnixNano. codexLogUnixNanos keeps
// the opposite ordering for the persisted event timestamp; changing that
// historical timestamp semantics is out of scope.
// attrIntOrLen reads a numeric attribute, falling back to the byte length
// of a sibling string attribute. Codex puts arguments_length/output_length
// only on the trace-safe variant of codex.tool_result (otel/src/tool_result.rs
// trace: block); the OTLP logs path carries the full `arguments` string and
// the output preview instead, so the length is derived from what IS present.
func attrIntOrLen(attrs map[string]string, intKey, stringKey string) int64 {
	if v := parseAttrInt(attrs, intKey); v > 0 {
		return v
	}
	return int64(len(attrs[stringKey]))
}

// toolResultError surfaces why a tool call failed. Current codex builds
// never send error.message on codex.tool_result (tool_result.rs: failure is
// success="false" with the error text in the output preview), so fall back
// to a trimmed output for failed calls.
func toolResultError(attrs map[string]string, success int) string {
	if msg := attrs["error.message"]; msg != "" || success == 1 {
		return msg
	}
	out := attrs["output"]
	const maxLen = 200
	if len(out) > maxLen {
		out = out[:maxLen]
	}
	return out
}

// toolOriginOrDerive reads tool_origin, deriving it from mcp_server presence
// when absent: Codex computes tool_origin the same way (tool_result.rs trace:
// block — "builtin" when mcp_server is empty, "mcp" otherwise).
func toolOriginOrDerive(attrs map[string]string) string {
	if v := attrs["tool_origin"]; v != "" {
		return v
	}
	if attrs["mcp_server"] != "" {
		return "mcp"
	}
	return "builtin"
}

// satAdd/satSub/satClampLow are saturating int64 helpers for client-reported
// token counters: a hostile or buggy exporter must not wrap the arithmetic
// (wrap-positive would defeat the <0 clamp and store garbage).
func satAdd(a, b int64) int64 {
	if a > 0 && b > math.MaxInt64-a {
		return math.MaxInt64
	}
	if a < 0 && b < math.MinInt64-a {
		return math.MinInt64
	}
	return a + b
}

func codexLogObservedUnixNanos(lr *logspb.LogRecord) int64 {
	if lr == nil {
		return 0
	}
	if lr.ObservedTimeUnixNano > 0 {
		return int64(lr.ObservedTimeUnixNano)
	}
	if lr.TimeUnixNano > 0 {
		return int64(lr.TimeUnixNano)
	}
	return 0
}

// codexRequestStartNanos reconstructs the model-request start boundary from the
// observed time of codex.api_request and its reported duration_ms (setup/header
// time). It clamps to the observed time when duration_ms is missing, negative,
// or would underflow the Unix epoch. A zero observed time yields zero.
func codexRequestStartNanos(observedNanos, reportedDurationMs int64) int64 {
	if observedNanos <= 0 {
		return 0
	}
	if reportedDurationMs <= 0 {
		return observedNanos
	}
	if reportedDurationMs > observedNanos/int64(time.Millisecond) {
		return observedNanos
	}
	return observedNanos - reportedDurationMs*int64(time.Millisecond)
}

// codexSpanTracker correlates a successful API/WebSocket request with its
// structured codex_api_requests row so the later codex.sse_event
// (response.completed) can write authoritative token/cost/TTFT/duration values
// onto the exact row. The primary key is the OTLP span_id; entries with an empty
// span_id are kept under an internal fallback:<n> key so session/model fallback
// still works. Lookup is non-destructive: state is removed only by an explicit
// removeRequest after the structured DB write commits.
// codexSpanTrackerMaxEntries bounds the tracker map: time-based eviction
// only runs inside recordRequest, so a chatty or clock-skewed client needs
// a hard cap too. 10k entries is ~hours of normal single-daemon traffic.
const codexSpanTrackerMaxEntries = 10000

type codexSpanTracker struct {
	mu           sync.Mutex
	spans        map[string]spanInfo
	nextFallback uint64
	// turnTTFT stashes codex.turn_ttft observations that arrived BEFORE
	// their anchor row was committed. Separate OTLP export flushes are not
	// order-preserving (live-verified: one exec landed ttft, the next lost
	// it because the turn event beat the anchor insert), so a miss is
	// retried when the anchor lands.
	turnTTFT map[string][]turnTTFTObs
}

// turnTTFTObs is one stashed turn-level TTFT observation.
type turnTTFTObs struct {
	tsUnixNano int64
	ms         int64
}

const (
	turnTTFTStashTTL    = 10 * time.Minute
	turnTTFTStashPerKey = 8
)

func turnTTFTKey(conversationID, model string) string {
	return conversationID + "\x00" + model
}

// stashTurnTTFT records an unapplied turn_ttft observation, pruning the
// per-key list by age (10min) and size (8, oldest dropped first).
func (t *codexSpanTracker) stashTurnTTFT(conversationID, model string, ts time.Time, ms int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.turnTTFT == nil {
		t.turnTTFT = make(map[string][]turnTTFTObs)
	}
	key := turnTTFTKey(conversationID, model)
	list := append(t.turnTTFT[key], turnTTFTObs{ts.UnixNano(), ms})
	now := time.Now().UnixNano()
	fresh := list[:0]
	for _, o := range list {
		if now-o.tsUnixNano < int64(turnTTFTStashTTL) {
			fresh = append(fresh, o)
		}
	}
	if len(fresh) > turnTTFTStashPerKey {
		fresh = fresh[len(fresh)-turnTTFTStashPerKey:]
	}
	if len(fresh) == 0 {
		delete(t.turnTTFT, key)
	} else {
		t.turnTTFT[key] = fresh
	}
}

// drainTurnTTFT returns and removes stashed observations that belong to
// THIS anchor: observed at or after the anchor timestamp (a turn's first
// output cannot precede the request that started the turn) and within 1h.
// Leftovers from other turns stay stashed for their own anchors and age out
// via the TTL prune. Returned observations are ts-ascending so the earliest
// (this turn's) fill wins under fill-only semantics.
func (t *codexSpanTracker) drainTurnTTFT(conversationID, model string, anchorTS time.Time) []turnTTFTObs {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := turnTTFTKey(conversationID, model)
	list := t.turnTTFT[key]
	if len(list) == 0 {
		return nil
	}
	lo := anchorTS.UnixNano()
	hi := lo + int64(time.Hour)
	var out []turnTTFTObs
	keep := list[:0]
	for _, o := range list {
		if o.tsUnixNano >= lo && o.tsUnixNano <= hi {
			out = append(out, o)
		} else {
			keep = append(keep, o)
		}
	}
	if len(keep) == 0 {
		delete(t.turnTTFT, key)
	} else {
		t.turnTTFT[key] = keep
	}
	sort.Slice(out, func(i, j int) bool { return out[i].tsUnixNano < out[j].tsUnixNano })
	return out
}

type spanInfo struct {
	rowID      int64
	sessionID  string
	model      string
	startNanos int64 // reconstructed request-start time in Unix nanoseconds
}

func newCodexSpanTracker() *codexSpanTracker {
	return &codexSpanTracker{spans: make(map[string]spanInfo)}
}

// recordRequest stores correlation state for one request and returns the
// internal map key. It evicts entries older than 30 minutes on every call. When
// an entry for the same key already exists, non-zero fields of the new info are
// merged onto the existing entry (preserving an earlier rowID and the earliest
// request-start boundary).
func (t *codexSpanTracker) recordRequest(spanID string, info spanInfo) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := info.startNanos - int64(30*time.Minute)
	if cutoff > 0 {
		evicted := 0
		for key, existing := range t.spans {
			if existing.startNanos > 0 && existing.startNanos < cutoff {
				delete(t.spans, key)
				evicted++
			}
		}
		// B4 observability: an evicted pending entry means its completion
		// never arrived (dropped connection, CLI crash) OR arrives later via
		// the orphan path; the count is the signal for either.
		if evicted > 0 {
			log.Printf("codex tracker: evicted %d pending span(s) older than 30min", evicted)
		}
	}
	// Size cap: time-based eviction only fires inside this method, so a
	// chatty or clock-skewed client could otherwise grow the map unbounded.
	// Evict oldest-start entries beyond the cap.
	if len(t.spans) > codexSpanTrackerMaxEntries {
		entries := make([]struct {
			key        string
			startNanos int64
		}, 0, len(t.spans))
		for k, v := range t.spans {
			entries = append(entries, struct {
				key        string
				startNanos int64
			}{k, v.startNanos})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].startNanos < entries[j].startNanos })
		for _, e := range entries[:len(entries)-codexSpanTrackerMaxEntries] {
			delete(t.spans, e.key)
		}
	}

	key := spanID
	if key == "" {
		t.nextFallback++
		key = "fallback:" + strconv.FormatUint(t.nextFallback, 10)
	}
	if existing, ok := t.spans[key]; ok {
		if info.rowID == 0 {
			info.rowID = existing.rowID
		}
		if info.sessionID == "" {
			info.sessionID = existing.sessionID
		}
		if info.model == "" {
			info.model = existing.model
		}
		if info.startNanos <= 0 ||
			(existing.startNanos > 0 && existing.startNanos < info.startNanos) {
			info.startNanos = existing.startNanos
		}
	}
	t.spans[key] = info
	return key
}

// lookupRequest returns the internal map key and span info for the best match. It
// tries the exact span key first, then the newest eligible entry matching
// sessionID + model within maxAge and not newer than observedNanos. It never
// mutates the map.
func (t *codexSpanTracker) lookupRequest(
	spanID, sessionID, model string,
	observedNanos int64,
	maxAge time.Duration,
) (string, spanInfo, bool) {
	if observedNanos <= 0 {
		return "", spanInfo{}, false
	}
	cutoff := observedNanos - int64(maxAge)
	eligible := func(info spanInfo) bool {
		if info.startNanos <= 0 || info.startNanos > observedNanos ||
			info.startNanos < cutoff {
			return false
		}
		if sessionID != "" && info.sessionID != sessionID {
			return false
		}
		if model != "" && info.model != model {
			return false
		}
		return true
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if spanID != "" {
		if info, ok := t.spans[spanID]; ok && eligible(info) {
			return spanID, info, true
		}
	}

	var bestKey string
	var best spanInfo
	for key, info := range t.spans {
		if !eligible(info) {
			continue
		}
		if bestKey == "" || info.startNanos > best.startNanos {
			bestKey = key
			best = info
		}
	}
	if bestKey == "" {
		return "", spanInfo{}, false
	}
	return bestKey, best, true
}

// removeRequest deletes the entry for the given internal map key. It is a no-op
// for an empty or missing key.
func (t *codexSpanTracker) removeRequest(key string) {
	if key == "" {
		return
	}
	t.mu.Lock()
	delete(t.spans, key)
	t.mu.Unlock()
}

// dispatchCodexLog handles a single Codex log record. Returns true when the
// record produced a side effect that should trigger SSE notification.
//
// pricer (optional) supplies recomputed cost_usd at the moment tokens are
// known (codex.sse_event with kind=response.completed). Codex never reports
// cost_usd itself, so without pricer the row stays at cost=0.
func dispatchCodexLog(ctx context.Context, repo *db.Repository, lr *logspb.LogRecord, res *resourcepb.Resource, notifier Notifier, tracker *codexSpanTracker, pricer Pricer, stats *ingestStats) bool {
	if lr == nil || repo == nil {
		return false
	}
	// Codex rows live in the shared pool since the merge. The All Clients
	// tab renders claude_pool views that exclude codex, so a Codex event
	// cannot dirty that tab — but the shared aggregates can shift, so keep
	// broadcasting the tags consumers subscribe to.
	notify := func() {
		if notifier != nil {
			// Refresh the Codex tab ("codex" tag) AND the "claude"-tagged
			// All Clients page (shared agg tables; broker.go documents both
			// tags).
			notifier.NotifySource("claude")
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
	// Future-dated records (broken client clock, hostile exporter) would pin
	// rows outside every retention sweep and poison MIN(timestamp)/date
	// bucketing for centuries (R4 live repro: a year-2262 row). Skew beyond
	// five minutes is treated as garbage and re-stamped at arrival time.
	if maxTS := time.Now().Add(5 * time.Minute); ts.After(maxTS) {
		ts = time.Now()
	}

	eventName := eventNameFromCodexLog(lr, attrs)
	switch eventName {
	case "codex.api_request":
		if attrs["model"] == "" {
			return false
		}
		reportedDurationMs := parseAttrInt(attrs, "duration_ms")
		status := parseAttrInt(attrs, "http.response.status_code")
		// A pending successful stream is one without an error and with a 2xx
		// (or unset) status. Its reported duration_ms is only setup/header time,
		// not the accepted full duration, so store zero and let the later
		// completion reconstruct the real duration.
		pendingStream := attrs["error.message"] == "" &&
			(status == 0 || (status >= 200 && status <= 299))
		storedDurationMs := reportedDurationMs
		if pendingStream {
			storedDurationMs = 0
		}
		// Codex never sends user.id. Its [otel] environment config
		// (dev / company / staging / prod; default "dev" — codex-rs
		// config/src/types.rs OtelConfigToml) rides on every OTLP signal as the
		// Resource attribute "env" (otel/src/provider.rs ENV_ATTRIBUTE). Map it
		// onto the same user_id column Claude Code's user.id uses, so the
		// Request Log User column answers "which environment drove this
		// request" instead of a permanent em-dash. An explicit user.id attr, if
		// a future Codex build starts sending one, wins over the environment.
		userID := attrs["user.id"]
		if userID == "" {
			userID = attrs["env"]
		}
		req := &db.CodexAPIRequest{
			Timestamp:      ts,
			SessionID:      attrs["conversation.id"],
			UserID:         userID,
			RequestID:      attrs["auth.request_id"],
			ConversationID: attrs["conversation.id"],
			Model:          attrs["model"],
			DurationMs:     storedDurationMs,
			HTTPStatus:     status,
			Endpoint:       attrs["endpoint"],
			EventName:      "codex.api_request",
			TerminalType:   attrs["terminal.type"],
			ServiceName:    attrs["service.name"],
			ServiceVersion: attrs["service.version"],
			// Codex's OTLP resource carries only service.*, env and host.name
			// (provider.rs) — host.arch/os.type/os.version never arrive, so those
			// columns stay empty for codex rows (they remain claude-path data).
			ErrorMessage: attrs["error.message"],
		}
		rowID, err := repo.InsertCodexAPIRequest(ctx, req)
		if err != nil {
			log.Printf("failed to insert codex api_request: %v", err)
			if stats != nil {
				stats.fail()
			}
			return false
		}
		if rowID > 0 && tracker != nil {
			// A turn_ttft that beat this anchor to the receiver is stashed;
			// fill now (exact-row, fill-only — an authoritative completion
			// ttft still overwrites via the finalize CASE-WHEN).
			for _, obs := range tracker.drainTurnTTFT(req.ConversationID, req.Model, req.Timestamp) {
				if err := repo.UpdateCodexRequestTTFT(
					ctx, rowID, req.SessionID, req.Model,
					time.Unix(0, obs.tsUnixNano), obs.ms,
				); err != nil {
					log.Printf("failed to apply stashed codex turn ttft (conv=%s): %v", req.ConversationID, err)
				}
			}
		}
		if pendingStream && tracker != nil {
			observedNanos := codexLogObservedUnixNanos(lr)
			if observedNanos == 0 {
				observedNanos = ts.UnixNano()
			}
			tracker.recordRequest(spanIDFromLog(lr), spanInfo{
				rowID:      rowID,
				sessionID:  req.SessionID,
				model:      req.Model,
				startNanos: codexRequestStartNanos(observedNanos, reportedDurationMs),
			})
		}
		notify()
		return true

	case "codex.sse_event":
		if eventKindFromLog(lr, attrs) == "response.completed" {
			upd := &db.CodexTokenUpdate{
				SessionID:           attrs["conversation.id"],
				Model:               attrs["model"],
				Timestamp:           ts,
				InputTokens:         parseAttrInt(attrs, "input_token_count"),
				OutputTokens:        parseAttrInt(attrs, "output_token_count"),
				CacheReadTokens:     parseAttrInt(attrs, "cached_token_count"),
				CacheCreationTokens: parseAttrInt(attrs, "cache_write_token_count"),
				ReasoningTokens:     parseAttrInt(attrs, "reasoning_token_count"),
				TotalTokens:         parseAttrInt(attrs, "tool_token_count"),
				TTFTMs:              parseAttrInt(attrs, "ttft_ms"),
			}
			// Codex emits TWO response.completed records per successful
			// HTTP-SSE request: a token-less per-poll record (session_telemetry.rs
			// sse_event fires for every SSE event, deltas aside) and the
			// authoritative sse_event_completed carrying usage. Only the latter
			// may consume the tracker entry or trigger the fallback INSERT —
			// a token-less record that lands after it would otherwise insert a
			// phantom zero-token row (request_count +1), and landing first it
			// would steal exact-row correlation from the real one.
			if upd.InputTokens == 0 && upd.OutputTokens == 0 &&
				upd.CacheReadTokens == 0 && upd.CacheCreationTokens == 0 &&
				upd.ReasoningTokens == 0 && upd.TotalTokens == 0 &&
				upd.TTFTMs == 0 {
				return false
			}
			// total_tokens keeps the CLIENT-REPORTED raw total. Codex sends it
			// as tool_token_count (otel/src/events/session_telemetry.rs
			// sse_event_completed: tool_token_count = %usage.total_tokens — the
			// name is historical, the value IS the Responses API total). Derive
			// input+output when a client build omits it; that matches the
			// migrated archive, where total_tokens = input_tokens + output_tokens
			// holds on every row.
			if upd.TotalTokens == 0 {
				upd.TotalTokens = satAdd(parseAttrInt(attrs, "input_token_count"), upd.OutputTokens)
			}
			// Normalize to Claude semantics BEFORE storing: OpenAI's Responses API
			// reports input_token_count as the TOTAL input side, which already
			// includes both cached_token_count (cache read) and
			// cache_write_token_count (cache write). The unified api_requests table
			// stores the four categories disjointly (Claude semantics), so subtract
			// both cache categories here — one place, once, and every downstream
			// SUM / dashboard / calendar becomes directly comparable across
			// clients. The raw reported value stays recoverable as
			// input + cache_read + cache_creation.
			if upd.InputTokens > 0 {
				// Saturating subtraction: hostile values (input=1, caches=MaxInt64)
				// must not wrap positive and defeat the clamp.
				upd.InputTokens = db.UncachedInput(upd.InputTokens, upd.CacheReadTokens, upd.CacheCreationTokens)
			}
			// Compute cost from the local pricing table — Codex never reports
			// cost_usd, and this path is never the authoritative Claude Code +
			// Claude combination, so every model is recomputed here. Input is
			// already uncached after the normalization above.
			if pricer != nil && !pricing.IsUpstreamAuthoritative(serviceNameFromResource(res), upd.Model) {
				upd.CostUSD = pricer.Calc(
					ctx, upd.Model, upd.InputTokens, upd.OutputTokens,
					upd.CacheReadTokens, upd.CacheCreationTokens,
				)
			}

			trackerKey := ""
			if tracker != nil {
				observedNanos := codexLogObservedUnixNanos(lr)
				if observedNanos == 0 {
					observedNanos = ts.UnixNano()
				}
				key, info, ok := tracker.lookupRequest(
					spanIDFromLog(lr), upd.SessionID, upd.Model,
					observedNanos, 10*time.Minute,
				)
				if ok {
					trackerKey = key
					upd.RequestRowID = info.rowID
					durationMs := (observedNanos - info.startNanos) / int64(time.Millisecond)
					if durationMs > 0 {
						upd.DurationMs = durationMs
					}
				}
			}

			if _, err := repo.UpdateCodexAPIRequestTokens(ctx, upd); err != nil {
				log.Printf("failed to update codex request tokens (session=%s model=%s): %v", upd.SessionID, upd.Model, err)
				if stats != nil {
					stats.fail()
				}
				return false
			}
			if tracker != nil {
				tracker.removeRequest(trackerKey)
			}
			notify()
			return true
		}
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
			ServiceVersion: attrs["service.version"],
		}
		if err := repo.InsertCodexUserPrompt(ctx, ev); err != nil {
			log.Printf("failed to insert codex user_prompt: %v", err)
			if stats != nil {
				stats.fail()
			}
			return false
		}
		notify()
		return true

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
		if err := repo.InsertCodexToolDecision(ctx, ev); err != nil {
			log.Printf("failed to insert codex tool_decision: %v", err)
			if stats != nil {
				stats.fail()
			}
			return false
		}
		notify()
		return true

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
			ArgumentsLength: attrIntOrLen(attrs, "arguments_length", "arguments"),
			OutputLength:    attrIntOrLen(attrs, "output_length", "output"),
			ToolOrigin:      toolOriginOrDerive(attrs),
			MCPServer:       attrs["mcp_server"],
			ErrorMessage:    toolResultError(attrs, success),
			TerminalType:    attrs["terminal.type"],
		}
		if err := repo.InsertCodexToolResult(ctx, ev); err != nil {
			log.Printf("failed to insert codex tool_result: %v", err)
			if stats != nil {
				stats.fail()
			}
			return false
		}
		notify()
		return true

	case "codex.websocket_request":
		if tracker != nil {
			observedNanos := codexLogObservedUnixNanos(lr)
			if observedNanos == 0 {
				observedNanos = ts.UnixNano()
			}
			tracker.recordRequest(spanIDFromLog(lr), spanInfo{
				sessionID: attrs["conversation.id"],
				model:     attrs["model"],
				startNanos: codexRequestStartNanos(
					observedNanos, parseAttrInt(attrs, "duration_ms"),
				),
			})
		}
		return false

	case "codex.websocket_event":
		eventKind := eventKindFromLog(lr, attrs)
		observedNanos := codexLogObservedUnixNanos(lr)
		if observedNanos == 0 {
			observedNanos = ts.UnixNano()
		}

		if tracker != nil {
			_, info, found := tracker.lookupRequest(
				spanIDFromLog(lr), attrs["conversation.id"], attrs["model"],
				observedNanos, 10*time.Minute,
			)
			if found && eventKind == "response.created" {
				ttftMs := (observedNanos - info.startNanos) / int64(time.Millisecond)
				if ttftMs > 0 {
					if err := repo.UpdateCodexRequestTTFT(
						ctx, info.rowID, info.sessionID, info.model, ts, ttftMs,
					); err != nil {
						log.Printf("failed to update codex ttft (session=%s): %v", info.sessionID, err)
						if stats != nil {
							stats.fail()
						}
					}
				}
			}
			if found && eventKind == "response.completed" {
				durationMs := (observedNanos - info.startNanos) / int64(time.Millisecond)
				if durationMs < 0 {
					durationMs = 0
				}
				updated, err := repo.UpdateCodexRequestDuration(
					ctx, info.rowID, info.sessionID, info.model, ts, durationMs,
				)
				if err != nil {
					log.Printf("failed to update codex duration (session=%s): %v", info.sessionID, err)
					if stats != nil {
						stats.fail()
					}
				}
				if err == nil {
					if updated {
						notify()
						return true
					}
					if durationMs > 0 && info.sessionID != "" && info.model != "" {
						_, insertErr := repo.InsertCodexAPIRequest(ctx, &db.CodexAPIRequest{
							Timestamp:      ts,
							SessionID:      info.sessionID,
							ConversationID: info.sessionID,
							Model:          info.model,
							DurationMs:     durationMs,
							EventName:      "codex.websocket_event:response.completed",
						})
						if insertErr != nil {
							log.Printf("failed to insert codex duration-only row (session=%s): %v", info.sessionID, insertErr)
							if stats != nil {
								stats.fail()
							}
						} else {
							notify()
							return true
						}
					}
				}
			}
		}
		return false

	case "codex.turn_ttft":
		// Turn-level TTFT (turn_timing.rs): duration from turn start to the
		// first output signal. The completion's ttft_ms attr is Option and is
		// absent for non-streamed/instant responses, so this event is often
		// the only TTFT a turn produces. Fill-only join onto the newest
		// zero-TTFT anchor of the same (conversation, model) at or before the
		// event time — see UpdateCodexTurnTTFT for the ordering argument.
		applied, err := repo.UpdateCodexTurnTTFT(
			ctx, attrs["conversation.id"], attrs["model"],
			ts, parseAttrInt(attrs, "duration_ms"),
		)
		if err != nil {
			log.Printf("failed to fill codex ttft from turn_ttft (conv=%s): %v", attrs["conversation.id"], err)
			if stats != nil {
				stats.fail()
			}
			return false
		}
		if !applied && tracker != nil {
			// The anchor row was not committed yet (export flush reorder —
			// live-verified intermittent miss). Stash and retry when the
			// anchor insert lands.
			tracker.stashTurnTTFT(attrs["conversation.id"], attrs["model"], ts, parseAttrInt(attrs, "duration_ms"))
		}
		return true

	default:
		// Accounting safety net: codex emits many events cc-otel does not
		// model (auth_recovery, sandbox_outcome, guardian_review,
		// skill_invocation, ...). Archive them raw so a future widening of the
		// model can backfill without losing data. sse_event kinds other than
		// response.completed stay excluded: they are per-poll noise by design.
		if eventName != "codex.sse_event" && strings.HasPrefix(eventName, "codex.") {
			if raw, mErr := json.Marshal(attrs); mErr == nil {
				if err := repo.InsertCodexRawEvent(ctx, eventName, ts.Unix(), string(raw)); err != nil {
					log.Printf("failed to archive codex raw event %s: %v", eventName, err)
					if stats != nil {
						stats.fail()
					}
					return false
				}
				return true
			}
		}
		return false
	}
}
