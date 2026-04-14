package receiver

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/young1lin/cc-otel/internal/db"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commontpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
)

type ModelResolver interface {
	ResolveActualModel(anthropicModel string) string
}

// logsServiceServer handles OTLP log exports (the primary data source for per-request details).
// Notifier is implemented by api.Broker to decouple packages.
type Notifier interface {
	Notify()
}

type logsServiceServer struct {
	collogspb.UnimplementedLogsServiceServer
	repo     *db.Repository
	cfg      ModelResolver
	notifier Notifier
}

func (s *logsServiceServer) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	shouldNotify := false
	for _, rl := range req.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				// 1. Parse ALL events into Event struct (never nil)
				event := parseEventFromOTLP(lr, rl.Resource)
				if event == nil {
					continue
				}

				// 2. Raw JSON backup of every log record
				rawJSON, _ := json.Marshal(event)
				s.repo.InsertRawEvent(ctx, "log", event.Timestamp.Unix(), string(rawJSON))

				// 3. Route by Claude Code event.name (not by model presence)
				switch event.EventName {
				case "api_request":
					apiReq := apiRequestFromEvent(event)
					apiReq.ActualModel = s.cfg.ResolveActualModel(apiReq.Model)
					if inserted, err := s.repo.InsertRequest(ctx, apiReq); err != nil {
						log.Printf("failed to insert api_request: %v", err)
					} else if inserted {
						shouldNotify = true
					}
				case "user_prompt":
					if err := s.repo.InsertUserPrompt(ctx, event); err != nil {
						log.Printf("failed to insert user_prompt: %v", err)
					}
				case "tool_decision":
					if err := s.repo.InsertToolDecision(ctx, event); err != nil {
						log.Printf("failed to insert tool_decision: %v", err)
					}
				case "tool_result":
					if err := s.repo.InsertToolResult(ctx, event); err != nil {
						log.Printf("failed to insert tool_result: %v", err)
					}
				case "api_error":
					if err := s.repo.InsertAPIError(ctx, event); err != nil {
						log.Printf("failed to insert api_error: %v", err)
					}
				default:
					if err := s.repo.InsertEvent(ctx, event); err != nil {
						log.Printf("failed to insert event: %v", err)
					}
				}
			}
		}
	}
	if shouldNotify && s.notifier != nil {
		s.notifier.Notify()
	}
	return &collogspb.ExportLogsServiceResponse{}, nil
}

// metricsServiceServer handles OTLP metric exports (token usage, cost aggregates, session counters, etc.).
type metricsServiceServer struct {
	colmetricspb.UnimplementedMetricsServiceServer
	repo *db.Repository
}

func (s *metricsServiceServer) Export(ctx context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	if s.repo == nil {
		return &colmetricspb.ExportMetricsServiceResponse{}, nil
	}
	for _, rm := range req.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				sum := m.GetSum()
				if sum == nil {
					continue
				}
				for _, dp := range sum.DataPoints {
					ts := time.Now().Unix()
					if dp.TimeUnixNano > 0 {
						ts = int64(dp.TimeUnixNano / 1e9)
					}
					attrs := extractAttrs(dp.Attributes)
					sessionID := attrs["session.id"]
					userID := attrs["user.id"]
					terminalType := attrs["terminal.type"]
					model := attrs["model"]
					attrType := attrs["type"]
					val := numberDataPointValue(dp)
					if err := s.repo.InsertMetricPoint(ctx, ts, m.Name, val, sessionID, userID, terminalType, model, attrType); err != nil {
						log.Printf("metric insert %s: %v", m.Name, err)
					}
				}
			}
		}
	}
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

// Receiver holds references to register both services on a gRPC server.
type Receiver struct {
	logs    *logsServiceServer
	metrics *metricsServiceServer
}

// New creates a Receiver that stores parsed OTEL data via repo and resolves model names via cfg.
func New(repo *db.Repository, cfg ModelResolver, notifier Notifier) *Receiver {
	return &Receiver{
		logs:    &logsServiceServer{repo: repo, cfg: cfg, notifier: notifier},
		metrics: &metricsServiceServer{repo: repo},
	}
}

func (r *Receiver) Register(srv *grpc.Server) {
	collogspb.RegisterLogsServiceServer(srv, r.logs)
	colmetricspb.RegisterMetricsServiceServer(srv, r.metrics)
}

// --- Parsing helpers ---

// parseEventFromOTLP parses ANY log record into an Event. Never returns nil.
func parseEventFromOTLP(lr *logspb.LogRecord, resource *resourcepb.Resource) *db.Event {
	attrs := extractAttrs(lr.Attributes)
	if resource != nil {
		for _, kv := range resource.Attributes {
			if _, exists := attrs[kv.Key]; !exists {
				attrs[kv.Key] = anyValueToString(kv.Value)
			}
		}
	}

	var ts time.Time
	if lr.TimeUnixNano > 0 {
		ts = time.Unix(0, int64(lr.TimeUnixNano))
	} else {
		ts = time.Now()
	}

	success := 0
	if attrs["success"] == "true" || attrs["success"] == "1" {
		success = 1
	}

	eventName := attrs["event.name"]
	if eventName == "" {
		eventName = eventNameFromLogBody(lr)
	}

	return &db.Event{
		Timestamp:            ts,
		SessionID:            attrs["session.id"],
		UserID:               attrs["user.id"],
		PromptID:             attrs["prompt.id"],
		PromptText:           attrs["prompt"],
		PromptLength:         parseAttrInt(attrs, "prompt_length"),
		EventName:            eventName,
		EventSequence:        parseAttrInt(attrs, "event.sequence"),
		Model:                attrs["model"],
		InputTokens:          parseAttrInt(attrs, "input_tokens"),
		OutputTokens:         parseAttrInt(attrs, "output_tokens"),
		CacheReadTokens:      parseAttrInt(attrs, "cache_read_tokens"),
		CacheCreationTokens:  parseAttrInt(attrs, "cache_creation_tokens"),
		CostUSD:              parseAttrFloat(attrs, "cost_usd"),
		DurationMs:           parseAttrInt(attrs, "duration_ms"),
		TTFTMs:               parseAttrInt(attrs, "ttft_ms"),
		Speed:                attrs["speed"],
		TerminalType:         attrs["terminal.type"],
		ToolName:             attrs["tool_name"],
		Decision:             attrs["decision"],
		Source:               attrs["source"],
		DecisionSource:       attrs["decision_source"],
		DecisionType:         attrs["decision_type"],
		Success:              success,
		ToolResultSizeBytes:  parseAttrInt(attrs, "tool_result_size_bytes"),
		ServiceName:          attrs["service.name"],
		ServiceVersion:       attrs["service.version"],
		HostArch:             attrs["host.arch"],
		OSType:               attrs["os.type"],
		OSVersion:            attrs["os.version"],
		RequestID:            attrs["request_id"],
		ErrorType:            attrs["error.type"],
		ErrorMessage:         attrs["error.message"],
		ErrorCode:            parseAttrInt(attrs, "error.code"),
		ErrorRetryable:       int(parseAttrInt(attrs, "error.retryable")),
	}
}

func eventNameFromLogBody(lr *logspb.LogRecord) string {
	if lr == nil || lr.Body == nil {
		return ""
	}
	s := anyValueToString(lr.Body)
	if strings.HasPrefix(s, "claude_code.") {
		return strings.TrimPrefix(s, "claude_code.")
	}
	return ""
}

func apiRequestFromEvent(e *db.Event) *db.APIRequest {
	return &db.APIRequest{
		Timestamp:           e.Timestamp,
		SessionID:           e.SessionID,
		UserID:              e.UserID,
		PromptID:            e.PromptID,
		PromptLength:        e.PromptLength,
		Model:               e.Model,
		InputTokens:         e.InputTokens,
		OutputTokens:        e.OutputTokens,
		CacheReadTokens:     e.CacheReadTokens,
		CacheCreationTokens: e.CacheCreationTokens,
		CostUSD:             e.CostUSD,
		DurationMs:          e.DurationMs,
		TTFTMs:              e.TTFTMs,
		RequestID:           e.RequestID,
		EventName:           e.EventName,
		EventSequence:       e.EventSequence,
		Speed:               e.Speed,
		TerminalType:        e.TerminalType,
		ToolName:            e.ToolName,
		Decision:            e.Decision,
		Source:              e.Source,
		ServiceName:         e.ServiceName,
		ServiceVersion:      e.ServiceVersion,
		HostArch:            e.HostArch,
		OSType:              e.OSType,
		OSVersion:           e.OSVersion,
		ErrorType:           e.ErrorType,
		ErrorMessage:        e.ErrorMessage,
		ErrorCode:           e.ErrorCode,
		ErrorRetryable:      e.ErrorRetryable,
	}
}

func extractAttrs(attrs []*commontpb.KeyValue) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		m[kv.Key] = anyValueToString(kv.Value)
	}
	return m
}

func anyValueToString(v *commontpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch val := v.Value.(type) {
	case *commontpb.AnyValue_StringValue:
		return val.StringValue
	case *commontpb.AnyValue_IntValue:
		return strconv.FormatInt(val.IntValue, 10)
	case *commontpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(val.DoubleValue, 'f', -1, 64)
	case *commontpb.AnyValue_BoolValue:
		return strconv.FormatBool(val.BoolValue)
	default:
		return ""
	}
}

func parseAttrInt(attrs map[string]string, key string) int64 {
	v, err := strconv.ParseInt(attrs[key], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func parseAttrFloat(attrs map[string]string, key string) float64 {
	v, err := strconv.ParseFloat(attrs[key], 64)
	if err != nil {
		return 0
	}
	return v
}

// --- Exported helpers for testing ---

func numberDataPointValue(dp *metricspb.NumberDataPoint) float64 {
	if dp == nil {
		return 0
	}
	switch v := dp.Value.(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		return v.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		return float64(v.AsInt)
	default:
		return 0
	}
}

func parseTokenMetric(m *metricspb.Metric) (string, float64) {
	sum := m.GetSum()
	if sum == nil || len(sum.DataPoints) == 0 {
		return "", 0
	}
	dp := sum.DataPoints[0]
	typ := ""
	for _, kv := range dp.Attributes {
		if kv.Key == "type" {
			typ = kv.Value.GetStringValue()
		}
	}
	return typ, numberDataPointValue(dp)
}

func parseCostMetric(m *metricspb.Metric) (string, float64) {
	sum := m.GetSum()
	if sum == nil || len(sum.DataPoints) == 0 {
		return "", 0
	}
	dp := sum.DataPoints[0]
	model := ""
	for _, kv := range dp.Attributes {
		if kv.Key == "model" {
			model = kv.Value.GetStringValue()
		}
	}
	return model, numberDataPointValue(dp)
}
