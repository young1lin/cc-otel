package db

import (
	"context"
	"math"
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

func TestGetCodexCalendarDaysAggregatesByDateAndTopModel(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()

	day := time.Date(2026, 5, 9, 10, 0, 0, 0, time.Local)
	fixtures := []CodexAPIRequest{
		{Timestamp: day, SessionID: "codex-cal-1", Model: "gpt-5.5", InputTokens: 100, OutputTokens: 50, CacheReadTokens: 20, ReasoningTokens: 7, TotalTokens: 157, CostUSD: 0.11},
		{Timestamp: day.Add(time.Hour), SessionID: "codex-cal-2", Model: "gpt-5.5", InputTokens: 200, OutputTokens: 25, CacheReadTokens: 30, ReasoningTokens: 3, TotalTokens: 228, CostUSD: 0.12},
		{Timestamp: day.Add(2 * time.Hour), SessionID: "codex-cal-3", Model: "gpt-5.4-mini", InputTokens: 40, OutputTokens: 10, CacheReadTokens: 0, ReasoningTokens: 0, TotalTokens: 50, CostUSD: 0.01},
	}
	for i := range fixtures {
		if _, err := repo.InsertCodexAPIRequest(ctx, &fixtures[i]); err != nil {
			t.Fatalf("insert fixture %d: %v", i, err)
		}
	}

	got, err := repo.GetCodexCalendarDays(ctx, "2026-05-09", "2026-05-09")
	if err != nil {
		t.Fatalf("GetCodexCalendarDays: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 day, got %d: %+v", len(got), got)
	}
	dayRow := got[0]
	if dayRow.Date != "2026-05-09" || dayRow.TopModel != "gpt-5.5" {
		t.Fatalf("unexpected date/top model: %+v", dayRow)
	}
	if dayRow.InputTokens != 340 || dayRow.OutputTokens != 85 || dayRow.CacheReadTokens != 50 || dayRow.RequestCount != 3 {
		t.Fatalf("unexpected codex calendar totals: %+v", dayRow)
	}
	if math.Abs(dayRow.CostUSD-0.24) > 0.000001 {
		t.Fatalf("expected cost 0.24, got %.8f", dayRow.CostUSD)
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
		SessionID:           "sess-A",
		Model:               "gpt-5.1",
		Timestamp:           t0.Add(3 * time.Second),
		InputTokens:         1000,
		OutputTokens:        200,
		CacheReadTokens:     50,
		CacheCreationTokens: 25,
		ReasoningTokens:     20,
		TotalTokens:         1200,
		CostUSD:             0.125,
		DurationMs:          2300,
		TTFTMs:              250,
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

	var input, output, cacheRead, cacheCreate, reasoning, total, duration, ttft int64
	var costUnits int64
	err = repo.db.QueryRowContext(ctx, `
		SELECT input_tokens, output_tokens, cache_read_tokens,
		       cache_creation_tokens, reasoning_tokens, total_tokens,
		       cost_usd, duration_ms, ttft_ms
		FROM codex_api_requests WHERE id = ?`, tokenisedID,
	).Scan(&input, &output, &cacheRead, &cacheCreate, &reasoning, &total, &costUnits, &duration, &ttft)
	if err != nil {
		t.Fatalf("read tokenised row: %v", err)
	}
	if input != 1000 || output != 200 || cacheRead != 50 || cacheCreate != 25 ||
		reasoning != 20 || total != 1200 || duration != 2300 || ttft != 250 {
		t.Fatalf("unexpected tokenised row: in=%d out=%d read=%d create=%d reasoning=%d total=%d duration=%d ttft=%d",
			input, output, cacheRead, cacheCreate, reasoning, total, duration, ttft)
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

	if err := repo.InsertCodexRawEvent(ctx, "log", now.Unix(), `{"e":"x"}`); err != nil {
		t.Fatalf("raw: %v", err)
	}

	for _, table := range []string{
		"codex_user_prompt_events",
		"codex_tool_decision_events",
		"codex_tool_result_events",
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
func readCodexAgg(t *testing.T, repo *Repository, date, model string) (
	input, output, cacheRead, cacheCreate, reasoning, totalTokens, count int64,
	ok bool,
) {
	t.Helper()
	row := repo.db.QueryRow(`
		SELECT input_tokens, output_tokens, cache_read_tokens,
		       cache_creation_tokens, reasoning_tokens, total_tokens, request_count
		FROM codex_daily_model_agg WHERE date = ? AND model = ?`, date, model)
	if err := row.Scan(
		&input, &output, &cacheRead, &cacheCreate, &reasoning, &totalTokens, &count,
	); err != nil {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	return input, output, cacheRead, cacheCreate, reasoning, totalTokens, count, true
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

	in, out, cache, cacheCreate, reason, totalTokens, count, ok := readCodexAgg(t, repo, dateKey, "gpt-5.1")
	if !ok {
		t.Fatalf("expected agg row after insert")
	}
	if in != 0 || out != 0 || cache != 0 || cacheCreate != 0 || reason != 0 || totalTokens != 0 || count != 1 {
		t.Fatalf("after insert: tokens should be 0 and count=1, got in=%d out=%d cache=%d create=%d reasoning=%d total=%d count=%d",
			in, out, cache, cacheCreate, reason, totalTokens, count)
	}

	// Step 2: codex.sse_event(response.completed) → UPDATE the row, add tokens to agg, count unchanged.
	updated, err := repo.UpdateCodexAPIRequestTokens(ctx, &CodexTokenUpdate{
		SessionID:           "sess-1",
		Model:               "gpt-5.1",
		Timestamp:           day.Add(2 * time.Second),
		InputTokens:         1000,
		OutputTokens:        200,
		CacheReadTokens:     50,
		CacheCreationTokens: 25,
		ReasoningTokens:     20,
		TotalTokens:         1200,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !updated {
		t.Fatal("expected UPDATE branch (matching pending row exists)")
	}

	in, out, cache, cacheCreate, reason, totalTokens, count, _ = readCodexAgg(t, repo, dateKey, "gpt-5.1")
	if in != 1000 || out != 200 || cache != 50 || cacheCreate != 25 || reason != 20 || totalTokens != 1200 || count != 1 {
		t.Fatalf("after update: expected tokens=(1000,200,50,25,20,1200) count=1; got in=%d out=%d cache=%d create=%d reasoning=%d total=%d count=%d",
			in, out, cache, cacheCreate, reason, totalTokens, count)
	}
}

func TestUpdateCodexAPIRequestTokens_ExactRowIsIdempotent(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()
	day := time.Date(2026, 7, 17, 10, 0, 0, 0, time.Local)

	olderID, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
		Timestamp: day, SessionID: "same-session", Model: "gpt-5.1",
	})
	if err != nil {
		t.Fatalf("insert older: %v", err)
	}
	newerID, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
		Timestamp: day.Add(time.Second), SessionID: "same-session", Model: "gpt-5.1",
	})
	if err != nil {
		t.Fatalf("insert newer: %v", err)
	}

	upd := &CodexTokenUpdate{
		RequestRowID: olderID, SessionID: "same-session", Model: "gpt-5.1",
		Timestamp:   day.Add(2 * time.Second),
		InputTokens: 100, OutputTokens: 10, CacheReadTokens: 40,
		CacheCreationTokens: 20, ReasoningTokens: 5, TotalTokens: 110,
		CostUSD: 0.01, DurationMs: 2300, TTFTMs: 250,
	}
	for attempt := 0; attempt < 2; attempt++ {
		updated, updateErr := repo.UpdateCodexAPIRequestTokens(ctx, upd)
		if updateErr != nil {
			t.Fatalf("attempt %d: %v", attempt, updateErr)
		}
		if !updated {
			t.Fatalf("attempt %d used fallback insert", attempt)
		}
	}

	var olderInput, olderCreate, newerInput int64
	if err := repo.db.QueryRowContext(ctx,
		`SELECT input_tokens, cache_creation_tokens FROM codex_api_requests WHERE id = ?`,
		olderID,
	).Scan(&olderInput, &olderCreate); err != nil {
		t.Fatalf("read older: %v", err)
	}
	if err := repo.db.QueryRowContext(ctx,
		`SELECT input_tokens FROM codex_api_requests WHERE id = ?`, newerID,
	).Scan(&newerInput); err != nil {
		t.Fatalf("read newer: %v", err)
	}
	if olderInput != 100 || olderCreate != 20 || newerInput != 0 {
		t.Fatalf("wrong exact-row result: older=(%d,%d) newer=%d",
			olderInput, olderCreate, newerInput)
	}

	var rows int64
	if err := repo.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM codex_api_requests WHERE session_id = 'same-session'`,
	).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 2 {
		t.Fatalf("duplicate fallback row inserted: rows=%d", rows)
	}

	_, _, _, cacheCreate, _, _, count, ok := readCodexAgg(
		t, repo, day.Format("2006-01-02"), "gpt-5.1",
	)
	if !ok || cacheCreate != 20 || count != 2 {
		t.Fatalf("aggregate applied more than once: create=%d count=%d ok=%v",
			cacheCreate, count, ok)
	}
}

func TestCodexAgg_FallbackInsert_CountsOnce(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()

	day := time.Date(2026, 4, 28, 10, 0, 0, 0, time.Local)
	dateKey := day.Format("2006-01-02")

	// No prior codex.api_request → fallback INSERT path.
	if _, err := repo.UpdateCodexAPIRequestTokens(ctx, &CodexTokenUpdate{
		SessionID:           "sess-fallback",
		Model:               "gpt-5.1",
		Timestamp:           day,
		InputTokens:         500,
		OutputTokens:        100,
		CacheReadTokens:     40,
		CacheCreationTokens: 20,
		TTFTMs:              250,
	}); err != nil {
		t.Fatalf("update fallback: %v", err)
	}

	in, out, cacheRead, cacheCreate, _, _, count, ok := readCodexAgg(
		t, repo, dateKey, "gpt-5.1",
	)
	if !ok || in != 500 || out != 100 || cacheRead != 40 ||
		cacheCreate != 20 || count != 1 {
		t.Fatalf("fallback aggregate: in=%d out=%d read=%d create=%d count=%d ok=%v",
			in, out, cacheRead, cacheCreate, count, ok)
	}
	var rowCreate, rowTTFT int64
	if err := repo.db.QueryRowContext(ctx, `
		SELECT cache_creation_tokens, ttft_ms
		FROM codex_api_requests WHERE session_id = 'sess-fallback'`,
	).Scan(&rowCreate, &rowTTFT); err != nil {
		t.Fatalf("read fallback request: %v", err)
	}
	if rowCreate != 20 || rowTTFT != 250 {
		t.Fatalf("fallback request lost completion fields: create=%d ttft=%d",
			rowCreate, rowTTFT)
	}
}

func TestCodexTimingHelpers_TargetExactRow(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.Local)

	olderID, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
		Timestamp: now, SessionID: "timing-session", Model: "gpt-5.1",
	})
	if err != nil {
		t.Fatalf("insert older: %v", err)
	}
	newerID, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
		Timestamp: now.Add(time.Second), SessionID: "timing-session", Model: "gpt-5.1",
	})
	if err != nil {
		t.Fatalf("insert newer: %v", err)
	}

	updated, err := repo.UpdateCodexRequestDuration(
		ctx, olderID, "timing-session", "gpt-5.1", now.Add(2*time.Second), 2300,
	)
	if err != nil || !updated {
		t.Fatalf("duration update: updated=%v err=%v", updated, err)
	}
	if err := repo.UpdateCodexRequestTTFT(
		ctx, olderID, "timing-session", "gpt-5.1", now.Add(2*time.Second), 250,
	); err != nil {
		t.Fatalf("ttft update: %v", err)
	}

	var olderDuration, olderTTFT, newerDuration, newerTTFT int64
	if err := repo.db.QueryRowContext(ctx,
		`SELECT duration_ms, ttft_ms FROM codex_api_requests WHERE id = ?`, olderID,
	).Scan(&olderDuration, &olderTTFT); err != nil {
		t.Fatalf("read older: %v", err)
	}
	if err := repo.db.QueryRowContext(ctx,
		`SELECT duration_ms, ttft_ms FROM codex_api_requests WHERE id = ?`, newerID,
	).Scan(&newerDuration, &newerTTFT); err != nil {
		t.Fatalf("read newer: %v", err)
	}
	if olderDuration != 2300 || olderTTFT != 250 ||
		newerDuration != 0 || newerTTFT != 0 {
		t.Fatalf("wrong timing target: older=(%d,%d) newer=(%d,%d)",
			olderDuration, olderTTFT, newerDuration, newerTTFT)
	}

	updated, err = repo.UpdateCodexRequestDuration(
		ctx, olderID, "timing-session", "gpt-5.1", now.Add(3*time.Second), 9999,
	)
	if err != nil || !updated {
		t.Fatalf("positive exact duration should be handled: updated=%v err=%v", updated, err)
	}
	if err := repo.UpdateCodexRequestTTFT(
		ctx, olderID, "timing-session", "gpt-5.1", now.Add(3*time.Second), 9999,
	); err != nil {
		t.Fatalf("positive exact ttft should be handled: %v", err)
	}
	if err := repo.db.QueryRowContext(ctx,
		`SELECT duration_ms, ttft_ms FROM codex_api_requests WHERE id = ?`, olderID,
	).Scan(&olderDuration, &olderTTFT); err != nil {
		t.Fatalf("reread older: %v", err)
	}
	if olderDuration != 2300 || olderTTFT != 250 {
		t.Fatalf("fallback overwrote authoritative values: duration=%d ttft=%d",
			olderDuration, olderTTFT)
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

	in, out, _, _, _, _, count, _ := readCodexAgg(t, repo, dateKey, "gpt-5.1")
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
