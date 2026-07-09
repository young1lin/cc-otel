package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"path/filepath"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

type aggRow struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CostUSDUnits        int64
	RequestCount        int64
}

type codexAggRow struct {
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
	var dbPath string
	var fromDate string
	var toDate string

	flag.StringVar(&dbPath, "db", "", "global db path")
	flag.StringVar(&fromDate, "from-date", "", "local date YYYY-MM-DD")
	flag.StringVar(&toDate, "to-date", "", "local date YYYY-MM-DD (inclusive)")
	flag.Parse()

	if dbPath == "" || fromDate == "" || toDate == "" {
		fmt.Println("usage: repair_daily_agg -db <global.db> -from-date YYYY-MM-DD -to-date YYYY-MM-DD")
		return
	}

	loc := time.Local
	fromT, err := time.ParseInLocation("2006-01-02", fromDate, loc)
	if err != nil {
		panic(err)
	}
	toT, err := time.ParseInLocation("2006-01-02", toDate, loc)
	if err != nil {
		panic(err)
	}
	if toT.Before(fromT) {
		panic("to-date must be >= from-date")
	}

	ctx := context.Background()
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", filepath.ToSlash(filepath.Clean(dbPath))))
	if err != nil {
		panic(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := ensureCodexTokenColumns(ctx, db); err != nil {
		panic(err)
	}

	days := int(toT.Sub(fromT).Hours()/24) + 1
	fmt.Printf("Repairing daily_model_agg for %d day(s): %s..%s\n", days, fromDate, toDate)

	var totalApplied int64
	for d := fromT; !d.After(toT); d = d.Add(24 * time.Hour) {
		day := d.Format("2006-01-02")
		applied, err := repairOneDay(ctx, db, day)
		if err != nil {
			panic(err)
		}
		totalApplied += applied
	}
	fmt.Printf("Done (Claude). Applied %d delta row(s).\n", totalApplied)

	// Codex
	fmt.Printf("Repairing codex_daily_model_agg for %d day(s): %s..%s\n", days, fromDate, toDate)
	var codexTotalApplied int64
	for d := fromT; !d.After(toT); d = d.Add(24 * time.Hour) {
		day := d.Format("2006-01-02")
		applied, err := repairCodexOneDay(ctx, db, day)
		if err != nil {
			panic(err)
		}
		codexTotalApplied += applied
	}
	fmt.Printf("Done (Codex). Applied %d delta row(s).\n", codexTotalApplied)

	// Gemini (independent product channel). Skip cleanly on pre-gemini dbs.
	if has, _ := tableExists(ctx, db, "gemini_api_requests"); has {
		fmt.Printf("Repairing gemini_daily_model_agg for %d day(s): %s..%s\n", days, fromDate, toDate)
		var geminiTotalApplied int64
		for d := fromT; !d.After(toT); d = d.Add(24 * time.Hour) {
			day := d.Format("2006-01-02")
			applied, err := repairGeminiOneDay(ctx, db, day)
			if err != nil {
				panic(err)
			}
			geminiTotalApplied += applied
		}
		fmt.Printf("Done (Gemini). Applied %d delta row(s).\n", geminiTotalApplied)
	} else {
		fmt.Println("Skipping gemini_daily_model_agg (table not present).")
	}
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var got string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func repairOneDay(ctx context.Context, db *sql.DB, day string) (int64, error) {
	desired, err := aggFromRequests(ctx, db, day)
	if err != nil {
		return 0, err
	}
	current, err := aggFromDailyAgg(ctx, db, day)
	if err != nil {
		return 0, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
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
		return 0, err
	}
	defer stmt.Close()

	var applied int64
	for model, want := range desired {
		have := current[model]
		delta := diffNonNegative(want, have)
		if delta.RequestCount == 0 && delta.CostUSDUnits == 0 &&
			delta.InputTokens == 0 && delta.OutputTokens == 0 &&
			delta.CacheReadTokens == 0 && delta.CacheCreationTokens == 0 {
			continue
		}
		if _, err := stmt.ExecContext(ctx, day, model,
			delta.InputTokens, delta.OutputTokens, delta.CacheReadTokens, delta.CacheCreationTokens, delta.CostUSDUnits, delta.RequestCount,
		); err != nil {
			return applied, err
		}
		applied++
	}

	if err := tx.Commit(); err != nil {
		return applied, err
	}
	return applied, nil
}

func repairCodexOneDay(ctx context.Context, db *sql.DB, day string) (int64, error) {
	desired, err := codexAggFromRequests(ctx, db, day)
	if err != nil {
		return 0, err
	}
	current, err := codexAggFromDailyAgg(ctx, db, day)
	if err != nil {
		return 0, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
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
		return 0, err
	}
	defer stmt.Close()

	var applied int64
	for model, want := range desired {
		have := current[model]
		delta := codexDiffNonNegative(want, have)
		if delta.RequestCount == 0 && delta.CostUSDUnits == 0 &&
			delta.InputTokens == 0 && delta.OutputTokens == 0 &&
			delta.CacheReadTokens == 0 && delta.CacheCreationTokens == 0 &&
			delta.ReasoningTokens == 0 && delta.TotalTokens == 0 {
			continue
		}
		if _, err := stmt.ExecContext(ctx, day, model,
			delta.InputTokens, delta.OutputTokens, delta.CacheReadTokens, delta.CacheCreationTokens,
			delta.ReasoningTokens, delta.TotalTokens, delta.CostUSDUnits, delta.RequestCount,
		); err != nil {
			return applied, err
		}
		applied++
	}

	if err := tx.Commit(); err != nil {
		return applied, err
	}
	return applied, nil
}

func diffNonNegative(want aggRow, have aggRow) aggRow {
	out := aggRow{}
	out.InputTokens = max0(want.InputTokens - have.InputTokens)
	out.OutputTokens = max0(want.OutputTokens - have.OutputTokens)
	out.CacheReadTokens = max0(want.CacheReadTokens - have.CacheReadTokens)
	out.CacheCreationTokens = max0(want.CacheCreationTokens - have.CacheCreationTokens)
	out.CostUSDUnits = max0(want.CostUSDUnits - have.CostUSDUnits)
	out.RequestCount = max0(want.RequestCount - have.RequestCount)
	return out
}

func codexDiffNonNegative(want codexAggRow, have codexAggRow) codexAggRow {
	out := codexAggRow{}
	out.InputTokens = max0(want.InputTokens - have.InputTokens)
	out.OutputTokens = max0(want.OutputTokens - have.OutputTokens)
	out.CacheReadTokens = max0(want.CacheReadTokens - have.CacheReadTokens)
	out.CacheCreationTokens = max0(want.CacheCreationTokens - have.CacheCreationTokens)
	out.ReasoningTokens = max0(want.ReasoningTokens - have.ReasoningTokens)
	out.TotalTokens = max0(want.TotalTokens - have.TotalTokens)
	out.CostUSDUnits = max0(want.CostUSDUnits - have.CostUSDUnits)
	out.RequestCount = max0(want.RequestCount - have.RequestCount)
	return out
}

func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
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

func aggFromRequests(ctx context.Context, db *sql.DB, day string) (map[string]aggRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT
			model,
			COALESCE(SUM(input_tokens),0),
			COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(cache_read_tokens),0),
			COALESCE(SUM(cache_creation_tokens),0),
			COALESCE(SUM(cost_usd),0),
			COALESCE(COUNT(*),0)
		FROM api_requests
		WHERE date(timestamp, 'unixepoch', 'localtime') = ?
		GROUP BY model
	`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]aggRow{}
	for rows.Next() {
		var model string
		var r aggRow
		if err := rows.Scan(&model, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheCreationTokens, &r.CostUSDUnits, &r.RequestCount); err != nil {
			return nil, err
		}
		out[model] = r
	}
	return out, rows.Err()
}

func aggFromDailyAgg(ctx context.Context, db *sql.DB, day string) (map[string]aggRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, cost_usd, request_count
		FROM daily_model_agg
		WHERE date = ?
	`, day)
	if err != nil {
		return map[string]aggRow{}, nil
	}
	defer rows.Close()

	out := map[string]aggRow{}
	for rows.Next() {
		var model string
		var r aggRow
		if err := rows.Scan(&model, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheCreationTokens, &r.CostUSDUnits, &r.RequestCount); err != nil {
			return nil, err
		}
		out[model] = r
	}
	return out, rows.Err()
}

func codexAggFromRequests(ctx context.Context, db *sql.DB, day string) (map[string]codexAggRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT
			model,
			COALESCE(SUM(input_tokens),0),
			COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(cache_read_tokens),0),
			COALESCE(SUM(cache_creation_tokens),0),
			COALESCE(SUM(reasoning_tokens),0),
			COALESCE(SUM(total_tokens),0),
			COALESCE(SUM(cost_usd),0),
			COALESCE(COUNT(*),0)
		FROM codex_api_requests
		WHERE date(timestamp, 'unixepoch', 'localtime') = ?
		GROUP BY model
	`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]codexAggRow{}
	for rows.Next() {
		var model string
		var r codexAggRow
		if err := rows.Scan(&model, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheCreationTokens, &r.ReasoningTokens, &r.TotalTokens, &r.CostUSDUnits, &r.RequestCount); err != nil {
			return nil, err
		}
		out[model] = r
	}
	return out, rows.Err()
}

func codexAggFromDailyAgg(ctx context.Context, db *sql.DB, day string) (map[string]codexAggRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, cost_usd, request_count
		FROM codex_daily_model_agg
		WHERE date = ?
	`, day)
	if err != nil {
		return map[string]codexAggRow{}, nil
	}
	defer rows.Close()

	out := map[string]codexAggRow{}
	for rows.Next() {
		var model string
		var r codexAggRow
		if err := rows.Scan(&model, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheCreationTokens, &r.ReasoningTokens, &r.TotalTokens, &r.CostUSDUnits, &r.RequestCount); err != nil {
			return nil, err
		}
		out[model] = r
	}
	return out, rows.Err()
}

// --- Gemini CLI daily aggregate repair (independent product channel) ---

type geminiAggRow struct {
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	ThoughtsTokens  int64
	ToolTokens      int64
	TotalTokens     int64
	CostUSDUnits    int64
	DurationMsSum   int64
	RequestCount    int64
}

func repairGeminiOneDay(ctx context.Context, db *sql.DB, day string) (int64, error) {
	desired, err := geminiAggFromRequests(ctx, db, day)
	if err != nil {
		return 0, err
	}
	current, err := geminiAggFromDailyAgg(ctx, db, day)
	if err != nil {
		return 0, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO gemini_daily_model_agg (date, model, total_requests, input_tokens, output_tokens, cache_read_tokens, thoughts_tokens, tool_tokens, total_tokens, cost_usd, duration_ms_sum)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date, model) DO UPDATE SET
			total_requests  = total_requests + excluded.total_requests,
			input_tokens    = input_tokens + excluded.input_tokens,
			output_tokens   = output_tokens + excluded.output_tokens,
			cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
			thoughts_tokens = thoughts_tokens + excluded.thoughts_tokens,
			tool_tokens     = tool_tokens + excluded.tool_tokens,
			total_tokens    = total_tokens + excluded.total_tokens,
			cost_usd        = cost_usd + excluded.cost_usd,
			duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum
	`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	var applied int64
	for model, want := range desired {
		have := current[model]
		delta := geminiDiffNonNegative(want, have)
		if delta.RequestCount == 0 && delta.CostUSDUnits == 0 &&
			delta.InputTokens == 0 && delta.OutputTokens == 0 &&
			delta.CacheReadTokens == 0 && delta.ThoughtsTokens == 0 &&
			delta.ToolTokens == 0 && delta.TotalTokens == 0 && delta.DurationMsSum == 0 {
			continue
		}
		if _, err := stmt.ExecContext(ctx, day, model,
			delta.RequestCount, delta.InputTokens, delta.OutputTokens, delta.CacheReadTokens,
			delta.ThoughtsTokens, delta.ToolTokens, delta.TotalTokens, delta.CostUSDUnits, delta.DurationMsSum,
		); err != nil {
			return applied, err
		}
		applied++
	}

	if err := tx.Commit(); err != nil {
		return applied, err
	}
	return applied, nil
}

func geminiDiffNonNegative(want geminiAggRow, have geminiAggRow) geminiAggRow {
	return geminiAggRow{
		InputTokens:     max0(want.InputTokens - have.InputTokens),
		OutputTokens:    max0(want.OutputTokens - have.OutputTokens),
		CacheReadTokens: max0(want.CacheReadTokens - have.CacheReadTokens),
		ThoughtsTokens:  max0(want.ThoughtsTokens - have.ThoughtsTokens),
		ToolTokens:      max0(want.ToolTokens - have.ToolTokens),
		TotalTokens:     max0(want.TotalTokens - have.TotalTokens),
		CostUSDUnits:    max0(want.CostUSDUnits - have.CostUSDUnits),
		DurationMsSum:   max0(want.DurationMsSum - have.DurationMsSum),
		RequestCount:    max0(want.RequestCount - have.RequestCount),
	}
}

func geminiAggFromRequests(ctx context.Context, db *sql.DB, day string) (map[string]geminiAggRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT
			model,
			COALESCE(SUM(input_tokens),0),
			COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(cache_read_tokens),0),
			COALESCE(SUM(thoughts_tokens),0),
			COALESCE(SUM(tool_tokens),0),
			COALESCE(SUM(total_tokens),0),
			COALESCE(SUM(cost_usd),0),
			COALESCE(SUM(duration_ms),0),
			COALESCE(COUNT(*),0)
		FROM gemini_api_requests
		WHERE date(timestamp, 'unixepoch', 'localtime') = ?
		GROUP BY model
	`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]geminiAggRow{}
	for rows.Next() {
		var model string
		var r geminiAggRow
		if err := rows.Scan(&model, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.ThoughtsTokens, &r.ToolTokens, &r.TotalTokens, &r.CostUSDUnits, &r.DurationMsSum, &r.RequestCount); err != nil {
			return nil, err
		}
		out[model] = r
	}
	return out, rows.Err()
}

func geminiAggFromDailyAgg(ctx context.Context, db *sql.DB, day string) (map[string]geminiAggRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT model, input_tokens, output_tokens, cache_read_tokens, thoughts_tokens, tool_tokens, total_tokens, cost_usd, duration_ms_sum, total_requests
		FROM gemini_daily_model_agg
		WHERE date = ?
	`, day)
	if err != nil {
		return map[string]geminiAggRow{}, nil
	}
	defer rows.Close()

	out := map[string]geminiAggRow{}
	for rows.Next() {
		var model string
		var r geminiAggRow
		if err := rows.Scan(&model, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.ThoughtsTokens, &r.ToolTokens, &r.TotalTokens, &r.CostUSDUnits, &r.DurationMsSum, &r.RequestCount); err != nil {
			return nil, err
		}
		out[model] = r
	}
	return out, rows.Err()
}
