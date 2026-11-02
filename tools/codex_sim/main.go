// codex_sim simulates a codex-cli client reporting OTLP/JSON logs to a
// cc-otel OTLP/HTTP receiver, so the AllClient merge can be verified
// end-to-end without a real Codex install.
//
// Each cycle sends the same record sequence the real exporter produces:
//
//  1. codex.api_request        (anchors the request; carries request id,
//     endpoint, HTTP status)
//  2. codex.user_prompt        (prompt sub-event)
//  3. codex.tool_decision      (tool sub-event)
//  4. codex.tool_result        (tool sub-event)
//  5. codex.sse_event response.completed (tokens — input is reported
//     INCLUSIVE of cached, exactly like OpenAI — plus ttft; correlated to
//     the anchor via span id)
//  6. the SAME sse_event again (must dedup: no second request row, no
//     double aggregate)
//
// One extra orphan completion (no preceding api_request) exercises the
// fallback INSERT path.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func str(k, v string) *commontpb.KeyValue {
	return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{
		Value: &commontpb.AnyValue_StringValue{StringValue: v},
	}}
}

func num(k string, v int64) *commontpb.KeyValue {
	return &commontpb.KeyValue{Key: k, Value: &commontpb.AnyValue{
		Value: &commontpb.AnyValue_IntValue{IntValue: v},
	}}
}

// record builds one log record with base64 span id + unix-nano timestamps.
func record(when time.Time, span []byte, attrs ...*commontpb.KeyValue) *logspb.LogRecord {
	nanos := uint64(when.UnixNano())
	return &logspb.LogRecord{
		TimeUnixNano:         nanos,
		ObservedTimeUnixNano: nanos,
		SpanId:               span,
		Attributes:           attrs,
	}
}

// post marshals one resource-logs envelope as OTLP/JSON and POSTs it.
func post(client *http.Client, endpoint string, recs ...*logspb.LogRecord) error {
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
				str("service.name", "codex-cli"),
				str("env", "sim"),
			}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}},
		}},
	}
	body, err := protojson.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:14318/v1/logs", "OTLP/HTTP logs URL")
	cycles := flag.Int("cycles", 3, "number of request cycles to simulate")
	model := flag.String("model", "gpt-5.5", "model name to report")
	flag.Parse()

	client := &http.Client{Timeout: 10 * time.Second}
	base := time.Now().Add(-time.Hour) // keep fixtures off "now" noise
	session := fmt.Sprintf("codex-sim-%d", time.Now().Unix())

	var sent int
	send := func(label string, recs ...*logspb.LogRecord) {
		if err := post(client, *endpoint, recs...); err != nil {
			log.Fatalf("%s: %v", label, err)
		}
		sent += len(recs)
		fmt.Println("  ok ", label)
	}

	for i := 0; i < *cycles; i++ {
		conv := fmt.Sprintf("%s-conv%02d", session, i)
		span := []byte{byte(i + 1), 2, 3, 4, 5, 6, 7, 8}
		t0 := base.Add(time.Duration(i) * 5 * time.Minute)

		fmt.Println("cycle", i, ":", conv, "(model", *model + ")")

		// 1) request anchor
		send("codex.api_request", record(t0, span,
			str("event.name", "codex.api_request"),
			str("conversation.id", conv),
			str("model", *model),
			str("auth.request_id", fmt.Sprintf("req-sim-%04d", i)),
			str("endpoint", "/v1/responses"),
			num("http.response.status_code", 200),
			str("terminal.type", "codex_cli"),
			str("service.version", "2.1.96"),
		))

		// 2) prompt sub-event
		promptText := fmt.Sprintf("sim prompt #%d: merge all clients", i)
		send("codex.user_prompt", record(t0.Add(100*time.Millisecond), nil,
			str("event.name", "codex.user_prompt"),
			str("conversation.id", conv),
			str("prompt", promptText),
			num("prompt_length", int64(len(promptText))),
		))

		// 3) tool decision sub-event
		callID := fmt.Sprintf("call-%04d", i)
		send("codex.tool_decision", record(t0.Add(200*time.Millisecond), nil,
			str("event.name", "codex.tool_decision"),
			str("conversation.id", conv),
			str("tool_name", "shell"),
			str("call_id", callID),
			str("decision", "accept"),
			str("source", "user"),
		))

		// 4) tool result sub-event
		send("codex.tool_result", record(t0.Add(600*time.Millisecond), nil,
			str("event.name", "codex.tool_result"),
			str("conversation.id", conv),
			str("tool_name", "shell"),
			str("call_id", callID),
			num("duration_ms", 50),
			str("success", "true"),
			str("arguments", "ls -la"), // logs path: lengths derive from strings
			str("output", "total 0\n"),
			str("mcp_server", ""),
		))

		// 5) completion: input 1200 is INCLUSIVE of 800 cached + 100 written,
		// so the unified row must store input = 300.
		completion := record(t0.Add(2300*time.Millisecond), span,
			str("event.name", "codex.sse_event"),
			str("event.kind", "response.completed"),
			str("conversation.id", conv),
			str("model", *model),
			num("input_token_count", 1200),
			num("cached_token_count", 800),
			num("cache_write_token_count", 100),
			num("reasoning_token_count", 40),
			num("tool_token_count", 1350), // real codex wire name for usage.total_tokens
			num("output_token_count", 150),
			num("ttft_ms", 250),
		)
		send("codex.sse_event/response.completed", completion)

		// 6) byte-identical replay of the completion — dedup proof.
		send("codex.sse_event (replay, must dedup)", completion)
	}

	// Orphan completion without a preceding api_request: fallback INSERT.
	fmt.Println("orphan: fallback INSERT path")
	send("codex.sse_event orphan", record(base.Add(time.Duration(*cycles)*5*time.Minute), []byte{9, 9, 9, 9, 9, 9, 9, 9},
		str("event.name", "codex.sse_event"),
		str("event.kind", "response.completed"),
		str("conversation.id", session+"-orphan"),
		str("model", *model),
		num("input_token_count", 500),
		num("output_token_count", 60),
	))

	fmt.Println("done:", sent, "records accepted by", *endpoint)
	os.Exit(0)
}