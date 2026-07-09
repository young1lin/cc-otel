package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
