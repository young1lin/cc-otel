package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/pricing"
)

// pricingTestHandler builds a Handler with a real pricing.Registry pointed
// at the same SQLite DB the rest of the API uses, so /api/status and
// /api/pricing/lookup tests exercise the production code paths.
func pricingTestHandler(t *testing.T) *Handler {
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
