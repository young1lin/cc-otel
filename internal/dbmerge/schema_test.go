package dbmerge

import (
	"slices"
	"testing"
)

func TestImportSpecsContainExactlyTheFourteenImportTables(t *testing.T) {
	want := []string{
		"api_requests", "user_prompt_events", "tool_decision_events",
		"tool_result_events", "api_error_events", "otel_metric_points",
		"events", "raw_otlp_events", "pending_ttft_spans",
		"codex_api_requests", "codex_user_prompt_events",
		"codex_tool_decision_events", "codex_tool_result_events",
		"codex_raw_otlp_events",
	}
	var got []string
	for _, spec := range ImportSpecs() {
		got = append(got, spec.Name)
		if slices.Contains(spec.Columns, "id") {
			t.Fatalf("%s imports id", spec.Name)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("import specs = %v, want %v", got, want)
	}
}

func TestRequestAndPendingKindsArePinned(t *testing.T) {
	tests := map[string]TableKind{
		"api_requests":       KindClaudeRequest,
		"codex_api_requests": KindCodexRequest,
		"raw_otlp_events":    KindRaw,
		"pending_ttft_spans": KindPendingTTFT,
	}
	for name, want := range tests {
		spec, ok := LookupSpec(name)
		if !ok || spec.Kind != want {
			t.Fatalf("%s kind = %v, %v; want %v", name, spec.Kind, ok, want)
		}
	}
}
