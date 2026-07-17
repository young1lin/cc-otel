package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/young1lin/cc-otel/internal/dbmerge"

	_ "github.com/ncruces/go-sqlite3/driver"
)

type exportRecord struct {
	UUID  string         `json:"uuid"`
	Table string         `json:"table"`
	Ts    int64          `json:"ts"`
	Row   map[string]any `json:"row"`
}

type tableCfg struct {
	Name      string
	Columns   []string
	WhereSQL  string
	Args      func(fromUnix, toUnix int64) []any
	TsFromRow func(row map[string]any) (int64, bool)
}

var errMissingTable = errors.New("missing table")

func main() {
	var srcPath string
	var outPath string
	var fromStr string
	var toStr string

	flag.StringVar(&srcPath, "src", "", "source bin db path (read-only)")
	flag.StringVar(&outPath, "out", "", "output JSONL path")
	flag.StringVar(&fromStr, "from", "", "range start RFC3339, e.g. 2026-04-23T00:00:00+08:00")
	flag.StringVar(&toStr, "to", "", "range end RFC3339 (optional; default now)")
	flag.Parse()

	if srcPath == "" || outPath == "" || fromStr == "" {
		fmt.Fprintln(os.Stderr, "usage: export_bin -src <bin.db> -out <file.jsonl> -from <RFC3339> [-to <RFC3339>]")
		os.Exit(2)
	}

	fromT, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse -from: %v\n", err)
		os.Exit(2)
	}

	var toT time.Time
	if toStr == "" {
		toT = time.Now()
	} else {
		toT, err = time.Parse(time.RFC3339, toStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse -to: %v\n", err)
			os.Exit(2)
		}
	}

	fromUnix := fromT.Unix()
	toUnix := toT.Unix()
	if toUnix < fromUnix {
		fmt.Fprintln(os.Stderr, "-to must be >= -from")
		os.Exit(2)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil && filepath.Dir(outPath) != "." {
		fmt.Fprintf(os.Stderr, "mkdir out dir: %v\n", err)
		os.Exit(1)
	}

	db, err := openSQLiteReadOnly(srcPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open src db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	outF, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create out: %v\n", err)
		os.Exit(1)
	}
	defer outF.Close()

	w := bufio.NewWriterSize(outF, 1024*1024)
	defer w.Flush()

	ctx := context.Background()
	cfgs := tableConfigs()

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	var total int64
	for _, cfg := range cfgs {
		n, qErr := exportTable(ctx, db, enc, cfg, fromUnix, toUnix)
		if qErr != nil {
			fmt.Fprintf(os.Stderr, "export %s: %v\n", cfg.Name, qErr)
			os.Exit(1)
		}
		total += n
	}

	fmt.Printf("Exported %d rows to %s\n", total, outPath)
}

func openSQLiteReadOnly(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", filepath.ToSlash(path))
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func exportTable(ctx context.Context, db *sql.DB, enc *json.Encoder, cfg tableCfg, fromUnix, toUnix int64) (int64, error) {
	selectCols, err := selectColumns(ctx, db, cfg)
	if err != nil {
		if errors.Is(err, errMissingTable) {
			fmt.Fprintf(os.Stderr, "skip %s: table does not exist\n", cfg.Name)
			return 0, nil
		}
		return 0, err
	}
	cols := strings.Join(selectCols, ", ")
	q := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s ASC", cols, cfg.Name, cfg.WhereSQL, cfg.Columns[0])

	args := cfg.Args(fromUnix, toUnix)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return 0, err
	}

	var n int64
	for rows.Next() {
		vals := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}

		row := make(map[string]any, len(colNames))
		for i, c := range colNames {
			row[c] = normalizeSQLiteValue(vals[i])
		}

		ts, ok := cfg.TsFromRow(row)
		if !ok {
			continue
		}

		u, err := dbmerge.LedgerID(dbmerge.Row{Table: cfg.Name, Values: row})
		if err != nil {
			return n, err
		}

		rec := exportRecord{
			UUID:  u,
			Table: cfg.Name,
			Ts:    ts,
			Row:   row,
		}
		if err := enc.Encode(&rec); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	return n, nil
}

func normalizeSQLiteValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(t)
	default:
		return t
	}
}

func selectColumns(ctx context.Context, db *sql.DB, cfg tableCfg) ([]string, error) {
	cols, err := tableColumns(ctx, db, cfg.Name)
	if err != nil {
		return nil, err
	}
	if cols == nil {
		return nil, errMissingTable
	}

	selectCols := make([]string, 0, len(cfg.Columns))
	for _, col := range cfg.Columns {
		if cfg.Name == "codex_api_requests" && col == "total_tokens" && cols["total_tokens"] && cols["tool_tokens"] {
			selectCols = append(selectCols, "CASE WHEN total_tokens != 0 THEN total_tokens WHEN tool_tokens > 0 THEN tool_tokens ELSE total_tokens END AS total_tokens")
			continue
		}
		if cols[col] {
			selectCols = append(selectCols, col)
			continue
		}
		if cfg.Name == "codex_api_requests" && col == "total_tokens" && cols["tool_tokens"] {
			selectCols = append(selectCols, "tool_tokens AS total_tokens")
			continue
		}
		if cfg.Name == "pending_ttft_spans" && col == "request_id" {
			selectCols = append(selectCols, "'' AS request_id")
			continue
		}
		return nil, fmt.Errorf("%s missing required column %s", cfg.Name, col)
	}
	return selectCols, nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	var tableName string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
	).Scan(&tableName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

func tableConfigs() []tableCfg {
	cfgs := []tableCfg{
		{
			Name: "api_requests",
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "prompt_length",
				"model", "actual_model", "input_tokens", "output_tokens",
				"cache_read_tokens", "cache_creation_tokens", "cost_usd", "duration_ms", "ttft_ms", "request_id",
				"event_name", "event_sequence", "speed", "terminal_type", "tool_name", "decision", "source",
				"service_name", "service_version", "host_arch", "os_type", "os_version",
				"error_type", "error_message", "error_code", "error_retryable",
			},
			WhereSQL: "timestamp BETWEEN ? AND ?",
			Args:     func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) {
				return toInt64(row["timestamp"])
			},
		},
		{
			Name: "user_prompt_events",
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "prompt_text", "prompt_length",
				"event_sequence", "terminal_type", "service_name", "service_version", "host_arch", "os_type", "os_version",
			},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name: "tool_decision_events",
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "event_sequence",
				"tool_name", "decision", "source", "terminal_type",
			},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name: "tool_result_events",
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "event_sequence",
				"tool_name", "success", "duration_ms", "tool_result_size_bytes",
				"decision_source", "decision_type", "terminal_type",
			},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name: "api_error_events",
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "event_sequence",
				"model", "duration_ms", "terminal_type",
				"error_type", "error_message", "error_code", "error_retryable",
				"service_name", "service_version",
			},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name: "otel_metric_points",
			Columns: []string{
				"timestamp", "metric_name", "value", "session_id", "user_id", "terminal_type", "model", "attr_type",
			},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name: "events",
			Columns: []string{
				"timestamp", "session_id", "user_id", "prompt_id", "prompt_length",
				"event_name", "event_sequence", "model",
				"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
				"cost_usd", "duration_ms", "speed", "terminal_type", "tool_name", "decision", "source",
				"success", "tool_result_size_bytes",
				"service_name", "service_version", "host_arch", "os_type", "os_version",
				"error_type", "error_message", "error_code", "error_retryable",
			},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name:      "raw_otlp_events",
			Columns:   []string{"timestamp", "event_type", "raw_json"},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name:     "pending_ttft_spans",
			Columns:  []string{"created_unix", "session_id", "model", "span_end_unix", "ttft_ms", "raw_json", "processed"},
			WhereSQL: "(span_end_unix BETWEEN ? AND ?) OR (created_unix BETWEEN ? AND ?)",
			Args:     func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix, fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) {
				if ts, ok := toInt64(row["span_end_unix"]); ok {
					return ts, true
				}
				return toInt64(row["created_unix"])
			},
		},
		// --- Codex tables ---
		{
			Name: "codex_api_requests",
			Columns: []string{
				"timestamp", "session_id", "user_id", "model",
				"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
				"reasoning_tokens", "total_tokens", "cost_usd", "duration_ms", "ttft_ms",
				"http_status", "endpoint", "conversation_id",
				"event_name", "event_sequence", "terminal_type",
				"service_name", "service_version", "host_arch", "os_type", "os_version",
				"error_message",
			},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name: "codex_user_prompt_events",
			Columns: []string{
				"timestamp", "session_id", "conversation_id", "prompt_text", "prompt_length",
				"event_sequence", "terminal_type", "service_name", "service_version",
			},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name: "codex_tool_decision_events",
			Columns: []string{
				"timestamp", "session_id", "conversation_id", "tool_name", "call_id",
				"decision", "source", "event_sequence", "terminal_type",
			},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name: "codex_tool_result_events",
			Columns: []string{
				"timestamp", "session_id", "conversation_id", "tool_name", "call_id",
				"success", "duration_ms", "arguments_length", "output_length",
				"tool_origin", "mcp_server", "error_message", "event_sequence", "terminal_type",
			},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
		{
			Name:      "codex_raw_otlp_events",
			Columns:   []string{"timestamp", "event_type", "raw_json"},
			WhereSQL:  "timestamp BETWEEN ? AND ?",
			Args:      func(fromUnix, toUnix int64) []any { return []any{fromUnix, toUnix} },
			TsFromRow: func(row map[string]any) (int64, bool) { return toInt64(row["timestamp"]) },
		},
	}

	sort.Slice(cfgs, func(i, j int) bool { return cfgs[i].Name < cfgs[j].Name })
	for i := range cfgs {
		if spec, ok := dbmerge.LookupSpec(cfgs[i].Name); ok {
			cfgs[i].Columns = spec.Columns
		}
	}
	return cfgs
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
