package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
)

func newCodexAPITestHandler(t *testing.T) (*Handler, *db.Repository) {
	t.Helper()
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	repo := db.NewRepository(d)
	return NewHandler(repo, NewBroker(), &config.Config{}, ""), repo
}

func TestCodexRoutes_DashboardEmpty(t *testing.T) {
	h, _ := newCodexAPITestHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest("GET", "/api/codex/dashboard?from=2026-01-01&to=2026-01-31", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request_count") {
		t.Errorf("body missing request_count: %s", rec.Body.String())
	}
}

func TestCodexRoutes_AllReturn200(t *testing.T) {
	h, _ := newCodexAPITestHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	for _, path := range []string{
		"/api/codex/dashboard?from=2026-01-01&to=2026-01-31",
		"/api/codex/daily?from=2026-01-01&to=2026-01-31",
		"/api/codex/requests?from=2026-01-01&to=2026-01-31",
		"/api/codex/sessions?from=2026-01-01&to=2026-01-31",
		"/api/codex/durations?from=2026-01-01&to=2026-01-31",
		"/api/codex/models",
	} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("%s: expected 200, got %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestCodexCalendarEndpointAggregatesDays(t *testing.T) {
	h, repo := newCodexAPITestHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	day := time.Date(2026, 5, 9, 10, 0, 0, 0, time.Local)
	for _, req := range []db.CodexAPIRequest{
		{Timestamp: day, SessionID: "codex-api-cal-1", Model: "gpt-5.5", InputTokens: 100, OutputTokens: 50, CacheReadTokens: 20, TotalTokens: 150, CostUSD: 0.10},
		{Timestamp: day.Add(time.Hour), SessionID: "codex-api-cal-2", Model: "gpt-5.5", InputTokens: 200, OutputTokens: 20, CacheReadTokens: 10, TotalTokens: 220, CostUSD: 0.20},
		{Timestamp: day.Add(2 * time.Hour), SessionID: "codex-api-cal-3", Model: "gpt-5.4-mini", InputTokens: 30, OutputTokens: 10, TotalTokens: 40, CostUSD: 0.01},
	} {
		if _, err := repo.InsertCodexAPIRequest(context.Background(), &req); err != nil {
			t.Fatalf("InsertCodexAPIRequest: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/codex/calendar?from=2026-05-09&to=2026-05-09", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []db.CalendarDay `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 day, got %d: %+v", len(resp.Data), resp.Data)
	}
	if resp.Data[0].TopModel != "gpt-5.5" || resp.Data[0].RequestCount != 3 {
		t.Fatalf("unexpected codex calendar row: %+v", resp.Data[0])
	}
}
