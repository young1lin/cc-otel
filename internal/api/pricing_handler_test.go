package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/pricing"
)

// newTestHandler builds a Handler backed by a temp SQLite DB with a real
// pricing.Registry wired via SetPricer, so /api/status, /api/pricing/lookup,
// and the pricing CRUD endpoints exercise the production code paths. Callers
// that need the Writer side can grab it via h.Pricer().(pricing.Writer).
func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	h, _, cleanup := setupTestHandler(t)
	t.Cleanup(cleanup)
	reg, err := pricing.NewRegistry(context.Background(), h.repo.DB(), &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	h.SetPricer(reg)
	return h
}

// pricingTestHandler is retained for the pre-existing lookup/status tests;
// it just delegates to newTestHandler so both names share one setup path.
func pricingTestHandler(t *testing.T) *Handler {
	t.Helper()
	return newTestHandler(t)
}

// Pricer is a test-only accessor exposing the wired pricing registry so tests
// can type-assert it to pricing.Writer for the CRUD endpoints. Defined here
// (not in handler.go) on purpose — it keeps the production Handler API clean.
func (h *Handler) Pricer() pricing.Registry { return h.pricer }

// doReq dispatches a request through a real mux built from h.Register, so the
// test exercises the actual route table (path → handler) plus the handler
// itself. body may be nil for GET/DELETE.
func doReq(t *testing.T, h *Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	req := httptest.NewRequest(method, target, body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestStatus_PricingBlockPresentWhenPricerWired(t *testing.T) {
	h := pricingTestHandler(t)

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	h.Status(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Pricing == nil {
		t.Fatal("pricing block should be present when pricer is wired")
	}
	if resp.Pricing.TableSize == 0 {
		t.Error("seed should have populated model_pricing; table_size is 0")
	}
}

func TestStatus_PricingBlockOmittedWhenNoPricer(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	h.Status(w, req)

	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Pricing != nil {
		t.Error("pricing block should be nil when no pricer is configured")
	}
}

func TestPricingLookup_KnownModel(t *testing.T) {
	h := pricingTestHandler(t)

	req := httptest.NewRequest("GET", "/api/pricing/lookup?model=gpt-5", nil)
	w := httptest.NewRecorder()
	h.PricingLookup(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var resp PricingLookupResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Found {
		t.Error("gpt-5 should resolve")
	}
	if resp.Kind != "exact" {
		t.Errorf("kind = %q, want exact", resp.Kind)
	}
	if resp.IsClaude {
		t.Error("gpt-5 must not be flagged as claude")
	}
	if resp.Input <= 0 || resp.Output <= 0 {
		t.Errorf("expected positive prices, got input=%v output=%v", resp.Input, resp.Output)
	}
}

func TestPricingLookup_ClaudeReturnsMissWithFlag(t *testing.T) {
	h := pricingTestHandler(t)

	req := httptest.NewRequest("GET", "/api/pricing/lookup?model=claude-sonnet-4-5", nil)
	w := httptest.NewRecorder()
	h.PricingLookup(w, req)

	var resp PricingLookupResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Found {
		t.Error("Claude must always be a miss in the registry")
	}
	if !resp.IsClaude {
		t.Error("is_claude flag should be true so callers know why it missed")
	}
}

func TestPricingLookup_AliasResolves(t *testing.T) {
	h := pricingTestHandler(t)

	req := httptest.NewRequest("GET", "/api/pricing/lookup?model=glm-4.6", nil)
	w := httptest.NewRecorder()
	h.PricingLookup(w, req)

	var resp PricingLookupResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Found {
		t.Fatal("glm-4.6 should resolve via alias")
	}
	if resp.Kind != "alias" {
		t.Errorf("kind = %q, want alias", resp.Kind)
	}
}

func TestPricingLookup_MissingParam(t *testing.T) {
	h := pricingTestHandler(t)

	req := httptest.NewRequest("GET", "/api/pricing/lookup", nil)
	w := httptest.NewRecorder()
	h.PricingLookup(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing model param, got %d", w.Code)
	}
}

func TestPricingLookup_NoPricerReturns503(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/pricing/lookup?model=gpt-5", nil)
	w := httptest.NewRecorder()
	h.PricingLookup(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when no pricer wired, got %d", w.Code)
	}
}

// TestPricingCollection_NoWriterReturns503 verifies the collection endpoint
// refuses CRUD when no pricing.Writer has been injected.
func TestPricingCollection_NoWriterReturns503(t *testing.T) {
	h := newTestHandler(t)
	// Deliberately do NOT call SetPricingWriter.
	rec := doReq(t, h, "GET", "/api/pricing", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no writer wired, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestPricingCollection_CRUD(t *testing.T) {
	h := newTestHandler(t)
	h.SetPricingWriter(h.Pricer().(pricing.Writer))

	// POST: USD/Mtok on the wire (input=1, output=2) → /1e6 stored as USD/token.
	body := strings.NewReader(`{"model":"test-x","input":1,"output":2,"cache_read":0.1,"cache_create":0.5}`)
	rec := doReq(t, h, "POST", "/api/pricing", body)
	if rec.Code != 200 {
		t.Fatalf("POST status %d: %s", rec.Code, rec.Body.String())
	}

	// GET list finds it; wire value is USD/Mtok so input should echo back as 1.
	rec = doReq(t, h, "GET", "/api/pricing?q=test-x", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "test-x") {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"input":1`) {
		t.Fatalf("expected USD/Mtok input=1 on the wire, got %s", rec.Body.String())
	}

	// DELETE removes the row (204 No Content).
	rec = doReq(t, h, "DELETE", "/api/pricing?model=test-x", nil)
	if rec.Code != 204 {
		t.Fatalf("DELETE status %d: %s", rec.Code, rec.Body.String())
	}

	// A follow-up GET no longer lists the deleted model.
	rec = doReq(t, h, "GET", "/api/pricing?q=test-x", nil)
	if rec.Code != 200 {
		t.Fatalf("GET after delete: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "test-x") {
		t.Fatalf("test-x should be gone after DELETE, got %s", rec.Body.String())
	}
}

func TestPricingCollection_PageSizeClamped(t *testing.T) {
	h := newTestHandler(t)
	h.SetPricingWriter(h.Pricer().(pricing.Writer))

	// An adversarial page_size should not blow up the (page-1)*pageSize math;
	// the handler clamps it to [1,200]. This request must just return 200 with
	// a bounded page rather than erroring or returning a giant page.
	rec := doReq(t, h, "GET", "/api/pricing?page=1&page_size=999999999", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200 with clamped page_size, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestPricing_POST_RejectsClaude(t *testing.T) {
	h := newTestHandler(t)
	h.SetPricingWriter(h.Pricer().(pricing.Writer))

	rec := doReq(t, h, "POST", "/api/pricing", strings.NewReader(`{"model":"claude-x","input":1,"output":1}`))
	if rec.Code != 400 {
		t.Fatalf("expected 400 for claude write, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestPricingSuggest_RequiresModel(t *testing.T) {
	h := newTestHandler(t)
	rec := doReq(t, h, "GET", "/api/pricing/suggest", nil)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for missing model, got %d", rec.Code)
	}
}

// TestPricingRecompute_PostStartsThenGetStatus starts a recompute via POST,
// then polls GET until the job reports running:false (i.e. it completed).
// We intentionally do NOT assert the POST body's running flag: on an empty
// test DB recompute.Run finishes in microseconds, so the goroutine can flip
// running to false before the POST handler takes its response snapshot —
// making a "running:true" assertion flaky. The robust contract is "POST
// returns 200, and a subsequent GET eventually observes completion".
func TestPricingRecompute_PostStartsThenGetStatus(t *testing.T) {
	h := newTestHandler(t)
	h.SetPricingWriter(h.Pricer().(pricing.Writer))

	rec := doReq(t, h, "POST", "/api/pricing/recompute", nil)
	if rec.Code != 200 {
		t.Fatalf("start POST: %d %s", rec.Code, rec.Body.String())
	}

	// Poll GET until the job reports finished (running:false). On the empty
	// in-mem DB this is near-instant; the 5s deadline is just a safety net.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g := doReq(t, h, "GET", "/api/pricing/recompute", nil)
		if g.Code != 200 {
			t.Fatalf("recompute GET: %d %s", g.Code, g.Body.String())
		}
		if strings.Contains(g.Body.String(), `"running":false`) {
			return // job ran and completed
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("recompute never reported running:false within deadline")
}

// TestPricingRecompute_SecondPostWhileRunningIsNoOp only asserts the second
// POST returns 200 (not an error). Whether it's a true no-op (first job still
// running) or a fresh start (first job already finished on the empty DB) is
// timing-dependent and not asserted here — per the task brief, only the
// status code is stable.
func TestPricingRecompute_SecondPostWhileRunningIsNoOp(t *testing.T) {
	h := newTestHandler(t)
	h.SetPricingWriter(h.Pricer().(pricing.Writer))
	h.SetShutdownContext(context.Background())

	doReq(t, h, "POST", "/api/pricing/recompute", nil)
	rec := doReq(t, h, "POST", "/api/pricing/recompute", nil)
	if rec.Code != 200 {
		t.Fatalf("second POST should return 200 (no-op or fresh start), got %d %s", rec.Code, rec.Body.String())
	}

	// Poll GET until the background goroutine reports running:false. Without
	// this wait, t.Cleanup can close the temp DB while runRecompute is still
	// inside recompute.Run — the one flake vector on this test. On the empty
	// in-mem DB this is near-instant; the 2s deadline is a safety net.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g := doReq(t, h, "GET", "/api/pricing/recompute", nil)
		if g.Code != 200 {
			t.Fatalf("recompute GET: %d %s", g.Code, g.Body.String())
		}
		if strings.Contains(g.Body.String(), `"running":false`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("recompute never reported running:false within deadline")
}

// TestPricing_OverriddenByYamlNormalized verifies the overridden_by_yaml badge
// compares the DB row's normalized model id against the YAML pricing keys after
// those keys are normalized. A YAML key written as "GLM-4.6" (case-preserving)
// must still flag a DB row stored as "glm-4.6". This mirrors how the registry's
// loadUserOverrides normalizes both sides — the old raw h.cfg.Pricing[e.Model]
// lookup missed any casing difference.
func TestPricing_OverriddenByYamlNormalized(t *testing.T) {
	h := newTestHandler(t)
	h.SetPricingWriter(h.Pricer().(pricing.Writer))

	// Inject a non-normalized YAML pricing key. The registry is untouched;
	// only the handler's config (the source of the badge) is overridden.
	h.cfg = &config.Config{Pricing: map[string]config.PriceEntry{
		"GLM-4.6": {Input: 1e-6, Output: 1e-6},
	}}

	// Upsert a glm-4.6 row so the DB stores it under the normalized key.
	rec := doReq(t, h, "POST", "/api/pricing", strings.NewReader(`{"model":"glm-4.6","input":1,"output":2}`))
	if rec.Code != 200 {
		t.Fatalf("POST glm-4.6: %d %s", rec.Code, rec.Body.String())
	}

	// Listing must badge the row as overridden_by_yaml:true, proving the
	// normalized YAML key "GLM-4.6" matched the DB row "glm-4.6".
	rec = doReq(t, h, "GET", "/api/pricing?q=glm-4.6", nil)
	if rec.Code != 200 {
		t.Fatalf("GET glm-4.6: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"overridden_by_yaml":true`) {
		t.Fatalf("expected overridden_by_yaml:true for normalized match, got %s", rec.Body.String())
	}
}
