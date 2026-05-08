package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func main() {
	dbPath := flag.String("db", filepath.Join("bin", "cc-otel.db"), "SQLite database path")
	dryRun := flag.Bool("dry-run", false, "print what would be done without writing")
	backup := flag.Bool("backup", true, "create a VACUUM INTO backup before mutating")
	flag.Parse()

	ctx := context.Background()
	database, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", filepath.ToSlash(filepath.Clean(*dbPath))))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	if _, err := database.ExecContext(ctx, `PRAGMA wal_checkpoint(FULL)`); err != nil {
		log.Printf("warning: wal checkpoint: %v", err)
	}

	if !*dryRun && *backup {
		backupPath := fmt.Sprintf("%s.backfill-%s.bak", *dbPath, time.Now().Format("20060102-150405"))
		if _, err := database.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", backupPath)); err != nil {
			log.Fatalf("backup: %v", err)
		}
		log.Printf("backup: %s", backupPath)
	}

	spanStarts, err := scanRawEvents(ctx, database)
	if err != nil {
		log.Fatalf("scan raw events: %v", err)
	}
	log.Printf("found %d span starts with duration/TTFT data", len(spanStarts))

	if len(spanStarts) == 0 {
		log.Println("no data to backfill, exiting")
		return
	}

	if *dryRun {
		printDryRun(spanStarts)
		return
	}

	stats, err := applyBackfill(ctx, database, spanStarts)
	if err != nil {
		log.Fatalf("apply: %v", err)
	}
	log.Printf("duration updated: %d rows", stats.DurationRows)
	log.Printf("ttft updated: %d rows", stats.TTFTRows)

	aggRows, err := rebuildAgg(ctx, database)
	if err != nil {
		log.Fatalf("rebuild agg: %v", err)
	}
	log.Printf("codex_daily_model_agg rebuilt: %d rows", aggRows)
}

type spanStart struct {
	SpanID      string
	SessionID   string
	Model       string
	StartNanos  int64
	DurationMs  int64
	TTFTMs      int64
	HasComplete bool
	HasCreated  bool
}

type backfillStats struct {
	DurationRows int64
	TTFTRows     int64
}

// rawLogRecord matches the JSON shape stored in codex_raw_otlp_events.raw_json.
// The OTLP proto JSON serialisation wraps AnyValue in an extra {"Value":{...}} layer.
type rawLogRecord struct {
	Attributes []struct {
		Key   string `json:"key"`
		Value struct {
			Inner struct {
				StringValue string `json:"stringValue"`
			} `json:"Value"`
		} `json:"value"`
	} `json:"attributes"`
	SpanID           string `json:"span_id"`
	ObservedTimeNano uint64 `json:"observed_time_unix_nano"`
	TimeUnixNano     uint64 `json:"time_unix_nano"`
}

func (r *rawLogRecord) attrs() map[string]string {
	m := make(map[string]string, len(r.Attributes))
	for _, kv := range r.Attributes {
		if v := kv.Value.Inner.StringValue; v != "" {
			m[kv.Key] = v
		}
	}
	return m
}

func (r *rawLogRecord) nanoTime() int64 {
	if r.ObservedTimeNano > 0 {
		return int64(r.ObservedTimeNano)
	}
	return int64(r.TimeUnixNano)
}

func scanRawEvents(ctx context.Context, db *sql.DB) (map[string]*spanStart, error) {
	var cnt int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='codex_raw_otlp_events'`).Scan(&cnt); err != nil {
		return nil, err
	}
	if cnt == 0 {
		return nil, nil
	}

	rows, err := db.QueryContext(ctx, `SELECT timestamp, raw_json FROM codex_raw_otlp_events ORDER BY timestamp ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	spans := make(map[string]*spanStart)

	for rows.Next() {
		var ts int64
		var rawJSON string
		if err := rows.Scan(&ts, &rawJSON); err != nil {
			return nil, err
		}

		var lr rawLogRecord
		if err := json.Unmarshal([]byte(rawJSON), &lr); err != nil {
			continue
		}

		attrs := lr.attrs()
		eventName := attrs["event.name"]
		spanID := lr.SpanID

		switch eventName {
		case "codex.websocket_request":
			if spanID == "" {
				continue
			}
			obsNano := lr.nanoTime()
			if obsNano == 0 {
				obsNano = ts * 1e9
			}
			spans[spanID] = &spanStart{
				SpanID:     spanID,
				SessionID:  attrs["conversation.id"],
				Model:      attrs["model"],
				StartNanos: obsNano,
			}

		case "codex.websocket_event":
			if spanID == "" {
				continue
			}
			start, ok := spans[spanID]
			if !ok {
				continue
			}
			obsNano := lr.nanoTime()
			if obsNano == 0 {
				obsNano = ts * 1e9
			}

			ek := attrs["event.kind"]
			switch ek {
			case "response.created":
				ttftMs := (obsNano - start.StartNanos) / 1e6
				if ttftMs > 0 {
					start.TTFTMs = ttftMs
					start.HasCreated = true
				}
			case "response.completed":
				durationMs := (obsNano - start.StartNanos) / 1e6
				if durationMs < 0 {
					durationMs = 0
				}
				start.DurationMs = durationMs
				start.HasComplete = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	filtered := make(map[string]*spanStart)
	for k, v := range spans {
		if v.HasComplete || v.HasCreated {
			filtered[k] = v
		}
	}
	return filtered, nil
}

func applyBackfill(ctx context.Context, db *sql.DB, spans map[string]*spanStart) (*backfillStats, error) {
	stats := &backfillStats{}

	type entry struct {
		key   string
		start *spanStart
	}
	entries := make([]entry, 0, len(spans))
	for k, v := range spans {
		entries = append(entries, entry{k, v})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].start.StartNanos < entries[j].start.StartNanos
	})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	for _, e := range entries {
		s := e.start
		tsSec := s.StartNanos / 1e9

		if s.HasComplete && s.DurationMs > 0 {
			res, err := tx.ExecContext(ctx, `
				UPDATE codex_api_requests
				SET duration_ms = ?
				WHERE rowid = (
					SELECT rowid FROM codex_api_requests
					WHERE session_id = ?
					  AND model = ?
					  AND duration_ms = 0
					  AND timestamp >= ? - 600
					  AND timestamp <= ? + 600
					ORDER BY ABS(timestamp - ?) ASC
					LIMIT 1
				)`,
				s.DurationMs, s.SessionID, s.Model, tsSec, tsSec, tsSec)
			if err != nil {
				return nil, fmt.Errorf("update duration span=%s: %w", s.SpanID, err)
			}
			n, _ := res.RowsAffected()
			stats.DurationRows += n
		}

		if s.HasCreated && s.TTFTMs > 0 {
			res, err := tx.ExecContext(ctx, `
				UPDATE codex_api_requests
				SET ttft_ms = ?
				WHERE rowid = (
					SELECT rowid FROM codex_api_requests
					WHERE session_id = ?
					  AND model = ?
					  AND (ttft_ms = 0 OR ttft_ms IS NULL)
					  AND timestamp >= ? - 600
					  AND timestamp <= ? + 600
					ORDER BY ABS(timestamp - ?) ASC
					LIMIT 1
				)`,
				s.TTFTMs, s.SessionID, s.Model, tsSec, tsSec, tsSec)
			if err != nil {
				return nil, fmt.Errorf("update ttft span=%s: %w", s.SpanID, err)
			}
			n, _ := res.RowsAffected()
			stats.TTFTRows += n
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stats, nil
}

func rebuildAgg(ctx context.Context, db *sql.DB) (int64, error) {
	if _, err := db.ExecContext(ctx, `DELETE FROM codex_daily_model_agg`); err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO codex_daily_model_agg (
			date, model, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, cost_usd, request_count
		)
		SELECT
			date(timestamp, 'unixepoch', 'localtime'),
			model,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COUNT(*)
		FROM codex_api_requests
		GROUP BY date(timestamp, 'unixepoch', 'localtime'), model`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func printDryRun(spans map[string]*spanStart) {
	type entry struct {
		key   string
		start *spanStart
	}
	entries := make([]entry, 0, len(spans))
	for k, v := range spans {
		entries = append(entries, entry{k, v})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].start.StartNanos < entries[j].start.StartNanos
	})

	fmt.Printf("%-20s %-20s %-30s %10s %10s\n", "span_id", "session_id", "model", "ttft_ms", "dur_ms")
	fmt.Println(strings.Repeat("-", 95))
	for _, e := range entries {
		s := e.start
		sid := s.SessionID
		if len(sid) > 18 {
			sid = sid[:18]
		}
		ttft := "-"
		if s.HasCreated {
			ttft = fmt.Sprintf("%d", s.TTFTMs)
		}
		dur := "-"
		if s.HasComplete {
			dur = fmt.Sprintf("%d", s.DurationMs)
		}
		sidShort := e.key
		if len(sidShort) > 20 {
			sidShort = sidShort[:20]
		}
		fmt.Printf("%-20s %-20s %-30s %10s %10s\n", sidShort, sid, s.Model, ttft, dur)
	}
}
