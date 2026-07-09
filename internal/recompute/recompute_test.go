package recompute

import (
	"context"
	"database/sql"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/pricing"
)

// newDB mirrors internal/receiver/pricing_recompute_test.go:358 — a full-schema
// in-memory SQLite via db.Init (creates api_requests, codex_api_requests,
// daily_model_agg, codex_daily_model_agg, model_pricing).
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func mustScan(t *testing.T, db *sql.DB, q string, into ...any) {
	t.Helper()
	if err := db.QueryRow(q).Scan(into...); err != nil {
		t.Fatalf("scan %q: %v", q, err)
	}
}

// TestRun_UpdatesCostAndRebuildsAgg: a non-Claude row reprices to the manual
// price and daily_model_agg reflects it after apply.
func TestRun_UpdatesCostAndRebuildsAgg(t *testing.T) {
	db := newDB(t)
	// Insert one api_requests row for model "test-x" with 1M input tokens,
	// cost_usd = 0 (uncosted). Seed model_pricing with input=$1/Mtok.
	mustExec(t, db, `INSERT INTO api_requests (id,timestamp,model,input_tokens,output_tokens,cache_read_tokens,cache_creation_tokens,cost_usd,duration_ms,session_id)
		VALUES (1,1700000000,'test-x',1000000,0,0,0,0,0,'s1')`)
	reg, err := pricing.NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	_, err = reg.(pricing.Writer).Upsert(context.Background(), pricing.Entry{Model: "test-x", Input: 1e-6, Output: 1e-6})
	if err != nil {
		t.Fatalf("upsert: %v", err) // depends on Task 3; gate this test behind Task 3 in execution
	}

	var ticks int
	res, err := Run(context.Background(), db, reg, Options{}, true, func(Progress) { ticks++ })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Updated != 1 {
		t.Fatalf("Updated = %d, want 1", res.Updated)
	}
	if ticks == 0 {
		t.Fatal("progress callback never fired")
	}
	var got int64
	mustScan(t, db, `SELECT cost_usd FROM api_requests WHERE id=1`, &got)
	// 1M tokens * $1/Mtok = $1 = 100000 units.
	if got != 100000 {
		t.Fatalf("cost_usd = %d, want 100000", got)
	}
	var aggCost int64
	mustScan(t, db, `SELECT cost_usd FROM daily_model_agg WHERE model='test-x'`, &aggCost)
	if aggCost != 100000 {
		t.Fatalf("agg cost_usd = %d, want 100000", aggCost)
	}
}

func TestRun_SkipsClaude(t *testing.T) {
	db := newDB(t)
	mustExec(t, db, `INSERT INTO api_requests (id,timestamp,model,input_tokens,output_tokens,cache_read_tokens,cache_creation_tokens,cost_usd,duration_ms,session_id)
		VALUES (1,1700000000,'claude-sonnet-5',1000,500,0,0,99999,0,'s1')`)
	reg, _ := pricing.NewRegistry(context.Background(), db, &config.Config{})
	res, err := Run(context.Background(), db, reg, Options{}, true, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.SkippedClaude != 1 || res.Updated != 0 {
		t.Fatalf("got skipped=%d updated=%d, want 1/0", res.SkippedClaude, res.Updated)
	}
	var got int64
	mustScan(t, db, `SELECT cost_usd FROM api_requests WHERE id=1`, &got)
	if got != 99999 {
		t.Fatalf("claude cost touched: %d", got)
	}
}

// TestRun_CodexSubtractsCacheRead: Codex stores input_tokens inclusive of
// cached tokens; the recompute path must subtract cache_read before pricing.
// Setup: input=2M, cache_read=1M, model priced at Input=$1/Mtok (Output same).
// Entry.Calc prices cacheRead at the cacheReadFallbackMult (0.1*Input) when the
// entry's CacheRead is 0, so:
//   - with subtraction:    1M uncached*$1 + 1M cacheRead*$0.10 = $1.10 = 110000 units
//   - without subtraction: 2M input*$1    + 1M cacheRead*$0.10 = $2.10 = 210000 units
// Asserting 110000 proves the 1M cache_read was subtracted from input first.
func TestRun_CodexSubtractsCacheRead(t *testing.T) {
	db := newDB(t)
	mustExec(t, db, `INSERT INTO codex_api_requests (id,timestamp,session_id,model,input_tokens,output_tokens,cache_read_tokens,cache_creation_tokens,reasoning_tokens,total_tokens,cost_usd,duration_ms)
		VALUES (1,1700000000,'s1','codex-test',2000000,0,1000000,0,0,0,0,0)`)
	reg, err := pricing.NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	_, err = reg.(pricing.Writer).Upsert(context.Background(), pricing.Entry{Model: "codex-test", Input: 1e-6, Output: 1e-6})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if _, err := Run(context.Background(), db, reg, Options{}, true, nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	var got int64
	mustScan(t, db, `SELECT cost_usd FROM codex_api_requests WHERE id=1`, &got)
	if got != 110000 {
		t.Fatalf("cost_usd = %d, want 110000 (1M uncached*$1 + 1M cacheRead*$0.10 fallback; without subtraction it would be 210000)", got)
	}
	var aggCost int64
	mustScan(t, db, `SELECT cost_usd FROM codex_daily_model_agg WHERE model='codex-test'`, &aggCost)
	if aggCost != 110000 {
		t.Fatalf("codex_daily_model_agg cost_usd = %d, want 110000 (atomic rebuild)", aggCost)
	}
}

func TestRun_DryRunMakesNoChanges(t *testing.T) {
	db := newDB(t)
	mustExec(t, db, `INSERT INTO api_requests (id,timestamp,model,input_tokens,output_tokens,cache_read_tokens,cache_creation_tokens,cost_usd,duration_ms,session_id)
		VALUES (1,1700000000,'test-x',1000000,0,0,0,0,0,'s1')`)
	reg, _ := pricing.NewRegistry(context.Background(), db, &config.Config{})
	_, _ = reg.(pricing.Writer).Upsert(context.Background(), pricing.Entry{Model: "test-x", Input: 1e-6, Output: 1e-6})
	res, err := Run(context.Background(), db, reg, Options{}, false, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Updated != 0 {
		t.Fatalf("dry-run Updated = %d, want 0", res.Updated)
	}
	if res.Detected != 1 {
		t.Fatalf("Detected = %d, want 1 (dry-run must still report would-change count)", res.Detected)
	}
	var got int64
	mustScan(t, db, `SELECT cost_usd FROM api_requests WHERE id=1`, &got)
	if got != 0 {
		t.Fatalf("dry-run wrote: %d", got)
	}
}
