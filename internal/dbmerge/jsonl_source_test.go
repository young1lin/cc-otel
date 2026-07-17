package dbmerge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestJSONLSourceScansLegacyRecordsRepeatably(t *testing.T) {
	path := filepath.Join(t.TempDir(), "merge.jsonl")
	request := completeValues("api_requests")
	request["timestamp"] = float64(100)
	request["request_id"] = "req-1"
	raw := completeValues("raw_otlp_events")
	raw["timestamp"] = float64(101)
	raw["event_type"] = "log"
	raw["raw_json"] = "{}"
	data := encodeLegacyLine(t, "old-uuid", "api_requests", request) +
		encodeLegacyLine(t, "old-uuid-2", "raw_otlp_events", raw)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	source := NewJSONLSource(path)
	for pass := 0; pass < 2; pass++ {
		var tables []string
		err := source.Scan(context.Background(), func(row Row) error {
			tables = append(tables, row.Table)
			if row.Table == "api_requests" && row.Values["timestamp"] != int64(100) {
				t.Fatalf("normalized timestamp = %#v", row.Values["timestamp"])
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(tables, []string{"api_requests", "raw_otlp_events"}) {
			t.Fatalf("pass %d tables = %v", pass, tables)
		}
	}
}

func TestJSONLSourceSkipsRecognizedIgnoredCodexEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "merge.jsonl")
	request := completeValues("api_requests")
	request["request_id"] = "request-a"
	ignored := map[string]any{"timestamp": float64(100), "event_name": "codex.sse_event"}
	data := encodeLegacyLine(t, "ignored", "codex_events", ignored) +
		encodeLegacyLine(t, "request", "api_requests", request)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	var tables []string
	err := NewJSONLSource(path).Scan(context.Background(), func(row Row) error {
		tables = append(tables, row.Table)
		return nil
	})
	if err != nil || !slices.Equal(tables, []string{"api_requests"}) {
		t.Fatalf("tables=%v err=%v", tables, err)
	}
}

func TestJSONLSourceRejectsUnknownTableAndOversizedMalformedLine(t *testing.T) {
	t.Run("unknown table", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "unknown.jsonl")
		if err := os.WriteFile(path, []byte(`{"uuid":"x","table":"future","row":{}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := NewJSONLSource(path).Scan(context.Background(), func(Row) error { return nil })
		if err == nil || !strings.Contains(err.Error(), `unknown table "future"`) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("large malformed line", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "large.jsonl")
		data := `{"uuid":"x","table":"api_requests","row":{"payload":"` + strings.Repeat("x", 128*1024) + "\n"
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		err := NewJSONLSource(path).Scan(context.Background(), func(Row) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "decode line 1") {
			t.Fatalf("error = %v", err)
		}
	})
}

func completeValues(table string) map[string]any {
	spec, _ := LookupSpec(table)
	values := make(map[string]any, len(spec.Columns))
	for _, column := range spec.Columns {
		values[column] = nil
	}
	return values
}

func encodeLegacyLine(t *testing.T, uuid, table string, row map[string]any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"uuid": uuid, "table": table, "ts": row["timestamp"], "row": row})
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}
