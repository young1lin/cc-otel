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

	// CacheHitRate = cache_read / (cache_read + cache_creation) = 800 / 1000 = 0.8
	if math.Abs(dash.CacheHitRate-0.8) > 0.001 {
		t.Errorf("expected CacheHitRate ~0.8, got %f", dash.CacheHitRate)
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
