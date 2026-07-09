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
	"github.com/young1lin/cc-otel/internal/recompute"

	_ "github.com/ncruces/go-sqlite3/driver"
)

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

	opts := recompute.Options{Tables: tables, From: fromUnix, To: toUnix}
	res, err := recompute.Run(ctx, database, reg, opts, *apply, func(p recompute.Progress) {
		if p.Total > 0 {
			log.Printf("%s: %d/%d scanned, %d changed", p.Table, p.Scanned, p.Total, p.Changed)
		}
	})
	if err != nil {
		log.Fatalf("recompute: %v", err)
	}
	printSummary(*table, res, *apply)
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

// printSummary prints a one-line summary plus the per-model miss rollup so
// the operator can spot-check before re-running with --apply.
func printSummary(label string, s recompute.Result, willApply bool) {
	mode := "DRY-RUN"
	if willApply {
		mode = "APPLY"
	}
	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("%s [%s] scanned=%d claude_skipped=%d unknown_model=%d to_change=%d\n",
		label, mode, s.Scanned, s.SkippedClaude, s.Missing, s.Detected)
	if s.Missing > 0 {
		var top []string
		for k, v := range s.MissByModel {
			top = append(top, fmt.Sprintf("%s(%d)", k, v))
		}
		sort.Strings(top)
		max := 8
		if len(top) < max {
			max = len(top)
		}
		fmt.Printf("  unknown models (top %d): %s\n", max, strings.Join(top[:max], ", "))
	}
}
