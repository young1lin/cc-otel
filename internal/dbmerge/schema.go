package dbmerge

import "strings"

type TableKind uint8

const (
	KindAllColumns TableKind = iota
	KindClaudeRequest
	KindCodexRequest
	KindRaw
	KindPendingTTFT
)

type TableSpec struct {
	Name    string
	Columns []string
	Kind    TableKind
	Group   string
	Ignored bool
}

func cols(s string) []string { return strings.Fields(s) }

var registry = []TableSpec{
	{Name: "api_requests", Kind: KindClaudeRequest, Group: "claude-core", Columns: cols("timestamp session_id user_id prompt_id prompt_length model actual_model input_tokens output_tokens cache_read_tokens cache_creation_tokens cost_usd duration_ms ttft_ms request_id event_name event_sequence speed terminal_type tool_name decision source service_name service_version host_arch os_type os_version error_type error_message error_code error_retryable")},
	{Name: "user_prompt_events", Kind: KindAllColumns, Group: "claude-core", Columns: cols("timestamp session_id user_id prompt_id prompt_text prompt_length event_sequence terminal_type service_name service_version host_arch os_type os_version")},
	{Name: "tool_decision_events", Kind: KindAllColumns, Group: "claude-core", Columns: cols("timestamp session_id user_id prompt_id event_sequence tool_name decision source terminal_type")},
	{Name: "tool_result_events", Kind: KindAllColumns, Group: "claude-core", Columns: cols("timestamp session_id user_id prompt_id event_sequence tool_name success duration_ms tool_result_size_bytes decision_source decision_type terminal_type")},
	{Name: "api_error_events", Kind: KindAllColumns, Group: "claude-core", Columns: cols("timestamp session_id user_id prompt_id event_sequence model duration_ms terminal_type error_type error_message error_code error_retryable service_name service_version")},
	{Name: "otel_metric_points", Kind: KindAllColumns, Group: "claude-core", Columns: cols("timestamp metric_name value session_id user_id terminal_type model attr_type")},
	{Name: "events", Kind: KindAllColumns, Group: "claude-core", Columns: cols("timestamp session_id user_id prompt_id prompt_length event_name event_sequence model input_tokens output_tokens cache_read_tokens cache_creation_tokens cost_usd duration_ms speed terminal_type tool_name decision source success tool_result_size_bytes service_name service_version host_arch os_type os_version error_type error_message error_code error_retryable")},
	{Name: "raw_otlp_events", Kind: KindRaw, Group: "claude-core", Columns: cols("timestamp event_type raw_json")},
	{Name: "pending_ttft_spans", Kind: KindPendingTTFT, Group: "pending", Columns: cols("created_unix session_id model span_end_unix ttft_ms raw_json processed request_id")},
	{Name: "codex_api_requests", Kind: KindCodexRequest, Group: "codex", Columns: cols("timestamp session_id user_id model input_tokens output_tokens cache_read_tokens cache_creation_tokens reasoning_tokens total_tokens cost_usd duration_ms ttft_ms http_status endpoint conversation_id event_name event_sequence terminal_type service_name service_version host_arch os_type os_version error_message")},
	{Name: "codex_user_prompt_events", Kind: KindAllColumns, Group: "codex", Columns: cols("timestamp session_id conversation_id prompt_text prompt_length event_sequence terminal_type service_name service_version")},
	{Name: "codex_tool_decision_events", Kind: KindAllColumns, Group: "codex", Columns: cols("timestamp session_id conversation_id tool_name call_id decision source event_sequence terminal_type")},
	{Name: "codex_tool_result_events", Kind: KindAllColumns, Group: "codex", Columns: cols("timestamp session_id conversation_id tool_name call_id success duration_ms arguments_length output_length tool_origin mcp_server error_message event_sequence terminal_type")},
	{Name: "codex_raw_otlp_events", Kind: KindRaw, Group: "codex", Columns: cols("timestamp event_type raw_json")},
}

func ImportSpecs() []TableSpec {
	out := make([]TableSpec, len(registry))
	copy(out, registry)
	return out
}

func LookupSpec(name string) (TableSpec, bool) {
	for _, spec := range registry {
		if spec.Name == name {
			return spec, true
		}
	}
	return TableSpec{}, false
}

var ignoredRegistry = map[string][]string{
	"daily_model_agg":        cols("date model input_tokens output_tokens cache_read_tokens cache_creation_tokens cost_usd request_count"),
	"codex_daily_model_agg":  cols("date model input_tokens output_tokens cache_read_tokens cache_creation_tokens reasoning_tokens total_tokens cost_usd request_count"),
	"model_pricing":          cols("model input_cost output_cost cache_read_cost cache_create_cost aliases source fetched_at updated_at"),
	"import_ledger":          cols("uuid imported_at source_db table_name"),
	"codex_events":           cols("id timestamp session_id conversation_id event_name event_kind model duration_ms error_message raw_attrs_json"),
	"gemini_api_requests":    cols("id timestamp session_id model input_tokens output_tokens cache_read_tokens thoughts_tokens tool_tokens total_tokens duration_ms cost_usd http_status_code prompt_id event_name service_name service_version"),
	"gemini_daily_model_agg": cols("date model total_requests input_tokens output_tokens cache_read_tokens thoughts_tokens tool_tokens total_tokens cost_usd duration_ms_sum"),
}

func ignoredColumns(name string) ([]string, bool) {
	columns, ok := ignoredRegistry[name]
	return columns, ok
}

func quoteIdent(name string) string {
	for _, spec := range registry {
		if spec.Name == name {
			return `"` + name + `"`
		}
		for _, column := range spec.Columns {
			if column == name {
				return `"` + name + `"`
			}
		}
	}
	if name == "id" {
		return `"id"`
	}
	panic("identifier is not registered: " + name)
}
