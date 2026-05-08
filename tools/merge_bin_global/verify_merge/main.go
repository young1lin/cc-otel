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

type tableStats struct {
	Count     int64
	CostUnits int64
	MinTS     int64
	MaxTS     int64
	Err       string
}

func main() {
	var binPath string
	var globalPath string
	var fromStr string
	var toStr string
	var localDay string

	flag.StringVar(&binPath, "bin", "", "bin db path")
	flag.StringVar(&globalPath, "global", "", "global db path")
	flag.StringVar(&fromStr, "from", "", "range start RFC3339, e.g. 2026-04-23T00:00:00+08:00")
	flag.StringVar(&toStr, "to", "", "range end RFC3339 (optional; default now)")
	flag.StringVar(&localDay, "local-day", "", "optional local day YYYY-MM-DD to compare per-model api_requests and daily_model_agg")
	flag.Parse()

	if binPath == "" || globalPath == "" || fromStr == "" {
		panic("usage: verify_merge -bin <bin.db> -global <global.db> -from <RFC3339> [-to <RFC3339>]")
	}

	fromT, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		panic(err)
	}
	var toT time.Time
	if toStr == "" {
		toT = time.Now()
	} else {
		toT, err = time.Parse(time.RFC3339, toStr)
		if err != nil {
			panic(err)
		}
	}
	fromUnix := fromT.Unix()
	toUnix := toT.Unix()

	ctx := context.Background()

	binDB := mustOpen(ctx, binPath, true)
	defer binDB.Close()
	globalDB := mustOpen(ctx, globalPath, false)
	defer globalDB.Close()

	fmt.Printf("Window: from=%s (%d) to=%s (%d)\n", fromT.Format(time.RFC3339), fromUnix, toT.Format(time.RFC3339), toUnix)
	fmt.Println()

	var failed bool
	check := func(name string, q string, checkCost bool) {
		b := mustStats(ctx, binDB, q, fromUnix, toUnix)
		g := mustStats(ctx, globalDB, q, fromUnix, toUnix)
		fmt.Printf("%-30s bin=%s  global=%s\n", name, b, g)
		if !containedAtLeast(b, g, checkCost) {
			failed = true
			fmt.Printf("%-30s FAIL: global does not contain at least the local rows for this window\n", name)
		}
	}

	// Claude tables
	check("api_requests", `SELECT
		COALESCE(COUNT(*),0),
		COALESCE(SUM(cost_usd),0),
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM api_requests WHERE timestamp BETWEEN ? AND ?`, true)

	check("user_prompt_events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM user_prompt_events WHERE timestamp BETWEEN ? AND ?`, false)

	check("tool_decision_events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM tool_decision_events WHERE timestamp BETWEEN ? AND ?`, false)

	check("tool_result_events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM tool_result_events WHERE timestamp BETWEEN ? AND ?`, false)

	check("api_error_events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM api_error_events WHERE timestamp BETWEEN ? AND ?`, false)

	check("otel_metric_points", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM otel_metric_points WHERE timestamp BETWEEN ? AND ?`, false)

	check("events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM events WHERE timestamp BETWEEN ? AND ?`, false)

	check("raw_otlp_events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM raw_otlp_events WHERE timestamp BETWEEN ? AND ?`, false)

	// pending_ttft_spans uses 4-arg query
	{
		q := `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(span_end_unix),0),
		COALESCE(MAX(span_end_unix),0)
	FROM pending_ttft_spans WHERE (span_end_unix BETWEEN ? AND ?) OR (created_unix BETWEEN ? AND ?)`
		b := mustStats4(ctx, binDB, q, fromUnix, toUnix)
		g := mustStats4(ctx, globalDB, q, fromUnix, toUnix)
		fmt.Printf("%-30s bin=%s  global=%s\n", "pending_ttft_spans", b, g)
		if !containedAtLeast(b, g, false) {
			failed = true
			fmt.Printf("%-30s FAIL: global does not contain at least the local rows for this window\n", "pending_ttft_spans")
		}
	}

	// Codex tables
	fmt.Println()
	fmt.Println("--- Codex tables ---")

	check("codex_api_requests", `SELECT
		COALESCE(COUNT(*),0),
		COALESCE(SUM(cost_usd),0),
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM codex_api_requests WHERE timestamp BETWEEN ? AND ?`, true)

	check("codex_user_prompt_events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM codex_user_prompt_events WHERE timestamp BETWEEN ? AND ?`, false)

	check("codex_tool_decision_events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM codex_tool_decision_events WHERE timestamp BETWEEN ? AND ?`, false)

	check("codex_tool_result_events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM codex_tool_result_events WHERE timestamp BETWEEN ? AND ?`, false)

	check("codex_events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM codex_events WHERE timestamp BETWEEN ? AND ?`, false)

	check("codex_raw_otlp_events", `SELECT
		COALESCE(COUNT(*),0), 0,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM codex_raw_otlp_events WHERE timestamp BETWEEN ? AND ?`, false)

	// Ledger presence in global only.
	var ledgerCnt int64
	_ = globalDB.QueryRowContext(ctx, `SELECT COALESCE(COUNT(*),0) FROM import_ledger`).Scan(&ledgerCnt)
	fmt.Println()
	fmt.Printf("global.import_ledger total rows=%d\n", ledgerCnt)

	if localDay != "" {
		fmt.Println()
		fmt.Printf("Per-model (local day %s) from api_requests\n", localDay)
		printModelBreakdown(ctx, "bin", binDB, `SELECT model, COUNT(*) AS cnt, COALESCE(SUM(cost_usd),0) AS cost_units FROM api_requests WHERE date(timestamp, 'unixepoch', 'localtime') = ? GROUP BY model ORDER BY cost_units DESC, cnt DESC, model ASC`, localDay)
		printModelBreakdown(ctx, "global", globalDB, `SELECT model, COUNT(*) AS cnt, COALESCE(SUM(cost_usd),0) AS cost_units FROM api_requests WHERE date(timestamp, 'unixepoch', 'localtime') = ? GROUP BY model ORDER BY cost_units DESC, cnt DESC, model ASC`, localDay)

		fmt.Println()
		fmt.Printf("Per-model (local day %s) from daily_model_agg\n", localDay)
		printAggBreakdown(ctx, "bin", binDB, `SELECT model, request_count, cost_usd FROM daily_model_agg WHERE date = ? ORDER BY cost_usd DESC, request_count DESC, model ASC`, localDay)
		printAggBreakdown(ctx, "global", globalDB, `SELECT model, request_count, cost_usd FROM daily_model_agg WHERE date = ? ORDER BY cost_usd DESC, request_count DESC, model ASC`, localDay)

		fmt.Println()
		fmt.Printf("Per-model (local day %s) from codex_api_requests\n", localDay)
		printModelBreakdown(ctx, "bin", binDB, `SELECT model, COUNT(*) AS cnt, COALESCE(SUM(cost_usd),0) AS cost_units FROM codex_api_requests WHERE date(timestamp, 'unixepoch', 'localtime') = ? GROUP BY model ORDER BY cost_units DESC, cnt DESC, model ASC`, localDay)
		printModelBreakdown(ctx, "global", globalDB, `SELECT model, COUNT(*) AS cnt, COALESCE(SUM(cost_usd),0) AS cost_units FROM codex_api_requests WHERE date(timestamp, 'unixepoch', 'localtime') = ? GROUP BY model ORDER BY cost_units DESC, cnt DESC, model ASC`, localDay)

		fmt.Println()
		fmt.Printf("Per-model (local day %s) from codex_daily_model_agg\n", localDay)
		printAggBreakdown(ctx, "bin", binDB, `SELECT model, request_count, cost_usd FROM codex_daily_model_agg WHERE date = ? ORDER BY cost_usd DESC, request_count DESC, model ASC`, localDay)
		printAggBreakdown(ctx, "global", globalDB, `SELECT model, request_count, cost_usd FROM codex_daily_model_agg WHERE date = ? ORDER BY cost_usd DESC, request_count DESC, model ASC`, localDay)
	}

	if failed {
		os.Exit(1)
	}
}

func mustOpen(ctx context.Context, path string, readOnly bool) *sql.DB {
	base := filepath.ToSlash(filepath.Clean(path))
	if readOnly {
		base = fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", base)
	} else {
		base = fmt.Sprintf("file:%s?_busy_timeout=5000", base)
	}
	dsn := base
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		panic(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		panic(err)
	}
	return db
}

func mustStats(ctx context.Context, db *sql.DB, q string, fromUnix, toUnix int64) tableStats {
	var s tableStats
	err := db.QueryRowContext(ctx, q, fromUnix, toUnix).Scan(&s.Count, &s.CostUnits, &s.MinTS, &s.MaxTS)
	if err != nil {
		s.Err = err.Error()
	}
	return s
}

func mustStats4(ctx context.Context, db *sql.DB, q string, fromUnix, toUnix int64) tableStats {
	var s tableStats
	err := db.QueryRowContext(ctx, q, fromUnix, toUnix, fromUnix, toUnix).Scan(&s.Count, &s.CostUnits, &s.MinTS, &s.MaxTS)
	if err != nil {
		s.Err = err.Error()
	}
	return s
}

func containedAtLeast(bin tableStats, global tableStats, checkCost bool) bool {
	if bin.Err != "" || global.Err != "" {
		return false
	}
	if global.Count < bin.Count {
		return false
	}
	if checkCost && global.CostUnits < bin.CostUnits {
		return false
	}
	return true
}

func (s tableStats) String() string {
	if s.Err != "" {
		return "ERR:" + s.Err
	}
	if s.CostUnits != 0 {
		return fmt.Sprintf("cnt=%d sum_cost=%d min_ts=%d max_ts=%d", s.Count, s.CostUnits, s.MinTS, s.MaxTS)
	}
	return fmt.Sprintf("cnt=%d min_ts=%d max_ts=%d", s.Count, s.MinTS, s.MaxTS)
}

func printModelBreakdown(ctx context.Context, label string, db *sql.DB, q string, localDay string) {
	rows, err := db.QueryContext(ctx, q, localDay)
	if err != nil {
		fmt.Printf("%s: ERR:%v\n", label, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var model string
		var cnt int64
		var cost int64
		_ = rows.Scan(&model, &cnt, &cost)
		fmt.Printf("%s: model=%s cnt=%d cost_units=%d\n", label, model, cnt, cost)
	}
}

func printAggBreakdown(ctx context.Context, label string, db *sql.DB, q string, localDay string) {
	rows, err := db.QueryContext(ctx, q, localDay)
	if err != nil {
		fmt.Printf("%s: ERR:%v\n", label, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var model string
		var cnt int64
		var cost int64
		_ = rows.Scan(&model, &cnt, &cost)
		fmt.Printf("%s: model=%s request_count=%d cost_units=%d\n", label, model, cnt, cost)
	}
}
