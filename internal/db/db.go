package db

import (
	"database/sql"
	"fmt"

	"github.com/young1lin/cc-otel/internal/config"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// Init opens the SQLite database at cfg.DBPath, runs schema migrations, and enables WAL mode.
func Init(cfg *config.Config) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}

	// WAL mode: allows concurrent reads while writing.
	// busy_timeout: wait up to 5s instead of failing immediately on lock contention.
	// cache_size=-65536: 64 MB page cache (negative = KB).
	// mmap_size=256MB: memory-mapped I/O to skip syscalls on reads of hot pages.
	// temp_store=MEMORY: keep temp tables/indexes (ORDER BY, GROUP BY) in RAM.
	// synchronous=NORMAL: WAL-safe; OS crash can lose last txn but DB stays consistent.
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")
	db.Exec("PRAGMA cache_size=-65536")
	db.Exec("PRAGMA mmap_size=268435456")
	db.Exec("PRAGMA temp_store=MEMORY")
	db.Exec("PRAGMA synchronous=NORMAL")

	// ANALYZE: refresh query-planner stats so the new composite indexes get picked.
	// Cheap on small DBs; skip failures silently (e.g., empty DB on first boot).
	db.Exec("ANALYZE")

	db.SetMaxOpenConns(4) // Allow concurrent readers; SQLite internal locking serializes writes
	return db, nil
}

func runMigrations(db *sql.DB) error {
	// 1) Create tables.
	_, err := db.Exec(`
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

		-- Pre-aggregated per-(local-day, model) rollup for Dashboard / Daily queries.
		-- Written alongside api_requests in InsertRequest; dashboard/daily reads hit
		-- at most O(days * models) rows instead of scanning raw requests.
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
	`)
	if err != nil {
		return err
	}

	// 2) Create indexes.
	_, err = db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_requests_request_id ON api_requests(request_id) WHERE request_id != '';
		CREATE INDEX IF NOT EXISTS idx_requests_timestamp ON api_requests(timestamp);
		CREATE INDEX IF NOT EXISTS idx_requests_model ON api_requests(model);
		CREATE INDEX IF NOT EXISTS idx_requests_session ON api_requests(session_id);
		CREATE INDEX IF NOT EXISTS idx_requests_user ON api_requests(user_id);
		-- Composite indexes: range-scan by timestamp then GROUP BY model/session in-place.
		-- Without these, GetDailyStatsByModel / GetSessionStats sort after scanning.
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
	`)
	if err != nil {
		return err
	}

	return nil
}
