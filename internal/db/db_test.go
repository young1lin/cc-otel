package db

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
)

func TestMigrationIdempotency(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	cfg := &config.Config{DBPath: dbPath}

	// First Init
	db1, err := Init(cfg)
	if err != nil {
		t.Fatalf("first Init failed: %v", err)
	}
	db1.Close()

	// Second Init on the same DB file must not error
	db2, err := Init(cfg)
	if err != nil {
		t.Fatalf("second Init failed: %v", err)
	}
	db2.Close()
}

func TestInitCreatesImportLedger(t *testing.T) {
	cfg := &config.Config{DBPath: filepath.Join(t.TempDir(), "ledger.db")}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var sqlText string
	err = database.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='import_ledger'`,
	).Scan(&sqlText)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"uuid", "imported_at", "source_db", "table_name"} {
		if !strings.Contains(sqlText, column) {
			t.Fatalf("ledger DDL missing %s: %s", column, sqlText)
		}
	}
}

func TestWALMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	cfg := &config.Config{DBPath: dbPath}

	database, err := Init(cfg)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer database.Close()

	var journalMode string
	err = database.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	if err != nil {
		t.Fatalf("PRAGMA journal_mode query failed: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", journalMode)
	}
}

func TestInit_CreatesCodexTables(t *testing.T) {
	cfg := &config.Config{DBPath: ":memory:"}
	d, err := Init(cfg)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer d.Close()

	tables := []string{
		"codex_api_requests",
		"codex_daily_model_agg",
		"codex_user_prompt_events",
		"codex_tool_decision_events",
		"codex_tool_result_events",
		"codex_events",
		"codex_raw_otlp_events",
	}
	for _, name := range tables {
		var got string
		err := d.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
		).Scan(&got)
		if err != nil {
			t.Errorf("table %s missing: %v", name, err)
		}
	}
}
