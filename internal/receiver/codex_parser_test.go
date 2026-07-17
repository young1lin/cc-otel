package receiver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"

	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func newCodexReceiverRepo(t *testing.T) *db.Repository {
	t.Helper()
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return db.NewRepository(d)
}

func attr(k, v string) *commontpb.KeyValue {
	return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: v}}}
}

func attrInt(k string, v int64) *commontpb.KeyValue {
	return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: v}}}
}

type codexPricingCall struct {
	model       string
	input       int64
	output      int64
	cacheRead   int64
	cacheCreate int64
}

type recordingCodexPricer struct {
	call codexPricingCall
}

func (p *recordingCodexPricer) Calc(
	_ context.Context,
	model string,
	input, output, cacheRead, cacheCreate int64,
) float64 {
	p.call = codexPricingCall{
		model: model, input: input, output: output,
		cacheRead: cacheRead, cacheCreate: cacheCreate,
	}
	return 0.0123
}

func TestCodexLogObservedUnixNanosPrefersObservedTime(t *testing.T) {
	lr := &logspb.LogRecord{
		TimeUnixNano:         100,
		ObservedTimeUnixNano: 200,
	}
	if got := codexLogObservedUnixNanos(lr); got != 200 {
		t.Fatalf("observed nanos = %d, want 200", got)
	}
	if got := codexLogUnixNanos(lr); got != 100 {
		t.Fatalf("persisted timestamp helper changed: got %d, want 100", got)
	}
}

func TestCodexRequestStartNanosClampsInvalidDuration(t *testing.T) {
	observed := int64(300 * time.Millisecond)
	if got := codexRequestStartNanos(observed, 300); got != 0 {
		t.Fatalf("start = %d, want 0", got)
	}
	if got := codexRequestStartNanos(observed, 301); got != observed {
		t.Fatalf("underflow was not clamped: got %d want %d", got, observed)
	}
	if got := codexRequestStartNanos(observed, -1); got != observed {
		t.Fatalf("negative duration changed start: got %d want %d", got, observed)
	}
}

func TestCodexSpanTrackerLookupRemoveFallbackMergeAndExpiry(t *testing.T) {
	tracker := newCodexSpanTracker()
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).UnixNano()

	fallbackKey := tracker.recordRequest("", spanInfo{
		rowID: 7, sessionID: "fallback-session", model: "gpt-5.1", startNanos: base,
	})
	if !strings.HasPrefix(fallbackKey, "fallback:") {
		t.Fatalf("fallback key = %q", fallbackKey)
	}
	key, info, ok := tracker.lookupRequest(
		"", "fallback-session", "gpt-5.1", base+int64(time.Second), 10*time.Minute,
	)
	if !ok || key != fallbackKey || info.rowID != 7 {
		t.Fatalf("fallback lookup: key=%q info=%+v ok=%v", key, info, ok)
	}
	if key2, _, ok2 := tracker.lookupRequest(
		"", "fallback-session", "gpt-5.1", base+int64(time.Second), 10*time.Minute,
	); !ok2 || key2 != fallbackKey {
		t.Fatal("lookup destructively removed tracker state")
	}

	spanKey := "AQIDBAUGBwg="
	tracker.recordRequest(spanKey, spanInfo{
		rowID: 99, sessionID: "merge-session", model: "gpt-5.1",
		startNanos: base + int64(500*time.Millisecond),
	})
	tracker.recordRequest(spanKey, spanInfo{
		sessionID: "merge-session", model: "gpt-5.1",
		startNanos: base + int64(600*time.Millisecond),
	})
	_, merged, ok := tracker.lookupRequest(
		spanKey, "merge-session", "gpt-5.1", base+int64(time.Second), 10*time.Minute,
	)
	if !ok || merged.rowID != 99 ||
		merged.startNanos != base+int64(500*time.Millisecond) {
		t.Fatalf("span merge lost row/start: %+v ok=%v", merged, ok)
	}

	tracker.removeRequest(fallbackKey)
	if _, _, ok := tracker.lookupRequest(
		"", "fallback-session", "gpt-5.1", base+int64(time.Second), 10*time.Minute,
	); ok {
		t.Fatal("explicit removal left fallback entry")
	}
	if _, _, ok := tracker.lookupRequest(
		spanKey, "merge-session", "gpt-5.1", base+int64(time.Second), 10*time.Minute,
	); !ok {
		t.Fatal("removing one key removed another entry")
	}

	tracker.recordRequest("old-span", spanInfo{
		sessionID: "old", model: "gpt-5.1", startNanos: base,
	})
	tracker.recordRequest("new-span", spanInfo{
		sessionID: "new", model: "gpt-5.1",
		startNanos: base + int64(31*time.Minute),
	})
	if _, _, ok := tracker.lookupRequest(
		"old-span", "old", "gpt-5.1", base+int64(31*time.Minute), 60*time.Minute,
	); ok {
		t.Fatal("30-minute eviction did not run below 100 entries")
	}
}

func TestDispatchCodexLog_APIRequestAnchorsSuccessfulStream(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	tracker := newCodexSpanTracker()
	base := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	observed := base.Add(300 * time.Millisecond)
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		attr("service.name", "codex-cli"),
	}}

	ok := dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		TimeUnixNano:         uint64(observed.UnixNano()),
		ObservedTimeUnixNano: uint64(observed.UnixNano()),
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.api_request"),
			attr("conversation.id", "anchor-session"),
			attr("model", "gpt-5.1"),
			attrInt("duration_ms", 300),
		},
	}, res, nil, tracker, nil)
	if !ok {
		t.Fatal("status-omitted successful request was not inserted")
	}

	var rowID, duration int64
	if err := repo.DB().QueryRowContext(ctx,
		`SELECT id, duration_ms FROM codex_api_requests WHERE session_id = 'anchor-session'`,
	).Scan(&rowID, &duration); err != nil {
		t.Fatalf("read request: %v", err)
	}
	if duration != 0 {
		t.Fatalf("successful setup duration persisted as final duration: %d", duration)
	}
	key, info, found := tracker.lookupRequest(
		"", "anchor-session", "gpt-5.1",
		observed.Add(time.Second).UnixNano(), 10*time.Minute,
	)
	if !found || !strings.HasPrefix(key, "fallback:") ||
		info.rowID != rowID || info.startNanos != base.UnixNano() {
		t.Fatalf("wrong empty-span anchor: key=%q info=%+v found=%v", key, info, found)
	}
}

func TestDispatchCodexLog_APIRequestFailureKeepsReportedDuration(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	tracker := newCodexSpanTracker()
	when := time.Date(2026, 7, 17, 13, 5, 0, 0, time.UTC)

	ok := dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(when.UnixNano()),
		SpanId:               []byte{1, 2, 3, 4, 5, 6, 7, 8},
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.api_request"),
			attr("conversation.id", "failed-session"),
			attr("model", "gpt-5.1"),
			attrInt("duration_ms", 300),
			attrInt("http.response.status_code", 500),
			attr("error.message", "upstream failed"),
		},
	}, nil, nil, tracker, nil)
	if !ok {
		t.Fatal("failed request row was not inserted")
	}
	var duration int64
	if err := repo.DB().QueryRowContext(ctx,
		`SELECT duration_ms FROM codex_api_requests WHERE session_id = 'failed-session'`,
	).Scan(&duration); err != nil {
		t.Fatalf("read failed request: %v", err)
	}
	if duration != 300 {
		t.Fatalf("failed request duration = %d, want 300", duration)
	}
	if _, _, found := tracker.lookupRequest(
		spanIDFromLog(&logspb.LogRecord{SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}}),
		"failed-session", "gpt-5.1", when.Add(time.Second).UnixNano(), 10*time.Minute,
	); found {
		t.Fatal("failed request must not wait for completion")
	}
}

func TestDispatchCodexLog_SSECompletedPersistsAccountingAndFullDuration(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	tracker := newCodexSpanTracker()
	pricer := &recordingCodexPricer{}
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		attr("service.name", "codex-cli"),
	}}
	spanID := []byte{1, 3, 5, 7, 9, 11, 13, 15}
	base := time.Date(2026, 7, 17, 14, 0, 0, 0, time.Local)

	if ok := dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		TimeUnixNano:         uint64(base.Add(300 * time.Millisecond).UnixNano()),
		ObservedTimeUnixNano: uint64(base.Add(300 * time.Millisecond).UnixNano()),
		SpanId:               spanID,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.api_request"),
			attr("conversation.id", "completion-session"),
			attr("model", "gpt-5.1"),
			attrInt("duration_ms", 300),
			attrInt("http.response.status_code", 200),
		},
	}, res, nil, tracker, pricer); !ok {
		t.Fatal("request insert failed")
	}

	if ok := dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		TimeUnixNano:         uint64(base.Add(2300 * time.Millisecond).UnixNano()),
		ObservedTimeUnixNano: uint64(base.Add(2300 * time.Millisecond).UnixNano()),
		SpanId:               spanID,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.sse_event"),
			attr("event.kind", "response.completed"),
			attr("conversation.id", "completion-session"),
			attr("model", "gpt-5.1"),
			attrInt("input_token_count", 100),
			attrInt("cached_token_count", 40),
			attrInt("cache_write_token_count", 20),
			attrInt("output_token_count", 10),
			attrInt("reasoning_token_count", 5),
			attrInt("tool_token_count", 110),
			attrInt("ttft_ms", 250),
		},
	}, res, nil, tracker, pricer); !ok {
		t.Fatal("completion update failed")
	}

	var input, output, cacheRead, cacheCreate, reasoning, total, duration, ttft int64
	if err := repo.DB().QueryRowContext(ctx, `
		SELECT input_tokens, output_tokens, cache_read_tokens,
		       cache_creation_tokens, reasoning_tokens, total_tokens,
		       duration_ms, ttft_ms
		FROM codex_api_requests WHERE session_id = 'completion-session'`,
	).Scan(&input, &output, &cacheRead, &cacheCreate, &reasoning, &total, &duration, &ttft); err != nil {
		t.Fatalf("read request: %v", err)
	}
	if input != 100 || output != 10 || cacheRead != 40 || cacheCreate != 20 ||
		reasoning != 5 || total != 110 || duration != 2300 || ttft != 250 {
		t.Fatalf("wrong completion row: in=%d out=%d read=%d create=%d reasoning=%d total=%d duration=%d ttft=%d",
			input, output, cacheRead, cacheCreate, reasoning, total, duration, ttft)
	}
	if pricer.call != (codexPricingCall{
		model: "gpt-5.1", input: 40, output: 10, cacheRead: 40, cacheCreate: 20,
	}) {
		t.Fatalf("wrong pricing arguments: %+v", pricer.call)
	}
	var aggregateCreate int64
	if err := repo.DB().QueryRowContext(ctx, `
		SELECT cache_creation_tokens FROM codex_daily_model_agg
		WHERE date = ? AND model = 'gpt-5.1'`,
		base.Format("2006-01-02"),
	).Scan(&aggregateCreate); err != nil {
		t.Fatalf("read aggregate: %v", err)
	}
	if aggregateCreate != 20 {
		t.Fatalf("aggregate cache creation = %d, want 20", aggregateCreate)
	}
	if _, _, found := tracker.lookupRequest(
		spanIDFromLog(&logspb.LogRecord{SpanId: spanID}),
		"completion-session", "gpt-5.1",
		base.Add(2400*time.Millisecond).UnixNano(), 10*time.Minute,
	); found {
		t.Fatal("successful completion did not remove tracker entry")
	}
}

func TestDispatchCodexLog_SSEDatabaseFailureRetainsTracker(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	tracker := newCodexSpanTracker()
	span := []byte{8, 6, 4, 2, 1, 3, 5, 7}
	base := time.Date(2026, 7, 17, 14, 10, 0, 0, time.UTC)

	dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(base.Add(100 * time.Millisecond).UnixNano()),
		SpanId:               span,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.api_request"),
			attr("conversation.id", "db-failure-session"),
			attr("model", "gpt-5.1"),
			attrInt("duration_ms", 100),
			attrInt("http.response.status_code", 200),
		},
	}, nil, nil, tracker, nil)
	if err := repo.DB().Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	ok := dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(base.Add(time.Second).UnixNano()),
		SpanId:               span,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.sse_event"),
			attr("event.kind", "response.completed"),
			attr("conversation.id", "db-failure-session"),
			attr("model", "gpt-5.1"),
			attrInt("input_token_count", 10),
			attrInt("output_token_count", 1),
		},
	}, nil, nil, tracker, nil)
	if ok {
		t.Fatal("database failure reported success")
	}
	if _, _, found := tracker.lookupRequest(
		spanIDFromLog(&logspb.LogRecord{SpanId: span}),
		"db-failure-session", "gpt-5.1",
		base.Add(1100*time.Millisecond).UnixNano(), 10*time.Minute,
	); !found {
		t.Fatal("database failure removed retryable tracker state")
	}
}

func TestDispatchCodexLog_WebsocketDurationBeforeSSEUsesObservedTime(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}}
	tracker := newCodexSpanTracker()
	spanID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	start := time.Date(2026, 4, 29, 8, 19, 18, 0, time.UTC)

	dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(start.UnixNano()),
		SpanId:               spanID,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.websocket_request"),
			attr("conversation.id", "conv-duration"),
			attr("model", "gpt-5.5"),
		},
	}, res, nil, tracker, nil)

	dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(start.Add(2408 * time.Millisecond).UnixNano()),
		SpanId:               spanID,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.websocket_event"),
			attr("event.kind", "response.completed"),
			attr("conversation.id", "conv-duration"),
			attr("model", "gpt-5.5"),
		},
	}, res, nil, tracker, nil)

	trackerSpan := spanIDFromLog(&logspb.LogRecord{SpanId: spanID})
	if _, _, found := tracker.lookupRequest(
		trackerSpan, "conv-duration", "gpt-5.5",
		start.Add(2450*time.Millisecond).UnixNano(), 10*time.Minute,
	); !found {
		t.Fatal("websocket completion consumed state before structured SSE completion")
	}

	var rows, duration int64
	if err := repo.DB().QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(duration_ms), 0) FROM codex_api_requests`).Scan(&rows, &duration); err != nil {
		t.Fatalf("duration row query: %v", err)
	}
	if rows != 1 || duration != 2408 {
		t.Fatalf("expected one duration-only row with 2408ms; rows=%d duration=%d", rows, duration)
	}

	dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(start.Add(2500 * time.Millisecond).UnixNano()),
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.sse_event"),
			attr("event.kind", "response.completed"),
			attr("conversation.id", "conv-duration"),
			attr("model", "gpt-5.5"),
			attrInt("input_token_count", 1234),
			attrInt("output_token_count", 567),
		},
	}, res, nil, tracker, nil)

	var input, output int64
	if err := repo.DB().QueryRowContext(ctx, `SELECT COUNT(*), input_tokens, output_tokens, duration_ms FROM codex_api_requests`).Scan(&rows, &input, &output, &duration); err != nil {
		t.Fatalf("merged row query: %v", err)
	}
	if rows != 1 || input != 1234 || output != 567 || duration != 2500 {
		t.Fatalf("expected SSE to finalize the row at 2500ms; rows=%d input=%d output=%d duration=%d", rows, input, output, duration)
	}
	if _, _, found := tracker.lookupRequest(
		trackerSpan, "conv-duration", "gpt-5.5",
		start.Add(2600*time.Millisecond).UnixNano(), 10*time.Minute,
	); found {
		t.Fatal("structured SSE completion did not remove state")
	}
}

func TestDispatchCodexLog_WebsocketEventNameCanComeFromBody(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}}
	tracker := newCodexSpanTracker()
	spanID := []byte{8, 7, 6, 5, 4, 3, 2, 1}
	start := time.Date(2026, 5, 12, 10, 9, 35, 0, time.UTC)

	dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(start.UnixNano()),
		SpanId:               spanID,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.websocket_request"),
			attr("conversation.id", "conv-body-kind"),
			attr("model", "gpt-5.5"),
		},
	}, res, nil, tracker, nil)

	// Current Codex websocket events may put response.completed in the body and
	// omit event.name. The receiver still needs to treat this as a
	// codex.websocket_event so the tracked span can backfill duration_ms.
	dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(start.Add(4200 * time.Millisecond).UnixNano()),
		SpanId:               spanID,
		Body:                 &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "response.completed"}},
		Attributes: []*commontpb.KeyValue{
			attr("conversation.id", "conv-body-kind"),
			attr("model", "gpt-5.5"),
		},
	}, res, nil, tracker, nil)

	var rows, duration int64
	if err := repo.DB().QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(duration_ms), 0) FROM codex_api_requests`).Scan(&rows, &duration); err != nil {
		t.Fatalf("duration row query: %v", err)
	}
	if rows != 1 || duration != 4200 {
		t.Fatalf("expected body-derived websocket duration row with 4200ms; rows=%d duration=%d", rows, duration)
	}
	if _, _, found := tracker.lookupRequest(
		spanIDFromLog(&logspb.LogRecord{SpanId: spanID}),
		"conv-body-kind", "gpt-5.5",
		start.Add(4300*time.Millisecond).UnixNano(), 10*time.Minute,
	); !found {
		t.Fatal("body-derived websocket completion consumed tracker state")
	}
}

func TestDispatchCodexLog_SSECompletedUsesTrackedWebsocketRequestDuration(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}}
	tracker := newCodexSpanTracker()
	spanID := []byte{2, 4, 6, 8, 10, 12, 14, 16}
	start := time.Date(2026, 5, 12, 10, 8, 25, 0, time.UTC)

	dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(start.UnixNano()),
		SpanId:               spanID,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.websocket_request"),
			attr("conversation.id", "conv-sse-duration"),
			attr("model", "gpt-5.5"),
		},
	}, res, nil, tracker, nil)

	dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(start.Add(3900 * time.Millisecond).UnixNano()),
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.sse_event"),
			attr("event.kind", "response.completed"),
			attr("conversation.id", "conv-sse-duration"),
			attr("model", "gpt-5.5"),
			attrInt("input_token_count", 1000),
			attrInt("output_token_count", 50),
		},
	}, res, nil, tracker, nil)

	var rows, input, output, duration int64
	if err := repo.DB().QueryRowContext(ctx, `SELECT COUNT(*), input_tokens, output_tokens, duration_ms FROM codex_api_requests`).Scan(&rows, &input, &output, &duration); err != nil {
		t.Fatalf("merged row query: %v", err)
	}
	if rows != 1 || input != 1000 || output != 50 || duration != 3900 {
		t.Fatalf("expected SSE completion row with tracked duration; rows=%d input=%d output=%d duration=%d", rows, input, output, duration)
	}
}

func readCount(t *testing.T, repo *db.Repository, sqlStr string) int64 {
	t.Helper()
	var n int64
	if err := repo.DB().QueryRowContext(context.Background(), sqlStr).Scan(&n); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return n
}

func TestDispatchCodexLog_DiagnosticEventsAreNotPersisted(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}}
	tracker := newCodexSpanTracker()

	logs := []*logspb.LogRecord{
		{Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.sse_event"),
			attr("event.kind", "response.output_text.delta"),
			attr("conversation.id", "conversation-a"),
		}},
		{SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.websocket_request"),
			attr("conversation.id", "conversation-a"),
			attr("model", "gpt-5.5"),
		}},
		{Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.startup_phase"),
			attr("conversation.id", "conversation-a"),
		}},
	}
	for _, record := range logs {
		dispatchCodexLog(ctx, repo, record, res, nil, tracker, nil)
	}
	if got := readCount(t, repo, `SELECT COUNT(*) FROM codex_events`); got != 0 {
		t.Fatalf("codex_events rows = %d, want 0", got)
	}
}

func TestDispatchCodexLog_OtherEvents(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}}

	cases := []struct {
		name     string
		attrs    []*commontpb.KeyValue
		countSQL string
	}{
		{
			"user_prompt",
			[]*commontpb.KeyValue{
				attr("event.name", "codex.user_prompt"),
				attr("conversation.id", "c1"),
				attr("prompt", "hello"),
				attrInt("prompt_length", 5),
			},
			`SELECT COUNT(*) FROM codex_user_prompt_events`,
		},
		{
			"tool_decision",
			[]*commontpb.KeyValue{
				attr("event.name", "codex.tool_decision"),
				attr("conversation.id", "c1"),
				attr("tool_name", "Bash"),
				attr("call_id", "x1"),
				attr("decision", "accept"),
				attr("source", "user"),
			},
			`SELECT COUNT(*) FROM codex_tool_decision_events`,
		},
		{
			"tool_result",
			[]*commontpb.KeyValue{
				attr("event.name", "codex.tool_result"),
				attr("conversation.id", "c1"),
				attr("tool_name", "Bash"),
				attr("call_id", "x1"),
				attrInt("duration_ms", 50),
				attr("success", "true"),
			},
			`SELECT COUNT(*) FROM codex_tool_result_events`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := readCount(t, repo, c.countSQL)
			dispatchCodexLog(ctx, repo, &logspb.LogRecord{
				TimeUnixNano: uint64(1700000000) * 1e9,
				Attributes:   c.attrs,
			}, res, nil, nil, nil)
			after := readCount(t, repo, c.countSQL)
			if after != before+1 {
				t.Errorf("expected count to increase by 1; before=%d after=%d", before, after)
			}
		})
	}
}
