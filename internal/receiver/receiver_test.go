package receiver

import (
	"testing"
	"time"

	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func makeNumberDataPoint(value float64, attrs ...*commontpb.KeyValue) *metricspb.NumberDataPoint {
	return &metricspb.NumberDataPoint{
		Value:      &metricspb.NumberDataPoint_AsDouble{AsDouble: value},
		Attributes: attrs,
	}
}

func strAttr(key, val string) *commontpb.KeyValue {
	return &commontpb.KeyValue{
		Key:   key,
		Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_StringValue{StringValue: val}},
	}
}

func intAttr(key string, val int64) *commontpb.KeyValue {
	return &commontpb.KeyValue{
		Key:   key,
		Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_IntValue{IntValue: val}},
	}
}

func dblAttr(key string, val float64) *commontpb.KeyValue {
	return &commontpb.KeyValue{
		Key:   key,
		Value: &commontpb.AnyValue{Value: &commontpb.AnyValue_DoubleValue{DoubleValue: val}},
	}
}

func TestParseTokenUsageMetric(t *testing.T) {
	metric := &metricspb.Metric{
		Name: "claude_code.token.usage",
		Data: &metricspb.Metric_Sum{
			Sum: &metricspb.Sum{
				DataPoints: []*metricspb.NumberDataPoint{
					makeNumberDataPoint(1500, strAttr("type", "input")),
				},
			},
		},
	}

	typ, value := parseTokenMetric(metric)
	if typ != "input" {
		t.Errorf("expected type input, got %s", typ)
	}
	if value != 1500 {
		t.Errorf("expected value 1500, got %f", value)
	}
}

func TestParseCostMetric(t *testing.T) {
	metric := &metricspb.Metric{
		Name: "claude_code.cost.usage",
		Data: &metricspb.Metric_Sum{
			Sum: &metricspb.Sum{
				DataPoints: []*metricspb.NumberDataPoint{
					makeNumberDataPoint(0.025, strAttr("model", "claude-opus-4-6")),
				},
			},
		},
	}

	model, cost := parseCostMetric(metric)
	if model != "claude-opus-4-6" {
		t.Errorf("expected model claude-opus-4-6, got %s", model)
	}
	if cost != 0.025 {
		t.Errorf("expected cost 0.025, got %f", cost)
	}
}

func TestParseEventFromOTLP(t *testing.T) {
	ts := time.Now()
	lr := &logspb.LogRecord{
		TimeUnixNano: uint64(ts.UnixNano()),
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "api_request"),
			strAttr("model", "claude-opus-4-6"),
			strAttr("session.id", "sess-abc"),
			strAttr("request_id", "req-xyz"),
			intAttr("input_tokens", 1234),
			intAttr("output_tokens", 567),
			intAttr("cache_read_tokens", 800),
			dblAttr("cost_usd", 0.017),
			intAttr("duration_ms", 2000),
			intAttr("ttft_ms", 500),
		},
	}

	event := parseEventFromOTLP(lr, nil)
	if event == nil {
		t.Fatal("expected non-nil event")
	}
	if event.EventName != "api_request" {
		t.Errorf("expected event_name api_request, got %s", event.EventName)
	}
	if event.Model != "claude-opus-4-6" {
		t.Errorf("expected model claude-opus-4-6, got %s", event.Model)
	}
	if event.SessionID != "sess-abc" {
		t.Errorf("expected session_id sess-abc, got %s", event.SessionID)
	}
	if event.RequestID != "req-xyz" {
		t.Errorf("expected request_id req-xyz, got %s", event.RequestID)
	}
	if event.InputTokens != 1234 {
		t.Errorf("expected input_tokens 1234, got %d", event.InputTokens)
	}
	if event.CostUSD != 0.017 {
		t.Errorf("expected cost 0.017, got %f", event.CostUSD)
	}
}

func TestParseEventPreservesModelCasing(t *testing.T) {
	lr := &logspb.LogRecord{
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "api_request"),
			strAttr("model", "GLM-4.7"),
			intAttr("input_tokens", 1),
		},
	}
	event := parseEventFromOTLP(lr, nil)
	if event == nil {
		t.Fatal("expected non-nil event")
	}
	if event.Model != "GLM-4.7" {
		t.Errorf("expected model string as sent by telemetry, got %q", event.Model)
	}
}

func TestParseEventNoModel(t *testing.T) {
	lr := &logspb.LogRecord{
		Attributes: []*commontpb.KeyValue{
			strAttr("event.name", "api_request"),
			intAttr("input_tokens", 1234),
		},
	}
	event := parseEventFromOTLP(lr, nil)
	if event == nil {
		t.Fatal("expected non-nil event even when model is empty")
	}
	if event.Model != "" {
		t.Errorf("expected empty model, got %q", event.Model)
	}
	if event.InputTokens != 1234 {
		t.Errorf("expected input_tokens 1234, got %d", event.InputTokens)
	}
}
