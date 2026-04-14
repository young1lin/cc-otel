package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
)

func TestHealthEndpoint(t *testing.T) {
	h := &Handler{} // nil repo → health returns 503
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	h.Health(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 without DB, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "error" {
		t.Errorf("expected status error, got %s", resp["status"])
	}
}

// setupTestHandler creates a Handler backed by a temp SQLite DB for testing.
func setupTestHandler(t *testing.T) (h *Handler, repo *db.Repository, cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		DBPath:   filepath.Join(tmpDir, "test.db"),
		OTELPort: 14317, // non-default to avoid conflicts
		WebPort:  18899,
	}
	sqlDB, err := db.Init(cfg)
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	repo = db.NewRepository(sqlDB)
	broker := NewBroker()
	h = NewHandler(repo, broker, cfg, filepath.Join(tmpDir, "cc-otel.yaml"))
	cleanup = func() { sqlDB.Close() }
	return
}

// insertTestRequest inserts a sample API request with the given model name.
func insertTestRequest(t *testing.T, repo *db.Repository, model string) {
	t.Helper()
	_, err := repo.InsertRequest(context.Background(), &db.APIRequest{
		Timestamp:           time.Now(),
		SessionID:           "sess-001",
		UserID:              "user-001",
		Model:               model,
		InputTokens:         100,
		OutputTokens:        50,
		CacheReadTokens:     80,
		CacheCreationTokens: 20,
		CostUSD:             0.01,
		DurationMs:          500,
		RequestID:           "", // leave empty so INSERT OR IGNORE works
	})
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
}

func TestStatusEndpoint(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	h.Status(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ServerTimeUnix == 0 {
		t.Error("server_time_unix should be non-zero")
	}
	if !resp.DBOK {
		t.Error("db_ok should be true with a valid DB")
	}
}

func TestDashboardEmpty(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/dashboard?range=all", nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp db.Dashboard
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TotalCostUSD != 0 {
		t.Errorf("expected 0 cost, got %f", resp.TotalCostUSD)
	}
	if resp.TotalInputTokens != 0 {
		t.Errorf("expected 0 input tokens, got %d", resp.TotalInputTokens)
	}
	if resp.TotalOutputTokens != 0 {
		t.Errorf("expected 0 output tokens, got %d", resp.TotalOutputTokens)
	}
	if resp.RequestCount != 0 {
		t.Errorf("expected 0 requests, got %d", resp.RequestCount)
	}
}

func TestDashboardWithData(t *testing.T) {
	h, repo, cleanup := setupTestHandler(t)
	defer cleanup()

	// Insert two requests with known values.
	// Each: input=100, output=50, cache_read=80, cache_creation=20, cost=0.01
	for i := 0; i < 2; i++ {
		_, err := repo.InsertRequest(context.Background(), &db.APIRequest{
			Timestamp:           time.Now(),
			SessionID:           "sess-dash",
			UserID:              "user-dash",
			Model:               "claude-opus-4-6",
			InputTokens:         100,
			OutputTokens:        50,
			CacheReadTokens:     80,
			CacheCreationTokens: 20,
			CostUSD:             0.01,
			DurationMs:          200,
			// request_id left empty so no UNIQUE conflict
		})
		if err != nil {
			t.Fatalf("InsertRequest #%d: %v", i, err)
		}
	}

	req := httptest.NewRequest("GET", "/api/dashboard?range=all", nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp db.Dashboard
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// total_input_tokens = SUM(input + cache_read + cache_creation) = 2*(100+80+20) = 400
	if resp.TotalInputTokens != 400 {
		t.Errorf("expected total_input_tokens=400, got %d", resp.TotalInputTokens)
	}
	// total_output_tokens = 2*50 = 100
	if resp.TotalOutputTokens != 100 {
		t.Errorf("expected total_output_tokens=100, got %d", resp.TotalOutputTokens)
	}
	// total_cache_read_tokens = 2*80 = 160
	if resp.TotalCacheReadTokens != 160 {
		t.Errorf("expected total_cache_read_tokens=160, got %d", resp.TotalCacheReadTokens)
	}
	// request_count = 2
	if resp.RequestCount != 2 {
		t.Errorf("expected request_count=2, got %d", resp.RequestCount)
	}
	// cost: costToInt64(0.01) = 1000 per row, total 2000, costToFloat64(2000) = 0.02
	if resp.TotalCostUSD != 0.02 {
		t.Errorf("expected total_cost_usd=0.02, got %f", resp.TotalCostUSD)
	}
	// cache_hit_rate = cache_read / (cache_read + cache_creation) = 160/(160+40) = 0.8
	if resp.CacheHitRate < 0.79 || resp.CacheHitRate > 0.81 {
		t.Errorf("expected cache_hit_rate ~0.8, got %f", resp.CacheHitRate)
	}
}

func TestDailyModelEndpoint(t *testing.T) {
	h, repo, cleanup := setupTestHandler(t)
	defer cleanup()

	insertTestRequest(t, repo, "claude-opus-4-6")

	req := httptest.NewRequest("GET", "/api/daily?range=all", nil)
	w := httptest.NewRecorder()
	h.DailyModel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PagedResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total < 1 {
		t.Errorf("expected total >= 1, got %d", resp.Total)
	}
	if resp.Page != 1 {
		t.Errorf("expected page=1, got %d", resp.Page)
	}
	if resp.PageSize != 20 {
		t.Errorf("expected page_size=20, got %d", resp.PageSize)
	}
	// data should be a non-empty slice
	dataSlice, ok := resp.Data.([]interface{})
	if !ok || len(dataSlice) == 0 {
		t.Errorf("expected non-empty data array, got %v", resp.Data)
	}
}

func TestDailyModelEndpointPagination(t *testing.T) {
	h, repo, cleanup := setupTestHandler(t)
	defer cleanup()

	insertTestRequest(t, repo, "claude-opus-4-6")

	req := httptest.NewRequest("GET", "/api/daily?range=all&page=1&page_size=1", nil)
	w := httptest.NewRecorder()
	h.DailyModel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PagedResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PageSize != 1 {
		t.Errorf("expected page_size=1, got %d", resp.PageSize)
	}
}

func TestRequestsEndpoint(t *testing.T) {
	h, repo, cleanup := setupTestHandler(t)
	defer cleanup()

	insertTestRequest(t, repo, "claude-opus-4-6")

	req := httptest.NewRequest("GET", "/api/requests?range=all", nil)
	w := httptest.NewRecorder()
	h.Requests(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PagedResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total < 1 {
		t.Errorf("expected total >= 1, got %d", resp.Total)
	}
	if resp.Page != 1 {
		t.Errorf("expected page=1, got %d", resp.Page)
	}

	dataSlice, ok := resp.Data.([]interface{})
	if !ok || len(dataSlice) == 0 {
		t.Fatalf("expected non-empty data array")
	}

	// Verify the first record has the correct model
	first, ok := dataSlice[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", dataSlice[0])
	}
	if first["model"] != "claude-opus-4-6" {
		t.Errorf("expected model=claude-opus-4-6, got %v", first["model"])
	}
	// Verify token values
	if int64(first["input_tokens"].(float64)) != 100 {
		t.Errorf("expected input_tokens=100, got %v", first["input_tokens"])
	}
	if int64(first["output_tokens"].(float64)) != 50 {
		t.Errorf("expected output_tokens=50, got %v", first["output_tokens"])
	}
}

func TestRequestsEndpointEmpty(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/requests?range=all", nil)
	w := httptest.NewRecorder()
	h.Requests(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PagedResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("expected total=0, got %d", resp.Total)
	}
	// data should be an empty array, not null
	dataSlice, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatalf("expected array, got %T", resp.Data)
	}
	if len(dataSlice) != 0 {
		t.Errorf("expected empty array, got %d items", len(dataSlice))
	}
}

func TestSessionsEndpoint(t *testing.T) {
	h, repo, cleanup := setupTestHandler(t)
	defer cleanup()

	insertTestRequest(t, repo, "claude-opus-4-6")

	req := httptest.NewRequest("GET", "/api/sessions?range=all", nil)
	w := httptest.NewRecorder()
	h.Sessions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PagedResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total < 1 {
		t.Errorf("expected total >= 1, got %d", resp.Total)
	}

	dataSlice, ok := resp.Data.([]interface{})
	if !ok || len(dataSlice) == 0 {
		t.Fatalf("expected non-empty data array")
	}

	first, ok := dataSlice[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", dataSlice[0])
	}
	if first["session_id"] != "sess-001" {
		t.Errorf("expected session_id=sess-001, got %v", first["session_id"])
	}
}

func TestSessionsEndpointEmpty(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/sessions?range=all", nil)
	w := httptest.NewRecorder()
	h.Sessions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PagedResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("expected total=0, got %d", resp.Total)
	}
}

func TestModelsEndpoint(t *testing.T) {
	h, repo, cleanup := setupTestHandler(t)
	defer cleanup()

	// Insert requests with two different models.
	insertTestRequest(t, repo, "claude-opus-4-6")

	// Insert a second request with a different model (need unique request_id or empty).
	_, err := repo.InsertRequest(context.Background(), &db.APIRequest{
		Timestamp:   time.Now(),
		SessionID:   "sess-002",
		UserID:      "user-002",
		Model:       "claude-sonnet-4-20250514",
		InputTokens: 200,
		OutputTokens: 100,
		CostUSD:     0.005,
	})
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/models", nil)
	w := httptest.NewRecorder()
	h.Models(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var models []string
	if err := json.NewDecoder(w.Body).Decode(&models); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %v", len(models), models)
	}

	// Models are returned ORDER BY model (alphabetical).
	found := map[string]bool{}
	for _, m := range models {
		found[m] = true
	}
	if !found["claude-opus-4-6"] {
		t.Error("missing model claude-opus-4-6")
	}
	if !found["claude-sonnet-4-20250514"] {
		t.Error("missing model claude-sonnet-4-20250514")
	}
}

func TestModelsEndpointEmpty(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/models", nil)
	w := httptest.NewRecorder()
	h.Models(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var models []string
	if err := json.NewDecoder(w.Body).Decode(&models); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected 0 models, got %d", len(models))
	}
}

func TestEventsSSE(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create a cancellable context so the SSE handler goroutine exits.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Events(w, req)
	}()

	// Give the handler a moment to write the initial "data: connected" line,
	// then cancel the context to terminate the handler.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	scanner := bufio.NewScanner(strings.NewReader(body))
	if !scanner.Scan() {
		t.Fatal("expected at least one line of SSE output")
	}
	firstLine := scanner.Text()
	if firstLine != "data: connected" {
		t.Errorf("expected first line 'data: connected', got %q", firstLine)
	}
}

func TestHealthEndpointWithDB(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	h.Health(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with valid DB, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status ok, got %s", resp["status"])
	}
}

func TestDashboardWithExplicitFromTo(t *testing.T) {
	h, repo, cleanup := setupTestHandler(t)
	defer cleanup()

	insertTestRequest(t, repo, "claude-opus-4-6")

	today := time.Now().Format("2006-01-02")
	req := httptest.NewRequest("GET", "/api/dashboard?from="+today+"&to="+today, nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp db.Dashboard
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RequestCount != 1 {
		t.Errorf("expected request_count=1, got %d", resp.RequestCount)
	}
}

func TestDashboardOutOfRange(t *testing.T) {
	h, repo, cleanup := setupTestHandler(t)
	defer cleanup()

	insertTestRequest(t, repo, "claude-opus-4-6")

	// Query a date range that excludes today's data.
	req := httptest.NewRequest("GET", "/api/dashboard?from=2000-01-01&to=2000-01-02", nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp db.Dashboard
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RequestCount != 0 {
		t.Errorf("expected 0 requests for out-of-range query, got %d", resp.RequestCount)
	}
}

func TestRequestsEndpointModelFilter(t *testing.T) {
	h, repo, cleanup := setupTestHandler(t)
	defer cleanup()

	insertTestRequest(t, repo, "claude-opus-4-6")
	_, err := repo.InsertRequest(context.Background(), &db.APIRequest{
		Timestamp:   time.Now(),
		SessionID:   "sess-filter",
		Model:       "claude-sonnet-4-20250514",
		InputTokens: 999,
		OutputTokens: 111,
		CostUSD:     0.05,
	})
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}

	// Filter by model=claude-opus-4-6 only.
	req := httptest.NewRequest("GET", "/api/requests?range=all&model=claude-opus-4-6", nil)
	w := httptest.NewRecorder()
	h.Requests(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PagedResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("expected total=1 for filtered model, got %d", resp.Total)
	}
}

func TestEventsSSEBrokerNotify(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Events(w, req)
	}()

	// Wait for initial connection, then push an update via the broker.
	time.Sleep(100 * time.Millisecond)
	h.broker.Notify()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	if !strings.Contains(body, "data: connected") {
		t.Error("expected 'data: connected' in SSE output")
	}
	if !strings.Contains(body, "data: update") {
		t.Error("expected 'data: update' in SSE output after Notify()")
	}
}
