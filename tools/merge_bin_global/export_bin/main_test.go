package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/young1lin/cc-otel/internal/dbmerge"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestTableConfigsExcludeCodexEvents(t *testing.T) {
	for _, cfg := range tableConfigs() {
		if cfg.Name == "codex_events" {
			t.Fatal("codex_events must not be exported")
		}
	}
}

func TestExportTableMapsOldCodexToolTokensToTotalTokens(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	_, err = db.ExecContext(ctx, `
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
		INSERT INTO codex_api_requests (
			timestamp, session_id, user_id, model,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, tool_tokens, cost_usd, duration_ms, ttft_ms,
			http_status, endpoint, conversation_id,
			event_name, event_sequence, terminal_type,
			service_name, service_version, host_arch, os_type, os_version,
			error_message
		) VALUES (
			1777248000, 'session-a', '', 'gpt-5.4',
			10, 20, 3, 0,
			7, 40, 1234, 2500, 100,
			200, '/v1/responses', 'conversation-a',
			'codex.api_request', 1, 'codex',
			'cc-otel', 'test', 'amd64', 'windows', '10',
			''
		);
	`)
	if err != nil {
		t.Fatalf("seed old codex table: %v", err)
	}

	var cfg tableCfg
	for _, c := range tableConfigs() {
		if c.Name == "codex_api_requests" {
			cfg = c
			break
		}
	}
	if cfg.Name == "" {
		t.Fatal("codex_api_requests config not found")
	}

	var buf bytes.Buffer
	n, err := exportTable(ctx, db, json.NewEncoder(&buf), cfg, 1777247999, 1777248001)
	if err != nil {
		t.Fatalf("export table: %v", err)
	}
	if n != 1 {
		t.Fatalf("exported rows = %d, want 1", n)
	}

	var rec exportRecord
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if rec.Row["total_tokens"] != float64(40) {
		t.Fatalf("total_tokens = %#v, want 40", rec.Row["total_tokens"])
	}
	if _, ok := rec.Row["tool_tokens"]; ok {
		t.Fatalf("export should not expose old tool_tokens field")
	}
	wantID, err := dbmerge.LedgerID(dbmerge.Row{Table: rec.Table, Values: rec.Row})
	if err != nil {
		t.Fatal(err)
	}
	if rec.UUID != wantID {
		t.Fatalf("export id = %q, want shared identity %q", rec.UUID, wantID)
	}
}
