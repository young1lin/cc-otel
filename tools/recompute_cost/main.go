// recompute_cost backfills cost_usd for non-Claude rows in api_requests
// and / or codex_api_requests using the local pricing registry.
//
// Use case: after onboarding a new model into model_pricing, or after a
// schema change, historical rows that were stored at a wrong cost (or 0
// for Codex pre-cost-recompute) can be repriced without losing the
// underlying token counts.
//
// Claude (claude-* prefix, case-insensitive) rows are NEVER touched —
// Anthropic owns the canonical price table for those.
//
// Defaults to dry-run; pass --apply to actually write.
//
// Usage:
//
//	go run ./tools/recompute_cost --db <path> [--from YYYY-MM-DD]
//	    [--to YYYY-MM-DD] [--table api_requests|codex_api_requests|both] [--apply]
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/pricing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// costScale matches internal/db/repository.go: cost is stored as int64
// 0.00001-USD units (1 USD = 100000). Kept as a local constant to avoid
// importing internal/db just for one number.
const costScale = 100000.0

func main() {
	dbPath := flag.String("db", filepath.Join("bin", "cc-otel.db"), "SQLite database path")
	cfgPath := flag.String("config", "", "optional cc-otel.yaml for user pricing overrides")
	from := flag.String("from", "", "inclusive start date YYYY-MM-DD (default: all history)")
	to := flag.String("to", "", "inclusive end date YYYY-MM-DD (default: all history)")
	table := flag.String("table", "both", "which table(s) to recompute: api_requests | codex_api_requests | both")
	apply := flag.Bool("apply", false, "actually write UPDATEs (default: dry-run)")
	backup := flag.Bool("backup", true, "create a VACUUM INTO backup before mutating (only with --apply)")
	flag.Parse()

	switch *table {
	case "api_requests", "codex_api_requests", "both":
	default:
		log.Fatalf("invalid --table %q (want api_requests | codex_api_requests | both)", *table)
	}

	fromUnix, toUnix, err := parseDateRange(*from, *to)
	if err != nil {
		log.Fatalf("date range: %v", err)
	}

	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000", filepath.ToSlash(filepath.Clean(*dbPath)))
	database, err := sql.Open("sqlite3", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	// Load user overrides from cfg.Pricing if a config path was given.
	var cfg *config.Config
	if *cfgPath != "" {
		c, err := config.Load(*cfgPath)
		if err != nil {
			log.Fatalf("load config %s: %v", *cfgPath, err)
		}
		cfg = c
	} else {
		cfg = &config.Config{}
	}

	reg, err := pricing.NewRegistry(ctx, database, cfg)
	if err != nil {
		log.Fatalf("build pricing registry: %v", err)
	}

	if *apply && *backup {
		backupPath := fmt.Sprintf("%s.recompute-%s.bak", *dbPath, time.Now().Format("20060102-150405"))
		if _, err := database.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", backupPath)); err != nil {
			log.Fatalf("backup: %v", err)
		}
		log.Printf("backup: %s", backupPath)
	}

	tables := []string{}
	if *table == "both" || *table == "api_requests" {
		tables = append(tables, "api_requests")
	}
	if *table == "both" || *table == "codex_api_requests" {
		tables = append(tables, "codex_api_requests")
	}

	for _, t := range tables {
		summary, err := scanTable(ctx, database, reg, t, fromUnix, toUnix)
		if err != nil {
			log.Fatalf("%s scan: %v", t, err)
		}
		printSummary(t, summary, *apply)

		if !*apply || len(summary.changes) == 0 {
			continue
		}
		if err := applyChanges(ctx, database, t, summary.changes); err != nil {
			log.Fatalf("%s apply: %v", t, err)
		}
		if err := rebuildAgg(ctx, database, t); err != nil {
			log.Fatalf("%s rebuild agg: %v", t, err)
		}
		log.Printf("%s: applied %d updates and rebuilt aggregate", t, len(summary.changes))
	}
}

// parseDateRange converts optional from/to YYYY-MM-DD to a [fromUnix, toUnix)
// half-open range. Empty strings mean "no bound" (returns 0 / max-int).
func parseDateRange(from, to string) (int64, int64, error) {
	loc := time.Local
	var fromUnix int64
	var toUnix int64 = math.MaxInt64
	if from != "" {
		t, err := time.ParseInLocation("2006-01-02", from, loc)
		if err != nil {
			return 0, 0, fmt.Errorf("parse --from %q: %w", from, err)
		}
		fromUnix = t.Unix()
	}
	if to != "" {
		t, err := time.ParseInLocation("2006-01-02", to, loc)
		if err != nil {
			return 0, 0, fmt.Errorf("parse --to %q: %w", to, err)
		}
		toUnix = t.Add(24 * time.Hour).Unix()
	}
	return fromUnix, toUnix, nil
}

type rowChange struct {
	id        int64
	model     string
	oldUnits  int64
	newUnits  int64
	deltaUSD  float64
	tsUnix    int64
	hadTokens bool
}

type tableSummary struct {
	scanned     int
	skippedCl   int // claude rows skipped
	missing     int // non-Claude row but registry returned 0
	missByModel map[string]int
	changes     []rowChange
}

// scanTable walks the requested table, applying the same single rule the
// receiver does: skip Claude, recompute everything else. Rows with zero
// tokens are skipped (no signal for cost). Only rows whose recomputed cost
// differs from the stored cost end up in summary.changes.
func scanTable(ctx context.Context, db *sql.DB, reg pricing.Registry, table string, fromUnix, toUnix int64) (*tableSummary, error) {
	summary := &tableSummary{missByModel: map[string]int{}}

	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, timestamp, model,
		       COALESCE(input_tokens, 0), COALESCE(output_tokens, 0),
		       COALESCE(cache_read_tokens, 0), COALESCE(cache_creation_tokens, 0),
		       COALESCE(cost_usd, 0)
		  FROM %s
		 WHERE timestamp >= ? AND timestamp < ?`, table),
		fromUnix, toUnix,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id                                    int64
			ts                                    int64
			model                                 string
			input, output, cacheRead, cacheCreate int64
			oldUnits                              int64
		)
		if err := rows.Scan(&id, &ts, &model, &input, &output, &cacheRead, &cacheCreate, &oldUnits); err != nil {
			return nil, err
		}
		summary.scanned++

		if pricing.IsClaudeModel(model) {
			summary.skippedCl++
			continue
		}
		if input == 0 && output == 0 && cacheRead == 0 && cacheCreate == 0 {
			// no token signal → can't compute, leave alone
			continue
		}

		// Codex stores input_tokens as the OpenAI raw value, which includes
		// cached tokens (cache_read_tokens ⊆ input_tokens). pricing.Calc
		// expects uncached input only, so subtract before pricing — same
		// fix the receiver applies on the live ingest path.
		uncachedInput := input
		if table == "codex_api_requests" {
			uncachedInput = input - cacheRead
			if uncachedInput < 0 {
				uncachedInput = 0
			}
		}
		newUSD := reg.Calc(ctx, model, uncachedInput, output, cacheRead, cacheCreate)
		if newUSD <= 0 {
			summary.missing++
			summary.missByModel[model]++
			continue
		}
		newUnits := int64(math.Round(newUSD * costScale))
		if newUnits == oldUnits {
			continue
		}
		summary.changes = append(summary.changes, rowChange{
			id:        id,
			model:     model,
			oldUnits:  oldUnits,
			newUnits:  newUnits,
			deltaUSD:  float64(newUnits-oldUnits) / costScale,
			tsUnix:    ts,
			hadTokens: true,
		})
	}
	return summary, rows.Err()
}

// printSummary prints a per-model rollup so the operator can spot-check
// before re-running with --apply. Sorted by absolute USD delta descending.
func printSummary(table string, s *tableSummary, willApply bool) {
	type modelStat struct {
		model    string
		rows     int
		oldUSD   float64
		newUSD   float64
		deltaUSD float64
	}
	per := map[string]*modelStat{}
	for _, c := range s.changes {
		st, ok := per[c.model]
		if !ok {
			st = &modelStat{model: c.model}
			per[c.model] = st
		}
		st.rows++
		st.oldUSD += float64(c.oldUnits) / costScale
		st.newUSD += float64(c.newUnits) / costScale
		st.deltaUSD += c.deltaUSD
	}
	stats := make([]*modelStat, 0, len(per))
	for _, v := range per {
		stats = append(stats, v)
	}
	sort.Slice(stats, func(i, j int) bool {
		return math.Abs(stats[i].deltaUSD) > math.Abs(stats[j].deltaUSD)
	})

	fmt.Println(strings.Repeat("-", 80))
	mode := "DRY-RUN"
	if willApply {
		mode = "APPLY"
	}
	fmt.Printf("%s [%s] scanned=%d claude_skipped=%d unknown_model=%d to_change=%d\n",
		table, mode, s.scanned, s.skippedCl, s.missing, len(s.changes))
	if s.missing > 0 {
		// Only list a handful — full list is in changes anyway.
		var top []string
		for k, v := range s.missByModel {
			top = append(top, fmt.Sprintf("%s(%d)", k, v))
		}
		sort.Strings(top)
		max := 8
		if len(top) < max {
			max = len(top)
		}
		fmt.Printf("  unknown models (top %d): %s\n", max, strings.Join(top[:max], ", "))
	}
	fmt.Printf("  %-40s %8s %14s %14s %14s\n", "model", "rows", "old_usd", "new_usd", "delta_usd")
	for _, st := range stats {
		fmt.Printf("  %-40s %8d %14.4f %14.4f %+14.4f\n",
			st.model, st.rows, st.oldUSD, st.newUSD, st.deltaUSD)
	}
}

// applyChanges UPDATEs cost_usd one row at a time within a single
// transaction. We could batch via CASE WHEN but the row count is generally
// small (per-day) and individual UPDATEs are clearer in error reporting.
func applyChanges(ctx context.Context, db *sql.DB, table string, changes []rowChange) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`UPDATE %s SET cost_usd = ? WHERE id = ?`, table))
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, c := range changes {
		if _, err := stmt.ExecContext(ctx, c.newUnits, c.id); err != nil {
			return fmt.Errorf("update id=%d: %w", c.id, err)
		}
	}
	return tx.Commit()
}

// rebuildAgg recomputes the daily model aggregate for the given parent
// table. Doing a full rebuild (vs. trying to delta-patch) is robust and
// matches what RebuildDailyAggregates does in the production code.
func rebuildAgg(ctx context.Context, db *sql.DB, parent string) error {
	var aggTable string
	switch parent {
	case "api_requests":
		aggTable = "daily_model_agg"
	case "codex_api_requests":
		aggTable = "codex_daily_model_agg"
	default:
		return fmt.Errorf("unknown parent table %q", parent)
	}

	if _, err := db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, aggTable)); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			date, model, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens,
			%s
			cost_usd, request_count
		)
		SELECT
			date(timestamp, 'unixepoch', 'localtime'),
			model,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			%s
			COALESCE(SUM(cost_usd), 0),
			COUNT(*)
		FROM %s
		GROUP BY date(timestamp, 'unixepoch', 'localtime'), model`,
		aggTable,
		extraAggColumns(parent),
		extraAggSelects(parent),
		parent,
	))
	return err
}

// extraAggColumns / extraAggSelects exist because codex_daily_model_agg
// carries reasoning_tokens and total_tokens that the Claude agg does not.
// Returning empty strings keeps the SQL valid for api_requests.
func extraAggColumns(parent string) string {
	if parent == "codex_api_requests" {
		return "reasoning_tokens, total_tokens,"
	}
	return ""
}

func extraAggSelects(parent string) string {
	if parent == "codex_api_requests" {
		return "COALESCE(SUM(reasoning_tokens), 0), COALESCE(SUM(total_tokens), 0),"
	}
	return ""
}
