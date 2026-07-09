package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

type exportRecord struct {
	UUID  string         `json:"uuid"`
	Table string         `json:"table"`
	Ts    int64          `json:"ts"`
	Row   map[string]any `json:"row"`
}

type aggKey struct {
	Date  string
	Model string
}

type aggDelta struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CostUSDUnits        int64
	RequestCount        int64
}

type codexAggDelta struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	TotalTokens         int64
	CostUSDUnits        int64
	RequestCount        int64
}

func main() {
	var dstPath string
	var inPath string
	var sourceTag string
	var batchSize int

	flag.StringVar(&dstPath, "dst", "", "destination global db path (write)")
	flag.StringVar(&inPath, "in", "", "input JSONL path from export_bin")
	flag.StringVar(&sourceTag, "source", "bin", "source tag written to ledger")
	flag.IntVar(&batchSize, "batch", 1000, "rows per transaction commit")
	flag.Parse()

	if dstPath == "" || inPath == "" {
		fmt.Fprintln(os.Stderr, "usage: import_global -dst <global.db> -in <merge.jsonl> [-source <tag>] [-batch 1000]")
		os.Exit(2)
	}
	if batchSize <= 0 {
		batchSize = 1000
	}

	db, err := openSQLiteWritable(dstPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open dst db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	if err := ensureLedger(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "ensure ledger: %v\n", err)
		os.Exit(1)
	}
	if err := ensureTargetSchema(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "ensure schema: %v\n", err)
		os.Exit(1)
	}

	inF, err := os.Open(inPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open input: %v\n", err)
		os.Exit(1)
	}
	defer inF.Close()

	deltas := make(map[aggKey]*aggDelta)
	codexDeltas := make(map[aggKey]*codexAggDelta)

	sc := bufio.NewScanner(inF)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)

	var (
		tx        *sql.Tx
		txCount   int
		total     int64
		imported  int64
		skipped   int64
		batchErr  error
		commitNow = func() error {
			if tx == nil {
				return nil
			}
			err := tx.Commit()
			tx = nil
			txCount = 0
			return err
		}
	)

	beginTx := func() error {
		if tx != nil {
			return nil
		}
		txx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		tx = txx
		return nil
	}

	for sc.Scan() {
		total++
		var rec exportRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			batchErr = fmt.Errorf("decode line %d: %w", total, err)
			break
		}
		if rec.UUID == "" || rec.Table == "" || rec.Row == nil {
			skipped++
			continue
		}

		if err := beginTx(); err != nil {
			batchErr = err
			break
		}

		ok, err := tryLedger(ctx, tx, rec.UUID, sourceTag, rec.Table)
		if err != nil {
			batchErr = err
			break
		}
		if !ok {
			skipped++
			continue
		}

		exists, err := recordExists(ctx, tx, rec)
		if err != nil {
			batchErr = err
			break
		}
		if exists {
			skipped++
			continue
		}

		insertedRow, err := insertRecord(ctx, tx, rec)
		if err != nil {
			batchErr = err
			break
		}
		if insertedRow {
			imported++
			if rec.Table == "api_requests" {
				addAggDelta(deltas, rec.Row)
			} else if rec.Table == "codex_api_requests" {
				addCodexAggDelta(codexDeltas, rec.Row)
			}
		} else {
			skipped++
		}

		txCount++
		if txCount >= batchSize {
			if err := commitNow(); err != nil {
				batchErr = err
				break
			}
		}
	}

	if err := sc.Err(); err != nil && batchErr == nil {
		batchErr = err
	}

	if batchErr != nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		fmt.Fprintf(os.Stderr, "import failed: %v\n", batchErr)
		os.Exit(1)
	}

	if err := commitNow(); err != nil {
		fmt.Fprintf(os.Stderr, "commit: %v\n", err)
		os.Exit(1)
	}

	if err := applyAggDeltas(ctx, db, deltas); err != nil {
		fmt.Fprintf(os.Stderr, "apply daily_model_agg: %v\n", err)
		os.Exit(1)
	}
	if err := applyCodexAggDeltas(ctx, db, codexDeltas); err != nil {
		fmt.Fprintf(os.Stderr, "apply codex_daily_model_agg: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Processed %d lines. Imported %d new rows. Skipped %d (already in ledger).\n", total, imported, skipped)
}

func openSQLiteWritable(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000", filepath.ToSlash(path))
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func ensureLedger(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS import_ledger (
			uuid TEXT PRIMARY KEY,
			imported_at INTEGER NOT NULL,
			source_db TEXT NOT NULL,
			table_name TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_import_ledger_table_time ON import_ledger(table_name, imported_at);
	`)
	return err
}

func ensureTargetSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS api_requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			user_id TEXT DEFAULT '',
			prompt_id TEXT DEFAULT '',
			prompt_length INTEGER DEFAULT 0,
			model TEXT DEFAULT '',
			actual_model TEXT DEFAULT '',
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			cache_creation_tokens INTEGER DEFAULT 0,
			cost_usd INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0,
			ttft_ms INTEGER DEFAULT 0,
			request_id TEXT DEFAULT '',
			event_name TEXT DEFAULT '',
			event_sequence INTEGER DEFAULT 0,
			speed TEXT DEFAULT '',
			terminal_type TEXT DEFAULT '',
			tool_name TEXT DEFAULT '',
			decision TEXT DEFAULT '',
			source TEXT DEFAULT '',
			service_name TEXT DEFAULT '',
			service_version TEXT DEFAULT '',
			host_arch TEXT DEFAULT '',
			os_type TEXT DEFAULT '',
			os_version TEXT DEFAULT '',
			error_type TEXT DEFAULT '',
			error_message TEXT DEFAULT '',
			error_code INTEGER DEFAULT 0,
			error_retryable INTEGER DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS user_prompt_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			user_id TEXT DEFAULT '',
			prompt_id TEXT DEFAULT '',
			prompt_text TEXT DEFAULT '',
			prompt_length INTEGER DEFAULT 0,
			event_sequence INTEGER DEFAULT 0,
			terminal_type TEXT DEFAULT '',
			service_name TEXT DEFAULT '',
			service_version TEXT DEFAULT '',
			host_arch TEXT DEFAULT '',
			os_type TEXT DEFAULT '',
			os_version TEXT DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS tool_decision_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			user_id TEXT DEFAULT '',
			prompt_id TEXT DEFAULT '',
			event_sequence INTEGER DEFAULT 0,
			tool_name TEXT DEFAULT '',
			decision TEXT DEFAULT '',
			source TEXT DEFAULT '',
			terminal_type TEXT DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS tool_result_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			user_id TEXT DEFAULT '',
			prompt_id TEXT DEFAULT '',
			event_sequence INTEGER DEFAULT 0,
			tool_name TEXT DEFAULT '',
			success INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0,
			tool_result_size_bytes INTEGER DEFAULT 0,
			decision_source TEXT DEFAULT '',
			decision_type TEXT DEFAULT '',
			terminal_type TEXT DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS api_error_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			user_id TEXT DEFAULT '',
			prompt_id TEXT DEFAULT '',
			event_sequence INTEGER DEFAULT 0,
			model TEXT DEFAULT '',
			duration_ms INTEGER DEFAULT 0,
			terminal_type TEXT DEFAULT '',
			error_type TEXT DEFAULT '',
			error_message TEXT DEFAULT '',
			error_code INTEGER DEFAULT 0,
			error_retryable INTEGER DEFAULT 0,
			service_name TEXT DEFAULT '',
			service_version TEXT DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS otel_metric_points (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			metric_name TEXT NOT NULL,
			value REAL NOT NULL,
			session_id TEXT DEFAULT '',
			user_id TEXT DEFAULT '',
			terminal_type TEXT DEFAULT '',
			model TEXT DEFAULT '',
			attr_type TEXT DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			user_id TEXT DEFAULT '',
			prompt_id TEXT DEFAULT '',
			prompt_length INTEGER DEFAULT 0,
			event_name TEXT DEFAULT '',
			event_sequence INTEGER DEFAULT 0,
			model TEXT DEFAULT '',
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			cache_creation_tokens INTEGER DEFAULT 0,
			cost_usd REAL DEFAULT 0,
			duration_ms INTEGER DEFAULT 0,
			speed TEXT DEFAULT '',
			terminal_type TEXT DEFAULT '',
			tool_name TEXT DEFAULT '',
			decision TEXT DEFAULT '',
			source TEXT DEFAULT '',
			success INTEGER DEFAULT 0,
			tool_result_size_bytes INTEGER DEFAULT 0,
			service_name TEXT DEFAULT '',
			service_version TEXT DEFAULT '',
			host_arch TEXT DEFAULT '',
			os_type TEXT DEFAULT '',
			os_version TEXT DEFAULT '',
			error_type TEXT DEFAULT '',
			error_message TEXT DEFAULT '',
			error_code INTEGER DEFAULT 0,
			error_retryable INTEGER DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS raw_otlp_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			raw_json TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS pending_ttft_spans (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_unix INTEGER NOT NULL,
			session_id TEXT NOT NULL,
			model TEXT NOT NULL,
			span_end_unix INTEGER NOT NULL,
			ttft_ms INTEGER NOT NULL,
			raw_json TEXT NOT NULL,
			processed INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS daily_model_agg (
			date TEXT NOT NULL,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd INTEGER NOT NULL DEFAULT 0,
			request_count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (date, model)
		) WITHOUT ROWID;

		-- Codex tables
		CREATE TABLE IF NOT EXISTS codex_api_requests (
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
			total_tokens INTEGER DEFAULT 0,
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

		CREATE TABLE IF NOT EXISTS codex_user_prompt_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			conversation_id TEXT DEFAULT '',
			prompt_text TEXT DEFAULT '',
			prompt_length INTEGER DEFAULT 0,
			event_sequence INTEGER DEFAULT 0,
			terminal_type TEXT DEFAULT '',
			service_name TEXT DEFAULT '',
			service_version TEXT DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS codex_tool_decision_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			conversation_id TEXT DEFAULT '',
			tool_name TEXT DEFAULT '',
			call_id TEXT DEFAULT '',
			decision TEXT DEFAULT '',
			source TEXT DEFAULT '',
			event_sequence INTEGER DEFAULT 0,
			terminal_type TEXT DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS codex_tool_result_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			conversation_id TEXT DEFAULT '',
			tool_name TEXT DEFAULT '',
			call_id TEXT DEFAULT '',
			success INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0,
			arguments_length INTEGER DEFAULT 0,
			output_length INTEGER DEFAULT 0,
			tool_origin TEXT DEFAULT '',
			mcp_server TEXT DEFAULT '',
			error_message TEXT DEFAULT '',
			event_sequence INTEGER DEFAULT 0,
			terminal_type TEXT DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS codex_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			session_id TEXT DEFAULT '',
			conversation_id TEXT DEFAULT '',
			event_name TEXT DEFAULT '',
			event_kind TEXT DEFAULT '',
			model TEXT DEFAULT '',
			duration_ms INTEGER DEFAULT 0,
			error_message TEXT DEFAULT '',
			raw_attrs_json TEXT DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS codex_raw_otlp_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			raw_json TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS codex_daily_model_agg (
			date TEXT NOT NULL,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd INTEGER NOT NULL DEFAULT 0,
			request_count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (date, model)
		) WITHOUT ROWID;

		-- Gemini CLI tables (independent product channel)
		CREATE TABLE IF NOT EXISTS gemini_api_requests (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp       INTEGER NOT NULL,
			session_id      TEXT     NOT NULL DEFAULT '',
			model           TEXT     NOT NULL DEFAULT '',
			input_tokens    INTEGER  NOT NULL DEFAULT 0,
			output_tokens   INTEGER  NOT NULL DEFAULT 0,
			cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
			thoughts_tokens     INTEGER NOT NULL DEFAULT 0,
			tool_tokens         INTEGER NOT NULL DEFAULT 0,
			total_tokens        INTEGER NOT NULL DEFAULT 0,
			duration_ms     INTEGER  NOT NULL DEFAULT 0,
			cost_usd        INTEGER  NOT NULL DEFAULT 0,
			http_status_code INTEGER NOT NULL DEFAULT 0,
			prompt_id       TEXT     NOT NULL DEFAULT '',
			event_name      TEXT     NOT NULL DEFAULT 'api_response',
			service_name    TEXT     NOT NULL DEFAULT '',
			service_version TEXT     NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS gemini_daily_model_agg (
			date            TEXT     NOT NULL,
			model           TEXT     NOT NULL,
			total_requests  INTEGER  NOT NULL DEFAULT 0,
			input_tokens    INTEGER  NOT NULL DEFAULT 0,
			output_tokens   INTEGER  NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			thoughts_tokens INTEGER  NOT NULL DEFAULT 0,
			tool_tokens     INTEGER  NOT NULL DEFAULT 0,
			total_tokens    INTEGER  NOT NULL DEFAULT 0,
			cost_usd        INTEGER  NOT NULL DEFAULT 0,
			duration_ms_sum INTEGER  NOT NULL DEFAULT 0,
			PRIMARY KEY (date, model)
		) WITHOUT ROWID;
	`)
	if err != nil {
		return err
	}

	if err := ensureCodexTokenColumns(ctx, db); err != nil {
		return err
	}

	_, err = db.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS idx_requests_request_id ON api_requests(request_id) WHERE request_id != '';
		CREATE INDEX IF NOT EXISTS idx_requests_timestamp ON api_requests(timestamp);
		CREATE INDEX IF NOT EXISTS idx_requests_model ON api_requests(model);
		CREATE INDEX IF NOT EXISTS idx_requests_session ON api_requests(session_id);
		CREATE INDEX IF NOT EXISTS idx_requests_user ON api_requests(user_id);
		CREATE INDEX IF NOT EXISTS idx_requests_time_model ON api_requests(timestamp, model);
		CREATE INDEX IF NOT EXISTS idx_requests_time_session ON api_requests(timestamp, session_id);

		CREATE INDEX IF NOT EXISTS idx_user_prompt_time ON user_prompt_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_user_prompt_user ON user_prompt_events(user_id);

		CREATE INDEX IF NOT EXISTS idx_tool_decision_time ON tool_decision_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_tool_decision_user ON tool_decision_events(user_id);

		CREATE INDEX IF NOT EXISTS idx_tool_result_time ON tool_result_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_tool_result_user ON tool_result_events(user_id);

		CREATE INDEX IF NOT EXISTS idx_api_error_time ON api_error_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_api_error_user ON api_error_events(user_id);

		CREATE INDEX IF NOT EXISTS idx_metric_points_time ON otel_metric_points(timestamp);
		CREATE INDEX IF NOT EXISTS idx_metric_points_name ON otel_metric_points(metric_name);
		CREATE INDEX IF NOT EXISTS idx_metric_points_user ON otel_metric_points(user_id);

		CREATE INDEX IF NOT EXISTS idx_events_time ON events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id);
		CREATE INDEX IF NOT EXISTS idx_events_name ON events(event_name);
		CREATE INDEX IF NOT EXISTS idx_events_prompt ON events(prompt_id);

		CREATE INDEX IF NOT EXISTS idx_raw_otlp_time ON raw_otlp_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_pending_ttft_lookup ON pending_ttft_spans(processed, session_id, model, span_end_unix);

		CREATE INDEX IF NOT EXISTS idx_codex_requests_timestamp ON codex_api_requests(timestamp);
		CREATE INDEX IF NOT EXISTS idx_codex_requests_model ON codex_api_requests(model);
		CREATE INDEX IF NOT EXISTS idx_codex_requests_session ON codex_api_requests(session_id);
		CREATE INDEX IF NOT EXISTS idx_codex_requests_time_model ON codex_api_requests(timestamp, model);
		CREATE INDEX IF NOT EXISTS idx_codex_requests_time_session ON codex_api_requests(timestamp, session_id);
		CREATE INDEX IF NOT EXISTS idx_codex_requests_pending ON codex_api_requests(session_id, model, input_tokens, output_tokens, id);

		CREATE INDEX IF NOT EXISTS idx_codex_user_prompt_time ON codex_user_prompt_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_codex_tool_dec_time ON codex_tool_decision_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_codex_tool_res_time ON codex_tool_result_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_codex_events_time ON codex_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_codex_events_session ON codex_events(session_id);
		CREATE INDEX IF NOT EXISTS idx_codex_events_name ON codex_events(event_name);
		CREATE INDEX IF NOT EXISTS idx_codex_raw_time ON codex_raw_otlp_events(timestamp);

		CREATE INDEX IF NOT EXISTS idx_codex_tool_decision_time ON codex_tool_decision_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_codex_tool_result_time ON codex_tool_result_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_codex_raw_otlp_time ON codex_raw_otlp_events(timestamp);

		CREATE INDEX IF NOT EXISTS idx_gemini_requests_timestamp ON gemini_api_requests(timestamp);
		CREATE INDEX IF NOT EXISTS idx_gemini_requests_model     ON gemini_api_requests(model);
		CREATE INDEX IF NOT EXISTS idx_gemini_requests_session   ON gemini_api_requests(session_id);
		CREATE INDEX IF NOT EXISTS idx_gemini_requests_time_model ON gemini_api_requests(timestamp, model);
		CREATE INDEX IF NOT EXISTS idx_gemini_requests_dedup     ON gemini_api_requests(session_id, prompt_id, timestamp);
	`)
	return err
}

func ensureCodexTokenColumns(ctx context.Context, db *sql.DB) error {
	if err := ensureColumn(ctx, db, "codex_api_requests", "total_tokens", "INTEGER DEFAULT 0"); err != nil {
		return fmt.Errorf("codex_api_requests.total_tokens: %w", err)
	}
	if err := ensureColumn(ctx, db, "codex_daily_model_agg", "total_tokens", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("codex_daily_model_agg.total_tokens: %w", err)
	}

	hasTool, err := columnExists(ctx, db, "codex_api_requests", "tool_tokens")
	if err != nil {
		return fmt.Errorf("codex_api_requests.tool_tokens check: %w", err)
	}
	if hasTool {
		if _, err := db.ExecContext(ctx, `
			UPDATE codex_api_requests
			SET total_tokens = tool_tokens
			WHERE total_tokens = 0 AND tool_tokens > 0`); err != nil {
			return fmt.Errorf("codex_api_requests.total_tokens backfill: %w", err)
		}
	}

	hasTool, err = columnExists(ctx, db, "codex_daily_model_agg", "tool_tokens")
	if err != nil {
		return fmt.Errorf("codex_daily_model_agg.tool_tokens check: %w", err)
	}
	if hasTool {
		if _, err := db.ExecContext(ctx, `
			UPDATE codex_daily_model_agg
			SET total_tokens = tool_tokens
			WHERE total_tokens = 0 AND tool_tokens > 0`); err != nil {
			return fmt.Errorf("codex_daily_model_agg.total_tokens backfill: %w", err)
		}
	}
	return nil
}

func ensureColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	exists, err := columnExists(ctx, db, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

func columnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func tryLedger(ctx context.Context, tx *sql.Tx, uuid, sourceTag, table string) (bool, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO import_ledger (uuid, imported_at, source_db, table_name) VALUES (?, ?, ?, ?)`,
		uuid, time.Now().Unix(), sourceTag, table,
	)
	if err != nil {
		return false, err
	}
	aff, _ := res.RowsAffected()
	return aff > 0, nil
}

func recordExists(ctx context.Context, tx *sql.Tx, rec exportRecord) (bool, error) {
	cfg, ok := tableInsertConfigs()[rec.Table]
	if !ok {
		return false, fmt.Errorf("unknown table %q", rec.Table)
	}

	clauses := make([]string, 0, len(cfg.Columns))
	args := make([]any, 0, len(cfg.Columns))
	for _, col := range cfg.Columns {
		clauses = append(clauses, col+" IS ?")
		args = append(args, normalizeForSQLite(rec.Row[col]))
	}

	q := fmt.Sprintf("SELECT 1 FROM %s WHERE %s LIMIT 1", rec.Table, strings.Join(clauses, " AND "))
	var one int
	err := tx.QueryRowContext(ctx, q, args...).Scan(&one)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, err
}

func insertRecord(ctx context.Context, tx *sql.Tx, rec exportRecord) (bool, error) {
	cfg, ok := tableInsertConfigs()[rec.Table]
	if !ok {
		return false, fmt.Errorf("unknown table %q", rec.Table)
	}

	cols := cfg.Columns
	vals := make([]any, 0, len(cols))
	for _, c := range cols {
		vals = append(vals, normalizeForSQLite(rec.Row[c]))
	}

	placeholders := make([]string, len(cols))
	for i := range placeholders {
		placeholders[i] = "?"
	}

	stmt := fmt.Sprintf("INSERT %s INTO %s (%s) VALUES (%s)",
		cfg.InsertPrefix, rec.Table, strings.Join(cols, ", "), strings.Join(placeholders, ", "),
	)

	res, err := tx.ExecContext(ctx, stmt, vals...)
	if err != nil {
		return false, err
	}
	aff, _ := res.RowsAffected()
	return aff > 0, nil
}

type insertCfg struct {
	Columns      []string
	InsertPrefix string
}

func tableInsertConfigs() map[string]insertCfg {
	return map[string]insertCfg{
		"api_requests": {
			InsertPrefix: "OR IGNORE",
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "prompt_length",
				"model", "actual_model", "input_tokens", "output_tokens",
				"cache_read_tokens", "cache_creation_tokens", "cost_usd", "duration_ms", "ttft_ms", "request_id",
				"event_name", "event_sequence", "speed", "terminal_type", "tool_name", "decision", "source",
				"service_name", "service_version", "host_arch", "os_type", "os_version",
				"error_type", "error_message", "error_code", "error_retryable",
			},
		},
		"user_prompt_events": {
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "prompt_text", "prompt_length",
				"event_sequence", "terminal_type", "service_name", "service_version", "host_arch", "os_type", "os_version",
			},
		},
		"tool_decision_events": {
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "event_sequence",
				"tool_name", "decision", "source", "terminal_type",
			},
		},
		"tool_result_events": {
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "event_sequence",
				"tool_name", "success", "duration_ms", "tool_result_size_bytes",
				"decision_source", "decision_type", "terminal_type",
			},
		},
		"api_error_events": {
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "event_sequence",
				"model", "duration_ms", "terminal_type",
				"error_type", "error_message", "error_code", "error_retryable",
				"service_name", "service_version",
			},
		},
		"otel_metric_points": {
			Columns: []string{
				"timestamp", "metric_name", "value", "session_id", "user_id", "terminal_type", "model", "attr_type",
			},
		},
		"events": {
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "prompt_length",
				"event_name", "event_sequence", "model",
				"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
				"cost_usd", "duration_ms", "speed", "terminal_type", "tool_name", "decision", "source",
				"success", "tool_result_size_bytes",
				"service_name", "service_version", "host_arch", "os_type", "os_version",
				"error_type", "error_message", "error_code", "error_retryable",
			},
		},
		"raw_otlp_events": {
			Columns: []string{"timestamp", "event_type", "raw_json"},
		},
		"pending_ttft_spans": {
			Columns: []string{"created_unix", "session_id", "model", "span_end_unix", "ttft_ms", "raw_json", "processed"},
		},
		// Codex tables
		"codex_api_requests": {
			Columns: []string{
				"timestamp", "session_id", "user_id", "model",
				"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
				"reasoning_tokens", "total_tokens", "cost_usd", "duration_ms", "ttft_ms",
				"http_status", "endpoint", "conversation_id",
				"event_name", "event_sequence", "terminal_type",
				"service_name", "service_version", "host_arch", "os_type", "os_version",
				"error_message",
			},
		},
		"codex_user_prompt_events": {
			Columns: []string{
				"timestamp", "session_id", "conversation_id", "prompt_text", "prompt_length",
				"event_sequence", "terminal_type", "service_name", "service_version",
			},
		},
		"codex_tool_decision_events": {
			Columns: []string{
				"timestamp", "session_id", "conversation_id", "tool_name", "call_id",
				"decision", "source", "event_sequence", "terminal_type",
			},
		},
		"codex_tool_result_events": {
			Columns: []string{
				"timestamp", "session_id", "conversation_id", "tool_name", "call_id",
				"success", "duration_ms", "arguments_length", "output_length",
				"tool_origin", "mcp_server", "error_message", "event_sequence", "terminal_type",
			},
		},
		"codex_events": {
			Columns: []string{
				"timestamp", "session_id", "conversation_id", "event_name", "event_kind",
				"model", "duration_ms", "error_message", "raw_attrs_json",
			},
		},
		"codex_raw_otlp_events": {
			Columns: []string{"timestamp", "event_type", "raw_json"},
		},
		// Gemini CLI table (independent product channel)
		"gemini_api_requests": {
			Columns: []string{
				"timestamp", "session_id", "model",
				"input_tokens", "output_tokens", "cache_read_tokens",
				"thoughts_tokens", "tool_tokens", "total_tokens",
				"duration_ms", "cost_usd", "http_status_code",
				"prompt_id", "event_name", "service_name", "service_version",
			},
		},
	}
}

func normalizeForSQLite(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	default:
		return t
	}
}

func addAggDelta(deltas map[aggKey]*aggDelta, row map[string]any) {
	ts, ok := toInt64(row["timestamp"])
	if !ok || ts <= 0 {
		return
	}
	model, _ := row["model"].(string)
	date := time.Unix(ts, 0).Local().Format("2006-01-02")
	key := aggKey{Date: date, Model: model}
	d := deltas[key]
	if d == nil {
		d = &aggDelta{}
		deltas[key] = d
	}

	d.InputTokens += mustInt64(row["input_tokens"])
	d.OutputTokens += mustInt64(row["output_tokens"])
	d.CacheReadTokens += mustInt64(row["cache_read_tokens"])
	d.CacheCreationTokens += mustInt64(row["cache_creation_tokens"])
	d.CostUSDUnits += mustInt64(row["cost_usd"])
	d.RequestCount += 1
}

func addCodexAggDelta(deltas map[aggKey]*codexAggDelta, row map[string]any) {
	ts, ok := toInt64(row["timestamp"])
	if !ok || ts <= 0 {
		return
	}
	model, _ := row["model"].(string)
	date := time.Unix(ts, 0).Local().Format("2006-01-02")
	key := aggKey{Date: date, Model: model}
	d := deltas[key]
	if d == nil {
		d = &codexAggDelta{}
		deltas[key] = d
	}

	d.InputTokens += mustInt64(row["input_tokens"])
	d.OutputTokens += mustInt64(row["output_tokens"])
	d.CacheReadTokens += mustInt64(row["cache_read_tokens"])
	d.CacheCreationTokens += mustInt64(row["cache_creation_tokens"])
	d.ReasoningTokens += mustInt64(row["reasoning_tokens"])
	d.TotalTokens += mustInt64(row["total_tokens"])
	d.CostUSDUnits += mustInt64(row["cost_usd"])
	d.RequestCount += 1
}

func applyAggDeltas(ctx context.Context, db *sql.DB, deltas map[aggKey]*aggDelta) error {
	if len(deltas) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO daily_model_agg (date, model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, cost_usd, request_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date, model) DO UPDATE SET
			input_tokens          = input_tokens + excluded.input_tokens,
			output_tokens         = output_tokens + excluded.output_tokens,
			cache_read_tokens     = cache_read_tokens + excluded.cache_read_tokens,
			cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
			cost_usd              = cost_usd + excluded.cost_usd,
			request_count         = request_count + excluded.request_count
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for k, d := range deltas {
		if d.RequestCount <= 0 {
			continue
		}
		if _, err := stmt.ExecContext(ctx,
			k.Date, k.Model,
			d.InputTokens, d.OutputTokens, d.CacheReadTokens, d.CacheCreationTokens, d.CostUSDUnits, d.RequestCount,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func applyCodexAggDeltas(ctx context.Context, db *sql.DB, deltas map[aggKey]*codexAggDelta) error {
	if len(deltas) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO codex_daily_model_agg (date, model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, cost_usd, request_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date, model) DO UPDATE SET
			input_tokens          = input_tokens + excluded.input_tokens,
			output_tokens         = output_tokens + excluded.output_tokens,
			cache_read_tokens     = cache_read_tokens + excluded.cache_read_tokens,
			cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
			reasoning_tokens      = reasoning_tokens + excluded.reasoning_tokens,
			total_tokens          = total_tokens + excluded.total_tokens,
			cost_usd              = cost_usd + excluded.cost_usd,
			request_count         = request_count + excluded.request_count
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for k, d := range deltas {
		if d.RequestCount <= 0 {
			continue
		}
		if _, err := stmt.ExecContext(ctx,
			k.Date, k.Model,
			d.InputTokens, d.OutputTokens, d.CacheReadTokens, d.CacheCreationTokens,
			d.ReasoningTokens, d.TotalTokens, d.CostUSDUnits, d.RequestCount,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case string:
		var out int64
		_, err := fmt.Sscan(t, &out)
		return out, err == nil
	default:
		return 0, false
	}
}

func mustInt64(v any) int64 {
	n, _ := toInt64(v)
	return n
}
