package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

type rawSpan struct {
	SpanName      string            `json:"span_name"`
	StartUnixNano uint64            `json:"start_unix_nano"`
	EndUnixNano   uint64            `json:"end_unix_nano"`
	Attributes    map[string]string `json:"attributes"`
	ResourceAttrs map[string]string `json:"resource_attributes,omitempty"`
}

type spanTTFT struct {
	RequestID string
	SessionID string
	PromptID  string
	Model     string
	EndUnix   int64
	TTFTMs    int64
}

type stats struct {
	Spans          int64
	UpdatedByReqID int64
	UpdatedPrompt  int64
	UpdatedLoose   int64
	NoMatch        int64
}

func main() {
	dbPath := flag.String("db", filepath.Join("bin", "cc-otel.db"), "SQLite database path")
	from := flag.String("from", "", "inclusive local date YYYY-MM-DD")
	to := flag.String("to", "", "inclusive local date YYYY-MM-DD")
	dryRun := flag.Bool("dry-run", false, "run inside a rollback-only transaction")
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
		backupPath := fmt.Sprintf("%s.claude-ttft-%s.bak", *dbPath, time.Now().Format("20060102-150405"))
		if _, err := database.ExecContext(ctx, "VACUUM INTO "+sqlString(backupPath)); err != nil {
			log.Fatalf("backup: %v", err)
		}
		log.Printf("backup: %s", backupPath)
	}

	fromUnix, toUnix, err := dateBounds(*from, *to)
	if err != nil {
		log.Fatalf("date range: %v", err)
	}
	spans, err := scanSpans(ctx, database, fromUnix, toUnix)
	if err != nil {
		log.Fatalf("scan spans: %v", err)
	}
	log.Printf("ttft spans found: %d", len(spans))

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	out, err := apply(ctx, tx, spans)
	if err != nil {
		log.Fatalf("apply: %v", err)
	}
	if *dryRun {
		log.Printf("dry-run complete; rolling back")
	} else if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	log.Printf("updated by request_id: %d", out.UpdatedByReqID)
	log.Printf("updated by prompt/session/model: %d", out.UpdatedPrompt)
	log.Printf("updated by loose session/model: %d", out.UpdatedLoose)
	log.Printf("no match: %d", out.NoMatch)
}

func scanSpans(ctx context.Context, db *sql.DB, fromUnix, toUnix int64) ([]spanTTFT, error) {
	args := []any{}
	where := `event_type='trace' AND raw_json LIKE '%ttft_ms%'`
	if fromUnix > 0 {
		where += ` AND timestamp >= ?`
		args = append(args, fromUnix)
	}
	if toUnix > 0 {
		where += ` AND timestamp < ?`
		args = append(args, toUnix)
	}

	rows, err := db.QueryContext(ctx, `SELECT timestamp, raw_json FROM raw_otlp_events WHERE `+where+` ORDER BY timestamp ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []spanTTFT
	for rows.Next() {
		var ts int64
		var raw string
		if err := rows.Scan(&ts, &raw); err != nil {
			return nil, err
		}
		var sp rawSpan
		if err := json.Unmarshal([]byte(raw), &sp); err != nil {
			continue
		}
		ttft := parseInt(pick(sp.Attributes, sp.ResourceAttrs, "ttft_ms"))
		if ttft <= 0 {
			continue
		}
		endUnix := ts
		if sp.EndUnixNano > 0 {
			endUnix = int64(sp.EndUnixNano / 1e9)
		} else if sp.StartUnixNano > 0 {
			endUnix = int64(sp.StartUnixNano / 1e9)
		}
		out = append(out, spanTTFT{
			RequestID: pick(sp.Attributes, sp.ResourceAttrs, "request_id", "request.id", "requestId", "cc.request_id"),
			SessionID: pick(sp.Attributes, sp.ResourceAttrs, "session.id", "session_id", "sessionId", "cc.session_id"),
			PromptID:  pick(sp.Attributes, sp.ResourceAttrs, "prompt.id", "prompt_id", "promptId", "cc.prompt_id"),
			Model: pick(sp.Attributes, sp.ResourceAttrs,
				"model", "model.name", "model_id", "modelId", "cc.model",
				"gen_ai.request.model", "gen_ai.response.model", "anthropic.model", "llm.model"),
			EndUnix: endUnix,
			TTFTMs:  ttft,
		})
	}
	return out, rows.Err()
}

func apply(ctx context.Context, tx *sql.Tx, spans []spanTTFT) (*stats, error) {
	out := &stats{Spans: int64(len(spans))}
	for _, sp := range spans {
		updated := false
		if sp.RequestID != "" {
			n, err := execRows(ctx, tx, `UPDATE api_requests SET ttft_ms = ? WHERE request_id = ? AND ttft_ms = 0`, sp.TTFTMs, sp.RequestID)
			if err != nil {
				return nil, err
			}
			if n > 0 {
				out.UpdatedByReqID += n
				updated = true
			}
		}
		if !updated && sp.SessionID != "" && sp.PromptID != "" && sp.Model != "" {
			n, err := execRows(ctx, tx, `
				UPDATE api_requests
				SET ttft_ms = ?
				WHERE id = (
					SELECT id FROM api_requests
					WHERE session_id = ?
					  AND prompt_id = ?
					  AND model = ?
					  AND ttft_ms = 0
					  AND timestamp BETWEEN (? - 600) AND (? + 600)
					ORDER BY ABS(timestamp - ?) ASC
					LIMIT 1
				)`, sp.TTFTMs, sp.SessionID, sp.PromptID, sp.Model, sp.EndUnix, sp.EndUnix, sp.EndUnix)
			if err != nil {
				return nil, err
			}
			if n > 0 {
				out.UpdatedPrompt += n
				updated = true
			}
		}
		if !updated && sp.SessionID != "" && sp.Model != "" {
			n, err := execRows(ctx, tx, `
				UPDATE api_requests
				SET ttft_ms = ?
				WHERE id = (
					SELECT id FROM api_requests
					WHERE session_id = ?
					  AND model = ?
					  AND ttft_ms = 0
					  AND timestamp BETWEEN (? - 120) AND (? + 120)
					ORDER BY ABS(timestamp - ?) ASC
					LIMIT 1
				)`, sp.TTFTMs, sp.SessionID, sp.Model, sp.EndUnix, sp.EndUnix, sp.EndUnix)
			if err != nil {
				return nil, err
			}
			if n > 0 {
				out.UpdatedLoose += n
				updated = true
			}
		}
		if !updated {
			out.NoMatch++
		}
	}
	return out, nil
}

func pick(attrs, res map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := attrs[k]; v != "" {
			return v
		}
		if v := res[k]; v != "" {
			return v
		}
	}
	return ""
}

func parseInt(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func execRows(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

func dateBounds(from, to string) (int64, int64, error) {
	if from == "" && to == "" {
		return 0, 0, nil
	}
	loc := time.Local
	var fromUnix int64
	var toUnix int64
	if from != "" {
		t, err := time.ParseInLocation("2006-01-02", from, loc)
		if err != nil {
			return 0, 0, err
		}
		fromUnix = t.Unix()
	}
	if to != "" {
		t, err := time.ParseInLocation("2006-01-02", to, loc)
		if err != nil {
			return 0, 0, err
		}
		toUnix = t.Add(24 * time.Hour).Unix()
	}
	return fromUnix, toUnix, nil
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
