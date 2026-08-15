package receiver

import (
	"context"
	"testing"

	"github.com/young1lin/cc-otel/internal/api"
	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func aiSDKSpan(name string, spanID []byte, attrs ...*commontpb.KeyValue) *tracepb.Span {
	return &tracepb.Span{
		Name:              name,
		SpanId:            spanID,
		StartTimeUnixNano: 1_700_000_000_000_000_000,
		EndTimeUnixNano:   1_700_000_002_000_000_000, // +2s
		Attributes:        attrs,
	}
}

func sendSpans(t *testing.T, rcv *Receiver, res *resourcepb.Resource, spans ...*tracepb.Span) {
	t.Helper()
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource:   res,
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
		}},
	}
	if _, err := rcv.traces.Export(context.Background(), req); err != nil {
		t.Fatalf("traces Export: %v", err)
	}
}

func newTraceTestReceiver(t *testing.T) (*Receiver, *db.Repository) {
	t.Helper()
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	repo := db.NewRepository(d)
	return New(repo, &fakeResolver{}, api.NewBroker(), nil), repo
}

// An AI-SDK usage span must land in api_requests with Anthropic-style token
// splits: input_tokens is the UNCACHED remainder, cache read/write separate.
func TestAISDKSpan_BecomesAPIRequest(t *testing.T) {
	rcv, repo := newTraceTestReceiver(t)

	sendSpans(t, rcv,
		&resourcepb.Resource{Attributes: []*commontpb.KeyValue{strAttr("service.name", "opencode")}},
		aiSDKSpan("ai.streamText.doStream", []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4},
			strAttr("ai.response.model", "claude-sonnet-4-5"),
			intAttr("ai.usage.inputTokens", 1000),
			intAttr("ai.usage.inputTokenDetails.cacheReadTokens", 600),
			intAttr("ai.usage.inputTokenDetails.cacheWriteTokens", 100),
			intAttr("ai.usage.outputTokens", 250),
			strAttr("ai.telemetry.metadata.sessionId", "sess-oc"),
		))

	var model, service, requestID string
	var in, out, cr, cc, dur int64
	err := repo.DB().QueryRowContext(context.Background(), `
		SELECT model, service_name, request_id, input_tokens, output_tokens,
		       cache_read_tokens, cache_creation_tokens, duration_ms
		  FROM api_requests ORDER BY id DESC LIMIT 1`).
		Scan(&model, &service, &requestID, &in, &out, &cr, &cc, &dur)
	if err != nil {
		t.Fatalf("no api_request row was created: %v", err)
	}
	if model != "claude-sonnet-4-5" {
		t.Errorf("model = %q", model)
	}
	if service != "opencode" {
		t.Errorf("service_name = %q, want opencode", service)
	}
	if in != 300 { // 1000 total - 600 read - 100 write
		t.Errorf("input_tokens = %d, want 300 (uncached remainder)", in)
	}
	if out != 250 || cr != 600 || cc != 100 {
		t.Errorf("output=%d cacheRead=%d cacheCreate=%d", out, cr, cc)
	}
	if dur != 2000 {
		t.Errorf("duration_ms = %d, want 2000 from span wall clock", dur)
	}
	if requestID != "deadbeef01020304" {
		t.Errorf("request_id = %q, want the hex span id", requestID)
	}
}

// The AI SDK reports usage on both the parent and the leaf span. Only the leaf
// may be counted, or every request is billed twice.
func TestAISDKSpan_ParentSpanIgnored(t *testing.T) {
	rcv, repo := newTraceTestReceiver(t)

	sendSpans(t, rcv, nil,
		aiSDKSpan("ai.streamText", []byte{1, 1, 1, 1, 1, 1, 1, 1},
			strAttr("ai.response.model", "claude-sonnet-4-5"),
			intAttr("ai.usage.inputTokens", 1000),
			intAttr("ai.usage.outputTokens", 250),
		),
		aiSDKSpan("ai.streamText.doStream", []byte{2, 2, 2, 2, 2, 2, 2, 2},
			strAttr("ai.response.model", "claude-sonnet-4-5"),
			intAttr("ai.usage.inputTokens", 1000),
			intAttr("ai.usage.outputTokens", 250),
		))

	var n int
	repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM api_requests`).Scan(&n)
	if n != 1 {
		t.Fatalf("api_requests rows = %d, want 1 (leaf span only)", n)
	}
}

// Re-delivering the same span (exporter retry) must not double-count, which is
// what carrying the span id as request_id buys us.
func TestAISDKSpan_RetryIsIdempotent(t *testing.T) {
	rcv, repo := newTraceTestReceiver(t)

	span := aiSDKSpan("ai.generateText.doGenerate", []byte{9, 9, 9, 9, 9, 9, 9, 9},
		strAttr("ai.response.model", "glm-4.6"),
		intAttr("ai.usage.inputTokens", 500),
		intAttr("ai.usage.outputTokens", 100),
	)
	sendSpans(t, rcv, nil, span)
	sendSpans(t, rcv, nil, span)

	var n int
	repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM api_requests`).Scan(&n)
	if n != 1 {
		t.Fatalf("api_requests rows after retry = %d, want 1", n)
	}
}

// A span with no service.name is attributed to opencode, not to the global
// claude-code default — whatever emitted an AI-SDK span, it was not Claude Code.
func TestAISDKSpan_DefaultsServiceToOpencode(t *testing.T) {
	rcv, repo := newTraceTestReceiver(t)

	sendSpans(t, rcv, nil,
		aiSDKSpan("ai.streamText.doStream", []byte{7, 7, 7, 7, 7, 7, 7, 7},
			strAttr("ai.model.id", "glm-4.6"),
			intAttr("ai.usage.inputTokens", 10),
			intAttr("ai.usage.outputTokens", 5),
		))

	var service string
	if err := repo.DB().QueryRowContext(context.Background(),
		`SELECT service_name FROM api_requests ORDER BY id DESC LIMIT 1`).Scan(&service); err != nil {
		t.Fatalf("no row: %v", err)
	}
	if service != "opencode" {
		t.Errorf("service_name = %q, want opencode", service)
	}
}

// Non-usage spans must still reach the TTFT backfill path untouched.
func TestAISDKSpan_NonUsageSpanUnaffected(t *testing.T) {
	rcv, repo := newTraceTestReceiver(t)

	sendSpans(t, rcv, nil,
		aiSDKSpan("some.other.span", []byte{3, 3, 3, 3, 3, 3, 3, 3},
			strAttr("model", "claude-sonnet-4-5"),
			intAttr("ttft_ms", 250),
			strAttr("session.id", "sess-x"),
		))

	var n int
	repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM api_requests`).Scan(&n)
	if n != 0 {
		t.Fatalf("api_requests rows = %d, want 0 — non-usage spans must not create requests", n)
	}
}
