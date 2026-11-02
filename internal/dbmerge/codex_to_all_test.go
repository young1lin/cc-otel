package dbmerge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
)

// TestMigrateCodexToAllNormalizesAndIsIdempotent seeds the legacy archive
// (raw cache-inclusive input, Codex semantics) and verifies the one-click
// migration: rows land in the unified pool with service_name='codex', input
// normalized to uncached, optionals carried, aggregates exactly once, and a
// second run is a full no-op (identity dedup). The archive stays untouched.

// TestMigrateCodexToAllSurvivesRetentionPruning pins the R3 retention
// finding: Cleanup prunes daily_model_agg older than the retention window
// while api_requests is never pruned, so the post-migration sanity check
// MUST be scoped to the window — an all-time comparison failed every re-run
// after the first post-migration restart (false "codex agg mismatch").
func TestMigrateCodexToAllSurvivesRetentionPruning(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: filepath.Join(t.TempDir(), "ret.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)

	// One archive row far older than the retention window.
	old := time.Now().AddDate(0, 0, -200)
	if _, err := repo.DB().Exec(`INSERT INTO codex_api_requests
		(timestamp, session_id, model, input_tokens, output_tokens, total_tokens) VALUES (?, ?, ?, 10, 5, 15)`,
		old.Unix(), "prune-conv", "gpt-5.1"); err != nil {
		t.Fatal(err)
	}

	// First migration (retention-aware) writes the agg row at the archive's
	// ancient date.
	if _, err := MigrateCodexToAll(context.Background(), repo.DB(), Options{RetentionDays: 90}); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	// Simulate the retention pruning Cleanup performs on every daemon start.
	cutoff := time.Now().AddDate(0, 0, -90).Format("2006-01-02")
	if _, err := repo.DB().Exec(`DELETE FROM daily_model_agg WHERE date < ?`, cutoff); err != nil {
		t.Fatal(err)
	}

	// The re-run must succeed: the deficit is entirely outside the window.
	res, err := MigrateCodexToAll(context.Background(), repo.DB(), Options{RetentionDays: 90})
	if err != nil {
		t.Fatalf("re-migration after retention pruning must not fail: %v", err)
	}
	if res.InsertedRows != 0 {
		t.Fatalf("re-migration inserted %d rows, want 0 (no-op)", res.InsertedRows)
	}
}

// TestMigrateCodexToAllBoundaryDaySanity pins the R4 regression P1: the
// retention-scoped sanity check compares instant-precise api_requests
// timestamps against day-granular agg dates. A row on the exact cutoff day
// BEFORE the current time-of-day kept the false-failure alive: agg counted
// the whole day, the row count did not, and even the FIRST migration
// reported verification_failed. The cutoff must be local midnight on both
// sides.
func TestMigrateCodexToAllBoundaryDaySanity(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: filepath.Join(t.TempDir(), "boundary.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := db.NewRepository(d)

	// A row early on the boundary day (-90d) — before any plausible time-of-day.
	boundary := time.Now().AddDate(0, 0, -90)
	earlyBoundary := time.Date(boundary.Year(), boundary.Month(), boundary.Day(), 0, 30, 0, 0, boundary.Location())
	if _, err := repo.DB().Exec(`INSERT INTO codex_api_requests
		(timestamp, session_id, model, input_tokens, output_tokens, total_tokens) VALUES (?, ?, ?, 8, 2, 10)`,
		earlyBoundary.Unix(), "boundary-conv", "gpt-5.1"); err != nil {
		t.Fatal(err)
	}

	// Migration with retention covering the boundary day must verify cleanly.
	res, err := MigrateCodexToAll(context.Background(), repo.DB(), Options{RetentionDays: 90})
	if err != nil {
		t.Fatalf("boundary-day row must not false-fail verification: %v", err)
	}
	if res.InsertedRows != 1 {
		t.Fatalf("inserted %d rows, want 1", res.InsertedRows)
	}

	// And re-runs stay clean.
	res2, err := MigrateCodexToAll(context.Background(), repo.DB(), Options{RetentionDays: 90})
	if err != nil {
		t.Fatalf("boundary-day re-run must not fail: %v", err)
	}
	if res2.InsertedRows != 0 {
		t.Fatalf("re-run inserted %d rows, want 0", res2.InsertedRows)
	}
}

func TestMigrateCodexToAllNormalizesAndIsIdempotent(t *testing.T) {
	target := importTarget(t)
	ctx := context.Background()

	// 1) Seed the legacy archive the way the pre-merge daemon wrote it.
	mustExec(t, target, `
		INSERT INTO codex_api_requests
			(timestamp, session_id, user_id, model,
			 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			 reasoning_tokens, total_tokens, cost_usd, duration_ms, ttft_ms,
			 http_status, endpoint, conversation_id,
			 event_name, event_sequence, terminal_type, service_version,
			 host_arch, os_type, os_version, error_message)
		VALUES
			(1770000000, 'conv-mig', 'dev', 'gpt-5.5',
			 1000, 100, 400, 100,
			 7, 1100, 12345, 2300, 250,
			 200, '/v1/responses', 'conv-mig',
			 'codex.api_request', 3, 'codex_cli', '2.1.96',
			 'x86_64', 'windows', '11', '')`)
	mustExec(t, target, `
		INSERT INTO codex_user_prompt_events
			(timestamp, session_id, conversation_id, prompt_text, prompt_length,
			 event_sequence, terminal_type, service_version)
		VALUES (1770000001, 'conv-mig', 'conv-mig', 'hello', 5, 1, 'codex_cli', '2.1.96')`)

	// 2) Preview reports both tables, with the request estimate precise.
	preview, err := InspectCodexToAll(ctx, target)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(preview) != len(codexToAllPlans) {
		t.Fatalf("preview tables = %d, want %d", len(preview), len(codexToAllPlans))
	}
	var reqPreview *CodexMigrationCounts
	for i := range preview {
		if preview[i].SourceTable == "codex_api_requests" {
			reqPreview = &preview[i]
		}
	}
	if reqPreview == nil || reqPreview.SourceRows != 1 || reqPreview.NewRows != 1 || reqPreview.ExistingRows != 0 {
		t.Fatalf("request preview = %+v", reqPreview)
	}

	// 3) First migration copies both rows into the unified pool.
	result, err := MigrateCodexToAll(ctx, target, Options{Location: time.UTC})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.InsertedRows != 2 {
		t.Fatalf("first migrate inserted = %d, want 2 (%+v)", result.InsertedRows, result)
	}

	var input, output, cacheRead, cacheCreate, reasoning, total, cost int64
	var service, endpoint string
	if err := target.QueryRow(`
		SELECT input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		       reasoning_tokens, total_tokens, cost_usd, service_name, endpoint
		FROM api_requests WHERE service_name = 'codex'`,
	).Scan(&input, &output, &cacheRead, &cacheCreate, &reasoning, &total, &cost, &service, &endpoint); err != nil {
		t.Fatalf("read unified codex row: %v", err)
	}
	// input 1000 was RAW (cache-inclusive); storage must be normalized 1000-400-100 = 500.
	if input != 500 || output != 100 || cacheRead != 400 || cacheCreate != 100 {
		t.Fatalf("normalized tokens wrong: in=%d out=%d read=%d create=%d", input, output, cacheRead, cacheCreate)
	}
	if reasoning != 7 || total != 1100 || cost != 12345 || endpoint != "/v1/responses" {
		t.Fatalf("optional columns lost: reasoning=%d total=%d cost=%d endpoint=%q", reasoning, total, cost, endpoint)
	}

	var promptCount int64
	if err := target.QueryRow(
		"SELECT COUNT(*) FROM user_prompt_events WHERE service_name = 'codex'",
	).Scan(&promptCount); err != nil || promptCount != 1 {
		t.Fatalf("unified prompt count = %d err=%v", promptCount, err)
	}

	// The unified aggregate must carry the request exactly once, with
	// normalized input.
	var aggInput, aggCount int64
	if err := target.QueryRow(`
		SELECT input_tokens, request_count FROM daily_model_agg
		WHERE service_name = 'codex' AND model = 'gpt-5.5'`,
	).Scan(&aggInput, &aggCount); err != nil {
		t.Fatalf("read agg: %v", err)
	}
	if aggInput != 500 || aggCount != 1 {
		t.Fatalf("agg wrong: input=%d count=%d", aggInput, aggCount)
	}

	// 4) Second migration is a no-op: every row dedups by identity.
	again, err := MigrateCodexToAll(ctx, target, Options{Location: time.UTC})
	if err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	if again.InsertedRows != 0 || again.DuplicateRows != 2 {
		t.Fatalf("second migrate = %+v", again)
	}
	assertCounts(t, target, "api_requests", 1)
	assertCounts(t, target, "user_prompt_events", 1)

	// The archive itself must be untouched.
	assertCounts(t, target, "codex_api_requests", 1)
	assertCounts(t, target, "codex_user_prompt_events", 1)

	// 5) Preview after migration reports everything as already merged.
	preview2, err := InspectCodexToAll(ctx, target)
	if err != nil {
		t.Fatalf("inspect 2: %v", err)
	}
	for _, counts := range preview2 {
		if counts.SourceTable == "codex_api_requests" && (counts.NewRows != 0 || counts.ExistingRows != 1) {
			t.Fatalf("post-migration request preview = %+v", counts)
		}
	}
}
