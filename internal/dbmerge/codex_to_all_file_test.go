package dbmerge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	appdb "github.com/young1lin/cc-otel/internal/db"
)

// TestMigrateCodexToAllOnFileDatabaseIsIdempotent runs the one-click migration
// against a FILE database (not :memory:), exercising newCodexToAllSource's
// dedicated read-only connection, and asserts the two invariants the live
// bin instance taught us:
//
//  1. PRAGMA quick_check stays ok — streaming the archive SELECT through the
//     target's own pool while staged INSERTs run on another pooled
//     connection reproducibly corrupted import_ledger ("Rowid out of order",
//     ledger rows lost, second migration re-inserting thousands of rows).
//  2. The second migration inserts nothing — full identity idempotency.
func TestMigrateCodexToAllOnFileDatabaseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-target.db")
	schemaDB, err := appdb.Init(&config.Config{DBPath: path})
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := schemaDB.Close(); err != nil {
		t.Fatalf("close schema handle: %v", err)
	}
	target := openWritable(t, path)
	defer target.Close()
	ctx := context.Background()

	mustExec(t, target, `
		INSERT INTO codex_api_requests
			(timestamp, session_id, user_id, model,
			 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			 reasoning_tokens, total_tokens, cost_usd, duration_ms, ttft_ms,
			 event_name, event_sequence, terminal_type, service_version)
		VALUES
			(1770000000, 'conv-file', 'dev', 'gpt-5.5',
			 900, 90, 300, 100, 5, 990, 1000, 1500, 200,
			 'codex.api_request', 2, 'codex_cli', '2.1.96'),
			(1770000600, 'conv-file', 'dev', 'gpt-5.5',
			 400, 40, 0, 0, 1, 440, 500, 900, 120,
			 'codex.api_request', 3, 'codex_cli', '2.1.96')`)

	mustExec(t, target, `
		INSERT INTO codex_user_prompt_events
			(timestamp, session_id, conversation_id, prompt_text, prompt_length,
			 event_sequence, terminal_type, service_version)
		VALUES (1770000001, 'conv-file', 'conv-file', 'file-backed prompt', 19, 1, 'codex_cli', '2.1.96')`)

	first, err := MigrateCodexToAll(ctx, target, Options{Location: time.UTC})
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if first.InsertedRows != 3 {
		t.Fatalf("first migrate inserted = %d, want 3", first.InsertedRows)
	}

	var check string
	if err := target.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		t.Fatalf("quick_check: %v", err)
	}
	if check != "ok" {
		t.Fatalf("quick_check = %q, want ok", check)
	}

	second, err := MigrateCodexToAll(ctx, target, Options{Location: time.UTC})
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if second.InsertedRows != 0 || second.DuplicateRows != 3 {
		t.Fatalf("second migrate = inserted %d / dup %d, want 0 / 3", second.InsertedRows, second.DuplicateRows)
	}

	var rows int64
	if err := target.QueryRow(
		"SELECT COUNT(*) FROM api_requests WHERE service_name = 'codex'",
	).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("codex rows after remigrate = %d err=%v, want 2", rows, err)
	}
	// input 900 was cache-inclusive: 900-300-100 = 500 normalized.
	var input int64
	if err := target.QueryRow(
		"SELECT input_tokens FROM api_requests WHERE service_name='codex' AND event_sequence = 2",
	).Scan(&input); err != nil || input != 500 {
		t.Fatalf("normalized input = %d err=%v, want 500", input, err)
	}
}
