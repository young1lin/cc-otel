package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Mixed-data unification e2e (final user-directed state): every client —
// Codex included — is normalized into the unified pool, and the generic
// dashboard endpoints serve ALL of it. The /api/codex/* mirror endpoints
// stay per-source (service_name='codex') for deep links and scripts. Seed
// one row per service and assert BOTH directions: /api/codex/* sees only
// codex; /api/dashboard and /api/clients see all three.
func TestUnifiedPool_MixedDataUnification(t *testing.T) {
	h, repo := newCodexAPITestHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	day := time.Date(2026, 5, 12, 10, 0, 0, 0, time.Local)
	for _, svc := range []string{"claude-code", "codex", "opencode"} {
		if _, err := repo.DB().Exec(`INSERT INTO api_requests
			(timestamp, session_id, model, service_name, input_tokens, output_tokens, total_tokens, cost_usd, user_id)
			VALUES (?, ?, ?, ?, 100, 10, 110, 1.0, ?)`,
			day.Unix(), "sess-"+svc, "m-"+svc, svc, "user-"+svc); err != nil {
			t.Fatalf("seed %s: %v", svc, err)
		}
	}
	if err := repo.RebuildDailyAggregates(context.Background()); err != nil {
		t.Fatalf("rebuild agg: %v", err)
	}

	get := func(path string) map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: expected 200, got %d body=%s", path, rec.Code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("%s: bad json: %v", path, err)
		}
		return m
	}
	marshaled := func(m map[string]any) string {
		t.Helper()
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// Codex mirror endpoints must see ONLY the codex row.
	cd := get("/api/codex/dashboard?from=2026-05-12&to=2026-05-12")
	if got := int(cd["request_count"].(float64)); got != 1 {
		t.Fatalf("codex dashboard request_count = %d, want 1 (claude/opencode must not leak in)", got)
	}
	creq := get("/api/codex/requests?from=2026-05-12&to=2026-05-12")
	if body := marshaled(creq); strings.Count(body, "sess-") != 1 || !strings.Contains(body, "sess-codex") {
		t.Fatalf("codex requests must contain exactly the codex row; body=%s", body)
	}

	// All-Clients endpoints read the claude_pool views, which now mirror the
	// full unified pool: they must see all three services, Codex included
	// (the merge's whole point — one dashboard, Source column labels rows).
	dash := get("/api/dashboard?from=2026-05-12&to=2026-05-12")
	if got := int(dash["request_count"].(float64)); got != 3 {
		t.Fatalf("unified dashboard request_count = %d, want 3 (claude+opencode+codex)", got)
	}
	clients := get("/api/clients?from=2026-05-12&to=2026-05-12")
	cbody := marshaled(clients)
	for _, svc := range []string{"claude-code", "opencode", "codex"} {
		if !strings.Contains(cbody, svc) {
			t.Fatalf("clients endpoint missing service %q; body=%s", svc, cbody)
		}
	}
}
