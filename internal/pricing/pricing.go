// Package pricing maintains a per-model USD-per-token price table used to
// recompute cost_usd for non-Claude models. Claude (claude-* prefix) is
// always trusted as reported by Claude Code.
//
// Layered lookup priority (highest first):
//  1. user YAML overrides         (in-memory, from cc-otel.yaml `pricing:`)
//  2. SQLite model_pricing table  (seeded from embed/seed.json on first boot;
//     edited via the Web UI Pricing Table modal — no automatic refresh)
//
// The package is safe for concurrent use.
package pricing

import (
	"context"
	"strings"
)

// Entry is a single model's per-token pricing in USD.
//
// CacheRead / CacheCreation are optional. When zero, Calc falls back to
// Anthropic-style multipliers of Input (0.1× for cache read, 1.25× for
// 5-minute cache creation), matching the prompt caching docs.
type Entry struct {
	Model         string
	Input         float64
	Output        float64
	CacheRead     float64
	CacheCreation float64
	Aliases       []string
	Source        string // "user" | "seed" | "litellm" | "openrouter"
	FetchedAt     int64  // unix seconds; 0 for in-memory user overrides
	UpdatedAt     int64
	// Variants are the other provider-prefixed entries that fold into this one
	// for display (same basename). Populated only by List for the admin table;
	// never persisted, ignored by Calc/Upsert/Lookup.
	Variants []Entry `json:"-"`
}

// MatchKind identifies how a model name resolved to an Entry.
type MatchKind string

const (
	MatchExact   MatchKind = "exact"
	MatchAlias   MatchKind = "alias"
	MatchPrefix  MatchKind = "prefix"
	MatchBasename MatchKind = "basename"
	MatchMiss    MatchKind = "miss"
)

// LookupResult is what Registry.Lookup returns for callers that care about
// the match strategy (e.g. /api/pricing/lookup debug endpoint).
type LookupResult struct {
	Entry      Entry
	Found      bool
	Kind       MatchKind
	MatchedKey string // the registry key that matched (after normalization)
}

// Registry is the read API for callers (receiver, backfill tool, /api routes).
type Registry interface {
	// Lookup returns the price entry for a model name. Empty / Claude-prefix
	// models still return Found=false (Claude is intentionally absent).
	Lookup(ctx context.Context, model string) LookupResult

	// Calc computes cost in USD. Returns 0 if model is Claude or unknown.
	Calc(ctx context.Context, model string, input, output, cacheRead, cacheCreate int64) float64

	// Snapshot returns metadata for /api/status (counts, miss tail, etc.).
	Snapshot(ctx context.Context) Snapshot
}

// ListFilter narrows a Writer.List query.
type ListFilter struct {
	Query    string // substring match on model (case-insensitive); "" = all
	Source   string // "manual" | "seed" | "user" | "" = all
	Page     int
	PageSize int
	// Local is the set of normalized model names actually seen in local
	// telemetry. List sorts these entries first. Nil/empty = no local boost.
	Local map[string]bool
}

// ListResult is a page of entries plus the total match count.
type ListResult struct {
	Entries []Entry `json:"entries"`
	Total   int     `json:"total"`
}

// Writer is the mutation API for the pricing admin UI. Implemented by
// *sqlRegistry; injected into the HTTP handler alongside Registry.
type Writer interface {
	List(ctx context.Context, f ListFilter) (ListResult, error)
	Upsert(ctx context.Context, e Entry) (Entry, error)
	Delete(ctx context.Context, model string) error
}

// Snapshot exposes registry diagnostics.
type Snapshot struct {
	TableSize     int      `json:"table_size"`
	UserOverrides int      `json:"user_overrides"`
	LastEditAt    int64    `json:"last_edit_at"` // max(updated_at) over model_pricing
	MissCount24h  int      `json:"miss_count_24h"`
	MissModelsTop []string `json:"miss_models_top"`
}

// cacheReadFallback / cacheCreateFallback derive default rates from Input
// when an entry omits the cache columns. Matches Anthropic's docs (5-min
// cache write = 1.25×, cache read = 0.1×).
const (
	cacheReadFallbackMult   = 0.1
	cacheCreateFallbackMult = 1.25
)

// Calc applies the cost formula to a single Entry. Standalone so it can be
// called by tests without spinning up a Registry.
func (e Entry) Calc(input, output, cacheRead, cacheCreate int64) float64 {
	cr := e.CacheRead
	if cr == 0 {
		cr = e.Input * cacheReadFallbackMult
	}
	cc := e.CacheCreation
	if cc == 0 {
		cc = e.Input * cacheCreateFallbackMult
	}
	return float64(input)*e.Input +
		float64(output)*e.Output +
		float64(cacheRead)*cr +
		float64(cacheCreate)*cc
}

// Normalize lower-cases and trims a model name. Used both as the storage
// key and the lookup key, so writes and reads agree.
func Normalize(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// SourceRank ranks an entry's source for basename-candidate tiebreaking.
// Higher wins. User overrides rank highest (a rare user-defined prefixed
// entry should outrank refreshed ones); litellm outranks openrouter (it
// carries cache_* fields) which outranks the offline seed snapshot.
func SourceRank(source string) int {
	switch source {
	case "user":
		return 40
	case "litellm":
		return 30
	case "openrouter":
		return 20
	case "seed":
		return 10
	default:
		return 0
	}
}

// DisplayRank orders sources for the Pricing Table UI sort: hand-configured
// prices first, then OpenRouter, then LiteLLM, then the offline seed snapshot.
// This is a *display* preference for the admin table, distinct from SourceRank
// (which ranks pricing correctness for basename tiebreaking in Lookup).
func DisplayRank(source string) int {
	switch source {
	case "manual", "user": // hand-configured: UI manual entry or YAML override
		return 100
	case "openrouter":
		return 80
	case "litellm":
		return 60
	case "seed":
		return 40
	default:
		return 0
	}
}
