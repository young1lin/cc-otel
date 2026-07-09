package main

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestRecordExistsDetectsExistingRowsByContent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	if err := ensureTargetSchema(ctx, db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	rec := exportRecord{
		Table: "user_prompt_events",
		Row: map[string]any{
			"timestamp":       int64(1777248000),
			"session_id":      "session-a",
			"user_id":         "user-a",
			"prompt_id":       "prompt-a",
			"prompt_text":     "hello",
			"prompt_length":   int64(5),
			"event_sequence":  int64(1),
			"terminal_type":   "cursor",
			"service_name":    "cc-otel",
			"service_version": "test",
			"host_arch":       "amd64",
			"os_type":         "windows",
			"os_version":      "10",
		},
	}

	exists, err := recordExists(ctx, tx, rec)
	if err != nil {
		t.Fatalf("record exists before insert: %v", err)
	}
	if exists {
		t.Fatal("record should not exist before insert")
	}

	inserted, err := insertRecord(ctx, tx, rec)
	if err != nil {
		t.Fatalf("insert record: %v", err)
	}
	if !inserted {
		t.Fatal("expected first insert to affect a row")
	}

	exists, err = recordExists(ctx, tx, rec)
	if err != nil {
		t.Fatalf("record exists after insert: %v", err)
	}
	if !exists {
		t.Fatal("record should exist after insert")
	}
}

func TestRecordExistsHandlesNullValues(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	if err := ensureTargetSchema(ctx, db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	rec := exportRecord{
		Table: "api_requests",
		Row: map[string]any{
			"timestamp":             int64(1777248000),
			"session_id":            "session-a",
			"user_id":               "user-a",
			"prompt_id":             "prompt-a",
			"prompt_length":         int64(5),
			"model":                 "claude",
			"actual_model":          "claude",
			"input_tokens":          int64(10),
			"output_tokens":         int64(2),
			"cache_read_tokens":     int64(0),
			"cache_creation_tokens": int64(0),
			"cost_usd":              int64(1234),
			"duration_ms":           int64(100),
			"ttft_ms":               nil,
			"request_id":            "",
			"event_name":            "",
			"event_sequence":        int64(1),
			"speed":                 "",
			"terminal_type":         "cursor",
			"tool_name":             "",
			"decision":              "",
			"source":                "",
			"service_name":          "cc-otel",
			"service_version":       "test",
			"host_arch":             "amd64",
			"os_type":               "windows",
			"os_version":            "10",
			"error_type":            "",
			"error_message":         "",
			"error_code":            int64(0),
			"error_retryable":       int64(0),
		},
	}

	inserted, err := insertRecord(ctx, tx, rec)
	if err != nil {
		t.Fatalf("insert record: %v", err)
	}
	if !inserted {
		t.Fatal("expected first insert to affect a row")
	}

	exists, err := recordExists(ctx, tx, rec)
	if err != nil {
		t.Fatalf("record exists after insert: %v", err)
	}
	if !exists {
		t.Fatal("record with nil value should exist after insert")
	}
}

func TestEnsureTargetSchemaMigratesOldCodexToolTokens(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	_, err := db.ExecContext(ctx, `
		CREATE TABLE codex_api_requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			user_id TEXT DEFAULT '',
			model TEXT DEFAULT '',
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			cache_creation_tokens INTEGER DEFAULT 0,
			reasoning_tokens INTEGER DEFAULT 0,
			tool_tokens INTEGER DEFAULT 0,
			cost_usd INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0,
			ttft_ms INTEGER DEFAULT 0,
			http_status INTEGER DEFAULT 0,
			endpoint TEXT DEFAULT '',
			conversation_id TEXT DEFAULT '',
			event_name TEXT DEFAULT '',
			event_sequence INTEGER DEFAULT 0,
			terminal_type TEXT DEFAULT '',
			service_name TEXT DEFAULT '',
			service_version TEXT DEFAULT '',
			host_arch TEXT DEFAULT '',
			os_type TEXT DEFAULT '',
			os_version TEXT DEFAULT '',
			error_message TEXT DEFAULT ''
		);
		CREATE TABLE codex_daily_model_agg (
			date TEXT NOT NULL,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			tool_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd INTEGER NOT NULL DEFAULT 0,
			request_count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (date, model)
		) WITHOUT ROWID;
		INSERT INTO codex_api_requests (timestamp, model, tool_tokens) VALUES (1777248000, 'gpt-5.4', 42);
		INSERT INTO codex_daily_model_agg (date, model, tool_tokens, request_count) VALUES ('2026-04-27', 'gpt-5.4', 42, 1);
	`)
	if err != nil {
		t.Fatalf("seed old schema: %v", err)
	}

	if err := ensureTargetSchema(ctx, db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	var apiTotal, aggTotal int64
	if err := db.QueryRowContext(ctx, `SELECT total_tokens FROM codex_api_requests`).Scan(&apiTotal); err != nil {
		t.Fatalf("query codex_api_requests.total_tokens: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT total_tokens FROM codex_daily_model_agg`).Scan(&aggTotal); err != nil {
		t.Fatalf("query codex_daily_model_agg.total_tokens: %v", err)
	}
	if apiTotal != 42 || aggTotal != 42 {
		t.Fatalf("migrated totals = api:%d agg:%d, want 42/42", apiTotal, aggTotal)
	}
}

func TestCodexImportAndAggUseTotalTokens(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	if err := ensureTargetSchema(ctx, db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	rec := exportRecord{
		Table: "codex_api_requests",
		Row: map[string]any{
			"timestamp":             int64(1777248000),
			"session_id":            "session-a",
			"user_id":               "",
			"model":                 "gpt-5.4",
			"input_tokens":          int64(10),
			"output_tokens":         int64(20),
			"cache_read_tokens":     int64(3),
			"cache_creation_tokens": int64(0),
			"reasoning_tokens":      int64(7),
			"total_tokens":          int64(40),
			"cost_usd":              int64(1234),
			"duration_ms":           int64(2500),
			"ttft_ms":               int64(100),
			"http_status":           int64(200),
			"endpoint":              "/v1/responses",
			"conversation_id":       "conversation-a",
			"event_name":            "codex.api_request",
			"event_sequence":        int64(1),
			"terminal_type":         "codex",
			"service_name":          "cc-otel",
			"service_version":       "test",
			"host_arch":             "amd64",
			"os_type":               "windows",
			"os_version":            "10",
			"error_message":         "",
		},
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	inserted, err := insertRecord(ctx, tx, rec)
	if err != nil {
		t.Fatalf("insert codex record: %v", err)
	}
	if !inserted {
		t.Fatal("expected codex insert to affect a row")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit insert: %v", err)
	}

	deltas := map[aggKey]*codexAggDelta{}
	addCodexAggDelta(deltas, rec.Row)
	if err := applyCodexAggDeltas(ctx, db, deltas); err != nil {
		t.Fatalf("apply codex agg: %v", err)
	}

	var rowTotal, aggTotal int64
	if err := db.QueryRowContext(ctx, `SELECT total_tokens FROM codex_api_requests`).Scan(&rowTotal); err != nil {
		t.Fatalf("query row total_tokens: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT total_tokens FROM codex_daily_model_agg WHERE date = '2026-04-27' AND model = 'gpt-5.4'`).Scan(&aggTotal); err != nil {
		t.Fatalf("query agg total_tokens: %v", err)
	}
	if rowTotal != 40 || aggTotal != 40 {
		t.Fatalf("total_tokens = row:%d agg:%d, want 40/40", rowTotal, aggTotal)
	}
}

func TestGeminiImportInsertsRowAndCreatesSchema(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	// ensureTargetSchema must create gemini tables so import into a pre-gemini
	// global db does not fail with "no such table".
	if err := ensureTargetSchema(ctx, db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	rec := exportRecord{
		Table: "gemini_api_requests",
		Row: map[string]any{
			"timestamp":         int64(1779868000),
			"session_id":        "gsession-a",
			"model":             "gemini-3-flash-preview",
			"input_tokens":      int64(100),
			"output_tokens":     int64(20),
			"cache_read_tokens": int64(50),
			"thoughts_tokens":   int64(5),
			"tool_tokens":       int64(0),
			"total_tokens":      int64(175),
			"duration_ms":       int64(1200),
			"cost_usd":          int64(684),
			"http_status_code":  int64(200),
			"prompt_id":         "gprompt-a",
			"event_name":        "api_response",
			"service_name":      "gemini_cli",
			"service_version":   "test",
		},
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	exists, err := recordExists(ctx, tx, rec)
	if err != nil {
		t.Fatalf("record exists before insert: %v", err)
	}
	if exists {
		t.Fatal("gemini record should not exist before insert")
	}
	inserted, err := insertRecord(ctx, tx, rec)
	if err != nil {
		t.Fatalf("insert gemini record: %v", err)
	}
	if !inserted {
		t.Fatal("expected gemini insert to affect a row")
	}
	exists, err = recordExists(ctx, tx, rec)
	if err != nil {
		t.Fatalf("record exists after insert: %v", err)
	}
	if !exists {
		t.Fatal("gemini record should exist after insert")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var cnt int64
	var model string
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(model),'') FROM gemini_api_requests`).Scan(&cnt, &model); err != nil {
		t.Fatalf("query gemini_api_requests: %v", err)
	}
	if cnt != 1 || model != "gemini-3-flash-preview" {
		t.Fatalf("gemini rows = %d model=%q, want 1 / gemini-3-flash-preview", cnt, model)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}
