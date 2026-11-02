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
	"google.golang.org/grpc/credentials/insecure"
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

type fakeResolver struct{}

func (fakeResolver) ResolveActualModel(s string) string { return s }
