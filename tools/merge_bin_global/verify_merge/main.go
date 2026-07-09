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

	// Attach the bin db so request tables can be checked row-by-row
	// (NOT EXISTS containment) instead of by raw counts: a deduplicating
	// merge legitimately produces fewer rows than a source with duplicates.
	if _, err := globalDB.ExecContext(ctx, `ATTACH DATABASE ? AS src`, filepath.ToSlash(filepath.Clean(binPath))); err != nil {
		panic(fmt.Sprintf("attach bin db: %v", err))
	}

	fmt.Printf("Window: from=%s (%d) to=%s (%d)\n", fromT.Format(time.RFC3339), fromUnix, toT.Format(time.RFC3339), toUnix)
	fmt.Println()

	var failed bool
	check := func(name string, q string, checkCost bool) {
		b := mustStats(ctx, binDB, q, fromUnix, toUnix)
		g := mustStats(ctx, globalDB, q, fromUnix, toUnix)
		fmt.Printf("%-30s bin=%s  global=%s\n", name, b, g)

		if mq, ok := containmentSQL()[name]; ok && hasTable(ctx, binDB, name) {
			// Row-by-row containment on the natural key (cost_usd excluded:
			// recompute_cost may have repriced one side). Raw counts can
			// differ when the source holds duplicate rows that merge dedupes.
			var missing int64
			if err := globalDB.QueryRowContext(ctx, mq, fromUnix, toUnix).Scan(&missing); err != nil {
				failed = true
				fmt.Printf("%-30s FAIL: containment query: %v\n", name, err)
				return
			}
			if missing > 0 {
				failed = true
				fmt.Printf("%-30s FAIL: %d local row(s) missing from global for this window\n", name, missing)
			} else if g.Count < b.Count {
				fmt.Printf("%-30s NOTE: counts differ (bin holds %d duplicate row(s) deduped in merged); per-row containment OK\n", name, b.Count-g.Count)
			}
		} else if !containedAtLeast(b, g) {
			failed = true
			fmt.Printf("%-30s FAIL: global does not contain at least the local rows for this window\n", name)
		}

		if checkCost && b.Err == "" && g.Err == "" && g.CostUnits < b.CostUnits {
			// Cost sums legitimately diverge when one side was repriced by
			// recompute_cost; row containment passing means no rows were lost.
			fmt.Printf("%-30s WARN: global cost sum below local (recompute asymmetry?); rows OK\n", name)
		}
	}

	// Uniform per-table count/cost checks, driven by a registry so new product
	// channels (Claude, Codex) cannot be silently forgotten.
	runGroup := func(group, header string) {
		if header != "" {
			fmt.Println()
			fmt.Println(header)
		}
		for _, c := range simpleTableChecks() {
			if c.Group == group {
				check(c.Name, countQuery(c.Name, c.CheckCost), c.CheckCost)
			}
		}
	}

	runGroup("claude", "")

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
		if !containedAtLeast(b, g) {
			failed = true
			fmt.Printf("%-30s FAIL: global does not contain at least the local rows for this window\n", "pending_ttft_spans")
		}
	}

	runGroup("codex", "--- Codex tables ---")

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

// tableCheck describes a uniform per-table count/cost comparison between the
// bin and global dbs. Group controls which header it prints under.
type tableCheck struct {
	Name      string
	Group     string // "claude" | "codex"
	CheckCost bool
}

func simpleTableChecks() []tableCheck {
	return []tableCheck{
		{"api_requests", "claude", true},
		{"user_prompt_events", "claude", false},
		{"tool_decision_events", "claude", false},
		{"tool_result_events", "claude", false},
		{"api_error_events", "claude", false},
		{"otel_metric_points", "claude", false},
		{"events", "claude", false},
		{"raw_otlp_events", "claude", false},
		// pending_ttft_spans is handled separately (4-arg span/created query).
		{"codex_api_requests", "codex", true},
		{"codex_user_prompt_events", "codex", false},
		{"codex_tool_decision_events", "codex", false},
		{"codex_tool_result_events", "codex", false},
		{"codex_events", "codex", false},
		{"codex_raw_otlp_events", "codex", false},
	}
}

// containmentSQL maps request tables to a per-row NOT EXISTS probe run on the
// global connection (bin attached as src). Keys mirror import_global's
// KeyColumns: the natural identity minus cost_usd, which recompute_cost may
// rewrite on either side. Event tables stay on count checks (no mutable cols).
func containmentSQL() map[string]string {
	return map[string]string{
		// Two indexable probes (request_id unique index / timestamp index);
		// a single NOT EXISTS with an OR of both branches defeats the planner
		// and degenerates into an O(n*m) scan on large dbs.
		"api_requests": `
			SELECT
			  (SELECT COUNT(*) FROM src.api_requests s
				WHERE s.timestamp BETWEEN ?1 AND ?2 AND s.request_id != ''
				  AND NOT EXISTS (
					SELECT 1 FROM main.api_requests m WHERE m.request_id = s.request_id
				  ))
			+ (SELECT COUNT(*) FROM src.api_requests s
				WHERE s.timestamp BETWEEN ?1 AND ?2 AND s.request_id = ''
				  AND NOT EXISTS (
					SELECT 1 FROM main.api_requests m
					WHERE m.timestamp = s.timestamp AND
					  m.session_id IS s.session_id AND
					  m.prompt_id IS s.prompt_id AND
					  m.event_sequence IS s.event_sequence AND
					  m.model IS s.model AND
					  m.input_tokens IS s.input_tokens AND
					  m.output_tokens IS s.output_tokens AND
					  m.duration_ms IS s.duration_ms
				  ))`,
		"codex_api_requests": `
			SELECT COUNT(*)
			FROM src.codex_api_requests s
			WHERE s.timestamp BETWEEN ? AND ?
			  AND NOT EXISTS (
				SELECT 1 FROM main.codex_api_requests m
				WHERE
				  m.timestamp = s.timestamp AND
				  m.session_id IS s.session_id AND
				  m.model IS s.model AND
				  m.input_tokens IS s.input_tokens AND
				  m.output_tokens IS s.output_tokens AND
				  m.duration_ms IS s.duration_ms
			  )`,
	}
}

// countQuery builds the standard count/cost/min-ts/max-ts probe over a single
// timestamp-keyed table. cost_usd is summed only when checkCost is set.
func countQuery(table string, checkCost bool) string {
	cost := "0"
	if checkCost {
		cost = "COALESCE(SUM(cost_usd),0)"
	}
	return fmt.Sprintf(`SELECT
		COALESCE(COUNT(*),0),
		%s,
		COALESCE(MIN(timestamp),0),
		COALESCE(MAX(timestamp),0)
	FROM %s WHERE timestamp BETWEEN ? AND ?`, cost, table)
}

func hasTable(ctx context.Context, db *sql.DB, name string) bool {
	var got string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
	return err == nil
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

// containedAtLeast fails only on missing rows. Cost sums are reported as a
// warning by the caller instead: recompute_cost can legitimately reprice one
// side, so a lower global cost sum does not imply data loss.
func containedAtLeast(bin tableStats, global tableStats) bool {
	if bin.Err != "" || global.Err != "" {
		return false
	}
	return global.Count >= bin.Count
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
