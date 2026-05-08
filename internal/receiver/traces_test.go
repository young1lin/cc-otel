package receiver

import (
	"context"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestTraceTTFTBackfillUsesRequestIDBeforeSessionModel(t *testing.T) {
	database, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := db.NewRepository(database)
	ctx := context.Background()
	ts := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC)

	_, err = repo.InsertRequest(ctx, &db.APIRequest{
		Timestamp: ts,
		SessionID: "api-session",
		PromptID:  "prompt-1",
		Model:     "claude-opus-4-7",
		RequestID: "req-trace",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := &tracesServiceServer{repo: repo}
	_, err = srv.Export(ctx, &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					Name:              "claude_code.llm_request",
					EndTimeUnixNano:   uint64(ts.UnixNano()),
					StartTimeUnixNano: uint64(ts.Add(-2 * time.Second).UnixNano()),
					Attributes: []*commontpb.KeyValue{
						strAttr("request_id", "req-trace"),
						strAttr("session.id", "trace-session"),
						strAttr("model", "claude-opus-4-7"),
						intAttr("ttft_ms", 3409),
					},
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var ttft int64
	if err := database.QueryRowContext(ctx, `SELECT ttft_ms FROM api_requests WHERE request_id = 'req-trace'`).Scan(&ttft); err != nil {
		t.Fatal(err)
	}
	if ttft != 3409 {
		t.Fatalf("expected ttft=3409, got %d", ttft)
	}
}
