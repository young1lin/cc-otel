package pricing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
)

// Refresher periodically pulls per-model prices from upstream Sources and
// diff-writes changed rows into the model_pricing table. After each tick
// it triggers Reloader.Reload so the in-memory Registry sees fresh data,
// and Reloader.SetRefreshStatus so /api/status can surface the outcome.
//
// Sources that fail are logged and ignored — a single network blip never
// blocks the pipeline. If *every* source fails the previous SQLite
// snapshot is kept untouched.
type Refresher struct {
	db       *sql.DB
	reg      Reloader
	sources  []Source
	interval time.Duration
	timeout  time.Duration
	logger   *log.Logger
}

// NewRefresher wires the default LiteLLM + OpenRouter sources from the
// config-supplied interval/timeout. Pass alt sources via SetSources for
// tests.
func NewRefresher(database *sql.DB, reg Reloader, cfg config.PricingRefreshConfig, logger *log.Logger) *Refresher {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Refresher{
		db:       database,
		reg:      reg,
		interval: interval,
		timeout:  timeout,
		logger:   logger,
		sources: []Source{
			NewLiteLLMSource("", timeout),
			NewOpenRouterSource("", timeout),
		},
	}
}

// SetSources replaces the Refresher's source list. Used by tests to inject
// a stub source without hitting the network.
func (r *Refresher) SetSources(s []Source) { r.sources = s }

// Run blocks until ctx is done. It performs an immediate first tick so
// startup populates fresh data without waiting up to interval. Designed
// to be launched as `go refresher.Run(ctx)`.
func (r *Refresher) Run(ctx context.Context) {
	r.tick(ctx) // immediate
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

// Tick is exported for tests + ad-hoc CLI triggers; production code uses Run.
func (r *Refresher) Tick(ctx context.Context) error { return r.tick(ctx) }

func (r *Refresher) tick(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, r.timeout*time.Duration(len(r.sources)+1))
	defer cancel()

	merged, srcErrs := r.fetchAll(tickCtx)
	if len(merged) == 0 {
		msg := "all pricing sources failed; keeping previous snapshot"
		r.logger.Print("pricing refresh: " + msg)
		r.reg.SetRefreshStatus(time.Now().Unix(), msg, joinErrs(srcErrs), 0)
		return errors.New(msg)
	}

	changed, err := r.diffAndApply(ctx, merged)
	if err != nil {
		r.logger.Printf("pricing refresh: diff/apply failed: %v", err)
		r.reg.SetRefreshStatus(time.Now().Unix(), "diff/apply failed", err.Error(), 0)
		return err
	}

	if changed > 0 {
		if err := r.reg.Reload(ctx); err != nil {
			r.logger.Printf("pricing refresh: registry reload failed: %v", err)
			r.reg.SetRefreshStatus(time.Now().Unix(), "registry reload failed", err.Error(), changed)
			return err
		}
	}

	status := "ok"
	if errStr := joinErrs(srcErrs); errStr != "" {
		// At least one source succeeded but others failed — degraded but not fatal.
		status = "partial"
		r.reg.SetRefreshStatus(time.Now().Unix(), status, errStr, changed)
	} else {
		r.reg.SetRefreshStatus(time.Now().Unix(), status, "", changed)
	}
	r.logger.Printf("pricing refresh done: fetched=%d changed=%d status=%s", len(merged), changed, status)
	return nil
}

// fetchAll runs every source concurrently, captures partial failures, and
// merges results in priority order (highest priority overwrites lower).
func (r *Refresher) fetchAll(ctx context.Context) (map[string]Entry, map[string]error) {
	type sourceResult struct {
		src     Source
		entries map[string]Entry
		err     error
	}

	results := make(chan sourceResult, len(r.sources))
	var wg sync.WaitGroup
	for _, s := range r.sources {
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			subCtx, cancel := context.WithTimeout(ctx, r.timeout)
			defer cancel()
			entries, err := s.Fetch(subCtx)
			results <- sourceResult{src: s, entries: entries, err: err}
		}(s)
	}
	go func() { wg.Wait(); close(results) }()

	merged := map[string]Entry{}
	priority := map[string]int{} // model -> priority of source that wrote it
	errs := map[string]error{}
	collected := []sourceResult{}
	for r := range results {
		collected = append(collected, r)
	}

	// Apply highest priority last so it overwrites lower-priority values.
	sort.Slice(collected, func(i, j int) bool {
		return collected[i].src.Priority() < collected[j].src.Priority()
	})
	for _, res := range collected {
		if res.err != nil {
			errs[res.src.Name()] = res.err
			continue
		}
		for k, v := range res.entries {
			if oldP, exists := priority[k]; exists && oldP >= res.src.Priority() {
				continue
			}
			merged[k] = v
			priority[k] = res.src.Priority()
		}
	}
	return merged, errs
}

// diffAndApply compares fetched entries to the current model_pricing
// snapshot. Rows whose price hash matches are left untouched (so
// updated_at doesn't churn). New rows get INSERT, changed rows UPDATE.
// Returns the number of rows that actually changed.
func (r *Refresher) diffAndApply(ctx context.Context, fetched map[string]Entry) (int, error) {
	// Read current snapshot.
	current, err := loadCurrentTable(ctx, r.db)
	if err != nil {
		return 0, fmt.Errorf("load current model_pricing: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	insStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO model_pricing
			(model, input_cost, output_cost, cache_read_cost, cache_create_cost,
			 aliases, source, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '[]', ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insStmt.Close()

	updStmt, err := tx.PrepareContext(ctx, `
		UPDATE model_pricing
		   SET input_cost = ?, output_cost = ?,
		       cache_read_cost = ?, cache_create_cost = ?,
		       source = ?, fetched_at = ?, updated_at = ?
		 WHERE model = ?`)
	if err != nil {
		return 0, err
	}
	defer updStmt.Close()

	changed := 0
	now := time.Now().Unix()
	for key, fresh := range fetched {
		old, exists := current[key]
		if !exists {
			if _, err := insStmt.ExecContext(ctx,
				key, fresh.Input, fresh.Output, fresh.CacheRead, fresh.CacheCreation,
				fresh.Source, now, now,
			); err != nil {
				return 0, fmt.Errorf("insert %s: %w", key, err)
			}
			changed++
			continue
		}
		if entryHash(fresh) == entryHash(old) {
			continue
		}
		if _, err := updStmt.ExecContext(ctx,
			fresh.Input, fresh.Output, fresh.CacheRead, fresh.CacheCreation,
			fresh.Source, now, now, key,
		); err != nil {
			return 0, fmt.Errorf("update %s: %w", key, err)
		}
		changed++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}

// loadCurrentTable reads model_pricing into a map for diff comparison.
// Aliases aren't loaded — the refresher never overwrites the alias column,
// so we don't need them for the hash check.
func loadCurrentTable(ctx context.Context, db *sql.DB) (map[string]Entry, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT model, input_cost, output_cost, cache_read_cost, cache_create_cost
		  FROM model_pricing`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Model, &e.Input, &e.Output, &e.CacheRead, &e.CacheCreation); err != nil {
			return nil, err
		}
		out[e.Model] = e
	}
	return out, rows.Err()
}

// entryHash collapses the four price columns into a stable string. Used by
// the refresher to skip writes when fetched data matches the table.
func entryHash(e Entry) string {
	return fmt.Sprintf("%.12g|%.12g|%.12g|%.12g", e.Input, e.Output, e.CacheRead, e.CacheCreation)
}

func joinErrs(m map[string]error) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %v", k, m[k]))
	}
	return joinComma(parts)
}

func joinComma(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "; " + p
	}
	return out
}
