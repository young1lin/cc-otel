package dbmerge

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func requestRow(requestID string, cost int64) Row {
	return Row{Table: "api_requests", Values: map[string]any{
		"timestamp": int64(100), "session_id": "session", "prompt_id": "prompt",
		"event_sequence": int64(1), "model": "claude", "input_tokens": int64(10),
		"output_tokens": int64(20), "duration_ms": int64(30), "request_id": requestID,
		"cost_usd": cost,
	}}
}

func pendingRow(requestID string, processed int64) Row {
	return Row{Table: "pending_ttft_spans", Values: map[string]any{
		"created_unix": int64(90), "session_id": "session", "model": "claude",
		"span_end_unix": int64(100), "ttft_ms": int64(12), "raw_json": "{}",
		"processed": processed, "request_id": requestID,
	}}
}

func TestClaudeRequestIdentityPrefersNonEmptyRequestID(t *testing.T) {
	a := requestRow("request-1", 1)
	b := requestRow("request-1", 999)
	b.Values["timestamp"] = int64(999)
	ia, err := IdentityFor(a)
	if err != nil {
		t.Fatal(err)
	}
	ib, err := IdentityFor(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(ia.Columns) != 1 || ia.Columns[0] != "request_id" || ia.Digest != ib.Digest {
		t.Fatalf("request id identity mismatch: %+v %+v", ia, ib)
	}
}

func TestClaudeRequestIdentityFallbackExcludesCostUSD(t *testing.T) {
	a := requestRow("", 100)
	b := requestRow("", 999)
	ia, err := IdentityFor(a)
	if err != nil {
		t.Fatal(err)
	}
	ib, err := IdentityFor(b)
	if err != nil {
		t.Fatal(err)
	}
	if ia.Digest != ib.Digest {
		t.Fatalf("repriced Claude request changed identity: %s != %s", ia.Digest, ib.Digest)
	}
}

func TestCodexRequestIdentityExcludesCostUSD(t *testing.T) {
	a := Row{Table: "codex_api_requests", Values: map[string]any{
		"timestamp": int64(1), "session_id": "s", "model": "gpt", "input_tokens": int64(2),
		"output_tokens": int64(3), "duration_ms": int64(4), "cost_usd": int64(5),
	}}
	b := a
	b.Values = cloneValues(a.Values)
	b.Values["cost_usd"] = int64(500)
	ia, _ := IdentityFor(a)
	ib, _ := IdentityFor(b)
	if ia.Digest != ib.Digest {
		t.Fatal("repriced Codex request changed identity")
	}
}

func TestAllColumnIdentityIncludesCostAndPreservesNull(t *testing.T) {
	a := Row{Table: "events", Values: map[string]any{"timestamp": int64(1), "cost_usd": int64(2), "prompt_id": nil}}
	b := Row{Table: a.Table, Values: cloneValues(a.Values)}
	b.Values["cost_usd"] = int64(3)
	c := Row{Table: a.Table, Values: cloneValues(a.Values)}
	c.Values["prompt_id"] = ""
	ia, _ := IdentityFor(a)
	ib, _ := IdentityFor(b)
	ic, _ := IdentityFor(c)
	if ia.Digest == ib.Digest {
		t.Fatal("ordinary event cost was excluded from identity")
	}
	if ia.Digest == ic.Digest {
		t.Fatal("NULL and empty text produced the same identity")
	}
}

func TestRawIdentityUsesTimestampTypeAndRawJSON(t *testing.T) {
	a := Row{Table: "raw_otlp_events", Values: map[string]any{"timestamp": int64(1), "event_type": "log", "raw_json": "{}"}}
	b := Row{Table: a.Table, Values: cloneValues(a.Values)}
	b.Values["raw_json"] = "{ }"
	ia, _ := IdentityFor(a)
	ib, _ := IdentityFor(b)
	if ia.Digest == ib.Digest || len(ia.Columns) != 3 {
		t.Fatalf("raw identities = %+v %+v", ia, ib)
	}
}

func TestPendingIdentityPrefersRequestIDAndIgnoresProcessed(t *testing.T) {
	a := pendingRow("req-1", int64(0))
	b := pendingRow("req-1", int64(1))
	a.Values["raw_json"] = `{"source":"a"}`
	b.Values["raw_json"] = `{"source":"b"}`
	ia, _ := IdentityFor(a)
	ib, _ := IdentityFor(b)
	if ia.Digest != ib.Digest {
		t.Fatalf("processed/raw_json changed pending identity")
	}
}

func TestLedgerIDIsStableAcrossMapIterationOrder(t *testing.T) {
	a := requestRow("", 1)
	b := requestRow("", 1)
	b.Values = map[string]any{}
	for _, key := range []string{"cost_usd", "duration_ms", "output_tokens", "input_tokens", "model", "event_sequence", "prompt_id", "session_id", "timestamp", "request_id"} {
		b.Values[key] = a.Values[key]
	}
	ida, _ := LedgerID(a)
	idb, _ := LedgerID(b)
	if ida != idb {
		t.Fatalf("ledger ids differ: %s != %s", ida, idb)
	}
}

func TestRecordExistsUsesSQLiteISForNull(t *testing.T) {
	target := openWritable(t, currentFixture(t))
	defer target.Close()
	row := Row{Table: "events", Values: make(map[string]any)}
	for _, column := range mustSpec(t, "events").Columns {
		row.Values[column] = nil
	}
	row.Values["timestamp"] = int64(1)
	insertTestRow(t, target, row)
	exists, err := RecordExists(context.Background(), target, row)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("row containing NULL values was not found with IS semantics")
	}
}

func TestStaleLedgerDoesNotAffectRecordExists(t *testing.T) {
	target := openWritable(t, currentFixture(t))
	defer target.Close()
	row := requestRow("stale-ledger", 1)
	id, err := LedgerID(row)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(`INSERT INTO import_ledger(uuid, imported_at, source_db, table_name) VALUES(?, 1, 'test', ?)`, id, row.Table); err != nil {
		t.Fatal(err)
	}
	exists, err := RecordExists(context.Background(), target, row)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("stale ledger entry caused a missing detail row to exist")
	}
}

func mustSpec(t *testing.T, name string) TableSpec {
	t.Helper()
	spec, ok := LookupSpec(name)
	if !ok {
		t.Fatalf("missing spec %s", name)
	}
	return spec
}

func insertTestRow(t *testing.T, target *sql.DB, row Row) {
	t.Helper()
	spec := mustSpec(t, row.Table)
	placeholders := make([]string, len(spec.Columns))
	values := make([]any, len(spec.Columns))
	for i, column := range spec.Columns {
		placeholders[i] = "?"
		values[i] = row.Values[column]
	}
	query := "INSERT INTO " + quoteIdent(row.Table) + " (" + quotedColumns(spec.Columns) + ") VALUES (" + strings.Join(placeholders, ",") + ")"
	if _, err := target.Exec(query, values...); err != nil {
		t.Fatal(err)
	}
}

func quotedColumns(columns []string) string {
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = quoteIdent(column)
	}
	return strings.Join(quoted, ",")
}

func cloneValues(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
