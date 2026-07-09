package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestRepairGeminiDailyAggRebuildsFromRequests(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	_, err = db.ExecContext(ctx, `
		CREATE TABLE gemini_api_requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			model TEXT DEFAULT '',
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			thoughts_tokens INTEGER DEFAULT 0,
			tool_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0,
			cost_usd INTEGER DEFAULT 0,
			http_status_code INTEGER DEFAULT 0,
			prompt_id TEXT DEFAULT '',
			event_name TEXT DEFAULT 'api_response',
			service_name TEXT DEFAULT '',
			service_version TEXT DEFAULT ''
		);
		CREATE TABLE gemini_daily_model_agg (
			date TEXT NOT NULL,
			model TEXT NOT NULL,
			total_requests INTEGER NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			thoughts_tokens INTEGER NOT NULL DEFAULT 0,
			tool_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd INTEGER NOT NULL DEFAULT 0,
			duration_ms_sum INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (date, model)
		) WITHOUT ROWID;
	`)
	if err != nil {
		t.Fatalf("seed schema: %v", err)
	}

	ts := int64(1779868000)
	day := time.Unix(ts, 0).Local().Format("2006-01-02")
	_, err = db.ExecContext(ctx, `
		INSERT INTO gemini_api_requests (timestamp, model, input_tokens, output_tokens, cache_read_tokens, thoughts_tokens, tool_tokens, total_tokens, cost_usd, duration_ms)
		VALUES (?, 'gemini-3-flash-preview', 100, 20, 50, 5, 0, 175, 684, 1200),
		       (?, 'gemini-3-flash-preview', 200, 30, 80, 10, 0, 320, 900, 1500)
	`, ts, ts+5)
	if err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	applied, err := repairGeminiOneDay(ctx, db, day)
	if err != nil {
		t.Fatalf("repair gemini: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1 (one model row)", applied)
	}

	var req, in, out, cache, total, cost, dur int64
	err = db.QueryRowContext(ctx, `
		SELECT total_requests, input_tokens, output_tokens, cache_read_tokens, total_tokens, cost_usd, duration_ms_sum
		FROM gemini_daily_model_agg WHERE date = ? AND model = 'gemini-3-flash-preview'`, day).
		Scan(&req, &in, &out, &cache, &total, &cost, &dur)
	if err != nil {
		t.Fatalf("query agg: %v", err)
	}
	if req != 2 || in != 300 || out != 50 || cache != 130 || total != 495 || cost != 1584 || dur != 2700 {
		t.Fatalf("agg = req:%d in:%d out:%d cache:%d total:%d cost:%d dur:%d; want 2/300/50/130/495/1584/2700",
			req, in, out, cache, total, cost, dur)
	}
}
