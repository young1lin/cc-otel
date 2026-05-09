package pricing

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/young1lin/cc-otel/internal/config"
)

func TestEntryCalc_Explicit(t *testing.T) {
	e := Entry{
		Input:         1e-6, // $1 / M tokens input
		Output:        4e-6, // $4 / M tokens output
		CacheRead:     1e-7, // $0.10 / M tokens cache read
		CacheCreation: 2e-6, // $2 / M tokens cache create (1h cache style)
	}
	got := e.Calc(1_000_000, 1_000_000, 10_000_000, 100_000)
	want := 1.0 + 4.0 + 1.0 + 0.2
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("Calc = %v, want %v", got, want)
	}
}

func TestEntryCalc_CacheFallback(t *testing.T) {
	e := Entry{Input: 1e-6, Output: 4e-6} // no cache fields
	// cache_read should default to 0.1 * input  = 1e-7
	// cache_create should default to 1.25 * input = 1.25e-6
	got := e.Calc(0, 0, 1_000_000, 1_000_000)
	want := 0.1 + 1.25
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("Calc with fallback = %v, want %v", got, want)
	}
}

func TestEntryCalc_ZeroTokens(t *testing.T) {
	e := Entry{Input: 1e-6, Output: 4e-6}
	if got := e.Calc(0, 0, 0, 0); got != 0 {
		t.Errorf("Calc(0,0,0,0) = %v, want 0", got)
	}
}

func TestLoadSeedEntries_NoClaude(t *testing.T) {
	entries, err := loadSeedEntries()
	if err != nil {
		t.Fatalf("loadSeedEntries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("seed has no entries")
	}
	for _, e := range entries {
		if IsClaudeModel(e.Model) {
			t.Errorf("seed contains Claude model %q — Claude must NEVER be in seed", e.Model)
		}
		if e.Input <= 0 || e.Output <= 0 {
			t.Errorf("seed entry %q has non-positive input/output cost", e.Model)
		}
		if e.Source != "seed" {
			t.Errorf("seed entry %q has source=%q, want seed", e.Model, e.Source)
		}
	}
}

// newTestDB opens an in-memory SQLite DB and runs the same migrations the
// daemon uses, so registry tests exercise the real schema.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := &config.Config{DBPath: ":memory:"}
	// db.Init applies PRAGMAs we can't sanity-check here, so re-create the
	// schema by hand using the same SQL (kept terse — only the tables the
	// pricing path touches).
	db, err := sql.Open("sqlite3", cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS model_pricing (
			model             TEXT PRIMARY KEY,
			input_cost        REAL NOT NULL,
			output_cost       REAL NOT NULL,
			cache_read_cost   REAL NOT NULL DEFAULT 0,
			cache_create_cost REAL NOT NULL DEFAULT 0,
			aliases           TEXT NOT NULL DEFAULT '[]',
			source            TEXT NOT NULL,
			fetched_at        INTEGER NOT NULL,
			updated_at        INTEGER NOT NULL
		) WITHOUT ROWID;
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func TestRegistry_SeedsEmptyTable(t *testing.T) {
	db := newTestDB(t)
	reg, err := NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	snap := reg.Snapshot(context.Background())
	if snap.TableSize == 0 {
		t.Fatal("expected seed to populate model_pricing, got empty table")
	}
}

func TestRegistry_LookupKnownAndUnknown(t *testing.T) {
	db := newTestDB(t)
	reg, err := NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Known: gpt-5 from seed
	res := reg.Lookup(context.Background(), "gpt-5")
	if !res.Found || res.Kind != MatchExact {
		t.Errorf("gpt-5 lookup: kind=%s found=%v, want exact/found", res.Kind, res.Found)
	}

	// Unknown
	miss := reg.Lookup(context.Background(), "totally-made-up-model")
	if miss.Found || miss.Kind != MatchMiss {
		t.Errorf("unknown lookup: found=%v kind=%s, want miss", miss.Found, miss.Kind)
	}

	// Claude — must always miss (intentionally absent)
	cl := reg.Lookup(context.Background(), "claude-sonnet-4-5")
	if cl.Found {
		t.Error("Claude lookup must return Found=false")
	}
}

func TestRegistry_UserOverridesWin(t *testing.T) {
	db := newTestDB(t)
	cfg := &config.Config{
		Pricing: map[string]config.PriceEntry{
			// Override seed value for gpt-5 with absurdly high price to detect priority
			"gpt-5": {Input: 99, Output: 999},
		},
	}
	reg, err := NewRegistry(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	res := reg.Lookup(context.Background(), "gpt-5")
	if !res.Found {
		t.Fatal("gpt-5 not found")
	}
	if res.Entry.Input != 99 || res.Entry.Source != "user" {
		t.Errorf("user override not applied: input=%v source=%q", res.Entry.Input, res.Entry.Source)
	}
}

func TestRegistry_UserCannotOverrideClaude(t *testing.T) {
	db := newTestDB(t)
	cfg := &config.Config{
		Pricing: map[string]config.PriceEntry{
			"claude-sonnet-4-5": {Input: 1, Output: 1}, // attempt — should be ignored
		},
	}
	reg, err := NewRegistry(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	res := reg.Lookup(context.Background(), "claude-sonnet-4-5")
	if res.Found {
		t.Error("Claude lookup must miss even when user puts it in YAML")
	}
}

func TestRegistry_Calc_NonClaude(t *testing.T) {
	db := newTestDB(t)
	reg, err := NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// gpt-5 seed: input=1.25e-6, output=1e-5, cache_read=1.25e-7
	// 1M input + 1M output + 1M cache read = 1.25 + 10 + 0.125 = 11.375
	got := reg.Calc(context.Background(), "gpt-5", 1_000_000, 1_000_000, 1_000_000, 0)
	if math.Abs(got-11.375) > 1e-6 {
		t.Errorf("gpt-5 Calc = %v, want 11.375", got)
	}

	// Claude must always return 0 from Calc (Lookup misses)
	if c := reg.Calc(context.Background(), "claude-sonnet-4-5", 1_000_000, 1_000_000, 0, 0); c != 0 {
		t.Errorf("Claude Calc = %v, want 0 (never recompute)", c)
	}
}

func TestRegistry_AliasResolves_BareGLM(t *testing.T) {
	// Real-world: GLM-4.6 via Claude Code reverse proxy reports model="glm-4.6",
	// but the seed keys it as "openrouter/z-ai/glm-4.6" with "glm-4.6" alias.
	db := newTestDB(t)
	reg, err := NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	res := reg.Lookup(context.Background(), "glm-4.6")
	if !res.Found {
		t.Fatal("expected glm-4.6 alias to resolve")
	}
	if res.Kind != MatchAlias {
		t.Errorf("Kind=%s, want %s", res.Kind, MatchAlias)
	}
	if !strings.Contains(res.MatchedKey, "glm-4.6") {
		t.Errorf("MatchedKey=%q, expected to contain glm-4.6", res.MatchedKey)
	}
}

func TestRegistry_AliasResolves_BareDeepSeek(t *testing.T) {
	db := newTestDB(t)
	reg, err := NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// deepseek-v3.2 → deepseek/deepseek-v3.2 (shortest path wins)
	res := reg.Lookup(context.Background(), "deepseek-v3.2")
	if !res.Found {
		t.Fatal("expected deepseek-v3.2 to resolve via alias")
	}
}

func TestRegistry_PrefixMatch_GPT5CodexVariant(t *testing.T) {
	db := newTestDB(t)
	reg, err := NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	res := reg.Lookup(context.Background(), "gpt-5-codex-2025-09-15")
	if !res.Found {
		t.Fatal("expected prefix match for gpt-5-codex-...")
	}
	if res.MatchedKey != "gpt-5-codex" {
		t.Errorf("expected MatchedKey=gpt-5-codex, got %q", res.MatchedKey)
	}
}

func TestRegistry_MissTracking(t *testing.T) {
	db := newTestDB(t)
	reg, err := NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for i := 0; i < 3; i++ {
		reg.Lookup(context.Background(), "weird-private-model-xyz")
	}
	snap := reg.Snapshot(context.Background())
	if snap.MissCount24h != 3 {
		t.Errorf("MissCount24h = %d, want 3", snap.MissCount24h)
	}
	if len(snap.MissModelsTop) == 0 || snap.MissModelsTop[0] != "weird-private-model-xyz" {
		t.Errorf("MissModelsTop = %v, want first entry to be weird-private-model-xyz", snap.MissModelsTop)
	}

	// Claude misses must NOT be tracked (intentional absence)
	for i := 0; i < 5; i++ {
		reg.Lookup(context.Background(), "claude-sonnet-4-5")
	}
	snap2 := reg.Snapshot(context.Background())
	if snap2.MissCount24h != 3 {
		t.Errorf("MissCount24h after Claude lookups = %d, want still 3", snap2.MissCount24h)
	}
}
