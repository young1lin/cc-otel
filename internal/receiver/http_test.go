package receiver

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"
)

// OTLP/HTTP exporters may gzip their payload, and real ones do: DeepSeek
// Harness's shipped composition sets compression: gzip. Without transparent
// decompression the export decodes as garbage and the batch is dropped.
func TestOTLPHTTP_AcceptsGzippedExport(t *testing.T) {
	rcv, repo := newDSHTestReceiver(t)
	srv := httptest.NewServer(rcv.HTTPHandler())
	t.Cleanup(srv.Close)

	payload, err := proto.Marshal(&collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: dshResource(),
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope:      &commontpb.InstrumentationScope{Name: dshScopeName},
				LogRecords: dshStepRecords(int64(1_700_000_000) * dshSecond),
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/logs", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// A 2xx that stored nothing is worse than an error: the exporter drops the
	// batch believing it was delivered.
	var rows int
	if err := repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM api_requests`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("api_requests rows = %d after a gzipped export, want 1", rows)
	}
}

// The uncompressed path must keep working unchanged.
func TestOTLPHTTP_AcceptsUncompressedExport(t *testing.T) {
	rcv, repo := newDSHTestReceiver(t)
	srv := httptest.NewServer(rcv.HTTPHandler())
	t.Cleanup(srv.Close)

	payload, err := proto.Marshal(&collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: dshResource(),
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope:      &commontpb.InstrumentationScope{Name: dshScopeName},
				LogRecords: dshStepRecords(int64(1_700_000_000) * dshSecond),
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := srv.Client().Post(srv.URL+"/v1/logs", "application/x-protobuf", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var rows int
	if err := repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM api_requests`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("api_requests rows = %d, want 1", rows)
	}
}
