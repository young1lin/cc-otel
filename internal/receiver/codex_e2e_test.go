package receiver

import (
	"context"
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
				Resource: codexResource,
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
		Resource: resource,
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

type fakeResolver struct{}

func (fakeResolver) ResolveActualModel(s string) string { return s }
