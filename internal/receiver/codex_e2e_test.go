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
	})

	var rows int
	repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_api_requests`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("expected 1 codex_api_requests row after correlation, got %d", rows)
	}

	var input int64
	repo.DB().QueryRowContext(ctx, `SELECT input_tokens FROM codex_api_requests`).Scan(&input)
	if input != 1500 {
		t.Fatalf("expected 1500 input tokens, got %d", input)
	}

	var output int64
	repo.DB().QueryRowContext(ctx, `SELECT output_tokens FROM codex_api_requests`).Scan(&output)
	if output != 250 {
		t.Fatalf("expected 250 output tokens, got %d", output)
	}
}

type fakeResolver struct{}

func (fakeResolver) ResolveActualModel(s string) string { return s }
