package cursorimport

import (
	"context"
	"strings"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
)

// fixture mirrors the four Cost shapes, a cloud-agent row, a zero-token row,
// an identity violation, and an invalid date line from the real 2026-11-07
// exports (20683 rows measured: zero key collisions, zero Cost conflicts).
const fixture = "Date,Cloud Agent ID,Automation ID,Kind,Model,Max Mode,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens,Cost\n" +
	"\"2026-11-06T00:31:49.403Z\",\"\",\"\",\"Free\",\"auto\",\"No\",\"0\",\"53345\",\"4352\",\"290\",\"57987\",\"Free\"\n" +
	"\"2026-11-05T10:00:00.000Z\",\"agent-42\",\"\",\"Included\",\"grok-4.7-xhigh\",\"No\",\"3690\",\"2\",\"17572\",\"40\",\"21304\",\"Included\"\n" +
	"\"2026-11-05T11:00:00.000Z\",\"\",\"\",\"User API Key\",\"gpt-5.2\",\"No\",\"0\",\"100\",\"10\",\"20\",\"130\",\"0.25\"\n" +
	"\"2026-11-05T12:00:00.000Z\",\"\",\"\",\"Included\",\"gpt-5.2\",\"No\",\"0\",\"0\",\"0\",\"0\",\"0\",\"-\"\n" +
	"\"2026-11-05T13:00:00.000Z\",\"\",\"\",\"Included\",\"composer-1\",\"No\",\"5\",\"5\",\"5\",\"5\",\"999\",\"Included\"\n" +
	"\"not-a-date\",\"\",\"\",\"Included\",\"x\",\"No\",\"0\",\"1\",\"0\",\"0\",\"1\",\"Included\"\n"

func TestParse_MapsColumnsCountsAndCostShapes(t *testing.T) {
	rows, stats, err := Parse("usage-events-fixture.csv", strings.NewReader(fixture), false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("kept rows = %d, want 4 (zero-token skipped, bad date dropped): %+v", len(rows), stats)
	}
	wantStats := ParseStats{Rows: 6, SkippedZeroToken: 1, IdentityViolations: 1, InvalidRows: 1}
	if stats != wantStats {
		t.Errorf("stats = %+v, want %+v", stats, wantStats)
	}

	byModel := map[string]Row{}
	for _, r := range rows {
		byModel[r.Model] = r
	}
	auto := byModel["auto"]
	if auto.InputTokens != 53345 || auto.CacheCreationTokens != 0 || auto.CacheReadTokens != 4352 || auto.OutputTokens != 290 {
		t.Errorf("auto token mapping wrong: %+v", auto)
	}
	if auto.TotalTokens != 57987 {
		t.Errorf("auto total = %d, want 57987", auto.TotalTokens)
	}
	if auto.CostUSD != 0 {
		t.Errorf("Free cost = %v, want 0", auto.CostUSD)
	}
	agent := byModel["grok-4.7-xhigh"]
	if agent.SessionID != "cursor-agent-agent-42" {
		t.Errorf("cloud-agent session = %q, want cursor-agent-agent-42", agent.SessionID)
	}
	if agent.InputTokens != 2 || agent.CacheCreationTokens != 3690 {
		t.Errorf("cache-write mapping wrong: %+v", agent)
	}
	gpt := byModel["gpt-5.2"]
	if gpt.CostUSD != 0.25 {
		t.Errorf("numeric cost = %v, want 0.25", gpt.CostUSD)
	}
	// Identity violation row: components win, CSV total discarded.
	if c := byModel["composer-1"]; c.TotalTokens != 20 {
		t.Errorf("violation row total = %d, want derived 20", c.TotalTokens)
	}
}

func TestParse_KeepZeroTokenOptIn(t *testing.T) {
	rows, stats, err := Parse("f.csv", strings.NewReader(fixture), true)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(rows) != 5 || stats.SkippedZeroToken != 0 {
		t.Fatalf("keepZero: rows=%d stats=%+v, want 5 kept / 0 skipped", len(rows), stats)
	}
}

func TestParse_RequestIDStableAcrossParsesAndFiles(t *testing.T) {
	a, _, err := Parse("a.csv", strings.NewReader(fixture), false)
	if err != nil {
		t.Fatalf("Parse a: %v", err)
	}
	b, _, err := Parse("b-duplicate-download.csv", strings.NewReader(fixture), false)
	if err != nil {
		t.Fatalf("Parse b: %v", err)
	}
	for i := range a {
		if a[i].RequestID != b[i].RequestID {
			t.Fatalf("row %d: request id drifted across files: %q vs %q", i, a[i].RequestID, b[i].RequestID)
		}
		if !strings.HasPrefix(a[i].RequestID, "cursor-") || len(a[i].RequestID) != len("cursor-")+16 {
			t.Fatalf("row %d: malformed request id %q", i, a[i].RequestID)
		}
	}
}

// TestNormalizeCursorModel pins the full mapping observed in the real
// 2026-11-07 exports (49 distinct slugs, cursor-csv-import design doc):
// mode suffixes (fast/max/thinking/xhigh/high/medium/low) are attributes of
// a request, not model identity; real names that merely CONTAIN those words
// (grok-code-fast-1, gemini-3-flash-preview, mimo-v2-flash) stay intact.
func TestNormalizeCursorModel(t *testing.T) {
	cases := []struct{ raw, base string }{
		{"grok-bot-automation", "grok-bot-automation"},
		{"grok-bot-default", "grok-bot-default"},
		{"grok-bot-cua", "grok-bot-cua"},
		{"gpt-5.1-codex", "gpt-5.1-codex"},
		{"gemini-3-flash-preview", "gemini-3-flash-preview"},
		{"auto", "auto"},
		{"grok-code-fast-1", "grok-code-fast-1"},
		{"composer-1", "composer-1"},
		{"claude-4.5-opus-high-thinking", "claude-4.5-opus"},
		{"claude-4.6-opus-high-thinking", "claude-4.6-opus"},
		{"claude-4.5-sonnet-thinking", "claude-4.5-sonnet"},
		{"gpt-5.2", "gpt-5.2"},
		{"composer-2.5-fast", "composer-2.5"},
		{"grok-4.7-xhigh-fast", "grok-4.7"},
		{"grok-4.7-xhigh", "grok-4.7"},
		{"cursor-grok-4.6-high-fast", "grok-4.6"},
		{"grok-4.5-fast-xhigh", "grok-4.5"},
		{"claude-opus-4-8-thinking-xhigh", "claude-opus-4-8"},
		{"cursor-grok-4.5-high-fast", "grok-4.5"},
		{"composer-2", "composer-2"},
		{"github_bugbot", "github_bugbot"},
		{"cursor-grok-4.6-xhigh-fast", "grok-4.6"},
		{"composer-2-fast", "composer-2"},
		{"claude-opus-4-7-thinking-high", "claude-opus-4-7"},
		{"claude-opus-5-thinking-high", "claude-opus-5"},
		{"claude-4.6-sonnet-medium-thinking", "claude-4.6-sonnet"},
		{"claude-opus-5-5-xhigh", "claude-opus-5-5"},
		{"grok-4.7-high-fast", "grok-4.7"},
		{"grok-4.7-high", "grok-4.7"},
		{"gpt-5.5-extra-high", "gpt-5.5"},
		{"claude-fable-5-thinking-xhigh", "claude-fable-5"},
		{"agent_review", "agent_review"},
		{"gpt-5.5-medium", "gpt-5.5"},
		{"cursor-grok-4.5-high", "grok-4.5"},
		{"grok-4.5-xhigh", "grok-4.5"},
		{"claude-opus-4-7-thinking-xhigh", "claude-opus-4-7"},
		{"claude-4.5-sonnet", "claude-4.5-sonnet"},
		{"gpt-5.2-codex", "gpt-5.2-codex"},
		{"composer-1.5", "composer-1.5"},
		{"gpt-5.6-terra-medium", "gpt-5.6-terra"},
		{"claude-opus-4-8-thinking-high", "claude-opus-4-8"},
		{"mimo-v2-flash", "mimo-v2-flash"},
		{"gpt-5.6-sol-medium", "gpt-5.6-sol"},
		{"glm-5.3", "glm-5.3"},
		{"claude-sonnet-5-thinking-high", "claude-sonnet-5"},
		{"gpt-5.1", "gpt-5.1"},
		{"claude-sonnet-5-5-low", "claude-sonnet-5-5"},
		{"claude-opus-5-high", "claude-opus-5"},
		{"claude-fable-5-thinking-high", "claude-fable-5"},
	}
	for _, c := range cases {
		if got := NormalizeCursorModel(c.raw); got != c.base {
			t.Errorf("NormalizeCursorModel(%q) = %q, want %q", c.raw, got, c.base)
		}
	}
}

// Automation ID (2026-11-08 export format) attributes a session when no
// Cloud Agent ID is present; a Cloud Agent ID still wins when both appear.
// Neither ID feeds the dedup hash, so format changes never break idempotency.
func TestParse_AutomationIDSession(t *testing.T) {
	csvText := "Date,Cloud Agent ID,Automation ID,Kind,Model,Max Mode,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens,Cost\n" +
		"\"2026-11-08T10:00:00.000Z\",\"\",\"auto-42\",\"Included\",\"grok-bot-automation\",\"No\",\"0\",\"100\",\"20\",\"5\",\"125\",\"Included\"\n" +
		"\"2026-11-08T10:01:00.000Z\",\"agent-9\",\"auto-42\",\"Included\",\"grok-bot-default\",\"No\",\"0\",\"100\",\"20\",\"5\",\"125\",\"Included\"\n" +
		"\"2026-11-08T10:02:00.000Z\",\"\",\"\",\"Included\",\"auto\",\"No\",\"0\",\"100\",\"20\",\"5\",\"125\",\"Included\"\n"
	rows, stats, err := Parse("f.csv", strings.NewReader(csvText), false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if stats.InvalidRows != 0 || len(rows) != 3 {
		t.Fatalf("rows=%d invalid=%d, want 3/0", len(rows), stats.InvalidRows)
	}
	if rows[0].SessionID != "cursor-automation-auto-42" {
		t.Errorf("automation row session = %q, want cursor-automation-auto-42", rows[0].SessionID)
	}
	if rows[1].SessionID != "cursor-agent-agent-9" {
		t.Errorf("both-ids row session = %q, want agent to win", rows[1].SessionID)
	}
	if rows[2].SessionID != "" {
		t.Errorf("no-id row session = %q, want empty", rows[2].SessionID)
	}
}

func TestParse_MissingRequiredColumnFails(t *testing.T) {
	bad := "Date,Model,Cost\n\"2026-11-05T10:00:00Z\",\"m\",\"Free\"\n"
	_, _, err := Parse("bad.csv", strings.NewReader(bad), false)
	if err == nil || !strings.Contains(err.Error(), "Input (w/o Cache Write)") {
		t.Fatalf("expected missing-column error naming the column, got %v", err)
	}
}

func newRepo(t *testing.T) *db.Repository {
	t.Helper()
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return db.NewRepository(d)
}

func count(t *testing.T, repo *db.Repository, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := repo.DB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// Import must be idempotent and leave daily_model_agg level with the pool.
// nil Pricer keeps CSV cost values verbatim (the legacy path; the priced
// path has its own test below).
func TestImport_IdempotentWithAggAndLedger(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	rows, _, err := Parse("usage-events-fixture.csv", strings.NewReader(fixture), false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	res, err := Import(ctx, repo, nil, rows, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Inserted != 4 || res.Duplicates != 0 {
		t.Fatalf("first run: inserted=%d duplicates=%d, want 4/0", res.Inserted, res.Duplicates)
	}
	if n := count(t, repo, "SELECT COUNT(*) FROM api_requests WHERE service_name = 'cursor'"); n != 4 {
		t.Fatalf("cursor rows = %d, want 4", n)
	}
	if n := count(t, repo, "SELECT COUNT(*) FROM import_ledger WHERE source_db LIKE 'cursor-csv:%'"); n != 4 {
		t.Fatalf("ledger rows = %d, want 4", n)
	}

	// Token sums reconcile against the parsed components (2026-11-05 bucket:
	// 3 rows; 2026-11-06 bucket: the auto row).
	var inTok, crTok, ccTok, outTok, reqs int64
	err = repo.DB().QueryRow(
		"SELECT input_tokens, cache_read_tokens, cache_creation_tokens, output_tokens, request_count"+
			" FROM daily_model_agg WHERE service_name = 'cursor' AND model = 'grok-4.7'").Scan(&inTok, &crTok, &ccTok, &outTok, &reqs)
	if err != nil {
		t.Fatalf("agg query: %v", err)
	}
	if inTok != 2 || crTok != 17572 || ccTok != 3690 || outTok != 40 || reqs != 1 {
		t.Errorf("agg row wrong: in=%d cr=%d cc=%d out=%d reqs=%d", inTok, crTok, ccTok, outTok, reqs)
	}

	// The RAW slug (grok-4.7-xhigh) is stored as its base identity;
	// the request id still hashes the raw slug.
	var storedModel string
	if err := repo.DB().QueryRow(
		"SELECT model FROM api_requests WHERE service_name = 'cursor' AND session_id = 'cursor-agent-agent-42'").Scan(&storedModel); err != nil {
		t.Fatalf("stored model query: %v", err)
	}
	if storedModel != "grok-4.7" {
		t.Errorf("stored model = %q, want base grok-4.7 (raw grok-4.7-xhigh)", storedModel)
	}

	// Re-import the SAME rows: nothing may land, nothing may double-count.
	res2, err := Import(ctx, repo, nil, rows, nil)
	if err != nil {
		t.Fatalf("re-Import: %v", err)
	}
	if res2.Inserted != 0 || res2.Duplicates != 4 {
		t.Fatalf("re-run: inserted=%d duplicates=%d, want 0/4", res2.Inserted, res2.Duplicates)
	}
	if n := count(t, repo, "SELECT COUNT(*) FROM api_requests WHERE service_name = 'cursor'"); n != 4 {
		t.Fatalf("cursor rows after re-run = %d, want still 4", n)
	}
	var totalReqs int64
	if err := repo.DB().QueryRow(
		"SELECT COALESCE(SUM(request_count), 0) FROM daily_model_agg WHERE service_name = 'cursor'").Scan(&totalReqs); err != nil {
		t.Fatalf("agg sum: %v", err)
	}
	if totalReqs != 4 {
		t.Fatalf("agg request_count sum = %d after re-run, want 4 (no double count)", totalReqs)
	}
	if n := count(t, repo, "SELECT COUNT(*) FROM import_ledger WHERE source_db LIKE 'cursor-csv:%'"); n != 4 {
		t.Fatalf("ledger rows after re-run = %d, want still 4", n)
	}
}

func TestImport_ProgressCallbackAdvances(t *testing.T) {
	repo := newRepo(t)
	rows, _, err := Parse("f.csv", strings.NewReader(fixture), true)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var lastProcessed, lastInserted int64
	calls := 0
	_, err = Import(context.Background(), repo, nil, rows, func(p, i, d int64) {
		lastProcessed, lastInserted = p, i
		calls++
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if calls == 0 || lastProcessed != int64(len(rows)) || lastInserted != int64(len(rows)) {
		t.Errorf("progress: calls=%d last=(%d,%d), want final call at (%d,%d)",
			calls, lastProcessed, lastInserted, len(rows), len(rows))
	}
}

// stubPricer returns a fixed per-call price so the pricing decision is
// observable without depending on pricing.Registry.
type stubPricer struct{ usd float64 }

func (s stubPricer) Calc(_ context.Context, _ string, _, _, _, _ int64) float64 {
	return s.usd
}

// The 2026-11-08 cost semantics: plan-included rows (CSV cost 0) are
// estimated at list price via the Pricer, while a positive CSV cost (User
// API Key billing — real money Cursor charged) is stored verbatim and never
// estimated. The estimate uses the NORMALIZED base model, so picker-mode
// suffixes cannot split the pricing dimension.
func TestImport_ZeroCostRowsEstimatedPaidRowsKept(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	rows, _, err := Parse("usage-events-fixture.csv", strings.NewReader(fixture), false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Fixture rows are all plan-included ($0) except one User API Key row.
	var paidRows, zeroRows int
	for i := range rows {
		if rows[i].CostUSD > 0 {
			paidRows++
			rows[i].CostUSD = 0.5
		} else {
			zeroRows++
		}
	}
	if paidRows == 0 || zeroRows == 0 {
		t.Fatalf("fixture must contain both paid and $0 rows, got paid=%d zero=%d", paidRows, zeroRows)
	}

	if _, err := Import(ctx, repo, stubPricer{usd: 0.25}, rows, nil); err != nil {
		t.Fatalf("Import: %v", err)
	}
	var est, paid int64
	if err := repo.DB().QueryRow(
		"SELECT COUNT(*) FROM api_requests WHERE service_name = 'cursor' AND cost_usd = 25000").Scan(&est); err != nil {
		t.Fatalf("estimated count: %v", err)
	}
	if err := repo.DB().QueryRow(
		"SELECT COUNT(*) FROM api_requests WHERE service_name = 'cursor' AND cost_usd = 50000").Scan(&paid); err != nil {
		t.Fatalf("paid count: %v", err)
	}
	if int(est) != zeroRows || int(paid) != paidRows {
		t.Fatalf("stored costs: estimated=%d (want %d), paid-kept=%d (want %d)", est, zeroRows, paid, paidRows)
	}
}

// A nil Pricer keeps the raw CSV cost even when it is zero — the daemon
// always wires a registry, but tests and offline tools may not.
func TestImport_NilPricerKeepsCSVZero(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	rows, _, err := Parse("f.csv", strings.NewReader(fixture), false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := Import(ctx, repo, nil, rows, nil); err != nil {
		t.Fatalf("Import: %v", err)
	}
	var n int64
	if err := repo.DB().QueryRow(
		"SELECT COUNT(*) FROM api_requests WHERE service_name = 'cursor' AND cost_usd = 0").Scan(&n); err != nil {
		t.Fatalf("zero-cost count: %v", err)
	}
	// 4 fixture rows: 3 plan-included ($0 stays $0) + 1 User API Key row whose
	// positive CSV cost must ALSO be kept verbatim (nil pricer never estimates).
	var paid int64
	if err := repo.DB().QueryRow(
		"SELECT COUNT(*) FROM api_requests WHERE service_name = 'cursor' AND cost_usd > 0").Scan(&paid); err != nil {
		t.Fatalf("paid count: %v", err)
	}
	if int(n) != len(rows)-1 || int(paid) != 1 {
		t.Fatalf("nil-pricer import: %d at $0 + %d paid, want %d + 1", n, paid, len(rows)-1)
	}
}
