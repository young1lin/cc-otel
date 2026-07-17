package dbmerge

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
	appdb "github.com/young1lin/cc-otel/internal/db"
)

func currentFixture(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	d, err := appdb.Init(&config.Config{DBPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func openWritable(t testing.TB, path string) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mustExec(t testing.TB, d *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSQLiteAcceptsCurrentSchema(t *testing.T) {
	info, err := ValidateSQLite(context.Background(), currentFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Present) != len(ImportSpecs()) {
		t.Fatalf("present import tables = %d, want %d", len(info.Present), len(ImportSpecs()))
	}
}

func TestValidateSQLiteAcceptsClaudeOnlySchema(t *testing.T) {
	path := currentFixture(t)
	d := openWritable(t, path)
	for _, table := range []string{
		"codex_api_requests", "codex_daily_model_agg", "codex_user_prompt_events",
		"codex_tool_decision_events", "codex_tool_result_events", "codex_events",
		"codex_raw_otlp_events", "pending_ttft_spans",
	} {
		mustExec(t, d, `DROP TABLE "`+table+`"`)
	}
	d.Close()

	if _, err := ValidateSQLite(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSQLiteMapsPendingWithoutRequestID(t *testing.T) {
	path := currentFixture(t)
	d := openWritable(t, path)
	mustExec(t, d, `DROP INDEX idx_pending_ttft_request`)
	mustExec(t, d, `ALTER TABLE pending_ttft_spans DROP COLUMN request_id`)
	d.Close()

	info, err := ValidateSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(info.Warnings, "Legacy pending_ttft_spans has no request_id; empty values will be used.") {
		t.Fatalf("warnings = %v", info.Warnings)
	}
	var rows []Row
	err = NewSQLiteSource(path, info, nil).Scan(context.Background(), func(row Row) error {
		if row.Table == "pending_ttft_spans" {
			rows = append(rows, row)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("unexpected pending rows: %v", rows)
	}
}

func TestValidateSQLiteMapsCodexToolTokens(t *testing.T) {
	path := currentFixture(t)
	d := openWritable(t, path)
	mustExec(t, d, `ALTER TABLE codex_api_requests RENAME COLUMN total_tokens TO tool_tokens`)
	mustExec(t, d, `ALTER TABLE codex_daily_model_agg RENAME COLUMN total_tokens TO tool_tokens`)
	mustExec(t, d, `INSERT INTO codex_api_requests(timestamp, tool_tokens) VALUES(?, ?)`, 100, 42)
	d.Close()

	info, err := ValidateSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(info.Warnings, "Legacy Codex tool_tokens will be mapped to total_tokens.") {
		t.Fatalf("warnings = %v", info.Warnings)
	}
	var total any
	err = NewSQLiteSource(path, info, nil).Scan(context.Background(), func(row Row) error {
		if row.Table == "codex_api_requests" {
			total = row.Values["total_tokens"]
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != int64(42) {
		t.Fatalf("mapped total_tokens = %#v, want 42", total)
	}
}

func TestValidateSQLiteIgnoresKnownGeminiPairWithWarning(t *testing.T) {
	path := currentFixture(t)
	d := openWritable(t, path)
	mustExec(t, d, `CREATE TABLE gemini_api_requests (
		id INTEGER PRIMARY KEY, timestamp INTEGER, session_id TEXT, model TEXT,
		input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER,
		thoughts_tokens INTEGER, tool_tokens INTEGER, total_tokens INTEGER,
		duration_ms INTEGER, cost_usd REAL, http_status_code INTEGER, prompt_id TEXT,
		event_name TEXT, service_name TEXT, service_version TEXT
	)`)
	mustExec(t, d, `CREATE TABLE gemini_daily_model_agg (
		date TEXT, model TEXT, total_requests INTEGER, input_tokens INTEGER,
		output_tokens INTEGER, cache_read_tokens INTEGER, thoughts_tokens INTEGER,
		tool_tokens INTEGER, total_tokens INTEGER, cost_usd REAL, duration_ms_sum INTEGER
	)`)
	d.Close()

	info, err := ValidateSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(info.Warnings, "Legacy Gemini tables are recognized but will not be imported.") {
		t.Fatalf("warnings = %v", info.Warnings)
	}
}

func TestValidateSQLiteRejectsUnknownTable(t *testing.T) {
	path := currentFixture(t)
	d := openWritable(t, path)
	mustExec(t, d, `CREATE TABLE future_events (id INTEGER PRIMARY KEY, payload TEXT)`)
	d.Close()

	_, err := ValidateSQLite(context.Background(), path)
	var mergeErr *MergeError
	if !errors.As(err, &mergeErr) || mergeErr.Code != ErrUnsupportedSchema {
		t.Fatalf("error = %v, want unsupported_schema", err)
	}
	if !strings.Contains(err.Error(), "future_events") {
		t.Fatalf("error does not name future_events: %v", err)
	}
}

func TestValidateSQLiteRejectsUnknownColumn(t *testing.T) {
	path := currentFixture(t)
	d := openWritable(t, path)
	mustExec(t, d, `ALTER TABLE events ADD COLUMN future_value TEXT`)
	d.Close()

	_, err := ValidateSQLite(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "events.future_value") {
		t.Fatalf("error = %v, want events.future_value", err)
	}
}

func TestValidateSQLiteRejectsPartialCodexGroup(t *testing.T) {
	path := currentFixture(t)
	d := openWritable(t, path)
	mustExec(t, d, `DROP TABLE codex_events`)
	d.Close()

	_, err := ValidateSQLite(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("error = %v, want incomplete codex group", err)
	}
}

func TestValidateSQLiteRejectsNonSQLiteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.db")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ValidateSQLite(context.Background(), path)
	var mergeErr *MergeError
	if !errors.As(err, &mergeErr) || mergeErr.Code != ErrInvalidSQLite {
		t.Fatalf("error = %v, want invalid_sqlite", err)
	}
}

func TestSQLiteSourceDoesNotCreateWALOrSHM(t *testing.T) {
	path := currentFixture(t)
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	info, err := ValidateSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewSQLiteSource(path, info, nil).Scan(context.Background(), func(Row) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("read-only source created %s", filepath.Base(path+suffix))
		}
	}
}

func TestSQLiteSourceAppliesTimeWindow(t *testing.T) {
	path := currentFixture(t)
	d := openWritable(t, path)
	mustExec(t, d, `INSERT INTO api_requests(timestamp, request_id) VALUES(10, 'old'), (20, 'in'), (30, 'new')`)
	d.Close()
	info, err := ValidateSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	err = NewSQLiteSource(path, info, &TimeWindow{FromUnix: 15, ToUnix: 25}).Scan(context.Background(), func(row Row) error {
		if row.Table == "api_requests" {
			ids = append(ids, row.Values["request_id"].(string))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "in" {
		t.Fatalf("request ids = %v, want [in]", ids)
	}
}

func containsText(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
