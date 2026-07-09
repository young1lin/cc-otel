package pricing

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// orRoutingServer serves modelsPayload for the catalog path and endpointsPayload
// for any ".../endpoints" path, so one stub backs both fetches. modelsHits, when
// non-nil, tallies catalog-path hits so tests can assert caching behavior without
// the endpoints fetch skewing the count.
func orRoutingServer(t *testing.T, modelsPayload, endpointsPayload string, modelsHits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/endpoints") {
			fmt.Fprint(w, endpointsPayload)
			return
		}
		if modelsHits != nil {
			atomic.AddInt32(modelsHits, 1)
		}
		fmt.Fprint(w, modelsPayload)
	}))
}

// redirectBoth points the package URL vars at baseURL so both the catalog and
// endpoints fetches hit the test stub instead of the real OpenRouter. Restored
// automatically on test cleanup.
func redirectBoth(t *testing.T, baseURL string) {
	t.Helper()
	origM := openRouterModelsURL
	origE := openRouterEndpointsURLFmt
	openRouterModelsURL = baseURL
	openRouterEndpointsURLFmt = baseURL + "/models/%s/endpoints"
	t.Cleanup(func() {
		openRouterModelsURL = origM
		openRouterEndpointsURLFmt = origE
	})
}

const emptyEndpoints = `{"data":{"endpoints":[]}}`

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSuggestOpenRouter_ExactAndBasename(t *testing.T) {
	srv := orRoutingServer(t, `{"data":[
		{"id":"z-ai/glm-4.6","pricing":{"prompt":"0.0000006","completion":"0.0000022","input_cache_read":"0.00000006","input_cache_write":""}},
		{"id":"openai/gpt-4o","pricing":{"prompt":"0.0000025","completion":"0.00001","input_cache_read":"","input_cache_write":""}}
	]}`, emptyEndpoints, nil)
	defer srv.Close()
	redirectBoth(t, srv.URL)

	// exact
	got, err := SuggestOpenRouter(context.Background(), "z-ai/glm-4.6")
	if err != nil || !got.Matched || got.MatchedBy != "exact" {
		t.Fatalf("exact: %+v err=%v", got, err)
	}
	if !approx(got.Input, 0.6) || !approx(got.Output, 2.2) || !approx(got.CacheRead, 0.06) {
		t.Fatalf("exact values: %+v", got)
	}
	// No endpoints data => default stays the blended catalog price, no providers.
	if len(got.Providers) != 0 || got.ProvidersTotal != 0 {
		t.Fatalf("expected no providers, got %d (total %d)", len(got.Providers), got.ProvidersTotal)
	}

	// basename: bare "glm-4.6" -> z-ai/glm-4.6 (reuses the cached catalog, no refetch)
	got2, err := SuggestOpenRouter(context.Background(), "glm-4.6")
	if err != nil || got2.MatchedBy != "basename" || got2.Model != "z-ai/glm-4.6" {
		t.Fatalf("basename: %+v err=%v", got2, err)
	}
}

func TestSuggestOpenRouter_NoMatch(t *testing.T) {
	srv := orRoutingServer(t,
		`{"data":[{"id":"openai/gpt-4o","pricing":{"prompt":"0.0000025","completion":"0.00001"}}]}`,
		emptyEndpoints, nil)
	defer srv.Close()
	redirectBoth(t, srv.URL)

	got, err := SuggestOpenRouter(context.Background(), "does-not-exist-xyz")
	if err != nil || got.Matched {
		t.Fatalf("expected no match, got %+v err=%v", got, err)
	}
}

// TestSuggestOpenRouter_CachesCatalogWithinTTL verifies that rapid repeated
// lookups reuse one catalog fetch instead of re-downloading per click. The
// endpoints fetch has its own cache (keyed by URL) and is served from the same
// stub, so it does not skew the catalog-hit assertion.
func TestSuggestOpenRouter_CachesCatalogWithinTTL(t *testing.T) {
	var modelsHits int32
	srv := orRoutingServer(t,
		`{"data":[{"id":"openai/gpt-4o","pricing":{"prompt":"0.0000025","completion":"0.00001"}}]}`,
		emptyEndpoints, &modelsHits)
	defer srv.Close()
	redirectBoth(t, srv.URL)

	for i := 0; i < 3; i++ {
		got, err := SuggestOpenRouter(context.Background(), "gpt-4o")
		if err != nil || !got.Matched {
			t.Fatalf("call %d: %+v err=%v", i, got, err)
		}
	}
	if h := atomic.LoadInt32(&modelsHits); h != 1 {
		t.Fatalf("catalog should be fetched once within TTL, got %d catalog hits", h)
	}
}

// TestSuggestOpenRouter_ProvidersAndOfficialDefault is the core case: the
// default is the first-party Official price (Z.AI 1.4/4.4/0.26), not the blended
// promo, and the picker lists providers capped at 15 with Official promoted to
// the front.
func TestSuggestOpenRouter_ProvidersAndOfficialDefault(t *testing.T) {
	eps := []string{
		`{"provider_name":"Novita","tag":"novita/fp8","quantization":"fp8","pricing":{"prompt":"0.00000084","completion":"0.00000264","input_cache_read":"0.000000156","discount":0.4}}`,
		`{"provider_name":"Inceptron","tag":"inceptron/fp4","quantization":"fp4","pricing":{"prompt":"0.0000009","completion":"0.00000308","input_cache_read":"0.00000018","discount":0}}`,
		`{"provider_name":"Z.AI","tag":"z-ai/fp8","quantization":"fp8","pricing":{"prompt":"0.0000014","completion":"0.0000044","input_cache_read":"0.00000026","input_cache_write":"0.00000175","discount":0}}`,
	}
	for i := 0; i < 15; i++ {
		eps = append(eps, fmt.Sprintf(
			`{"provider_name":"Filler%d","tag":"filler%d/fp8","quantization":"fp8","pricing":{"prompt":"0.000002","completion":"0.000006","input_cache_read":"0.0000004","discount":0}}`,
			i, i))
	}
	endpointsPayload := `{"data":{"id":"z-ai/glm-5.2","endpoints":[` + strings.Join(eps, ",") + `]}}`
	// Blended catalog minimum = Novita's 40%-off promo.
	modelsPayload := `{"data":[{"id":"z-ai/glm-5.2","pricing":{"prompt":"0.00000084","completion":"0.00000264","input_cache_read":"0.000000156"}}]}`

	srv := orRoutingServer(t, modelsPayload, endpointsPayload, nil)
	defer srv.Close()
	redirectBoth(t, srv.URL)

	got, err := SuggestOpenRouter(context.Background(), "glm-5.2")
	if err != nil || !got.Matched {
		t.Fatalf("suggest: %+v err=%v", got, err)
	}
	// D1: default = first-party Z.AI official, NOT the blended Novita promo.
	if !approx(got.Input, 1.4) || !approx(got.Output, 4.4) || !approx(got.CacheRead, 0.26) {
		t.Fatalf("default should be Z.AI official 1.4/4.4/0.26, got %v/%v/%v", got.Input, got.Output, got.CacheRead)
	}
	// cache_create (input_cache_write) flows through the Official override.
	if !approx(got.CacheCreation, 1.75) {
		t.Fatalf("default cache_creation should be Z.AI 1.75, got %v", got.CacheCreation)
	}
	if got.ProvidersTotal != 18 {
		t.Fatalf("providers_total = %d, want 18", got.ProvidersTotal)
	}
	if len(got.Providers) != 18 {
		t.Fatalf("providers = %d, want all 18 (no cap)", len(got.Providers))
	}
	// Official is pinned first and matches the default price.
	first := got.Providers[0]
	if !first.Official || first.Provider != "Z.AI" || !approx(first.Input, 1.4) {
		t.Fatalf("first provider should be Z.AI official, got %+v", first)
	}
	// Novita's 40% discount and Inceptron's fp4 quant are surfaced.
	var novita, incep *ProviderSuggestion
	for i := range got.Providers {
		switch got.Providers[i].Provider {
		case "Novita":
			novita = &got.Providers[i]
		case "Inceptron":
			incep = &got.Providers[i]
		}
	}
	if novita == nil || !approx(novita.Discount, 0.4) || !approx(novita.Input, 0.84) {
		t.Fatalf("Novita not surfaced correctly: %+v", novita)
	}
	if incep == nil || quantLabel(incep.Quant) != "quantized" {
		t.Fatalf("Inceptron fp4 not labeled quantized: %+v", incep)
	}
}

// TestSuggestOpenRouter_EndpointsFailureFallsBackToBlended verifies D6: when the
// endpoints listing is unreachable, the default stays the blended catalog price
// and providers is empty. The lookup never returns an error.
func TestSuggestOpenRouter_EndpointsFailureFallsBackToBlended(t *testing.T) {
	modelsPayload := `{"data":[{"id":"z-ai/glm-5.2","pricing":{"prompt":"0.00000084","completion":"0.00000264","input_cache_read":"0.000000156"}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/endpoints") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, modelsPayload)
	}))
	defer srv.Close()
	redirectBoth(t, srv.URL)

	got, err := SuggestOpenRouter(context.Background(), "glm-5.2")
	if err != nil {
		t.Fatalf("suggest should not error on endpoints failure: %v", err)
	}
	if !got.Matched || !approx(got.Input, 0.84) {
		t.Fatalf("default should fall back to blended 0.84, got %+v", got)
	}
	if len(got.Providers) != 0 {
		t.Fatalf("providers should be empty on endpoints failure, got %d", len(got.Providers))
	}
}
