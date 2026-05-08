package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func main() {
	var sourcePath string
	var mergedPath string
	var fromStr string
	var toStr string

	flag.StringVar(&sourcePath, "source", "", "source db path that must be contained")
	flag.StringVar(&mergedPath, "merged", "", "merged db path")
	flag.StringVar(&fromStr, "from", "", "range start RFC3339")
	flag.StringVar(&toStr, "to", "", "range end RFC3339")
	flag.Parse()

	if sourcePath == "" || mergedPath == "" || fromStr == "" || toStr == "" {
		fmt.Fprintln(os.Stderr, "usage: verify_union -source <source.db> -merged <merged.db> -from <RFC3339> -to <RFC3339>")
		os.Exit(2)
	}

	fromT, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse -from: %v\n", err)
		os.Exit(2)
	}
	toT, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse -to: %v\n", err)
		os.Exit(2)
	}

	ctx := context.Background()
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", filepath.ToSlash(filepath.Clean(mergedPath))))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open merged: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	attachPath := filepath.ToSlash(filepath.Clean(sourcePath))
	if _, err := db.ExecContext(ctx, `ATTACH DATABASE ? AS src`, attachPath); err != nil {
		fmt.Fprintf(os.Stderr, "attach source: %v\n", err)
		os.Exit(1)
	}

	fromUnix, toUnix := fromT.Unix(), toT.Unix()

	failed := false

	// Claude api_requests
	missingRequests := mustCount(ctx, db, `
		SELECT COUNT(*)
		FROM src.api_requests s
		WHERE s.timestamp BETWEEN ? AND ?
		  AND NOT EXISTS (
			SELECT 1
			FROM main.api_requests m
			WHERE
			  (s.request_id != '' AND m.request_id = s.request_id)
			  OR (
				s.request_id = '' AND
				m.timestamp IS s.timestamp AND
				m.session_id IS s.session_id AND
				m.prompt_id IS s.prompt_id AND
				m.event_sequence IS s.event_sequence AND
				m.model IS s.model AND
				m.input_tokens IS s.input_tokens AND
				m.output_tokens IS s.output_tokens AND
				m.cost_usd IS s.cost_usd AND
				m.duration_ms IS s.duration_ms
			  )
		  )
	`, fromUnix, toUnix)

	sourceRequests := mustCount(ctx, db, `SELECT COUNT(*) FROM src.api_requests WHERE timestamp BETWEEN ? AND ?`, fromUnix, toUnix)
	mergedRequests := mustCount(ctx, db, `SELECT COUNT(*) FROM main.api_requests WHERE timestamp BETWEEN ? AND ?`, fromUnix, toUnix)

	fmt.Printf("api_requests: source=%d merged=%d missing=%d\n", sourceRequests, mergedRequests, missingRequests)
	if missingRequests != 0 {
		failed = true
	}

	// Codex api_requests
	missingCodex := mustCount(ctx, db, `
		SELECT COUNT(*)
		FROM src.codex_api_requests s
		WHERE s.timestamp BETWEEN ? AND ?
		  AND NOT EXISTS (
			SELECT 1
			FROM main.codex_api_requests m
			WHERE
				m.timestamp IS s.timestamp AND
				m.session_id IS s.session_id AND
				m.model IS s.model AND
				m.input_tokens IS s.input_tokens AND
				m.output_tokens IS s.output_tokens AND
				m.cost_usd IS s.cost_usd AND
				m.duration_ms IS s.duration_ms
		  )
	`, fromUnix, toUnix)

	sourceCodex := mustCount(ctx, db, `SELECT COUNT(*) FROM src.codex_api_requests WHERE timestamp BETWEEN ? AND ?`, fromUnix, toUnix)
	mergedCodex := mustCount(ctx, db, `SELECT COUNT(*) FROM main.codex_api_requests WHERE timestamp BETWEEN ? AND ?`, fromUnix, toUnix)

	fmt.Printf("codex_api_requests: source=%d merged=%d missing=%d\n", sourceCodex, mergedCodex, missingCodex)
	if missingCodex != 0 {
		failed = true
	}

	if failed {
		os.Exit(1)
	}
}

func mustCount(ctx context.Context, db *sql.DB, q string, args ...any) int64 {
	var n int64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		fmt.Fprintf(os.Stderr, "query count: %v\n", err)
		os.Exit(1)
	}
	return n
}
