// OTLP dump tool — listens for OTLP telemetry and prints everything it receives
// as JSON, without storing any of it.
//
// Use it when onboarding a new client: point the client here instead of at
// cc-otel to see exactly which attributes it emits, and — for Vercel AI SDK
// clients such as opencode — what cc-otel would turn each usage span into.
//
// Both transports are served, because clients differ: Claude Code and Codex
// speak OTLP gRPC, while opencode and other AI SDK apps speak OTLP/HTTP.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/young1lin/cc-otel/internal/receiver"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type dumpServer struct {
	collogspb.UnimplementedLogsServiceServer
}

type noopMetricsServer struct {
	colmetricspb.UnimplementedMetricsServiceServer
}

type traceDumpServer struct {
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

func emit(entry map[string]interface{}) {
	b, _ := json.MarshalIndent(entry, "", "  ")
	fmt.Println(string(b))
	fmt.Println("---")
}

func dumpLogs(req *collogspb.ExportLogsServiceRequest, transport string) {
	for i, rl := range req.ResourceLogs {
		resAttrs := map[string]interface{}{}
		if rl.Resource != nil {
			resAttrs = attrsToMap(rl.Resource.Attributes)
		}
		for j, sl := range rl.ScopeLogs {
			for k, lr := range sl.LogRecords {
				emit(map[string]interface{}{
					"kind":            "log",
					"transport":       transport,
					"resource_idx":    i,
					"scope_idx":       j,
					"log_idx":         k,
					"resource_attrs":  resAttrs,
					"log_attrs":       attrsToMap(lr.Attributes),
					"time_unix_nano":  lr.TimeUnixNano,
					"observed_time":   lr.ObservedTimeUnixNano,
					"severity_number": lr.SeverityNumber,
					"body":            valToStr(lr.Body),
				})
			}
		}
	}
}

// dumpTraces prints every span and, for AI SDK usage spans, the api_request
// cc-otel would derive from it. The mapping comes from the receiver package, so
// what you see here is what ingestion would actually store.
func dumpTraces(req *coltracepb.ExportTraceServiceRequest, transport string) {
	for i, rs := range req.ResourceSpans {
		resAttrs := map[string]interface{}{}
		if rs.Resource != nil {
			resAttrs = attrsToMap(rs.Resource.Attributes)
		}
		for j, ss := range rs.ScopeSpans {
			scope := ""
			if ss.Scope != nil {
				scope = ss.Scope.Name
			}
			for k, sp := range ss.Spans {
				entry := map[string]interface{}{
					"kind":           "span",
					"transport":      transport,
					"resource_idx":   i,
					"scope_idx":      j,
					"span_idx":       k,
					"scope_name":     scope,
					"span_name":      sp.Name,
					"trace_id":       hex.EncodeToString(sp.TraceId),
					"span_id":        hex.EncodeToString(sp.SpanId),
					"duration_ms":    spanDurationMs(sp),
					"resource_attrs": resAttrs,
					"span_attrs":     attrsToMap(sp.Attributes),
				}
				if mapped, ok := receiver.DebugMapAISDKSpan(sp, rs.Resource); ok {
					entry["would_store_api_request"] = mapped
				}
				emit(entry)
			}
		}
	}
}

func spanDurationMs(sp *tracepb.Span) float64 {
	if sp.EndTimeUnixNano > sp.StartTimeUnixNano {
		return float64(sp.EndTimeUnixNano-sp.StartTimeUnixNano) / 1e6
	}
	return 0
}

func (s *dumpServer) Export(_ context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	dumpLogs(req, "grpc")
	return &collogspb.ExportLogsServiceResponse{}, nil
}

func (s *noopMetricsServer) Export(_ context.Context, _ *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

func (s *traceDumpServer) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	dumpTraces(req, "grpc")
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// httpDumpHandler mirrors the receiver's OTLP/HTTP shim closely enough to accept
// the same clients: protobuf or JSON in, same encoding back out.
func httpDumpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/traces", otlpDumpEndpoint(
		func() proto.Message { return &coltracepb.ExportTraceServiceRequest{} },
		func(m proto.Message) proto.Message {
			dumpTraces(m.(*coltracepb.ExportTraceServiceRequest), "http")
			return &coltracepb.ExportTraceServiceResponse{}
		}))
	mux.Handle("/v1/logs", otlpDumpEndpoint(
		func() proto.Message { return &collogspb.ExportLogsServiceRequest{} },
		func(m proto.Message) proto.Message {
			dumpLogs(m.(*collogspb.ExportLogsServiceRequest), "http")
			return &collogspb.ExportLogsServiceResponse{}
		}))
	mux.Handle("/v1/metrics", otlpDumpEndpoint(
		func() proto.Message { return &colmetricspb.ExportMetricsServiceRequest{} },
		func(proto.Message) proto.Message { return &colmetricspb.ExportMetricsServiceResponse{} }))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(os.Stderr, "[http] unknown path %s %s\n", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	return mux
}

func otlpDumpEndpoint(newReq func() proto.Message, handle func(proto.Message) proto.Message) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Same body reading as the daemon's shim, so a client that works
		// against cc-otel — gzip included, which DSH always sends — dumps here
		// too instead of failing as "malformed".
		body, err := receiver.ReadOTLPBody(w, r)
		if err != nil {
			http.Error(w, "cannot read body", http.StatusBadRequest)
			return
		}
		msg := newReq()
		asJSON := strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "json")
		if err := decode(body, msg, asJSON); err != nil {
			fmt.Fprintf(os.Stderr, "[http] %s: undecodable body (%d bytes): %v\n", r.URL.Path, len(body), err)
			http.Error(w, "malformed OTLP payload", http.StatusBadRequest)
			return
		}
		resp := handle(msg)
		var out []byte
		if asJSON {
			out, _ = protojson.Marshal(resp)
			w.Header().Set("Content-Type", "application/json")
		} else {
			out, _ = proto.Marshal(resp)
			w.Header().Set("Content-Type", "application/x-protobuf")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	})
}

func decode(body []byte, msg proto.Message, asJSON bool) error {
	if asJSON {
		if err := protojson.Unmarshal(body, msg); err == nil {
			return nil
		}
		return proto.Unmarshal(body, msg)
	}
	if err := proto.Unmarshal(body, msg); err == nil {
		return nil
	}
	return protojson.Unmarshal(body, msg)
}

func main() {
	grpcPort := flag.Int("grpc", 12233, "OTLP gRPC listen port (0 disables)")
	httpPort := flag.Int("http", 14319, "OTLP/HTTP listen port (0 disables)")
	timeout := flag.Duration("timeout", 5*time.Minute, "auto-shutdown after this long (0 runs forever)")
	flag.Parse()

	// Backwards compatibility: the tool used to take a bare gRPC port argument.
	if arg := flag.Arg(0); arg != "" {
		if _, err := fmt.Sscanf(arg, "%d", grpcPort); err != nil {
			log.Fatalf("bad port argument %q", arg)
		}
	}

	var grpcSrv *grpc.Server
	if *grpcPort > 0 {
		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *grpcPort))
		if err != nil {
			log.Fatalf("listen gRPC :%d: %v", *grpcPort, err)
		}
		grpcSrv = grpc.NewServer()
		collogspb.RegisterLogsServiceServer(grpcSrv, &dumpServer{})
		colmetricspb.RegisterMetricsServiceServer(grpcSrv, &noopMetricsServer{})
		coltracepb.RegisterTraceServiceServer(grpcSrv, &traceDumpServer{})
		_ = gzip.Name // ensure the gzip compressor is registered
		fmt.Fprintf(os.Stderr, "OTLP gRPC dump listening on :%d\n", *grpcPort)
		go func() {
			if err := grpcSrv.Serve(lis); err != nil && err != grpc.ErrServerStopped {
				log.Fatalf("grpc serve: %v", err)
			}
		}()
	}

	var httpSrv *http.Server
	if *httpPort > 0 {
		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *httpPort))
		if err != nil {
			log.Fatalf("listen HTTP :%d: %v", *httpPort, err)
		}
		httpSrv = &http.Server{Handler: httpDumpHandler()}
		fmt.Fprintf(os.Stderr, "OTLP/HTTP dump listening on http://localhost:%d\n", *httpPort)
		fmt.Fprintf(os.Stderr, "  point a client here: OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:%d\n", *httpPort)
		go func() {
			if err := httpSrv.Serve(lis); err != nil && err != http.ErrServerClosed {
				log.Fatalf("http serve: %v", err)
			}
		}()
	}

	if grpcSrv == nil && httpSrv == nil {
		log.Fatal("both listeners disabled; nothing to do")
	}
	fmt.Fprintln(os.Stderr, "nothing is stored — press Ctrl+C to stop")

	if *timeout > 0 {
		time.Sleep(*timeout)
		fmt.Fprintf(os.Stderr, "\n%s timeout reached, shutting down\n", *timeout)
	} else {
		select {}
	}
	if httpSrv != nil {
		_ = httpSrv.Shutdown(context.Background())
	}
	if grpcSrv != nil {
		grpcSrv.GracefulStop()
	}
}
