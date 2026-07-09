package receiver

import (
	"context"
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

func TestDispatchCodexLog_APIRequest(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	lr := &logspb.LogRecord{
		TimeUnixNano: uint64(1700000000) * 1e9,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.api_request"),
			attr("conversation.id", "conv-1"),
			attr("model", "gpt-5.1"),
			attrInt("duration_ms", 1234),
			attrInt("http.response.status_code", 200),
			attr("endpoint", "/v1/responses"),
		},
	}
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		attr("service.name", "codex-cli"),
	}}

	ok := dispatchCodexLog(context.Background(), repo, lr, res, nil, nil, nil)
	if !ok {
		t.Fatal("expected dispatch to return true (insert happened)")
	}

	var n int
	repo.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM codex_api_requests`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 row, got %d", n)
	}
}

func TestDispatchCodexLog_SSECompletedUpdatesTokens(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}}

	// 1. api_request first
	dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		TimeUnixNano: uint64(1700000000) * 1e9,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.api_request"),
			attr("conversation.id", "conv-X"),
			attr("model", "gpt-5.1"),
			attrInt("duration_ms", 555),
		},
	}, res, nil, nil, nil)

	// 2. sse_event response.completed second
	dispatchCodexLog(ctx, repo, &logspb.LogRecord{
		TimeUnixNano: uint64(1700000005) * 1e9,
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.sse_event"),
			attr("event.kind", "response.completed"),
			attr("conversation.id", "conv-X"),
			attr("model", "gpt-5.1"),
			attrInt("input_token_count", 1234),
			attrInt("output_token_count", 567),
			attrInt("cached_token_count", 100),
			attrInt("reasoning_token_count", 42),
			attrInt("tool_token_count", 13),
		},
	}, res, nil, nil, nil)

	var n, input, total int64
	repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_api_requests WHERE input_tokens > 0`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 tokenised row, got %d", n)
	}
	repo.DB().QueryRowContext(ctx, `SELECT input_tokens, total_tokens FROM codex_api_requests`).Scan(&input, &total)
	if input != 1234 {
		t.Fatalf("input_tokens not updated, got %d", input)
	}
	if total != 13 {
		t.Fatalf("total_tokens not updated, got %d", total)
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
	if rows != 1 || input != 1234 || output != 567 || duration != 2408 {
		t.Fatalf("expected SSE tokens to merge into duration row; rows=%d input=%d output=%d duration=%d", rows, input, output, duration)
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
		{
			"sse_event_non_completion",
			[]*commontpb.KeyValue{
				attr("event.name", "codex.sse_event"),
				attr("event.kind", "response.created"),
				attr("conversation.id", "c1"),
			},
			`SELECT COUNT(*) FROM codex_events`,
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
