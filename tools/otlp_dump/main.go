// Temporary OTLP gRPC dump tool — listens on a port, prints all received LogRecords as JSON.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
)

type dumpServer struct {
	collogspb.UnimplementedLogsServiceServer
}

type noopMetricsServer struct {
	colmetricspb.UnimplementedMetricsServiceServer
}

type noopTraceServer struct {
	coltracepb.UnimplementedTraceServiceServer
}

func valToStr(v *commonpb.AnyValue) interface{} {
	if v == nil {
		return nil
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_ArrayValue:
		arr := make([]interface{}, len(x.ArrayValue.Values))
		for i, v := range x.ArrayValue.Values {
			arr[i] = valToStr(v)
		}
		return arr
	case *commonpb.AnyValue_KvlistValue:
		m := make(map[string]interface{})
		for _, kv := range x.KvlistValue.Values {
			m[kv.Key] = valToStr(kv.Value)
		}
		return m
	default:
		return fmt.Sprintf("<unknown:%T>", v.Value)
	}
}

func attrsToMap(kvs []*commonpb.KeyValue) map[string]interface{} {
	m := make(map[string]interface{}, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = valToStr(kv.Value)
	}
	return m
}

func (s *dumpServer) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	for i, rl := range req.ResourceLogs {
		resAttrs := map[string]interface{}{}
		if rl.Resource != nil {
			resAttrs = attrsToMap(rl.Resource.Attributes)
		}

		for j, sl := range rl.ScopeLogs {
			for k, lr := range sl.LogRecords {
				entry := map[string]interface{}{
					"resource_idx": i,
					"scope_idx":    j,
					"log_idx":      k,
					"resource_attrs": resAttrs,
					"log_attrs":      attrsToMap(lr.Attributes),
					"time_unix_nano": lr.TimeUnixNano,
					"observed_time":  lr.ObservedTimeUnixNano,
					"severity_number": lr.SeverityNumber,
					"body":           valToStr(lr.Body),
				}

				b, _ := json.MarshalIndent(entry, "", "  ")
				fmt.Println(string(b))
				fmt.Println("---")
			}
		}
	}
	return &collogspb.ExportLogsServiceResponse{}, nil
}

func (s *noopMetricsServer) Export(ctx context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

func (s *noopTraceServer) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

func main() {
	port := "12233"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("listen :%s: %v", port, err)
	}

	s := grpc.NewServer()
	collogspb.RegisterLogsServiceServer(s, &dumpServer{})
	colmetricspb.RegisterMetricsServiceServer(s, &noopMetricsServer{})
	coltracepb.RegisterTraceServiceServer(s, &noopTraceServer{})

	// ensure gzip compressor is registered
	_ = gzip.Name

	fmt.Fprintf(os.Stderr, "OTLP dump server listening on :%s — press Ctrl+C to stop\n", port)

	go func() {
		time.Sleep(300 * time.Second)
		fmt.Fprintln(os.Stderr, "\n5 min timeout reached, shutting down")
		s.GracefulStop()
	}()

	if err := s.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

var _ = sort.Sort
