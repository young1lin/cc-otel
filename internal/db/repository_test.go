package db

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
)

func TestInsertAndQuery(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)

	req := &APIRequest{
		Timestamp:           time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC),
		SessionID:           "sess-123",
		Model:               "claude-opus-4-6",
		ActualModel:         "glm-5",
		InputTokens:         1500,
		OutputTokens:        300,
		CacheReadTokens:     1000,
		CacheCreationTokens: 500,
		CostUSD:             0.025,
		DurationMs:          2000,
		TTFTMs:              500,
		RequestID:           "req-abc",
	}

	_, err = repo.InsertRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	reqs, err := repo.GetRecentRequests(context.Background(), 10, 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Model != "claude-opus-4-6" {
		t.Errorf("expected model claude-opus-4-6, got %s", reqs[0].Model)
	}
	if reqs[0].ActualModel != "glm-5" {
		t.Errorf("expected actual_model glm-5, got %s", reqs[0].ActualModel)
	}
}

func TestDailyStats(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)

	now := time.Now()
	for i := 0; i < 3; i++ {
		ts := now.AddDate(0, 0, -i)
		_, _ = repo.InsertRequest(context.Background(), &APIRequest{
			Timestamp:    ts,
			Model:        "claude-opus-4-6",
			InputTokens:  1000,
			OutputTokens: 200,
			CostUSD:      0.01,
		})
	}

	stats, err := repo.GetDailyStats(context.Background(), now.AddDate(0, 0, -7).Format("2006-01-02"), now.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 3 {
		t.Errorf("expected 3 days, got %d", len(stats))
	}
}

func TestGetCalendarDaysAggregatesByDateAndTopModel(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	day1 := time.Date(2026, 5, 9, 10, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 5, 10, 10, 0, 0, 0, time.Local)
	fixtures := []APIRequest{
		{Timestamp: day1, RequestID: "cal-1", Model: "glm-5-turbo", InputTokens: 100, OutputTokens: 50, CacheReadTokens: 300, CacheCreationTokens: 20, CostUSD: 0.011},
		{Timestamp: day1.Add(time.Hour), RequestID: "cal-2", Model: "glm-5-turbo", InputTokens: 200, OutputTokens: 70, CacheReadTokens: 400, CacheCreationTokens: 30, CostUSD: 0.012},
		{Timestamp: day1.Add(2 * time.Hour), RequestID: "cal-3", Model: "claude-opus-4-7", InputTokens: 900, OutputTokens: 10, CacheReadTokens: 0, CacheCreationTokens: 0, CostUSD: 0.02},
		{Timestamp: day2, RequestID: "cal-4", Model: "glm-5.1", InputTokens: 10, OutputTokens: 5, CacheReadTokens: 20, CacheCreationTokens: 0, CostUSD: 0.001},
	}
	for i := range fixtures {
		if _, err := repo.InsertRequest(ctx, &fixtures[i]); err != nil {
			t.Fatalf("insert fixture %d: %v", i, err)
		}
	}

	got, err := repo.GetCalendarDays(ctx, "2026-05-09", "2026-05-10")
	if err != nil {
		t.Fatalf("GetCalendarDays: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 days, got %d: %+v", len(got), got)
	}

	if got[0].Date != "2026-05-09" {
		t.Fatalf("expected first date 2026-05-09, got %s", got[0].Date)
	}
	if got[0].TopModel != "glm-5-turbo" {
		t.Fatalf("expected top model glm-5-turbo, got %s", got[0].TopModel)
	}
	if got[0].InputTokens != 1200 || got[0].OutputTokens != 130 || got[0].CacheReadTokens != 700 || got[0].CacheCreationTokens != 50 {
		t.Fatalf("unexpected day1 token totals: %+v", got[0])
	}
	if got[0].RequestCount != 3 {
		t.Fatalf("expected day1 request_count=3, got %d", got[0].RequestCount)
	}
	if math.Abs(got[0].CostUSD-0.043) > 0.000001 {
		t.Fatalf("expected day1 cost 0.043, got %.8f", got[0].CostUSD)
	}

	if got[1].Date != "2026-05-10" || got[1].TopModel != "glm-5.1" {
		t.Fatalf("unexpected second day: %+v", got[1])
	}
}

func TestCleanup(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	now := time.Now()
	oldTime := now.AddDate(0, 0, -200)
	recentTime := now

	// Insert old and recent raw_otlp_events
	if err := repo.InsertRawEvent(ctx, "log", oldTime.Unix(), `{"old":"data"}`); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertRawEvent(ctx, "log", recentTime.Unix(), `{"recent":"data"}`); err != nil {
		t.Fatal(err)
	}

	// Insert old and recent events
	if err := repo.InsertEvent(ctx, &Event{Timestamp: oldTime, EventName: "old_event"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertEvent(ctx, &Event{Timestamp: recentTime, EventName: "recent_event"}); err != nil {
		t.Fatal(err)
	}

	// Cutoff: 100 days ago (should delete 200-day-old records, keep recent)
	cutoff := now.AddDate(0, 0, -100).Unix()
	deleted, err := repo.Cleanup(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Errorf("expected 2 deleted rows, got %d", deleted)
	}

	// Verify recent raw_otlp_events kept
	var rawCount int
	database.QueryRow("SELECT COUNT(*) FROM raw_otlp_events").Scan(&rawCount)
	if rawCount != 1 {
		t.Errorf("expected 1 raw_otlp_event remaining, got %d", rawCount)
	}

	// Verify recent events kept
	var evtCount int
	database.QueryRow("SELECT COUNT(*) FROM events").Scan(&evtCount)
	if evtCount != 1 {
		t.Errorf("expected 1 event remaining, got %d", evtCount)
	}
}

func TestGetDashboardCacheHitRate(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	now := time.Now()
	_, err = repo.InsertRequest(ctx, &APIRequest{
		Timestamp:           now,
		Model:               "claude-opus-4-6",
		CacheReadTokens:     800,
		CacheCreationTokens: 200,
		InputTokens:         100,
		OutputTokens:        50,
		CostUSD:             0.01,
	})
	if err != nil {
		t.Fatal(err)
	}

	from := now.Format("2006-01-02")
	to := from
	dash, err := repo.GetDashboardForRange(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}

	// CacheHitRate = cache_read / input-side total
	//             = cache_read / (input + cache_read + cache_creation)
	//             = 800 / (100 + 800 + 200) = 800 / 1100 = 0.72727...
	if math.Abs(dash.CacheHitRate-0.72727) > 0.001 {
		t.Errorf("expected CacheHitRate ~0.7273, got %f", dash.CacheHitRate)
	}
}

// TestGetDashboardCacheHitNoCacheCreation guards the GLM/mimo regression:
// reverse-proxied providers report cache_read but never cache_creation. The
// hit rate must use the full input-side as denominator, so it must NOT collapse
// to 100% just because cache_creation is 0.
func TestGetDashboardCacheHitNoCacheCreation(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	now := time.Now()
	_, err = repo.InsertRequest(ctx, &APIRequest{
		Timestamp:           now,
		Model:               "glm-5.1",
		CacheReadTokens:     900,
		CacheCreationTokens: 0, // provider never reports cache creation
		InputTokens:         100,
		OutputTokens:        50,
		CostUSD:             0.01,
	})
	if err != nil {
		t.Fatal(err)
	}

	from := now.Format("2006-01-02")
	dash, err := repo.GetDashboardForRange(ctx, from, from)
	if err != nil {
		t.Fatal(err)
	}

	// CacheHitRate = 900 / (100 + 900 + 0) = 0.9 — must not be 1.0.
	if math.Abs(dash.CacheHitRate-0.9) > 0.001 {
		t.Errorf("expected CacheHitRate ~0.9 (not 100%%), got %f", dash.CacheHitRate)
	}
}

func TestEmptyDatabaseQueries(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	from := "2026-01-01"
	to := "2026-12-31"

	// GetDashboardForRange on empty DB
	dash, err := repo.GetDashboardForRange(ctx, from, to)
	if err != nil {
		t.Fatalf("GetDashboardForRange on empty DB: %v", err)
	}
	if dash.RequestCount != 0 {
		t.Errorf("expected 0 requests, got %d", dash.RequestCount)
	}
	if dash.TotalCostUSD != 0 {
		t.Errorf("expected 0 cost, got %f", dash.TotalCostUSD)
	}

	// GetDailyStats on empty DB
	stats, err := repo.GetDailyStats(ctx, from, to)
	if err != nil {
		t.Fatalf("GetDailyStats on empty DB: %v", err)
	}
	if len(stats) != 0 {
		t.Errorf("expected 0 daily stats, got %d", len(stats))
	}

	// GetRecentRequests on empty DB
	reqs, err := repo.GetRecentRequests(ctx, 10, 0, "", from, to)
	if err != nil {
		t.Fatalf("GetRecentRequests on empty DB: %v", err)
	}
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests, got %d", len(reqs))
	}

	// GetDistinctModels on empty DB
	models, err := repo.GetDistinctModels(ctx)
	if err != nil {
		t.Fatalf("GetDistinctModels on empty DB: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected 0 models, got %d", len(models))
	}

	// GetSessionStats on empty DB
	sessions, err := repo.GetSessionStats(ctx, from, to, 10, 0)
	if err != nil {
		t.Fatalf("GetSessionStats on empty DB: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestCostPrecision(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	now := time.Now()
	_, err = repo.InsertRequest(ctx, &APIRequest{
		Timestamp: now,
		Model:     "claude-opus-4-6",
		CostUSD:   0.00001,
		RequestID: "cost-precision-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	reqs, err := repo.GetRecentRequests(ctx, 10, 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if math.Abs(reqs[0].CostUSD-0.00001) > 1e-10 {
		t.Errorf("expected CostUSD=0.00001, got %v", reqs[0].CostUSD)
	}
}

func TestInsertRequestUpdatesAgg(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	now := time.Now()
	inserted, err := repo.InsertRequest(ctx, &APIRequest{
		Timestamp:           now,
		Model:               "claude-opus-4-6",
		InputTokens:         1000,
		OutputTokens:        200,
		CacheReadTokens:     800,
		CacheCreationTokens: 100,
		CostUSD:             0.05,
		RequestID:           "req-agg-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Error("expected inserted=true for new request")
	}

	dateKey := now.Local().Format("2006-01-02")
	var aggInput, aggOutput, aggCacheRead, aggCacheCreate, aggCost, aggCount int64
	err = database.QueryRow(`SELECT input_tokens, output_tokens, cache_read_tokens,
		cache_creation_tokens, cost_usd, request_count
		FROM daily_model_agg WHERE date = ? AND model = ?`,
		dateKey, "claude-opus-4-6").Scan(
		&aggInput, &aggOutput, &aggCacheRead, &aggCacheCreate, &aggCost, &aggCount)
	if err != nil {
		t.Fatalf("query agg: %v", err)
	}
	if aggInput != 1000 || aggOutput != 200 || aggCacheRead != 800 || aggCacheCreate != 100 {
		t.Errorf("token mismatch: in=%d out=%d cr=%d cc=%d", aggInput, aggOutput, aggCacheRead, aggCacheCreate)
	}
	if aggCost != costToInt64(0.05) {
		t.Errorf("cost mismatch: got %d, want %d", aggCost, costToInt64(0.05))
	}
	if aggCount != 1 {
		t.Errorf("count mismatch: got %d, want 1", aggCount)
	}

	// Insert a second request — agg should accumulate.
	_, err = repo.InsertRequest(ctx, &APIRequest{
		Timestamp:           now,
		Model:               "claude-opus-4-6",
		InputTokens:         500,
		OutputTokens:        100,
		CacheReadTokens:     400,
		CacheCreationTokens: 50,
		CostUSD:             0.02,
		RequestID:           "req-agg-2",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = database.QueryRow(`SELECT input_tokens, request_count FROM daily_model_agg
		WHERE date = ? AND model = ?`, dateKey, "claude-opus-4-6").Scan(&aggInput, &aggCount)
	if err != nil {
		t.Fatal(err)
	}
	if aggInput != 1500 {
		t.Errorf("expected accumulated input_tokens=1500, got %d", aggInput)
	}
	if aggCount != 2 {
		t.Errorf("expected accumulated request_count=2, got %d", aggCount)
	}
}

func TestInsertRequestDuplicateSkipsAgg(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	now := time.Now()
	req := &APIRequest{
		Timestamp:   now,
		Model:       "claude-opus-4-6",
		InputTokens: 1000,
		CostUSD:     0.01,
		RequestID:   "dup-test-id",
	}

	inserted, err := repo.InsertRequest(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Error("first insert should return true")
	}

	inserted, err = repo.InsertRequest(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Error("duplicate insert should return false")
	}

	dateKey := now.Local().Format("2006-01-02")
	var aggCount int64
	database.QueryRow(`SELECT request_count FROM daily_model_agg WHERE date = ? AND model = ?`,
		dateKey, "claude-opus-4-6").Scan(&aggCount)
	if aggCount != 1 {
		t.Errorf("expected agg count=1 after duplicate, got %d", aggCount)
	}
}

func TestBackfillTTFTByRequestID(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	_, err = repo.InsertRequest(ctx, &APIRequest{
		Timestamp: time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC),
		SessionID: "api-session",
		PromptID:  "prompt-1",
		Model:     "claude-opus-4-7",
		RequestID: "req-exact",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := repo.BackfillTTFTByRequestID(ctx, "req-exact", 3409)
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("expected request_id TTFT update")
	}

	var ttft int64
	if err := database.QueryRowContext(ctx, `SELECT ttft_ms FROM api_requests WHERE request_id = 'req-exact'`).Scan(&ttft); err != nil {
		t.Fatal(err)
	}
	if ttft != 3409 {
		t.Fatalf("expected ttft 3409, got %d", ttft)
	}
}

func TestPendingTTFTByRequestIDSurvivesSessionMismatch(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()
	ts := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC)

	if err := repo.EnqueuePendingTTFTSpan(ctx, "req-pending", "trace-session", "claude-opus-4-7", ts.Unix(), 2552, `{}`); err != nil {
		t.Fatal(err)
	}
	_, err = repo.InsertRequest(ctx, &APIRequest{
		Timestamp: ts,
		SessionID: "api-session",
		PromptID:  "prompt-1",
		Model:     "claude-opus-4-7",
		RequestID: "req-pending",
	})
	if err != nil {
		t.Fatal(err)
	}

	var ttft, processed int64
	if err := database.QueryRowContext(ctx, `SELECT ttft_ms FROM api_requests WHERE request_id = 'req-pending'`).Scan(&ttft); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT processed FROM pending_ttft_spans WHERE request_id = 'req-pending'`).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if ttft != 2552 || processed != 1 {
		t.Fatalf("expected ttft=2552 and processed=1, got ttft=%d processed=%d", ttft, processed)
	}
}

func TestRebuildDailyAggregates(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	now := time.Now()
	for i := 0; i < 3; i++ {
		_, _ = repo.InsertRequest(ctx, &APIRequest{
			Timestamp:   now,
			Model:       "claude-opus-4-6",
			InputTokens: 100,
			CostUSD:     0.01,
		})
	}

	// Corrupt agg by clearing it
	database.Exec("DELETE FROM daily_model_agg")

	if err := repo.RebuildDailyAggregates(ctx); err != nil {
		t.Fatal(err)
	}

	dateKey := now.Local().Format("2006-01-02")
	var count int64
	database.QueryRow(`SELECT request_count FROM daily_model_agg WHERE date = ? AND model = ?`,
		dateKey, "claude-opus-4-6").Scan(&count)
	if count != 3 {
		t.Errorf("expected rebuilt count=3, got %d", count)
	}
}

func TestCleanupRemovesAgg(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	now := time.Now()
	oldTime := now.AddDate(0, 0, -200)

	_, _ = repo.InsertRequest(ctx, &APIRequest{
		Timestamp:   oldTime,
		Model:       "claude-opus-4-6",
		InputTokens: 100,
		CostUSD:     0.01,
		RequestID:   "old-req",
	})
	_, _ = repo.InsertRequest(ctx, &APIRequest{
		Timestamp:   now,
		Model:       "claude-opus-4-6",
		InputTokens: 200,
		CostUSD:     0.02,
		RequestID:   "recent-req",
	})

	cutoff := now.AddDate(0, 0, -100).Unix()
	_, err = repo.Cleanup(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}

	var aggCount int
	database.QueryRow("SELECT COUNT(*) FROM daily_model_agg").Scan(&aggCount)
	if aggCount != 1 {
		t.Errorf("expected 1 agg row remaining, got %d", aggCount)
	}
}

func TestNeedsAggRebuild(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	// Empty DB: no rebuild needed
	needs, err := repo.NeedsAggRebuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if needs {
		t.Error("empty DB should not need rebuild")
	}

	// Insert a request (populates agg): no rebuild needed
	_, _ = repo.InsertRequest(ctx, &APIRequest{
		Timestamp: time.Now(),
		Model:     "test",
	})
	needs, err = repo.NeedsAggRebuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if needs {
		t.Error("populated agg should not need rebuild")
	}

	// Clear agg but keep api_requests: rebuild needed
	database.Exec("DELETE FROM daily_model_agg")
	needs, err = repo.NeedsAggRebuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Error("empty agg with data in api_requests should need rebuild")
	}
}

func TestGetSessionRecentMinuteRate(t *testing.T) {
	cfg := &config.Config{DBPath: t.TempDir() + "/test.db"}
	database, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewRepository(database)
	ctx := context.Background()

	now := time.Now()
	bucketStart := now.Unix() - (now.Unix() % 60)

	_, err = repo.InsertRequest(ctx, &APIRequest{
		Timestamp: time.Unix(bucketStart-90, 0), SessionID: "s1", Model: "m",
		OutputTokens: 999, DurationMs: 1000, RequestID: "old",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.InsertRequest(ctx, &APIRequest{
		Timestamp: time.Unix(bucketStart+5, 0), SessionID: "s1", Model: "m",
		OutputTokens: 20, DurationMs: 2000, RequestID: "new1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.InsertRequest(ctx, &APIRequest{
		Timestamp: time.Unix(bucketStart+15, 0), SessionID: "s1", Model: "m",
		OutputTokens: 80, DurationMs: 8000, RequestID: "new2",
	})
	if err != nil {
		t.Fatal(err)
	}

	snap, err := repo.GetSessionRecentMinuteRate(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	if snap.RequestCount != 2 || snap.OutTokens != 100 {
		t.Fatalf("unexpected aggregate: %+v", snap)
	}
	wantWeighted := 100.0 * 1000.0 / 10000.0
	if math.Abs(snap.WeightedOutPerS-wantWeighted) > 0.01 {
		t.Fatalf("weighted_out_per_s = %v, want %v", snap.WeightedOutPerS, wantWeighted)
	}

	missing, err := repo.GetSessionRecentMinuteRate(ctx, "no-such-session")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatalf("expected nil for missing session, got %+v", missing)
	}
}
