package db

import (
	"path/filepath"
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
