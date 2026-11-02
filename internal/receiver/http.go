package receiver

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// maxOTLPBodyBytes caps a single OTLP/HTTP export. Real batches are well under
// a megabyte; the limit exists so a malformed or hostile Content-Length cannot
// make the daemon allocate without bound.
const maxOTLPBodyBytes = 16 << 20 // 16 MiB

// ReadOTLPBody reads one export payload, transparently decompressing it when
// the exporter gzipped the request. gzip is the only content coding OTLP/HTTP
// defines, and exporters do use it — DeepSeek Harness's shipped composition
// sets compression: gzip, and without this an export decodes as garbage and is
// rejected as malformed.
//
// Both the compressed stream and its expansion are bounded, so a small hostile
// body cannot inflate without limit.
//
// Exported for tools/otlp_dump, which must accept the same clients the daemon
// does — a dump tool that 400s on gzip would mislead exactly the onboarding
// debugging it exists for.
func ReadOTLPBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	var src io.Reader = http.MaxBytesReader(w, r.Body, maxOTLPBodyBytes)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(src)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		src = gz
	}
	// One byte over the cap is read on purpose: it separates "exactly at the
	// limit" from "truncated here", which would otherwise decode as malformed.
	body, err := io.ReadAll(io.LimitReader(src, maxOTLPBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxOTLPBodyBytes {
		return nil, errOTLPBodyTooLarge
	}
	return body, nil
}

var errOTLPBodyTooLarge = errors.New("decompressed OTLP payload exceeds the size limit")

// HTTPHandler exposes the same three OTLP services as the gRPC receiver over
// OTLP/HTTP, for clients that only speak HTTP (opencode and other AI-SDK apps).
//
// This is a protocol shim and nothing more: every request is decoded and handed
// to the identical Export method the gRPC path calls, so source routing, cost
// recompute, dedup and SSE notification cannot drift between the two transports.
func (r *Receiver) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/traces", otlpEndpoint(
		func() proto.Message { return &coltracepb.ExportTraceServiceRequest{} },
		func(ctx context.Context, m proto.Message) (proto.Message, error) {
			return r.traces.Export(ctx, m.(*coltracepb.ExportTraceServiceRequest))
		}))
	mux.Handle("/v1/logs", otlpEndpoint(
		func() proto.Message { return &collogspb.ExportLogsServiceRequest{} },
		func(ctx context.Context, m proto.Message) (proto.Message, error) {
			return r.logs.Export(ctx, m.(*collogspb.ExportLogsServiceRequest))
		}))
	mux.Handle("/v1/metrics", otlpEndpoint(
		func() proto.Message { return &colmetricspb.ExportMetricsServiceRequest{} },
		func(ctx context.Context, m proto.Message) (proto.Message, error) {
			return r.metrics.Export(ctx, m.(*colmetricspb.ExportMetricsServiceRequest))
		}))
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		log.Printf("OTLP/HTTP: unknown path %s %s", req.Method, req.URL.Path)
		http.NotFound(w, req)
	})
	return mux
}

// otlpEndpoint builds a handler for one OTLP service.
//
// Status codes matter here: an OTLP exporter treats 2xx as "delivered" and
// drops the batch. Anything we could not store must therefore NOT return 2xx,
// or the data is lost with no retry and no signal to the client.
func otlpEndpoint(newReq func() proto.Message, export func(context.Context, proto.Message) (proto.Message, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := ReadOTLPBody(w, r)
		if err != nil {
			// 413 for size violations (retrying the same body cannot succeed;
			// OTLP collectors conventionally answer 413/400 here), 400 for
			// transport-level read failures. MaxBytesError covers the raw
			// (uncompressed) over-limit path — only the inflated-over-limit path
			// raised errOTLPBodyTooLarge before (R4 protocol F8).
			status := http.StatusBadRequest
			var maxErr *http.MaxBytesError
			if errors.Is(err, errOTLPBodyTooLarge) || errors.As(err, &maxErr) {
				status = http.StatusRequestEntityTooLarge
			}
			log.Printf("OTLP/HTTP %s: read body: %v", r.URL.Path, err)
			http.Error(w, "cannot read body", status)
			return
		}

		msg := newReq()
		asJSON := isOTLPJSON(r.Header.Get("Content-Type"))
		if err := decodeOTLP(body, msg, asJSON); err != nil {
			log.Printf("OTLP/HTTP %s: decode (%d bytes, json=%t): %v", r.URL.Path, len(body), asJSON, err)
			http.Error(w, "malformed OTLP payload", http.StatusBadRequest)
			return
		}

		resp, err := export(r.Context(), msg)
		if err != nil {
			log.Printf("OTLP/HTTP %s: export: %v", r.URL.Path, err)
			http.Error(w, "export failed", http.StatusInternalServerError)
			return
		}

		// Answer in the encoding the client used; an exporter that sent JSON
		// cannot parse a protobuf reply and will treat it as a failed export.
		var out []byte
		if asJSON {
			out, err = protojson.Marshal(resp)
			w.Header().Set("Content-Type", "application/json")
		} else {
			out, err = proto.Marshal(resp)
			w.Header().Set("Content-Type", "application/x-protobuf")
		}
		if err != nil {
			log.Printf("OTLP/HTTP %s: encode response: %v", r.URL.Path, err)
			http.Error(w, "encode failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	})
}

// isOTLPJSON reports whether the request body is OTLP-JSON rather than the
// default binary protobuf.
func isOTLPJSON(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "json")
}

// decodeOTLP parses a body in the declared encoding, then falls back to the
// other one — some SDKs mislabel Content-Type, and guessing right is cheaper
// than rejecting real telemetry.
func decodeOTLP(body []byte, msg proto.Message, asJSON bool) error {
	primary, secondary := proto.Unmarshal, protojsonUnmarshal
	if asJSON {
		primary, secondary = protojsonUnmarshal, proto.Unmarshal
	}
	if err := primary(body, msg); err == nil {
		return nil
	}
	return secondary(body, msg)
}

func protojsonUnmarshal(body []byte, msg proto.Message) error {
	return protojson.Unmarshal(body, msg)
}
