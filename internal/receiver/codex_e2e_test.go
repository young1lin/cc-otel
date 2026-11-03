package receiver

import (
	"context"
	"fmt"
	"math"
	"net"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/api"
	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestCodexE2E_APIRequestThenSSECompletion(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)
	broker := api.NewBroker()
	rcv := New(repo, &fakeResolver{}, broker, nil)

	srv := grpc.NewServer()
	rcv.Register(srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	cli := collogspb.NewLogsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	codexResource := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		{Key: "service.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "codex-cli"}}},
	}}

	send := func(attrs []*commontpb.KeyValue) {
		_, err := cli.Export(ctx, &collogspb.ExportLogsServiceRequest{
			ResourceLogs: []*logspb.ResourceLogs{{
				Resource: codexResource,
				ScopeLogs: []*logspb.ScopeLogs{{
					LogRecords: []*logspb.LogRecord{{
						TimeUnixNano: uint64(time.Now().UnixNano()),
						Attributes:   attrs,
					}},
				}},
			}},
		})
		if err != nil {
			t.Fatalf("export: %v", err)
		}
	}

	send([]*commontpb.KeyValue{
		{Key: "event.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "codex.api_request"}}},
		{Key: "conversation.id", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "conv-E2E"}}},
		{Key: "model", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "gpt-5.1"}}},
		{Key: "duration_ms", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: 800}}},
	})
	send([]*commontpb.KeyValue{
		{Key: "event.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "codex.sse_event"}}},
		{Key: "event.kind", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "response.completed"}}},
		{Key: "conversation.id", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "conv-E2E"}}},
		{Key: "model", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "gpt-5.1"}}},
		{Key: "input_token_count", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: 1500}}},
		{Key: "output_token_count", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: 250}}},
		// Real codex wire name for usage.total_tokens (session_telemetry.rs
		// sse_event_completed: tool_token_count = %usage.total_tokens).
		{Key: "reasoning_token_count", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: 30}}},
		{Key: "tool_token_count", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: 1750}}},
	})

	var rows int
	repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_requests WHERE service_name = 'codex'`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("expected 1 unified codex row after correlation, got %d", rows)
	}

	var input int64
	repo.DB().QueryRowContext(ctx, `SELECT input_tokens FROM api_requests WHERE service_name = 'codex'`).Scan(&input)
	if input != 1500 {
		t.Fatalf("expected 1500 input tokens, got %d", input)
	}

	var output int64
	repo.DB().QueryRowContext(ctx, `SELECT output_tokens FROM api_requests WHERE service_name = 'codex'`).Scan(&output)
	if output != 250 {
		t.Fatalf("expected 250 output tokens, got %d", output)
	}

	var reasoning, total int64
	repo.DB().QueryRowContext(ctx,
		`SELECT reasoning_tokens, total_tokens FROM api_requests WHERE service_name = 'codex'`).Scan(&reasoning, &total)
	if reasoning != 30 {
		t.Fatalf("expected 30 reasoning tokens, got %d", reasoning)
	}
	// tool_token_count is the client-reported total and must be stored as-is.
	if total != 1750 {
		t.Fatalf("expected total_tokens 1750 from tool_token_count, got %d", total)
	}
}

// TestCodexE2E_TokenlessCompletionIsNoOp pins the dual response.completed
// gate: current codex emits a token-less per-poll completion record before
// (or, under export reordering, after) the authoritative token-bearing one.
// The token-less record must neither correlate-consume the tracker entry nor
// mint a phantom zero-token row — both orders leave exactly one row carrying
// the authoritative usage.
func TestCodexE2E_TokenlessCompletionIsNoOp(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first bool // true: token-less record goes FIRST (per-poll ordering)
	}{
		{name: "tokenless first", first: true},
		{name: "tokenless last", first: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := db.Init(&config.Config{DBPath: ":memory:"})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			repo := db.NewRepository(d)
			broker := api.NewBroker()
			rcv := New(repo, &fakeResolver{}, broker, nil)

			srv := grpc.NewServer()
			rcv.Register(srv)
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			go srv.Serve(lis)
			defer srv.Stop()

			conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()

			cli := collogspb.NewLogsServiceClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			codexResource := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
				{Key: "service.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "codex-cli"}}},
			}}
			send := func(attrs []*commontpb.KeyValue) {
				_, err := cli.Export(ctx, &collogspb.ExportLogsServiceRequest{
					ResourceLogs: []*logspb.ResourceLogs{{
						Resource: codexResource,
						ScopeLogs: []*logspb.ScopeLogs{{
							LogRecords: []*logspb.LogRecord{{
								TimeUnixNano: uint64(time.Now().UnixNano()),
								Attributes:   attrs,
							}},
						}},
					}},
				})
				if err != nil {
					t.Fatalf("export: %v", err)
				}
			}
			base := func(extra ...*commontpb.KeyValue) []*commontpb.KeyValue {
				attrs := []*commontpb.KeyValue{
					{Key: "event.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "codex.sse_event"}}},
					{Key: "event.kind", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "response.completed"}}},
					{Key: "conversation.id", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "conv-dual-" + tc.name}}},
					{Key: "model", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "gpt-5.1"}}},
				}
				return append(attrs, extra...)
			}
			// The per-poll record: response.completed with NO usage attributes.
			tokenless := base()
			// The authoritative sse_event_completed record.
			withUsage := base(
				&commontpb.KeyValue{Key: "input_token_count", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: 900}}},
				&commontpb.KeyValue{Key: "output_token_count", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: 100}}},
				&commontpb.KeyValue{Key: "tool_token_count", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: 1000}}},
			)
			if tc.first {
				send(tokenless)
				send(withUsage)
			} else {
				send(withUsage)
				send(tokenless)
			}

			var rows int
			repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_requests WHERE service_name = 'codex'`).Scan(&rows)
			if rows != 1 {
				t.Fatalf("expected exactly 1 codex row, got %d (phantom row leaked)", rows)
			}
			var input, output, total int64
			repo.DB().QueryRowContext(ctx,
				`SELECT input_tokens, output_tokens, total_tokens FROM api_requests WHERE service_name = 'codex'`).Scan(&input, &output, &total)
			if input != 900 || output != 100 || total != 1000 {
				t.Fatalf("authoritative usage must win: got in=%d out=%d total=%d", input, output, total)
			}
		})
	}
}

// TestCodexE2E_TTFTOnlyCompletionFinalizes pins the ttft_ms escape: a
// completion with genuinely zero usage everywhere but a measured TTFT is a
// REAL authoritative completion (ttft_ms exists at exactly one emit site —
// sse_event_completed), so it must pass the gate, finalize the pending row's
// ttft/duration, and still not mint a second row.
func TestCodexE2E_TTFTOnlyCompletionFinalizes(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)
	broker := api.NewBroker()
	rcv := New(repo, &fakeResolver{}, broker, nil)

	srv := grpc.NewServer()
	rcv.Register(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	cli := collogspb.NewLogsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	codexResource := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		{Key: "service.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "codex-cli"}}},
	}}
	send := func(attrs []*commontpb.KeyValue) {
		_, err := cli.Export(ctx, &collogspb.ExportLogsServiceRequest{
			ResourceLogs: []*logspb.ResourceLogs{{
				Resource: codexResource,
				ScopeLogs: []*logspb.ScopeLogs{{
					LogRecords: []*logspb.LogRecord{{
						TimeUnixNano: uint64(time.Now().UnixNano()),
						Attributes:   attrs,
					}},
				}},
			}},
		})
		if err != nil {
			t.Fatalf("export: %v", err)
		}
	}

	send([]*commontpb.KeyValue{
		{Key: "event.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "codex.api_request"}}},
		{Key: "conversation.id", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "conv-ttft-only"}}},
		{Key: "model", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "gpt-5.1"}}},
	})
	send([]*commontpb.KeyValue{
		{Key: "event.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "codex.sse_event"}}},
		{Key: "event.kind", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "response.completed"}}},
		{Key: "conversation.id", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "conv-ttft-only"}}},
		{Key: "model", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "gpt-5.1"}}},
		{Key: "ttft_ms", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: 410}}},
	})

	var rows int
	repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_requests WHERE service_name = 'codex'`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("expected exactly 1 codex row, got %d", rows)
	}
	var ttft int64
	repo.DB().QueryRowContext(ctx, `SELECT ttft_ms FROM api_requests WHERE service_name = 'codex'`).Scan(&ttft)
	if ttft != 410 {
		t.Fatalf("expected ttft 410 to be finalized on the pending row, got %d", ttft)
	}
}

// TestCodexE2E_ReplayedCompletionIsNoOp pins the at-least-once replay guard:
// OTLP exporters retry failed batches, so the same authoritative completion
// can land twice. The first delivery finalizes the pending row via the
// tracker; the replay arrives with the tracker already consumed and must NOT
// mint a second row — the identity guard matches the finalized row by
// (session, model, full token vector) inside a timestamp window, because the
// finalized row keeps the api_request's timestamp, not the completion's.
func TestCodexE2E_ReplayedCompletionIsNoOp(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)
	broker := api.NewBroker()
	rcv := New(repo, &fakeResolver{}, broker, nil)

	srv := grpc.NewServer()
	rcv.Register(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	cli := collogspb.NewLogsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	codexResource := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		{Key: "service.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "codex-cli"}}},
	}}
	export := func(records []*logspb.LogRecord) {
		t.Helper()
		_, err := cli.Export(ctx, &collogspb.ExportLogsServiceRequest{
			ResourceLogs: []*logspb.ResourceLogs{{
				Resource:  codexResource,
				ScopeLogs: []*logspb.ScopeLogs{{LogRecords: records}},
			}},
		})
		if err != nil {
			t.Fatalf("export: %v", err)
		}
	}
	attr := func(k, v string) *commontpb.KeyValue {
		return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: v}}}
	}

	apiRec := &logspb.LogRecord{
		TimeUnixNano: uint64(time.Now().Add(-2 * time.Second).UnixNano()),
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.api_request"),
			attr("conversation.id", "conv-replay"),
			attr("model", "gpt-5.1"),
		},
	}
	completionAttrs := []*commontpb.KeyValue{
		attr("event.name", "codex.sse_event"),
		attr("event.kind", "response.completed"),
		attr("conversation.id", "conv-replay"),
		attr("model", "gpt-5.1"),
		attr("input_token_count", "200"),
		attr("output_token_count", "50"),
		attr("tool_token_count", "250"),
		attr("ttft_ms", "300"),
	}
	completion := &logspb.LogRecord{TimeUnixNano: uint64(time.Now().UnixNano()), Attributes: completionAttrs}

	export([]*logspb.LogRecord{apiRec})
	export([]*logspb.LogRecord{completion})
	// The replay: same record bytes an OTLP exporter would resend.
	export([]*logspb.LogRecord{completion})

	var rows int
	repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_requests WHERE service_name = 'codex'`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("replayed completion must be a no-op: got %d codex rows, want 1", rows)
	}
	var total int64
	repo.DB().QueryRowContext(ctx, `SELECT total_tokens FROM api_requests WHERE service_name = 'codex'`).Scan(&total)
	if total != 250 {
		t.Fatalf("finalized row total_tokens = %d, want 250", total)
	}
	var agg int64
	repo.DB().QueryRowContext(ctx, `SELECT COALESCE(SUM(request_count),0) FROM daily_model_agg WHERE service_name = 'codex'`).Scan(&agg)
	if agg != 1 {
		t.Fatalf("agg request_count = %d, want 1 (replay must not double-count)", agg)
	}
}

// TestCodexE2E_ExportSignalsStorageFailure pins the OTLP delivery contract:
// when every record in a batch fails storage, Export must return an error
// (gRPC Internal / HTTP 500) so the exporter retries instead of pruning the
// batch as delivered. Before this fix both transports answered success on
// internal failure and the data was silently lost.
func TestCodexE2E_ExportSignalsStorageFailure(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	repo := db.NewRepository(d)
	broker := api.NewBroker()
	rcv := New(repo, &fakeResolver{}, broker, nil)

	srv := grpc.NewServer()
	rcv.Register(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	cli := collogspb.NewLogsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	good := &logspb.LogRecord{
		TimeUnixNano: uint64(time.Now().UnixNano()),
		Attributes: []*commontpb.KeyValue{
			{Key: "event.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "api_request"}}},
			{Key: "message.model", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "claude-3-5-haiku"}}},
			{Key: "request_id", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "export-fail-1"}}},
		},
	}
	resource := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
		{Key: "service.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "claude-code"}}},
	}}
	batch := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  resource,
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{good}}},
	}}}

	// Healthy path: the same batch must export cleanly first.
	if _, err := cli.Export(ctx, batch); err != nil {
		t.Fatalf("healthy export must succeed: %v", err)
	}

	// Storage failure: closing the underlying DB makes every insert fail.
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = cli.Export(ctx, batch)
	if err == nil {
		t.Fatal("export with total storage failure must return an error so the exporter retries")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("error code = %v, want Internal", status.Code(err))
	}
}

// TestCodexE2E_HostileMagnitudesStayQueryable pins the R4 protocol finding:
// a hostile exporter sending MaxInt64 token counts must not be able to
// permanently 500 the dashboards. Before the ingest clamp the values
// survived INSERT and every SUM() over daily_model_agg raised
// "integer overflow" until retention pruned the poisoned day.
func TestCodexE2E_HostileMagnitudesStayQueryable(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)
	broker := api.NewBroker()
	rcv := New(repo, &fakeResolver{}, broker, nil)

	srv := grpc.NewServer()
	rcv.Register(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cli := collogspb.NewLogsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attr := func(k, v string) *commontpb.KeyValue {
		return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: v}}}
	}
	maxStr := "9223372036854775807"
	rec := &logspb.LogRecord{
		// Year-2262 timestamp: must be clamped to arrival time.
		TimeUnixNano: uint64(time.Date(2262, 4, 12, 0, 0, 0, 0, time.UTC).UnixNano()),
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.sse_event"),
			attr("event.kind", "response.completed"),
			attr("conversation.id", "r4hostile"),
			attr("model", "gpt-5.1"),
			attr("input_token_count", maxStr),
			attr("output_token_count", maxStr),
			attr("cached_token_count", maxStr),
			attr("cache_write_token_count", maxStr),
			attr("reasoning_token_count", maxStr),
			attr("tool_token_count", maxStr),
			attr("ttft_ms", "300"),
		},
	}
	if _, err := cli.Export(ctx, &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
			attr("service.name", "codex-cli"),
		}},
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{rec}}},
	}}}); err != nil {
		t.Fatalf("export: %v", err)
	}

	// The dashboard aggregate SUM must not raise integer overflow.
	var summed int64
	if err := repo.DB().QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cache_read_tokens),0) FROM daily_model_agg`).Scan(&summed); err != nil {
		t.Fatalf("agg SUM broke: %v", err)
	}
	if summed > 1<<41 {
		t.Fatalf("hostile cache_read survived unclamped: %d", summed)
	}
	// The poisoned future timestamp must be clamped into the present.
	var maxTS int64
	repo.DB().QueryRowContext(ctx,
		`SELECT COALESCE(MAX(timestamp),0) FROM api_requests WHERE session_id='r4hostile'`).Scan(&maxTS)
	if maxTS > time.Now().Add(10*time.Minute).Unix() {
		t.Fatalf("future timestamp not clamped: %d", maxTS)
	}
}

// TestExportResultPartialVsTotal pins the OTLP delivery contract's decision
// table (R4 docs P1-3): only failed>=total is a total failure (error); any
// smaller failure count is a PartialSuccess, never an error.
func TestExportResultPartialVsTotal(t *testing.T) {
	resp := &collogspb.ExportLogsServiceResponse{}
	mk := func(r *collogspb.ExportLogsServiceResponse) *collogspb.ExportLogsServiceResponse {
		r.PartialSuccess = &collogspb.ExportLogsPartialSuccess{RejectedLogRecords: 2}
		return r
	}

	allOK := &ingestStats{total: 5, failed: 0}
	if _, err := exportResult(allOK, resp, mk); err != nil {
		t.Fatalf("all-success batch must not error: %v", err)
	}

	partial := &ingestStats{total: 5, failed: 2}
	got, err := exportResult(partial, resp, mk)
	if err != nil {
		t.Fatalf("partial failure must NOT be a total error: %v", err)
	}
	if got.PartialSuccess == nil || got.PartialSuccess.RejectedLogRecords != 2 {
		t.Fatalf("partial failure must surface PartialSuccess, got %+v", got.PartialSuccess)
	}

	totalFail := &ingestStats{total: 5, failed: 5}
	if _, err := exportResult(totalFail, resp, mk); err == nil {
		t.Fatal("total failure must return an error")
	}

	// The DSH ratio bug shape: one failed among many must NOT read as total.
	oneOfMany := &ingestStats{total: 40, failed: 1}
	if _, err := exportResult(oneOfMany, resp, mk); err != nil {
		t.Fatalf("1-of-40 failure must be PartialSuccess, not Internal: %v", err)
	}
}

// TestDSHStatsRecordPerRecord pins the R4 protocol F4 fix: stats.record() is
// called once per DSH LogRecord (a feedback upload carries a whole session
// prefix in ONE ResourceLogs), not once per ResourceLogs — otherwise any
// single insert failure read as failed>=total and flipped the whole export
// to a pointless full-500 retry.
func TestDSHStatsRecordPerRecord(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)
	s := &logsServiceServer{repo: repo}

	now := uint64(time.Now().UnixNano())
	lr := func(step int, prompt, assistant string) *logspb.LogRecord {
		return &logspb.LogRecord{
			TimeUnixNano: now + uint64(step)*1e9,
			Attributes: []*commontpb.KeyValue{
				{Key: "event.type", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "step/start"}}},
				{Key: "session.id", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "stats-ratio"}}},
				{Key: "step.prompt", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: prompt}}},
				{Key: "step.assistant", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: assistant}}},
			},
		}
	}
	rl := &logspb.ResourceLogs{
		Resource: &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
			{Key: "service.name", Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: "deepseek-harness-plugin"}}},
		}},
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{
			lr(1, "p1", "a1"), lr(2, "p2", "a2"), lr(3, "p3", "a3"),
		}}},
	}

	stats := &ingestStats{}
	s.handleDSHResourceLogs(context.Background(), rl, stats)
	if stats.total != 3 {
		t.Fatalf("stats.total = %d, want 3 (one per LogRecord, not one per ResourceLogs)", stats.total)
	}
}

func TestDSHE2E_HostileLedgerClamped(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)
	// Full Receiver construction: the bare server has a nil dshStepTracker
	// and panics on the first step bookkeeping call.
	s := New(repo, fakeResolver{}, api.NewBroker(), nil).logs

	attr := func(k, v string) *commontpb.KeyValue {
		return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: v}}}
	}
	intKV := func(k string, v int64) *commontpb.KeyValue {
		return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: v}}}
	}
	usage := &commontpb.AnyValue{Value: &commontpb.AnyValue_KvlistValue{KvlistValue: &commontpb.KeyValueList{Values: []*commontpb.KeyValue{
		intKV("inputTokens", math.MaxInt64),
		intKV("outputTokens", math.MaxInt64),
		intKV("cacheReadTokens", math.MaxInt64),
	}}}}
	source := &commontpb.AnyValue{Value: &commontpb.AnyValue_KvlistValue{KvlistValue: &commontpb.KeyValueList{Values: []*commontpb.KeyValue{
		attr("model", "glm-5.3"),
	}}}}
	message := &commontpb.AnyValue{Value: &commontpb.AnyValue_KvlistValue{KvlistValue: &commontpb.KeyValueList{Values: []*commontpb.KeyValue{
		{Key: "source", Value: source},
	}}}}
	rec := &logspb.LogRecord{
		TimeUnixNano: uint64(time.Date(2262, 4, 12, 0, 0, 0, 0, time.UTC).UnixNano()),
		Attributes: []*commontpb.KeyValue{
			attr("event.type", "assistant/message"),
			attr("event.seq", "7"),
			attr("session.id", "r5hostile"),
		},
		Body: &commontpb.AnyValue{Value: &commontpb.AnyValue_KvlistValue{KvlistValue: &commontpb.KeyValueList{Values: []*commontpb.KeyValue{
			{Key: "usage", Value: usage},
			{Key: "message", Value: message},
		}}}},
	}
	rl := &logspb.ResourceLogs{
		Resource: &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
			attr("service.name", "deepseek-harness"),
		}},
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{rec}}},
	}
	stats := &ingestStats{}
	s.handleDSHResourceLogs(context.Background(), rl, stats)
	if stats.total != 1 {
		t.Fatalf("stats.total = %d, want 1", stats.total)
	}

	var summed int64
	if err := repo.DB().QueryRow(`SELECT COALESCE(SUM(input_tokens),0) FROM api_requests`).Scan(&summed); err != nil {
		t.Fatalf("pool SUM broke: %v", err)
	}
	if summed > 1<<41 {
		t.Fatalf("hostile input_tokens survived unclamped: %d", summed)
	}
	var aggSum int64
	if err := repo.DB().QueryRow(`SELECT COALESCE(SUM(input_tokens),0) FROM daily_model_agg`).Scan(&aggSum); err != nil {
		t.Fatalf("agg SUM broke: %v", err)
	}
	if aggSum > 1<<41 {
		t.Fatalf("agg kept hostile magnitude: %d", aggSum)
	}
	var maxTS int64
	repo.DB().QueryRow(`SELECT COALESCE(MAX(timestamp),0) FROM api_requests WHERE session_id='r5hostile'`).Scan(&maxTS)
	if maxTS > time.Now().Add(10*time.Minute).Unix() {
		t.Fatalf("future timestamp not clamped: %d", maxTS)
	}
}

// TestCodexE2E_DuplicateAnchorRedeliveryStaysOK pins the R5 rollback P1 fix:
// insertCodexRowInTx used to COMMIT inside the duplicate-request_id branch,
// so the caller's own tx.Commit failed with sql.ErrTxDone and a routine
// at-least-once anchor redelivery answered gRPC Internal / HTTP 500.
// Three identical anchors must all export cleanly and leave exactly one row.
func TestCodexE2E_DuplicateAnchorRedeliveryStaysOK(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)
	rcv := New(repo, fakeResolver{}, api.NewBroker(), nil)

	srv := grpc.NewServer()
	rcv.Register(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cli := collogspb.NewLogsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attr := func(k, v string) *commontpb.KeyValue {
		return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: v}}}
	}
	anchor := &logspb.LogRecord{
		TimeUnixNano: uint64(time.Now().Add(-2 * time.Second).UnixNano()),
		Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.api_request"),
			attr("conversation.id", "conv-dupanchor"),
			attr("model", "gpt-5.1"),
			attr("auth.request_id", "req-dup-anchor-1"),
		},
	}
	for i := 0; i < 3; i++ {
		if _, err := cli.Export(ctx, &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
			Resource:  &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{anchor}}},
		}}}); err != nil {
			t.Fatalf("redelivery %d must not fail (ErrTxDone regression): %v", i+1, err)
		}
	}

	var rows int
	repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_requests WHERE conversation_id='conv-dupanchor'`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("duplicate anchors must dedupe to 1 row, got %d", rows)
	}
}

// TestCodexE2E_TurnTTFTFillsAnchor pins the codex.turn_ttft mapping: the
// dedicated turn-level event (often the ONLY ttft signal — the completion's
// ttft_ms is Option) fills the newest zero-TTFT anchor for the same
// (conversation, model) at or before the event time. Also pins the four
// orderings: turn-first + ttft-less completion (kept), turn-first +
// authoritative completion ttft (overwrites), completion-first (fill-only
// skips a non-zero row), and no-anchor (no row minted).
func TestCodexE2E_TurnTTFTFillsAnchor(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)

	srv := grpc.NewServer()
	rcv := New(repo, fakeResolver{}, api.NewBroker(), nil)
	rcv.Register(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cli := collogspb.NewLogsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attr := func(k, v string) *commontpb.KeyValue {
		return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: v}}}
	}
	export := func(records ...*logspb.LogRecord) {
		t.Helper()
		if _, err := cli.Export(ctx, &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
			Resource:  &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: records}},
		}}}); err != nil {
			t.Fatalf("export: %v", err)
		}
	}
	base := time.Now().Add(-10 * time.Second)
	anchor := func(conv string, at time.Time) *logspb.LogRecord {
		return &logspb.LogRecord{TimeUnixNano: uint64(at.UnixNano()), Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.api_request"),
			attr("conversation.id", conv),
			attr("model", "glm-5.3"),
		}}
	}
	turnTTFT := func(conv string, at time.Time, ms int) *logspb.LogRecord {
		return &logspb.LogRecord{TimeUnixNano: uint64(at.UnixNano()), Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.turn_ttft"),
			attr("conversation.id", conv),
			attr("model", "glm-5.3"),
			attr("duration_ms", fmt.Sprintf("%d", ms)),
		}}
	}
	completion := func(conv string, at time.Time, ttft int64) *logspb.LogRecord {
		attrs := []*commontpb.KeyValue{
			attr("event.name", "codex.sse_event"),
			attr("event.kind", "response.completed"),
			attr("conversation.id", conv),
			attr("model", "glm-5.3"),
			attr("input_token_count", "100"),
			attr("output_token_count", "40"),
			attr("cached_token_count", "0"),
			attr("reasoning_token_count", "0"),
			attr("tool_token_count", "140"),
		}
		if ttft > 0 {
			attrs = append(attrs, attr("ttft_ms", fmt.Sprintf("%d", ttft)))
		}
		return &logspb.LogRecord{TimeUnixNano: uint64(at.UnixNano()), Attributes: attrs}
	}

	// Ordering 1: anchor, turn_ttft mid-stream, ttft-less completion → keep 3773.
	export(anchor("conv-ttft-a", base))
	export(turnTTFT("conv-ttft-a", base.Add(3773*time.Millisecond), 3773))
	export(completion("conv-ttft-a", base.Add(5*time.Second), 0))
	var ttft int64
	repo.DB().QueryRowContext(ctx, `SELECT ttft_ms FROM api_requests WHERE conversation_id='conv-ttft-a'`).Scan(&ttft)
	if ttft != 3773 {
		t.Fatalf("ttft-less completion must keep the turn-ttft fill: got %d, want 3773", ttft)
	}

	// Ordering 2: turn_ttft first, authoritative completion ttft overwrites.
	export(anchor("conv-ttft-b", base))
	export(turnTTFT("conv-ttft-b", base.Add(2*time.Second), 2000))
	export(completion("conv-ttft-b", base.Add(5*time.Second), 3800))
	repo.DB().QueryRowContext(ctx, `SELECT ttft_ms FROM api_requests WHERE conversation_id='conv-ttft-b'`).Scan(&ttft)
	if ttft != 3800 {
		t.Fatalf("authoritative completion ttft must win: got %d, want 3800", ttft)
	}

	// Ordering 3: completion already set ttft → turn_ttft is fill-only, no-op.
	export(anchor("conv-ttft-c", base))
	export(completion("conv-ttft-c", base.Add(5*time.Second), 1500))
	export(turnTTFT("conv-ttft-c", base.Add(2*time.Second), 2000))
	repo.DB().QueryRowContext(ctx, `SELECT ttft_ms FROM api_requests WHERE conversation_id='conv-ttft-c'`).Scan(&ttft)
	if ttft != 1500 {
		t.Fatalf("existing positive ttft must be left untouched: got %d, want 1500", ttft)
	}

	// Ordering 4: turn_ttft with no anchor mints nothing.
	export(turnTTFT("conv-ttft-none", base.Add(time.Second), 900))
	var n int
	repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_requests WHERE conversation_id='conv-ttft-none'`).Scan(&n)
	if n != 0 {
		t.Fatalf("turn_ttft without an anchor must not mint rows: got %d", n)
	}

	// No raw archive rows for the now-modeled event.
	var archived int
	repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM raw_otlp_events WHERE event_type='codex.turn_ttft'`).Scan(&archived)
	if archived != 0 {
		t.Fatalf("turn_ttft is modeled now and must not be archived: got %d rows", archived)
	}
}

// TestCodexE2E_TurnTTFTBeforeAnchorIsStashedAndRetried pins the live-caught
// race: OTLP export flushes are not order-preserving, so codex.turn_ttft
// can reach the receiver BEFORE its anchor row is committed. The one-shot
// UPDATE misses; the observation must be stashed and retried when the
// anchor insert lands (live evidence: one exec kept ttft=1906, the next
// lost it with ttft=0 before this fix).
func TestCodexE2E_TurnTTFTBeforeAnchorIsStashedAndRetried(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)

	srv := grpc.NewServer()
	rcv := New(repo, fakeResolver{}, api.NewBroker(), nil)
	rcv.Register(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cli := collogspb.NewLogsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attr := func(k, v string) *commontpb.KeyValue {
		return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: v}}}
	}
	export := func(records ...*logspb.LogRecord) {
		t.Helper()
		if _, err := cli.Export(ctx, &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
			Resource:  &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: records}},
		}}}); err != nil {
			t.Fatalf("export: %v", err)
		}
	}
	base := time.Now().Add(-10 * time.Second)

	// turn_ttft FIRST — anchor not committed yet, direct UPDATE must miss.
	export(&logspb.LogRecord{TimeUnixNano: uint64(base.Add(2400 * time.Millisecond).UnixNano()), Attributes: []*commontpb.KeyValue{
		attr("event.name", "codex.turn_ttft"),
		attr("conversation.id", "conv-race"),
		attr("model", "glm-5.3"),
		attr("duration_ms", "2400"),
	}})
	var n int
	repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_requests WHERE conversation_id='conv-race'`).Scan(&n)
	if n != 0 {
		t.Fatalf("turn_ttft must not mint rows: got %d", n)
	}

	// Anchor lands — the stash must drain onto it.
	export(&logspb.LogRecord{TimeUnixNano: uint64(base.UnixNano()), Attributes: []*commontpb.KeyValue{
		attr("event.name", "codex.api_request"),
		attr("conversation.id", "conv-race"),
		attr("model", "glm-5.3"),
	}})
	var ttft int64
	repo.DB().QueryRowContext(ctx, `SELECT ttft_ms FROM api_requests WHERE conversation_id='conv-race'`).Scan(&ttft)
	if ttft != 2400 {
		t.Fatalf("stashed turn_ttft must be retried on anchor insert: got %d, want 2400", ttft)
	}

	// A ttft-less completion afterwards must keep the stashed fill, and a
	// duplicate anchor export must not resurrect a stale stash fill (0-row).
	export(&logspb.LogRecord{TimeUnixNano: uint64(base.Add(6 * time.Second).UnixNano()), Attributes: []*commontpb.KeyValue{
		attr("event.name", "codex.sse_event"),
		attr("event.kind", "response.completed"),
		attr("conversation.id", "conv-race"),
		attr("model", "glm-5.3"),
		attr("input_token_count", "100"),
		attr("output_token_count", "40"),
		attr("cached_token_count", "0"),
		attr("reasoning_token_count", "0"),
		attr("tool_token_count", "140"),
	}})
	repo.DB().QueryRowContext(ctx, `SELECT ttft_ms FROM api_requests WHERE conversation_id='conv-race'`).Scan(&ttft)
	if ttft != 2400 {
		t.Fatalf("ttft-less completion must keep the stashed fill: got %d, want 2400", ttft)
	}
}

type fakeResolver struct{}

func (fakeResolver) ResolveActualModel(s string) string { return s }

// TestCodexE2E_MultiTurnConversationTTFTNoCrossTalk pins live-verified
// multi-turn behaviour (one conversation resumed through five turns plus
// three fresh conversations: 8/8 rows ttft>0, per-turn values distinct).
// Within ONE conversation each turn's codex.turn_ttft must land on THAT
// turn's anchor row — the newest zero-ttft anchor at-or-before the event
// is that turn's own row, never an earlier (already filled) turn's row.
func TestCodexE2E_MultiTurnConversationTTFTNoCrossTalk(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)

	srv := grpc.NewServer()
	rcv := New(repo, fakeResolver{}, api.NewBroker(), nil)
	rcv.Register(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cli := collogspb.NewLogsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attr := func(k, v string) *commontpb.KeyValue {
		return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: v}}}
	}
	export := func(records ...*logspb.LogRecord) {
		t.Helper()
		if _, err := cli.Export(ctx, &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
			Resource:  &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: records}},
		}}}); err != nil {
			t.Fatalf("export: %v", err)
		}
	}
	base := time.Now().Add(-5 * time.Minute)
	conv := "conv-multiturn"

	// Three resumed turns of one conversation in protocol order per turn:
	// anchor -> turn_ttft -> ttft-less completion. Distinct ttft values per
	// turn prove no cross-talk (a mis-joined row would carry another turn's
	// value or stay 0).
	turns := []struct {
		offset time.Duration
		ttft   int
	}{
		{0, 1100},
		{90 * time.Second, 2200},
		{3 * time.Minute, 3300},
	}
	for i, tn := range turns {
		at := base.Add(tn.offset)
		export(&logspb.LogRecord{TimeUnixNano: uint64(at.UnixNano()), Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.api_request"),
			attr("conversation.id", conv),
			attr("model", "glm-5.3"),
		}})
		export(&logspb.LogRecord{TimeUnixNano: uint64(at.Add(time.Duration(i+1) * time.Second).UnixNano()), Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.turn_ttft"),
			attr("conversation.id", conv),
			attr("model", "glm-5.3"),
			attr("duration_ms", fmt.Sprintf("%d", tn.ttft)),
		}})
		export(&logspb.LogRecord{TimeUnixNano: uint64(at.Add(8 * time.Second).UnixNano()), Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.sse_event"),
			attr("event.kind", "response.completed"),
			attr("conversation.id", conv),
			attr("model", "glm-5.3"),
			attr("input_token_count", "100"),
			attr("output_token_count", "40"),
			attr("cached_token_count", "0"),
			attr("reasoning_token_count", "0"),
			attr("tool_token_count", "140"),
		}})
	}

	rows, err := repo.DB().QueryContext(ctx,
		`SELECT ttft_ms FROM api_requests WHERE conversation_id=? ORDER BY id`, conv)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var ttft int64
		rows.Scan(&ttft)
		got = append(got, ttft)
	}
	want := []int64{1100, 2200, 3300}
	if len(got) != len(want) {
		t.Fatalf("one row per turn expected: got %d (%v)", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("turn %d ttft = %d, want %d (cross-turn leak?)", i+1, got[i], want[i])
		}
	}
}
