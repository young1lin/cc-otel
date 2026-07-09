package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
)

// sqlRegistry is the production Registry. It loads the user-override layer
// from cc-otel.yaml at construction time, and queries SQLite for the rest.
//
// DB rows are cached in memory because cost_usd recompute happens on every
// log record — we don't want a SQL round-trip per request. The cache is
// invalidated by Reload after the Web UI (or a tool) writes new rows.
type sqlRegistry struct {
	db *sql.DB

	mu         sync.RWMutex
	userByKey  map[string]Entry // YAML override layer (highest priority)
	tableByKey map[string]Entry // model_pricing snapshot
	keys       map[string]struct{}
	aliasIndex map[string]string // alias-normalized -> canonical key
	missCounts map[string]int    // model -> miss count (24h, hand-trimmed)
	missOrder  []string          // insertion order for "top miss" report
	lastReload time.Time
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

// Reload re-reads the model_pricing table into memory. Called after the Web
// UI or a tool writes rows; safe to call concurrently with Lookup.
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
	var lastEdit int64
	for _, e := range r.tableByKey {
		if e.UpdatedAt > lastEdit {
			lastEdit = e.UpdatedAt
		}
	}
	top := r.missTopLocked(5)
	return Snapshot{
		TableSize:     len(r.tableByKey),
		UserOverrides: len(r.userByKey),
		LastEditAt:    lastEdit,
		MissCount24h:  sumMisses(r.missCounts),
		MissModelsTop: top,
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

// Reloader is the subset of Registry the write path needs to reload after
// mutating model_pricing. Defined here so callers don't type-assert against
// the concrete struct.
type Reloader interface {
	Registry
	Reload(ctx context.Context) error
}

// Compile-time check.
var _ Reloader = (*sqlRegistry)(nil)

// List returns a filtered, paginated page of the merged user > table view.
// Sort order: models seen in local telemetry (f.Local) first, then by display
// source rank (manual > openrouter > litellm > seed), then by name. Pagination
// is applied after the sort.
func (r *sqlRegistry) List(ctx context.Context, f ListFilter) (ListResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	merged := make(map[string]Entry, len(r.tableByKey)+len(r.userByKey))
	for k, e := range r.tableByKey {
		merged[k] = e
	}
	for k, e := range r.userByKey {
		merged[k] = e // user overrides win on key clash
	}

	// Claude is priced upstream (Anthropic reports cost_usd directly) and is
	// intentionally absent from model_pricing, so it never appears above. But a
	// model the user has actually run — including Claude — should be visible:
	// inject a read-only row for each reported Claude model that has no entry,
	// populated with its reference price from the cached OpenRouter catalog.
	if len(f.Local) > 0 {
		anyCold := false
		for model := range f.Local {
			if !IsClaudeModel(model) {
				continue
			}
			if _, ok := merged[model]; ok {
				continue
			}
			e := Entry{Model: model, Source: "upstream"}
			if in, out, cr, ok := CachedCatalogPrice(model); ok {
				// CachedCatalogPrice returns USD/Mtok; Entry stores USD/token.
				e.Input, e.Output, e.CacheRead = in/perMtok, out/perMtok, cr/perMtok
				// Anthropic prompt caching: a 5-min cache write costs 1.25x the
				// input price (the 1-hour tier is 2x). OpenRouter publishes no
				// cache-write field, so default to the 5-min tier.
				e.CacheCreation = e.Input * 1.25
			} else {
				anyCold = true
			}
			merged[model] = e
		}
		if anyCold {
			WarmCatalogInBackground() // populate the cache for the next request
		}
	}

	q := strings.ToLower(strings.TrimSpace(f.Query))
	entries := make([]Entry, 0, len(merged))
	for k, e := range merged {
		if f.Source != "" && e.Source != f.Source {
			continue
		}
		if q != "" && !strings.Contains(k, q) {
			continue
		}
		entries = append(entries, e)
	}

	// Fold duplicates: one representative per basename (model). The rep mirrors
	// Lookup's resolution — exact bare key, else fewest segments, else display
	// rank — so the price shown is the price actually applied. The other members
	// are attached as Variants so the UI can expand the row to see them.
	groups := make(map[string][]Entry, len(entries))
	for _, e := range entries {
		b := basenameOf(e.Model)
		groups[b] = append(groups[b], e)
	}
	deduped := make([]Entry, 0, len(groups))
	for _, members := range groups {
		sort.Slice(members, func(i, j int) bool { return repBetter(members[i], members[j]) })
		rep := members[0]
		if len(members) > 1 {
			rep.Variants = members[1:]
		}
		deduped = append(deduped, rep)
	}
	entries = deduped

	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		la, lb := EntryIsLocal(a, f.Local), EntryIsLocal(b, f.Local)
		if la != lb {
			return la // local-relevant entries sort first
		}
		ra, rb := DisplayRank(a.Source), DisplayRank(b.Source)
		if ra != rb {
			return ra > rb // higher display rank first
		}
		return a.Model < b.Model // stable alphabetical tiebreak
	})

	total := len(entries)
	page, pageSize := f.Page, f.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return ListResult{Entries: entries[start:end], Total: total}, nil
}

// EntryIsLocal reports whether a pricing entry corresponds to a model that has
// appeared in local telemetry (local set, normalized). It matches on the entry
// key, the key's basename (so "minimax/minimax-m3" counts when "minimax-m3" was
// seen), or any alias. An empty/nil local set means "no local boost" → false.
func EntryIsLocal(e Entry, local map[string]bool) bool {
	if len(local) == 0 {
		return false
	}
	k := Normalize(e.Model)
	if local[k] {
		return true
	}
	if b := basenameOf(e.Model); b != k && local[b] {
		return true
	}
	for _, a := range e.Aliases {
		if local[Normalize(a)] {
			return true
		}
	}
	return false
}

// repBetter reports whether a is a better dedup representative than b for the
// same basename: a bare (un-prefixed) key wins (it's the exact match), else
// fewer segments (more canonical), else higher display source rank.
func repBetter(a, b Entry) bool {
	ab := !strings.Contains(a.Model, "/")
	bb := !strings.Contains(b.Model, "/")
	if ab != bb {
		return ab
	}
	sa, sb := strings.Count(a.Model, "/"), strings.Count(b.Model, "/")
	if sa != sb {
		return sa < sb
	}
	return DisplayRank(a.Source) > DisplayRank(b.Source)
}

// Upsert inserts or updates one model's price; source is "manual" (hand-typed)
// or "openrouter" (accepted from the provider picker). Claude is rejected;
// input/output must be > 0; cache_* >= 0. Reloads cache.
func (r *sqlRegistry) Upsert(ctx context.Context, e Entry) (Entry, error) {
	key := Normalize(e.Model)
	if key == "" {
		return Entry{}, fmt.Errorf("model name is required")
	}
	if IsClaudeModel(key) {
		return Entry{}, fmt.Errorf("claude models are never recomputed; price is fixed")
	}
	if e.Input <= 0 || e.Output <= 0 {
		return Entry{}, fmt.Errorf("input and output price must be > 0")
	}
	if e.CacheRead < 0 || e.CacheCreation < 0 {
		return Entry{}, fmt.Errorf("cache prices must be >= 0")
	}
	// A saved price is either "manual" (hand-typed) or "openrouter" (accepted
	// from the OpenRouter provider picker); anything else defaults to manual.
	source := "manual"
	if e.Source == "openrouter" {
		source = "openrouter"
	}

	aliases := slices.Clone(e.Aliases)
	clean := aliases[:0]
	for _, a := range aliases {
		na := Normalize(a)
		if na == "" || na == key {
			continue
		}
		clean = append(clean, na)
	}
	// Ensure empty aliases marshal to [] (not null): the column is
	// TEXT NOT NULL DEFAULT '[]' and a literal "null" violates that intent.
	if len(clean) == 0 {
		clean = []string{}
	}
	aliasJSON, _ := json.Marshal(clean)

	now := time.Now().Unix()
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO model_pricing
			(model, input_cost, output_cost, cache_read_cost, cache_create_cost,
			 aliases, source, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET
			input_cost        = excluded.input_cost,
			output_cost       = excluded.output_cost,
			cache_read_cost   = excluded.cache_read_cost,
			cache_create_cost = excluded.cache_create_cost,
			aliases           = excluded.aliases,
			source            = excluded.source,
			updated_at        = excluded.updated_at`,
		key, e.Input, e.Output, e.CacheRead, e.CacheCreation,
		string(aliasJSON), source, now, now,
	); err != nil {
		return Entry{}, fmt.Errorf("upsert %s: %w", key, err)
	}

	if err := r.Reload(ctx); err != nil {
		return Entry{}, fmt.Errorf("reload after upsert: %w", err)
	}
	return Entry{
		Model: key, Input: e.Input, Output: e.Output,
		CacheRead: e.CacheRead, CacheCreation: e.CacheCreation,
		Aliases: clean, Source: source, UpdatedAt: now,
	}, nil
}

// Delete removes one model from model_pricing and reloads.
func (r *sqlRegistry) Delete(ctx context.Context, model string) error {
	key := Normalize(model)
	if key == "" {
		return fmt.Errorf("model name is required")
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM model_pricing WHERE model = ?`, key); err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return r.Reload(ctx)
}
