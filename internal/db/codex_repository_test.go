package db

import (
	"context"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
)

func newCodexTestRepo(t *testing.T) *Repository {
	t.Helper()
	d, err := Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return NewRepository(d)
}

func TestInsertCodexAPIRequest_RoundTrip(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()

	req := &CodexAPIRequest{
		Timestamp:      time.Unix(1700000000, 0),
		SessionID:      "sess-1",
		Model:          "gpt-5.1",
		DurationMs:     1234,
		HTTPStatus:     200,
		Endpoint:       "/v1/responses",
		EventName:      "codex.api_request",
		ServiceName:    "codex-cli",
		ServiceVersion: "2.1.96",
	}
	id, err := repo.InsertCodexAPIRequest(ctx, req)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	got, err := repo.getCodexAPIRequestByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != "sess-1" || got.Model != "gpt-5.1" || got.DurationMs != 1234 || got.HTTPStatus != 200 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Fatalf("freshly-inserted row should have zero tokens, got input=%d output=%d", got.InputTokens, got.OutputTokens)
	}
}

func TestUpdateCodexAPIRequestTokens_UpdatesNewestZeroTokenRow(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()

	t0 := time.Now()
	for i := 0; i < 3; i++ {
		_, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
			Timestamp:  t0.Add(time.Duration(i) * time.Second),
			SessionID:  "sess-A",
			Model:      "gpt-5.1",
			DurationMs: int64(100 + i),
		})
		if err != nil {
			t.Fatalf("seed insert: %v", err)
		}
	}

	updated, err := repo.UpdateCodexAPIRequestTokens(ctx, &CodexTokenUpdate{
		SessionID:       "sess-A",
		Model:           "gpt-5.1",
		Timestamp:       t0.Add(3 * time.Second),
		InputTokens:     1000,
		OutputTokens:    200,
		CacheReadTokens: 50,
		ReasoningTokens: 20,
		TotalTokens:     1200,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !updated {
		t.Fatal("expected an update, got false (would have inserted instead)")
	}

	var n int
	if err := repo.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM codex_api_requests WHERE session_id='sess-A' AND model='gpt-5.1' AND input_tokens > 0`,
	).Scan(&n); err != nil {
		t.Fatalf("count tokenised: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 row tokenised, got %d", n)
	}

	var maxID, tokenisedID int64
	repo.db.QueryRowContext(ctx, `SELECT MAX(id) FROM codex_api_requests WHERE session_id='sess-A'`).Scan(&maxID)
	repo.db.QueryRowContext(ctx, `SELECT id   FROM codex_api_requests WHERE session_id='sess-A' AND input_tokens > 0`).Scan(&tokenisedID)
	if maxID != tokenisedID {
		t.Fatalf("expected newest row %d to be tokenised, but row %d was tokenised", maxID, tokenisedID)
	}
}

func TestUpdateCodexAPIRequestTokens_InsertsWhenNoMatch(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()

	updated, err := repo.UpdateCodexAPIRequestTokens(ctx, &CodexTokenUpdate{
		SessionID:    "sess-B",
		Model:        "gpt-5.1",
		Timestamp:    time.Now(),
		InputTokens:  500,
		OutputTokens: 100,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated {
		t.Fatal("expected fallback INSERT (returns false), got update=true")
	}

	var n int64
	repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_api_requests`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected exactly 1 row inserted, got %d", n)
	}
}

func TestCodexSubEvent_Inserts(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	if err := repo.InsertCodexUserPrompt(ctx, &CodexEvent{
		Timestamp: now, SessionID: "s1", PromptText: "hi", PromptLength: 2,
	}); err != nil {
		t.Fatalf("user_prompt: %v", err)
	}

	if err := repo.InsertCodexToolDecision(ctx, &CodexEvent{
		Timestamp: now, SessionID: "s1", ToolName: "Bash", CallID: "c1", Decision: "accept", Source: "user",
	}); err != nil {
		t.Fatalf("tool_decision: %v", err)
	}

	if err := repo.InsertCodexToolResult(ctx, &CodexEvent{
		Timestamp: now, SessionID: "s1", ToolName: "Bash", CallID: "c1",
		Success: 1, DurationMs: 50, OutputLength: 100, ToolOrigin: "builtin",
	}); err != nil {
		t.Fatalf("tool_result: %v", err)
	}

	if err := repo.InsertCodexEvent(ctx, &CodexEvent{
		Timestamp: now, SessionID: "s1", EventName: "codex.sse_event", EventKind: "response.created",
	}); err != nil {
		t.Fatalf("event: %v", err)
	}

	if err := repo.InsertCodexRawEvent(ctx, "log", now.Unix(), `{"e":"x"}`); err != nil {
		t.Fatalf("raw: %v", err)
	}

	for _, table := range []string{
		"codex_user_prompt_events",
		"codex_tool_decision_events",
		"codex_tool_result_events",
		"codex_events",
		"codex_raw_otlp_events",
	} {
		var n int
		repo.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n)
		if n != 1 {
			t.Errorf("table %s: expected 1 row, got %d", table, n)
		}
	}
}

// readCodexAgg fetches the agg row for (date, model). Returns false if missing.
func readCodexAgg(t *testing.T, repo *Repository, date, model string) (input, output, cacheRead, reasoning, totalTokens, count int64, ok bool) {
	t.Helper()
	row := repo.db.QueryRow(`
		SELECT input_tokens, output_tokens, cache_read_tokens,
		       reasoning_tokens, total_tokens, request_count
		FROM codex_daily_model_agg WHERE date = ? AND model = ?`, date, model)
	if err := row.Scan(&input, &output, &cacheRead, &reasoning, &totalTokens, &count); err != nil {
		return 0, 0, 0, 0, 0, 0, false
	}
	return input, output, cacheRead, reasoning, totalTokens, count, true
}

func TestCodexAgg_InsertAndUpdate_PopulatesAggExactlyOnce(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()

	// Use a fixed local-day timestamp so dateKey is deterministic.
	day := time.Date(2026, 4, 28, 10, 0, 0, 0, time.Local)
	dateKey := day.Format("2006-01-02")

	// Step 1: codex.api_request → row #1, agg row created with count=1, tokens=0.
	if _, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
		Timestamp:  day,
		SessionID:  "sess-1",
		Model:      "gpt-5.1",
		DurationMs: 1000,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	in, out, cache, reason, totalTokens, count, ok := readCodexAgg(t, repo, dateKey, "gpt-5.1")
	if !ok {
		t.Fatalf("expected agg row after insert")
	}
	if in != 0 || out != 0 || cache != 0 || reason != 0 || totalTokens != 0 || count != 1 {
		t.Fatalf("after insert: tokens should be 0 and count=1, got in=%d out=%d cache=%d reasoning=%d total=%d count=%d",
			in, out, cache, reason, totalTokens, count)
	}

	// Step 2: codex.sse_event(response.completed) → UPDATE the row, add tokens to agg, count unchanged.
	updated, err := repo.UpdateCodexAPIRequestTokens(ctx, &CodexTokenUpdate{
		SessionID:       "sess-1",
		Model:           "gpt-5.1",
		Timestamp:       day.Add(2 * time.Second),
		InputTokens:     1000,
		OutputTokens:    200,
		CacheReadTokens: 50,
		ReasoningTokens: 20,
		TotalTokens:     1200,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !updated {
		t.Fatal("expected UPDATE branch (matching pending row exists)")
	}

	in, out, cache, reason, totalTokens, count, _ = readCodexAgg(t, repo, dateKey, "gpt-5.1")
	if in != 1000 || out != 200 || cache != 50 || reason != 20 || totalTokens != 1200 || count != 1 {
		t.Fatalf("after update: expected tokens=(1000,200,50,20,1200) count=1; got in=%d out=%d cache=%d reasoning=%d total=%d count=%d",
			in, out, cache, reason, totalTokens, count)
	}
}

func TestCodexAgg_FallbackInsert_CountsOnce(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()

	day := time.Date(2026, 4, 28, 10, 0, 0, 0, time.Local)
	dateKey := day.Format("2006-01-02")

	// No prior codex.api_request → fallback INSERT path.
	if _, err := repo.UpdateCodexAPIRequestTokens(ctx, &CodexTokenUpdate{
		SessionID:    "sess-fallback",
		Model:        "gpt-5.1",
		Timestamp:    day,
		InputTokens:  500,
		OutputTokens: 100,
	}); err != nil {
		t.Fatalf("update fallback: %v", err)
	}

	in, out, _, _, _, count, ok := readCodexAgg(t, repo, dateKey, "gpt-5.1")
	if !ok {
		t.Fatal("expected agg row after fallback insert")
	}
	if in != 500 || out != 100 || count != 1 {
		t.Fatalf("fallback: expected (500,100,count=1); got (%d,%d,count=%d)", in, out, count)
	}
}

func TestCodexAgg_TwoRequestsSameDay_AccumulateCorrectly(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()

	day := time.Date(2026, 4, 28, 10, 0, 0, 0, time.Local)
	dateKey := day.Format("2006-01-02")

	// Two full request cycles on the same day, same model.
	for i := 0; i < 2; i++ {
		if _, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
			Timestamp: day.Add(time.Duration(i) * time.Minute),
			SessionID: "sess-X",
			Model:     "gpt-5.1",
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		if _, err := repo.UpdateCodexAPIRequestTokens(ctx, &CodexTokenUpdate{
			SessionID:    "sess-X",
			Model:        "gpt-5.1",
			Timestamp:    day.Add(time.Duration(i)*time.Minute + 2*time.Second),
			InputTokens:  100,
			OutputTokens: 30,
		}); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}

	in, out, _, _, _, count, _ := readCodexAgg(t, repo, dateKey, "gpt-5.1")
	if in != 200 || out != 60 || count != 2 {
		t.Fatalf("expected (200,60,count=2); got (%d,%d,count=%d)", in, out, count)
	}
}

func TestCodexQueries_RoundTrip(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Hour)
	for i := 0; i < 2; i++ {
		_, _ = repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
			Timestamp:       now.Add(time.Duration(i) * time.Minute),
			SessionID:       "s1",
			Model:           "gpt-5.1",
			InputTokens:     1000,
			OutputTokens:    100,
			CacheReadTokens: 200,
			DurationMs:      200,
			HTTPStatus:      200,
		})
	}

	from, to := now.Format("2006-01-02"), now.Format("2006-01-02")

	dash, err := repo.GetCodexDashboard(ctx, from, to)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if dash.RequestCount != 2 || dash.TotalInputTokens != 2000 {
		t.Fatalf("dashboard wrong: %+v", dash)
	}
	if dash.TotalCacheReadTokens != 400 || dash.CacheHitRate != 0.2 {
		t.Fatalf("dashboard cache wrong: %+v", dash)
	}

	daily, total, err := repo.GetCodexDailyStatsByModel(ctx, from, to, 100, 0, "day")
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if total == 0 || len(daily) == 0 {
		t.Fatalf("daily empty: total=%d rows=%d", total, len(daily))
	}

	reqs, total, err := repo.GetCodexRecentRequests(ctx, 10, 0, "", from, to)
	if err != nil {
		t.Fatalf("requests: %v", err)
	}
	if total != 2 || len(reqs) != 2 {
		t.Fatalf("requests wrong: total=%d rows=%d", total, len(reqs))
	}

	sessions, total, err := repo.GetCodexSessionStats(ctx, from, to, 10, 0)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if total != 1 || len(sessions) != 1 {
		t.Fatalf("sessions wrong: total=%d rows=%d", total, len(sessions))
	}

	durs, err := repo.GetCodexDurationStatsByModel(ctx, "", from, to)
	if err != nil {
		t.Fatalf("durations: %v", err)
	}
	if len(durs) == 0 {
		t.Fatalf("durations empty")
	}
}
