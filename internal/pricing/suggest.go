package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Suggestion is the default OpenRouter-listed price for one model, expressed in
// USD/Mtok for the UI. It carries the price that a one-click 💡 fill puts into
// the manual-entry form: the first-party provider's price when OpenRouter lists
// one (see SuggestOpenRouter), otherwise the blended catalog minimum.
type Suggestion struct {
	Model         string  `json:"model"`      // matched OpenRouter id, e.g. "z-ai/glm-4.6"
	MatchedBy     string  `json:"matched_by"` // "exact" | "basename" | ""
	Matched       bool    `json:"matched"`
	Input         float64 `json:"input"`          // USD/Mtok
	Output        float64 `json:"output"`         // USD/Mtok
	CacheRead     float64 `json:"cache_read"`     // USD/Mtok (0 if absent)
	CacheCreation float64 `json:"cache_creation"` // USD/Mtok (0 if absent)
}

// ProviderSuggestion is one OpenRouter provider's price for a model, in USD/Mtok.
// The provider list lets the user pick a specific quote (official first-party, a
// discounted promo, or a quantized variant) instead of accepting the default.
type ProviderSuggestion struct {
	Provider      string  `json:"provider"`       // OpenRouter provider_name, e.g. "Z.AI", "Novita"
	Quant         string  `json:"quant"`          // raw quantization, e.g. "fp8", "fp4", "unknown"
	Discount      float64 `json:"discount"`       // fraction in [0,1): 0.4 == 40% off
	Official      bool    `json:"official"`       // first-party provider (tag matches the model's org)
	Input         float64 `json:"input"`          // USD/Mtok
	Output        float64 `json:"output"`         // USD/Mtok
	CacheRead     float64 `json:"cache_read"`     // USD/Mtok (0 if absent)
	CacheCreation float64 `json:"cache_creation"` // USD/Mtok (0 if absent)
}

// SuggestResult is what SuggestOpenRouter returns: the default Suggestion
// (one-click fill) plus the per-provider alternatives shown in the picker.
// Embedding Suggestion flattens its fields to the top level of the JSON, so the
// wire shape stays backward-compatible and gains "providers" + "providers_total".
type SuggestResult struct {
	Suggestion
	Providers      []ProviderSuggestion `json:"providers"`
	ProvidersTotal int                  `json:"providers_total"`
}

// openRouterModelsURL and openRouterEndpointsURLFmt are vars (not consts) so
// tests can redirect them to an httptest stub. The endpoints URL is a fmt
// format whose single %s is the matched OpenRouter model id.
var (
	openRouterModelsURL       = "https://openrouter.ai/api/v1/models"
	openRouterEndpointsURLFmt = "https://openrouter.ai/api/v1/models/%s/endpoints"

	// urlMu guards the two URL vars above. They are effectively const in
	// production (set once at init), but tests reassign them per-case via
	// redirectBoth, and WarmCatalogInBackground's fire-and-forget fetch reads
	// them from a goroutine that can outlive the test that spawned it. Every
	// read goes through modelsURL/endpointsURLFmt; every write (redirectBoth)
	// takes the lock — so a background fetch can't race with a reassignment.
	urlMu sync.RWMutex
)

// modelsURL / endpointsURLFmt read the redirectable URL vars under urlMu.
func modelsURL() string {
	urlMu.RLock()
	defer urlMu.RUnlock()
	return openRouterModelsURL
}

func endpointsURLFmt() string {
	urlMu.RLock()
	defer urlMu.RUnlock()
	return openRouterEndpointsURLFmt
}

const (
	openRouterTimeout = 15 * time.Second
	perMtok           = 1_000_000.0
	// catalogTTL bounds how long the (multi-MB) models listing — and each
	// model's endpoints listing — is reused before a refresh. Lookup is
	// user-initiated only, so ten minutes is plenty and stops a repeat click
	// (or re-opening the picker) from re-downloading.
	catalogTTL = 10 * time.Minute
)

type orModel struct {
	ID      string `json:"id"`
	Pricing struct {
		Prompt       string `json:"prompt"`
		Completion   string `json:"completion"`
		InputCacheRd string `json:"input_cache_read"`
		InputCacheWr string `json:"input_cache_write"`
	} `json:"pricing"`
}

type orResponse struct {
	Data []orModel `json:"data"`
}

// endpoint is one entry in OpenRouter's per-model endpoints listing.
type endpoint struct {
	ProviderName string `json:"provider_name"`
	Tag          string `json:"tag"`
	Quantization string `json:"quantization"`
	Pricing      struct {
		Prompt       string  `json:"prompt"`
		Completion   string  `json:"completion"`
		InputCacheRd string  `json:"input_cache_read"`
		InputCacheWr string  `json:"input_cache_write"`
		Discount     float64 `json:"discount"`
	} `json:"pricing"`
}

// endpointsResponse wraps the {"data":{"endpoints":[...]}} shape of the
// per-model endpoints API.
type endpointsResponse struct {
	Data struct {
		Endpoints []endpoint `json:"endpoints"`
	} `json:"data"`
}

// Catalog + endpoints caches, keyed by the (redirectable) request URL so tests
// that point the URL vars at a stub get their own entries and never see another
// test's data. The network fetch runs OUTSIDE the lock, so one slow OpenRouter
// round-trip can't block concurrent lookups; a simultaneous cache miss may
// double-fetch, which is harmless for manual use.
var (
	catalogMu    sync.Mutex
	catalogCache = map[string]catalogEntry{}

	endpointsMu    sync.Mutex
	endpointsCache = map[string]endpointsEntry{}
)

type catalogEntry struct {
	data []orModel
	at   time.Time
}

type endpointsEntry struct {
	data []endpoint
	at   time.Time
}

// fetchDecode GETs url and decodes the JSON body into a new T. Shared by the
// catalog and endpoints fetches.
func fetchDecode[T any](ctx context.Context, url string) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero, err
	}
	client := &http.Client{Timeout: openRouterTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("openrouter fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("openrouter fetch: HTTP %d", resp.StatusCode)
	}
	var body T
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return zero, fmt.Errorf("parse openrouter: %w", err)
	}
	return body, nil
}

// fetchCatalog returns OpenRouter's model listing, reusing a cached copy for
// catalogTTL. On a miss it fetches and parses the full catalog once, then stores
// it for subsequent lookups (any model, within the TTL window).
func fetchCatalog(ctx context.Context) ([]orModel, error) {
	url := modelsURL()

	catalogMu.Lock()
	if e, ok := catalogCache[url]; ok && time.Since(e.at) < catalogTTL {
		data := e.data
		catalogMu.Unlock()
		return data, nil
	}
	catalogMu.Unlock()

	body, err := fetchDecode[orResponse](ctx, url)
	if err != nil {
		return nil, err
	}
	data := body.Data

	catalogMu.Lock()
	catalogCache[url] = catalogEntry{data: data, at: time.Now()}
	catalogMu.Unlock()
	return data, nil
}

// fetchEndpoints returns OpenRouter's per-provider listing for one model id,
// reusing a cached copy for catalogTTL. On error the caller degrades silently to
// the blended catalog default — the UI never errors on a suggest lookup.
func fetchEndpoints(ctx context.Context, id string) ([]endpoint, error) {
	url := fmt.Sprintf(endpointsURLFmt(), id)

	endpointsMu.Lock()
	if e, ok := endpointsCache[url]; ok && time.Since(e.at) < catalogTTL {
		data := e.data
		endpointsMu.Unlock()
		return data, nil
	}
	endpointsMu.Unlock()

	body, err := fetchDecode[endpointsResponse](ctx, url)
	if err != nil {
		return nil, err
	}
	data := body.Data.Endpoints

	endpointsMu.Lock()
	endpointsCache[url] = endpointsEntry{data: data, at: time.Now()}
	endpointsMu.Unlock()
	return data, nil
}

// CachedCatalogPrice returns the cached OpenRouter catalog price for a model
// (matched by exact id or basename) in USD/Mtok. It reads the in-memory cache
// only — never the network — so it is safe to call from List. ok is false when
// the catalog is cold or the model is absent. A stale cached price is returned
// in preference to none.
// looseForm normalizes a model id for fuzzy Claude matching against the
// OpenRouter catalog: lowercase, dots→dashes, and strip a trailing date suffix
// (reusing dateSuffixRe from match.go). Telemetry reports
// "claude-opus-4-6-20251001" while OpenRouter lists "claude-opus-4.6"; both
// collapse to "claude-opus-4-6".
func looseForm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, ".", "-")
	return dateSuffixRe.ReplaceAllString(s, "")
}

func CachedCatalogPrice(model string) (in, out, cacheRead float64, ok bool) {
	q := looseForm(model)
	if q == "" {
		return 0, 0, 0, false
	}
	url := modelsURL()
	catalogMu.Lock()
	e, hit := catalogCache[url]
	catalogMu.Unlock()
	if !hit || len(e.data) == 0 {
		return 0, 0, 0, false
	}
	var m *orModel
	for i := range e.data {
		if looseForm(e.data[i].ID) == q {
			m = &e.data[i]
			break
		}
		if m == nil && looseForm(basenameOf(e.data[i].ID)) == q {
			m = &e.data[i]
		}
	}
	if m == nil {
		return 0, 0, 0, false
	}
	in, _ = strconv.ParseFloat(m.Pricing.Prompt, 64)
	out, _ = strconv.ParseFloat(m.Pricing.Completion, 64)
	cacheRead, _ = strconv.ParseFloat(m.Pricing.InputCacheRd, 64)
	return in * perMtok, out * perMtok, cacheRead * perMtok, true
}

// WarmCatalogInBackground fetches the OpenRouter catalog asynchronously when it
// is not yet cached, so the next read-only price lookup (e.g. Claude rows in the
// pricing table) hits the cache. No-op when already warm.
func WarmCatalogInBackground() {
	url := modelsURL()
	catalogMu.Lock()
	warm := false
	if e, ok := catalogCache[url]; ok && len(e.data) > 0 {
		warm = true
	}
	catalogMu.Unlock()
	if warm {
		return
	}
	go func() { _, _ = fetchCatalog(context.Background()) }()
}

// EnsureCatalogWarm blocks until the OpenRouter catalog is cached, fetching it
// if cold. Called by the pricing-table handler so Claude reference prices are
// populated on the same load instead of appearing blank until a reload. A fetch
// error or ctx cancellation leaves the cache cold; the caller proceeds and
// Claude rows simply show no price (List schedules a background retry).
func EnsureCatalogWarm(ctx context.Context) {
	url := modelsURL()
	catalogMu.Lock()
	warm := false
	if e, ok := catalogCache[url]; ok && len(e.data) > 0 {
		warm = true
	}
	catalogMu.Unlock()
	if warm {
		return
	}
	_, _ = fetchCatalog(ctx)
}

// SuggestOpenRouter queries OpenRouter for one model and returns the default
// one-click price plus the per-provider alternatives shown in the picker.
//
// The default is the first-party ("Official") provider's price when OpenRouter
// lists one (e.g. the Z.AI endpoint for z-ai/glm-5.2 → 1.4/4.4/0.26); otherwise
// it falls back to the blended catalog minimum.
//
// User-initiated only — never on a timer. Both the models catalog and each
// model's endpoints listing are cached for catalogTTL, so a rapid double-click
// reuses one fetch. Prices convert from OpenRouter's per-token strings to
// USD/Mtok. A non-match returns a zero-valued SuggestResult (Matched=false).
func SuggestOpenRouter(ctx context.Context, model string) (SuggestResult, error) {
	q := Normalize(model)
	if q == "" {
		return SuggestResult{}, nil
	}

	data, err := fetchCatalog(ctx)
	if err != nil {
		return SuggestResult{}, err
	}

	// Pass 1: exact normalized id. Pass 2: basename (last segment).
	var matched *orModel
	var how string
	var basenameHit *orModel
	for i := range data {
		m := &data[i]
		if Normalize(m.ID) == q {
			matched, how = m, "exact"
			break
		}
		if basenameHit == nil && basenameOf(m.ID) == q {
			basenameHit = m
		}
	}
	if matched == nil && basenameHit != nil {
		matched, how = basenameHit, "basename"
	}
	if matched == nil {
		return SuggestResult{}, nil
	}

	// Default starts as the blended catalog price; the endpoints listing may
	// override it with the first-party Official price below.
	res := SuggestResult{Suggestion: orToSuggestion(matched, how)}

	eps, err := fetchEndpoints(ctx, matched.ID)
	if err != nil || len(eps) == 0 {
		return res, nil // degrade silently to the blended default
	}

	org := orgOf(matched.ID)
	all := make([]ProviderSuggestion, 0, len(eps))
	for i := range eps {
		ps := endpointToProvider(&eps[i])
		ps.Official = isOfficialTag(eps[i].Tag, org) || isOfficialProvider(eps[i].ProviderName, org)
		all = append(all, ps)
	}

	// D1: the default is the first Official provider's price, if any.
	for _, ps := range all {
		if ps.Official {
			res.Suggestion = Suggestion{
				Model:         matched.ID,
				MatchedBy:     how,
				Matched:       true,
				Input:         ps.Input,
				Output:        ps.Output,
				CacheRead:     ps.CacheRead,
				CacheCreation: ps.CacheCreation,
			}
			break
		}
	}

	// No cap. Official (first-party) is pinned first so the canonical price is
	// always at the top; the rest keep OpenRouter's order.
	res.ProvidersTotal = len(all)
	res.Providers = orderProvidersOfficialFirst(all)
	return res, nil
}

// orderProvidersOfficialFirst returns every provider with the Official
// (first-party) entry/entries moved to the front; the rest keep OpenRouter's
// order. No cap — all providers are returned.
func orderProvidersOfficialFirst(all []ProviderSuggestion) []ProviderSuggestion {
	out := make([]ProviderSuggestion, 0, len(all))
	rest := make([]ProviderSuggestion, 0, len(all))
	for _, p := range all {
		if p.Official {
			out = append(out, p)
		} else {
			rest = append(rest, p)
		}
	}
	return append(out, rest...)
}

func basenameOf(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return Normalize(id[i+1:])
	}
	return Normalize(id)
}

// orgOf returns the provider-org segment of an OpenRouter id
// ("z-ai/glm-5.2" -> "z-ai"); "" for a bare id.
func orgOf(id string) string {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		return Normalize(id[:i])
	}
	return ""
}

// isOfficialTag reports whether an endpoint tag identifies the model's own org
// (org "z-ai" matches tag "z-ai" or "z-ai/fp8").
func isOfficialTag(tag, org string) bool {
	if org == "" {
		return false
	}
	t := Normalize(tag)
	return t == org || strings.HasPrefix(t, org+"/")
}

// isOfficialProvider matches a provider display name to the org, tolerating the
// punctuation differences between "z-ai" and "Z.AI".
func isOfficialProvider(name, org string) bool {
	if org == "" {
		return false
	}
	return stripSep(name) == stripSep(org)
}

// stripSep lowercases and removes -, _, ., and space so "Z.AI" and "z-ai" both
// collapse to "zai".
func stripSep(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '-', '_', '.', ' ':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// quantLabel returns "quantized" for aggressive low-bit quants (fp4/int4/...),
// "" for full or unknown precision. Used only for the UI badge; it never affects
// matching or the default.
func quantLabel(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" || q == "unknown" || q == "none" {
		return ""
	}
	for _, p := range []string{"fp4", "fp6", "int4", "int3", "int2", "nf4", "q4", "q3", "q2"} {
		if strings.HasPrefix(q, p) {
			return "quantized"
		}
	}
	return ""
}

// endpointToProvider converts one OpenRouter endpoint into a USD/Mtok ProviderSuggestion.
func endpointToProvider(e *endpoint) ProviderSuggestion {
	in, _ := strconv.ParseFloat(e.Pricing.Prompt, 64)
	out, _ := strconv.ParseFloat(e.Pricing.Completion, 64)
	cr, _ := strconv.ParseFloat(e.Pricing.InputCacheRd, 64)
	cc, _ := strconv.ParseFloat(e.Pricing.InputCacheWr, 64)
	return ProviderSuggestion{
		Provider:      e.ProviderName,
		Quant:         e.Quantization,
		Discount:      e.Pricing.Discount,
		Input:         in * perMtok,
		Output:        out * perMtok,
		CacheRead:     cr * perMtok,
		CacheCreation: cc * perMtok,
	}
}

func orToSuggestion(m *orModel, how string) Suggestion {
	in, _ := strconv.ParseFloat(m.Pricing.Prompt, 64)
	out, _ := strconv.ParseFloat(m.Pricing.Completion, 64)
	cr, _ := strconv.ParseFloat(m.Pricing.InputCacheRd, 64)
	cc, _ := strconv.ParseFloat(m.Pricing.InputCacheWr, 64)
	return Suggestion{
		Model:         m.ID,
		MatchedBy:     how,
		Matched:       true,
		Input:         in * perMtok,
		Output:        out * perMtok,
		CacheRead:     cr * perMtok,
		CacheCreation: cc * perMtok,
	}
}
