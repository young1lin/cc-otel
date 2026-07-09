package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestLocalDateRangeIncludesCodexDates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "local.db")

	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(dbPath)+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE api_requests (timestamp INTEGER NOT NULL);
		CREATE TABLE codex_api_requests (timestamp INTEGER NOT NULL);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	// Only a codex row exists — Claude is empty. The repair window must
	// still cover this day, so localDateRange has to consider codex_api_requests.
	ts := int64(1779868000)
	want := time.Unix(ts, 0).Local().Format("2006-01-02")
	if _, err := db.ExecContext(ctx, `INSERT INTO codex_api_requests (timestamp) VALUES (?)`, ts); err != nil {
		t.Fatalf("insert codex: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	minDay, maxDay, err := localDateRange(ctx, dbPath)
	if err != nil {
		t.Fatalf("localDateRange: %v", err)
	}
	if minDay != want || maxDay != want {
		t.Fatalf("range = [%q..%q], want codex date %q included", minDay, maxDay, want)
	}
}
