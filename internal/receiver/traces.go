package receiver

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/young1lin/cc-otel/internal/db"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// tracesServiceServer handles OTLP trace exports.
// Goal: extract ttft_ms from span attributes and backfill into api_requests.ttft_ms.
type tracesServiceServer struct {
	coltracepb.UnimplementedTraceServiceServer
	repo     *db.Repository
	notifier Notifier
	// logs lets AI-SDK usage spans (opencode et al.) re-enter the normal log
	// ingestion path instead of growing a second, divergent insert path here.
	logs *logsServiceServer
}

type rawSpan struct {
	SpanName      string            `json:"span_name"`
	SpanKind      string            `json:"span_kind"`
	StartUnixNano uint64            `json:"start_unix_nano"`
	EndUnixNano   uint64            `json:"end_unix_nano"`
	Attributes    map[string]string `json:"attributes"`
	ResourceAttrs map[string]string `json:"resource_attributes,omitempty"`
	ScopeName     string            `json:"scope_name,omitempty"`
	ScopeVersion  string            `json:"scope_version,omitempty"`
}

func firstNonEmptyMany(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func pickAttr(attrs, resAttrs map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := attrs[k]; v != "" {
			return v
		}
		if v := resAttrs[k]; v != "" {
			return v
		}
	}
	return ""
}

func spanKindString(k tracepb.Span_SpanKind) string {
	switch k {
	case tracepb.Span_SPAN_KIND_CLIENT:
		return "client"
	case tracepb.Span_SPAN_KIND_SERVER:
		return "server"
	case tracepb.Span_SPAN_KIND_PRODUCER:
		return "producer"
	case tracepb.Span_SPAN_KIND_CONSUMER:
		return "consumer"
	case tracepb.Span_SPAN_KIND_INTERNAL:
		return "internal"
	default:
		return "unspecified"
	}
}

func (s *tracesServiceServer) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	if s.repo == nil || req == nil {
		return &coltracepb.ExportTraceServiceResponse{}, nil
	}

	shouldNotify := false
	spanCount := 0
	ttftCount := 0
	stats := &ingestStats{}
	// AI-SDK usage spans converted to api_request log records, grouped by the
	// resource they arrived with so service.name attribution survives.
	var aiSDKBatches []*logspb.ResourceLogs

	for _, rs := range req.ResourceSpans {
		resAttrs := map[string]string{}
		if rs.Resource != nil {
			resAttrs = extractAttrs(rs.Resource.Attributes)
		}
		var aiSDKRecords []*logspb.LogRecord

		for _, ss := range rs.ScopeSpans {
			scopeName := ""
			scopeVer := ""
			if ss.Scope != nil {
				scopeName = ss.Scope.Name
				scopeVer = ss.Scope.Version
			}

			for _, sp := range ss.Spans {
				spanCount++
				stats.record()
				attrs := extractAttrs(sp.Attributes)

				// Always keep a raw backup for later inspection / join-key selection.
				backup := rawSpan{
					SpanName:      sp.Name,
					SpanKind:      spanKindString(sp.Kind),
					StartUnixNano: sp.StartTimeUnixNano,
					EndUnixNano:   sp.EndTimeUnixNano,
					Attributes:    attrs,
					ResourceAttrs: resAttrs,
					ScopeName:     scopeName,
					ScopeVersion:  scopeVer,
				}
				rawJSON, _ := json.Marshal(backup)
				ts := time.Now().Unix()
				if sp.EndTimeUnixNano > 0 {
					ts = int64(sp.EndTimeUnixNano / 1e9)
				} else if sp.StartTimeUnixNano > 0 {
					ts = int64(sp.StartTimeUnixNano / 1e9)
				}
				// AI-SDK clients report token usage on the span itself rather
				// than as a log record. Reshape and hand off; these spans carry
				// their own timings, so they never also need the ttft backfill.
				if isAISDKUsageSpan(sp.Name) {
					if rec := mapAISDKSpan(sp, attrs, resAttrs); rec != nil {
						aiSDKRecords = append(aiSDKRecords, rec)
						continue
					}
				}

				ttft := parseAttrInt(attrs, "ttft_ms")
				if ttft <= 0 {
					continue
				}
				ttftCount++

				// Best-effort join keys: prefer exact request_id, then prompt+session+model,
				// anchored by span end time.
				sessionID := pickAttr(attrs, resAttrs, "session.id", "session_id", "sessionId", "cc.session_id")
				promptID := pickAttr(attrs, resAttrs, "prompt.id", "prompt_id", "promptId", "cc.prompt_id")
				requestID := pickAttr(attrs, resAttrs, "request_id", "request.id", "requestId", "cc.request_id")
				model := pickAttr(attrs, resAttrs, "model", "model.name", "model_id", "modelId", "cc.model", "gen_ai.request.model", "gen_ai.response.model")
				if model == "" {
					// Some spans may only have the anthropic model on a different key.
					model = firstNonEmptyMany(attrs["anthropic.model"], resAttrs["anthropic.model"], attrs["llm.model"], resAttrs["llm.model"])
				}

				// Debug-friendly: ttft spans are rare; log enough to diagnose join-key mismatch quickly.
				// The span is NOT persisted to raw_otlp_events (that table is write-frozen);
				// the log line is the only durable trace of unmatched spans.
				log.Printf("trace ttft span: name=%q kind=%s ttft_ms=%d end_unix=%d join(request=%q session=%q prompt=%q model=%q) raw=%s",
					sp.Name, spanKindString(sp.Kind), ttft, ts, requestID, sessionID, promptID, model, string(rawJSON))

				if requestID == "" && (sessionID == "" || model == "") {
					log.Printf("trace ttft span missing join keys: span=%q kind=%s ttft_ms=%d request=%t session=%t prompt=%t model=%t",
						sp.Name, spanKindString(sp.Kind), ttft, requestID != "", sessionID != "", promptID != "", model != "")
					continue
				}

				endUnix := ts
				var updated bool
				var err error
				if requestID != "" {
					updated, err = s.repo.BackfillTTFTByRequestID(ctx, requestID, ttft)
				}
				if !updated && err == nil && promptID != "" && sessionID != "" && model != "" {
					updated, err = s.repo.BackfillTTFTNearest(ctx, sessionID, promptID, model, endUnix, ttft)
				}
				if !updated && err == nil && sessionID != "" && model != "" {
					updated, err = s.repo.BackfillTTFTNearestLoose(ctx, sessionID, model, endUnix, 120, ttft)
				}
				if err != nil {
					log.Printf("trace backfill ttft failed: %v", err)
					stats.fail()
				} else if updated {
					shouldNotify = true
					log.Printf("trace backfill ttft updated: request=%q session=%q prompt=%q model=%q end_unix=%d ttft_ms=%d",
						requestID, sessionID, promptID, model, endUnix, ttft)
				} else {
					log.Printf("trace backfill ttft no match: request=%q session=%q prompt=%q model=%q end_unix=%d ttft_ms=%d",
						requestID, sessionID, promptID, model, endUnix, ttft)
					// Common in practice: trace export arrives before the api_request log row.
					// Enqueue for opportunistic application when InsertRequest runs.
					if qErr := s.repo.EnqueuePendingTTFTSpan(ctx, requestID, sessionID, model, endUnix, ttft, string(rawJSON)); qErr != nil {
						log.Printf("trace enqueue pending ttft failed: %v", qErr)
						stats.fail()
					}
				}
			}
		}

		if len(aiSDKRecords) > 0 {
			aiSDKBatches = append(aiSDKBatches, &logspb.ResourceLogs{
				Resource:  aiSDKResource(rs.Resource),
				ScopeLogs: []*logspb.ScopeLogs{{LogRecords: aiSDKRecords}},
			})
		}
	}

	// Replay converted spans through the log path so they get the same cost
	// recompute, service_name defaulting, dedup and notification as every other
	// client. The inner Export honours the delivery contract: total storage
	// failure returns an error — that MUST propagate, otherwise an opencode
	// trace export answers 200 while every converted api_request was dropped
	// (R4 protocol F3). Partial failures fold into this export's own stats via
	// the PartialSuccess response.
	if len(aiSDKBatches) > 0 && s.logs != nil {
		converted := 0
		for _, rl := range aiSDKBatches {
			for _, sl := range rl.ScopeLogs {
				converted += len(sl.LogRecords)
			}
		}
		resp, err := s.logs.Export(ctx, &collogspb.ExportLogsServiceRequest{ResourceLogs: aiSDKBatches})
		if err != nil {
			log.Printf("ai-sdk span ingest failed: %v", err)
			// The whole converted batch failed storage: count every converted
			// record as failed so exportResult retries instead of 200-ACKing
			// data that was dropped one transport hop away.
			for i := uint32(0); i < uint32(converted); i++ {
				stats.fail()
			}
		} else if resp != nil && resp.PartialSuccess != nil && resp.PartialSuccess.RejectedLogRecords > 0 {
			for i := int64(0); i < resp.PartialSuccess.RejectedLogRecords; i++ {
				stats.fail()
			}
			log.Printf("OTEL traces: %d converted AI-SDK record(s) failed storage", resp.PartialSuccess.RejectedLogRecords)
		} else {
			log.Printf("OTEL traces: converted %d AI-SDK usage span(s) into api_request events", converted)
		}
	}

	if spanCount > 0 {
		log.Printf("OTEL traces received: spans=%d ttft_spans=%d", spanCount, ttftCount)
	}
	if shouldNotify && s.notifier != nil {
		s.notifier.Notify()
	}

	return exportResult(stats, &coltracepb.ExportTraceServiceResponse{}, func(r *coltracepb.ExportTraceServiceResponse) *coltracepb.ExportTraceServiceResponse {
		r.PartialSuccess = &coltracepb.ExportTracePartialSuccess{
			RejectedSpans: int64(stats.failed),
			ErrorMessage:  "cc-otel: " + strconv.Itoa(int(stats.failed)) + " spans failed storage",
		}
		return r
	})
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
