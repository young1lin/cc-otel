package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
)

// sqlRegistry is the production Registry. It loads the user-override layer
// from cc-otel.yaml at construction time, and queries SQLite for the rest.
//
// DB rows are cached in memory because cost_usd recompute happens on every
// log record — we don't want a SQL round-trip per request. The cache is
// invalidated by the refresher (via Reload) when it writes new rows.
type sqlRegistry struct {
	db *sql.DB

	mu              sync.RWMutex
	userByKey       map[string]Entry // YAML override layer (highest priority)
	tableByKey      map[string]Entry // model_pricing snapshot
	keys            map[string]struct{}
	aliasIndex      map[string]string // alias-normalized -> canonical key
	missCounts      map[string]int    // model -> miss count (24h, hand-trimmed)
	missOrder       []string          // insertion order for "top miss" report
	lastReload      time.Time
	lastRefreshAt   int64
	lastRefreshMsg  string
	lastRefreshErr  string
	lastChangedRows int
}

// NewRegistry constructs the production Registry. On first call it ensures
// the seed has been loaded. It then loads user overrides from cfg and
// caches the model_pricing table contents in memory.
//
// Returns an error if the seed embed is malformed (test-time bug). If the
// DB query fails we still succeed with just the user layer plus seed
// fallback — the registry must never block startup.
func NewRegistry(ctx context.Context, db *sql.DB, cfg *config.Config) (Registry, error) {
	r := &sqlRegistry{
		db:         db,
		userByKey:  map[string]Entry{},
		tableByKey: map[string]Entry{},
		keys:       map[string]struct{}{},
		aliasIndex: map[string]string{},
		missCounts: map[string]int{},
	}

	if _, err := seedIfEmpty(ctx, db); err != nil {
		return nil, err
	}

	r.loadUserOverrides(cfg)

	if err := r.Reload(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// loadUserOverrides reads cfg.Pricing into the in-memory user layer.
// Called once at construction; YAML changes require a daemon restart
// (consistent with how the rest of the config is consumed).
func (r *sqlRegistry) loadUserOverrides(cfg *config.Config) {
	if cfg == nil || len(cfg.Pricing) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, p := range cfg.Pricing {
		key := Normalize(name)
		if key == "" || IsClaudeModel(key) {
			continue // user can't override Claude — Claude is never recomputed
		}
		r.userByKey[key] = Entry{
			Model:         key,
			Input:         p.Input,
			Output:        p.Output,
			CacheRead:     p.CacheRead,
			CacheCreation: p.CacheCreation,
			Aliases:       p.Aliases,
			Source:        "user",
		}
	}
}

// Reload re-reads the model_pricing table into memory. Called by the
// refresher after diff-writes; safe to call concurrently with Lookup.
func (r *sqlRegistry) Reload(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `
		SELECT model, input_cost, output_cost, cache_read_cost, cache_create_cost,
		       aliases, source, fetched_at, updated_at
		  FROM model_pricing
	`)
	if err != nil {
		return fmt.Errorf("query model_pricing: %w", err)
	}
	defer rows.Close()

	table := map[string]Entry{}
	for rows.Next() {
		var (
			e          Entry
			aliasesRaw string
		)
		if err := rows.Scan(&e.Model, &e.Input, &e.Output, &e.CacheRead, &e.CacheCreation,
			&aliasesRaw, &e.Source, &e.FetchedAt, &e.UpdatedAt); err != nil {
			return fmt.Errorf("scan model_pricing: %w", err)
		}
		if aliasesRaw != "" {
			_ = json.Unmarshal([]byte(aliasesRaw), &e.Aliases)
		}
		table[e.Model] = e
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iter model_pricing: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.tableByKey = table
	r.rebuildIndexLocked()
	r.lastReload = time.Now()
	return nil
}

// rebuildIndexLocked refreshes keys + aliasIndex from the merged user/table
// view. Caller must hold r.mu (write).
func (r *sqlRegistry) rebuildIndexLocked() {
	r.keys = make(map[string]struct{}, len(r.tableByKey)+len(r.userByKey))
	r.aliasIndex = map[string]string{}
	add := func(e Entry) {
		r.keys[e.Model] = struct{}{}
		for _, a := range e.Aliases {
			na := Normalize(a)
			if na == "" || na == e.Model {
				continue
			}
			r.aliasIndex[na] = e.Model
		}
	}
	for _, e := range r.tableByKey {
		add(e)
	}
	// User overrides win: process last so their aliases shadow table aliases.
	for _, e := range r.userByKey {
		add(e)
	}
}

// keySourceRank resolves a registry key to its source rank for basename
// tiebreaking. Caller must hold r.mu (read). A key absent from both layers
// ranks 0 — but basename candidates always come from r.keys, so this is just
// defensive.
func (r *sqlRegistry) keySourceRank(key string) int {
	if e, ok := r.resolveLocked(key); ok {
		return SourceRank(e.Source)
	}
	return 0
}

// resolveLocked returns the Entry for a canonical key, applying user > table priority.
func (r *sqlRegistry) resolveLocked(canonical string) (Entry, bool) {
	if e, ok := r.userByKey[canonical]; ok {
		return e, true
	}
	if e, ok := r.tableByKey[canonical]; ok {
		return e, true
	}
	return Entry{}, false
}

// Lookup implements Registry.
func (r *sqlRegistry) Lookup(ctx context.Context, model string) LookupResult {
	q := Normalize(model)
	if q == "" || IsClaudeModel(q) {
		// Don't count Claude as a miss — by design, Claude is intentionally absent.
		return LookupResult{Kind: MatchMiss}
	}

	r.mu.RLock()
	canonical, kind := matchKey(q, r.keys, r.aliasIndex)
	if kind == MatchMiss {
		// Last resort: a bare name may match a provider-prefixed key by
		// basename (glm-5.2 -> z-ai/glm-5.2). Pick the best candidate; if
		// none, it stays a miss.
		if cands := basenameCandidates(q, r.keys); len(cands) > 0 {
			canonical = pickBasenameWinner(cands, r.keySourceRank)
			kind = MatchBasename
		}
	}
	if kind == MatchMiss {
		r.mu.RUnlock()
		r.recordMiss(q)
		return LookupResult{Kind: MatchMiss}
	}
	entry, ok := r.resolveLocked(canonical)
	r.mu.RUnlock()
	if !ok {
		r.recordMiss(q)
		return LookupResult{Kind: MatchMiss}
	}
	return LookupResult{Entry: entry, Found: true, Kind: kind, MatchedKey: canonical}
}

// Calc implements Registry. Returns 0 for Claude / unknown models.
func (r *sqlRegistry) Calc(ctx context.Context, model string, input, output, cacheRead, cacheCreate int64) float64 {
	res := r.Lookup(ctx, model)
	if !res.Found {
		return 0
	}
	return res.Entry.Calc(input, output, cacheRead, cacheCreate)
}

// Snapshot implements Registry.
func (r *sqlRegistry) Snapshot(ctx context.Context) Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	top := r.missTopLocked(5)
	return Snapshot{
		TableSize:       len(r.tableByKey),
		UserOverrides:   len(r.userByKey),
		MissCount24h:    sumMisses(r.missCounts),
		MissModelsTop:   top,
		LastRefreshAt:   r.lastRefreshAt,
		LastRefreshMsg:  r.lastRefreshMsg,
		LastRefreshErr:  r.lastRefreshErr,
		LastChangedRows: r.lastChangedRows,
	}
}

// missLimit caps the miss tracker to keep memory bounded under a flood of
// novel model names.
const missLimit = 64

func (r *sqlRegistry) recordMiss(model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.missCounts[model]; !ok {
		if len(r.missOrder) >= missLimit {
			old := r.missOrder[0]
			r.missOrder = r.missOrder[1:]
			delete(r.missCounts, old)
		}
		r.missOrder = append(r.missOrder, model)
	}
	r.missCounts[model]++
}

func (r *sqlRegistry) missTopLocked(n int) []string {
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(r.missCounts))
	for k, v := range r.missCounts {
		all = append(all, kv{k, v})
	}
	// simple selection sort for tiny n; missCounts is bounded by missLimit
	for i := 0; i < len(all) && i < n; i++ {
		max := i
		for j := i + 1; j < len(all); j++ {
			if all[j].v > all[max].v {
				max = j
			}
		}
		all[i], all[max] = all[max], all[i]
	}
	if len(all) > n {
		all = all[:n]
	}
	out := make([]string, 0, len(all))
	for _, x := range all {
		out = append(out, x.k)
	}
	return out
}

func sumMisses(m map[string]int) int {
	s := 0
	for _, v := range m {
		s += v
	}
	return s
}

// SetRefreshStatus is called by the refresher after each tick. Public on
// the registry so the refresher can push its outcome into Snapshot without
// coupling the two packages with a third channel.
func (r *sqlRegistry) SetRefreshStatus(at int64, msg, errStr string, changed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastRefreshAt = at
	r.lastRefreshMsg = msg
	r.lastRefreshErr = errStr
	r.lastChangedRows = changed
}

// Reloader is the subset of Registry the refresher needs to reload + report.
// Defined here (vs. refresher.go in Phase 3) so callers don't need to type-
// assert against the concrete struct.
type Reloader interface {
	Registry
	Reload(ctx context.Context) error
	SetRefreshStatus(at int64, msg, errStr string, changed int)
}

// Compile-time check.
var _ Reloader = (*sqlRegistry)(nil)
