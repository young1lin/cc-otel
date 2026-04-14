package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync/atomic"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commontpb "go.opentelemetry.io/proto/otlp/common/v1"

	"google.golang.org/grpc"
)

const maxMessages = 20

func main() {
	var count int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outFile, err := os.Create("integration-test/debug-samples.jsonl")
	if err != nil {
		log.Fatalf("create output: %v", err)
	}
	defer outFile.Close()

	grpcSrv := grpc.NewServer()
	collogspb.RegisterLogsServiceServer(grpcSrv, &debugLogs{count: &count, out: outFile})
	colmetricspb.RegisterMetricsServiceServer(grpcSrv, &debugMetrics{count: &count, out: outFile})

	grpcLis, err := net.Listen("tcp", ":14317")
	if err != nil {
		log.Fatalf("listen :14317: %v", err)
	}

	fmt.Printf("=== cc-otel debug receiver ===\n")
	fmt.Printf("Listening on :14317 (OTLP gRPC)\n")
	fmt.Printf("Will collect %d messages then exit\n\n", maxMessages)
	fmt.Printf("Output: integration-test/debug-samples.jsonl\n")

	go func() {
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Fatalf("gRPC serve: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh)

	select {
	case <-sigCh:
		fmt.Println("\nInterrupted")
	case <-ctx.Done():
	}

	grpcSrv.GracefulStop()
	fmt.Printf("\nDone. Collected %d messages.\n", atomic.LoadInt64(&count))
}

// ── Logs service ───────────────────────────────────────────────────────────

type debugLogs struct{ collogspb.UnimplementedLogsServiceServer; count *int64; out *os.File }

func (s *debugLogs) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	n := atomic.AddInt64(s.count, 1)

	for _, rl := range req.ResourceLogs {
		entry := map[string]interface{}{
			"_type":    "log",
			"_msgNum":  n,
			"resource": flatAttrs(rl.Resource.Attributes),
		}

		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				rec := make(map[string]interface{})
				for k, v := range entry {
					rec[k] = v
				}
				rec["scope"] = fmt.Sprintf("%s/%s", sl.Scope.Name, sl.Scope.Version)
				rec["time_unix_nano"] = lr.TimeUnixNano
				rec["observed_time_unix_nano"] = lr.ObservedTimeUnixNano
				rec["severity_number"] = int32(lr.SeverityNumber)
				rec["severity_text"] = lr.SeverityText
				rec["body"] = anyVal(lr.Body)
				rec["flags"] = lr.Flags
				rec["attributes"] = flatAttrs(lr.Attributes)

				line, _ := json.Marshal(rec)
				fmt.Fprintln(s.out, string(line))
				fmt.Fprintf(os.Stdout, "[%d] log: body=%s attrs=%+v\n", n, anyVal(lr.Body), rec["attributes"])
			}
		}
	}

	if atomic.LoadInt64(s.count) >= maxMessages {
		fmt.Printf("\n=== Collected %d messages, exiting ===\n", maxMessages)
		os.Exit(0)
	}
	return &collogspb.ExportLogsServiceResponse{}, nil
}

// ── Metrics service ────────────────────────────────────────────────────────

type debugMetrics struct{ colmetricspb.UnimplementedMetricsServiceServer; count *int64; out *os.File }

func (s *debugMetrics) Export(ctx context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	n := atomic.AddInt64(s.count, 1)

	for _, rm := range req.ResourceMetrics {
		entry := map[string]interface{}{
			"_type":    "metric",
			"_msgNum":  n,
			"resource": flatAttrs(rm.Resource.Attributes),
		}

		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				rec := make(map[string]interface{})
				for k, v := range entry {
					rec[k] = v
				}
				rec["scope"] = fmt.Sprintf("%s/%s", sm.Scope.Name, sm.Scope.Version)
				rec["metric_name"] = m.Name
				rec["unit"] = m.Unit
				rec["description"] = m.Description

				if sum := m.GetSum(); sum != nil {
					rec["data_type"] = "sum"
					rec["is_monotonic"] = sum.IsMonotonic
					var dps []map[string]interface{}
					for _, dp := range sum.DataPoints {
						dps = append(dps, map[string]interface{}{"time": dp.TimeUnixNano, "value": dp.GetAsDouble(), "attrs": flatAttrs(dp.Attributes)})
					}
					rec["data_points"] = dps
				} else if gauge := m.GetGauge(); gauge != nil {
					rec["data_type"] = "gauge"
					var dps []map[string]interface{}
					for _, dp := range gauge.DataPoints {
						dps = append(dps, map[string]interface{}{"time": dp.TimeUnixNano, "value": dp.GetAsDouble(), "attrs": flatAttrs(dp.Attributes)})
					}
					rec["data_points"] = dps
				} else if hist := m.GetHistogram(); hist != nil {
					rec["data_type"] = "histogram"
					var dps []map[string]interface{}
					for _, dp := range hist.DataPoints {
						sumV := float64(0)
						if dp.Sum != nil { sumV = *dp.Sum }
						dps = append(dps, map[string]interface{}{"time": dp.TimeUnixNano, "count": dp.Count, "sum": sumV, "attrs": flatAttrs(dp.Attributes)})
					}
					rec["data_points"] = dps
				}

				line, _ := json.Marshal(rec)
				fmt.Fprintln(s.out, string(line))
				fmt.Fprintf(os.Stdout, "[%d] metric: name=%s type=%s\n", n, m.Name, rec["data_type"])
			}
		}
	}

	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────

func flatAttrs(attrs []*commontpb.KeyValue) map[string]string {
	m := make(map[string]string)
	for _, kv := range attrs {
		m[kv.Key] = anyVal(kv.Value)
	}
	return m
}

func anyVal(v *commontpb.AnyValue) string {
	if v == nil { return "" }
	switch val := v.Value.(type) {
	case *commontpb.AnyValue_StringValue:
		return val.StringValue
	case *commontpb.AnyValue_IntValue:
		return fmt.Sprintf("%d", val.IntValue)
	case *commontpb.AnyValue_DoubleValue:
		return fmt.Sprintf("%.6f", val.DoubleValue)
	case *commontpb.AnyValue_BoolValue:
		return fmt.Sprintf("%t", val.BoolValue)
	case *commontpb.AnyValue_BytesValue:
		return fmt.Sprintf("<bytes:%d>", len(val.BytesValue))
	default:
		return ""
	}
}
