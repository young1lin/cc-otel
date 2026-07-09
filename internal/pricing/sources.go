package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Source fetches per-model pricing from one upstream and returns it keyed by
// normalized model id. Implementations are expected to be cheap to construct
// (no network at construction; only Fetch makes calls).
type Source interface {
	Name() string

	// Priority decides which source wins when two sources return entries for
	// the same model id. Higher = stronger. LiteLLM (priority 10) outranks
	// OpenRouter (priority 5) because LiteLLM carries cache_* fields that
	// OpenRouter omits.
	Priority() int

	// Fetch reads the source. Implementations should respect ctx cancellation
	// and apply their own timeout via a context.WithTimeout wrapper if the
	// source's HTTP timeout doesn't already bound the call.
	Fetch(ctx context.Context) (map[string]Entry, error)
}

// allowedLiteLLMProviders mirrors tools/dump_pricing_snapshot — keep the
// runtime refresher and the offline snapshot picking from the same provider
// universe so seed and live entries don't silently disagree.
var allowedLiteLLMProviders = map[string]bool{
	"openai":       true,
	"openrouter":   true,
	"deepseek":     true,
	"fireworks_ai": true,
	"together_ai":  true,
	"groq":         true,
	"mistral":      true,
	"perplexity":   true,
	"xai":          true,
	"cohere_chat":  true,
	"cohere":       true,
	"moonshot":     true,
	"vertex_ai":    true,
	"gemini":       true,
}

var anthropicProviders = map[string]bool{
	"anthropic":                  true,
	"bedrock_converse":           true,
	"vertex_ai-anthropic_models": true,
}

// LiteLLMSource pulls the raw BerriAI/litellm prices file from GitHub raw.
// The file is ~11k entries; we filter to the providers we care about and
// drop every Anthropic/Claude entry on the way through.
type LiteLLMSource struct {
	URL     string
	Timeout time.Duration
	client  *http.Client
}

// NewLiteLLMSource returns a LiteLLM source pointing at BerriAI/main.
// Pass the empty url to use the default. Timeout 0 ⇒ 30s.
func NewLiteLLMSource(url string, timeout time.Duration) *LiteLLMSource {
	if url == "" {
		url = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &LiteLLMSource{URL: url, Timeout: timeout, client: &http.Client{Timeout: timeout}}
}

func (s *LiteLLMSource) Name() string  { return "litellm" }
func (s *LiteLLMSource) Priority() int { return 10 }

// litellmEntry matches the JSON shape; everything is optional because the
// upstream file is heterogeneous (chat / embedding / image / audio entries
// share the same map).
type litellmEntry struct {
	InputCostPerToken           *float64 `json:"input_cost_per_token,omitempty"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token,omitempty"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost,omitempty"`
	LiteLLMProvider             string   `json:"litellm_provider,omitempty"`
	Mode                        string   `json:"mode,omitempty"`
}

func (s *LiteLLMSource) Fetch(ctx context.Context) (map[string]Entry, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("litellm fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("litellm fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse litellm json: %w", err)
	}

	now := time.Now().Unix()
	out := make(map[string]Entry, 256)
	for name, payload := range raw {
		if name == "sample_spec" {
			continue
		}
		var le litellmEntry
		if err := json.Unmarshal(payload, &le); err != nil {
			continue
		}
		if anthropicProviders[le.LiteLLMProvider] {
			continue
		}
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "claude-") || strings.Contains(lower, "/claude-") || strings.Contains(lower, "anthropic.claude") {
			continue
		}
		if !allowedLiteLLMProviders[le.LiteLLMProvider] {
			continue
		}
		if le.Mode != "" && le.Mode != "chat" && le.Mode != "completion" && le.Mode != "responses" {
			continue
		}
		if le.InputCostPerToken == nil || le.OutputCostPerToken == nil ||
			*le.InputCostPerToken <= 0 || *le.OutputCostPerToken <= 0 {
			continue
		}
		key := Normalize(name)
		e := Entry{
			Model:     key,
			Input:     *le.InputCostPerToken,
			Output:    *le.OutputCostPerToken,
			Source:    s.Name(),
			FetchedAt: now,
			UpdatedAt: now,
		}
		if le.CacheReadInputTokenCost != nil {
			e.CacheRead = *le.CacheReadInputTokenCost
		}
		if le.CacheCreationInputTokenCost != nil {
			e.CacheCreation = *le.CacheCreationInputTokenCost
		}
		out[key] = e
	}
	return out, nil
}

// OpenRouterSource pulls https://openrouter.ai/api/v1/models. OpenRouter
// returns prices as decimal strings ("0.000003"), and they cover several
// providers that LiteLLM doesn't track on the same day — this source is
// kept around as a tiebreaker / patch source, not the primary feed.
type OpenRouterSource struct {
	URL     string
	Timeout time.Duration
	client  *http.Client
}

func NewOpenRouterSource(url string, timeout time.Duration) *OpenRouterSource {
	if url == "" {
		url = "https://openrouter.ai/api/v1/models"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &OpenRouterSource{URL: url, Timeout: timeout, client: &http.Client{Timeout: timeout}}
}

func (s *OpenRouterSource) Name() string  { return "openrouter" }
func (s *OpenRouterSource) Priority() int { return 5 }

type openrouterModel struct {
	ID      string `json:"id"`
	Pricing struct {
		Prompt       string `json:"prompt"`
		Completion   string `json:"completion"`
		InputCacheRd string `json:"input_cache_read"`
		InputCacheWr string `json:"input_cache_write"`
	} `json:"pricing"`
}

type openrouterResponse struct {
	Data []openrouterModel `json:"data"`
}

func (s *OpenRouterSource) Fetch(ctx context.Context) (map[string]Entry, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter fetch: HTTP %d", resp.StatusCode)
	}

	var or openrouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&or); err != nil {
		return nil, fmt.Errorf("parse openrouter json: %w", err)
	}

	now := time.Now().Unix()
	out := make(map[string]Entry, len(or.Data))
	var firstPartyIDs []string
	for _, m := range or.Data {
		if m.ID == "" {
			continue
		}
		lower := strings.ToLower(m.ID)
		// OpenRouter routes Anthropic models too — drop them so we don't
		// accidentally win the merge for a Claude id.
		if strings.HasPrefix(lower, "anthropic/") || strings.Contains(lower, "/claude-") {
			continue
		}
		input, errIn := parseORPrice(m.Pricing.Prompt)
		output, errOut := parseORPrice(m.Pricing.Completion)
		if errIn != nil || errOut != nil || input <= 0 || output <= 0 {
			continue
		}
		cacheRd, _ := parseORPrice(m.Pricing.InputCacheRd)
		cacheWr, _ := parseORPrice(m.Pricing.InputCacheWr)
		key := Normalize(m.ID)
		out[key] = Entry{
			Model:         key,
			Input:         input,
			Output:        output,
			CacheRead:     cacheRd,
			CacheCreation: cacheWr,
			Source:        s.Name(),
			FetchedAt:     now,
			UpdatedAt:     now,
		}
		if firstPartyPrefixes[ownerSlug(m.ID)] {
			firstPartyIDs = append(firstPartyIDs, m.ID)
		}
	}

	// Override the blended price with the first-party provider price for models
	// owned by a first-party provider (e.g. z-ai). The blended /api/v1/models
	// price is the cheapest provider, which is frequently a lower-precision
	// quantized variant (fp4) — a different product that is not price-comparable
	// to the first-party fp8 list price. The per-endpoint price from the owner
	// provider is the real list price, so prefer it.
	for _, id := range firstPartyIDs {
		if e, ok, err := s.firstPartyPrice(ctx, id); err == nil && ok {
			out[Normalize(id)] = e
		}
	}
	return out, nil
}

// parseORPrice handles the empty-string-as-no-data case OpenRouter uses for
// fields like input_cache_read on models that don't support caching.
func parseORPrice(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

// firstPartyPrefixes is the set of OpenRouter owner slugs (the id segment
// before '/') for which Fetch resolves the first-party provider price via
// /endpoints instead of the blended /api/v1/models price. Restricted to a
// small set to bound request volume — extend here when another provider's
// blended price is wrong (e.g. picking a quantized-discount reseller).
var firstPartyPrefixes = map[string]bool{
	"z-ai": true,
}

// ownerSlug returns the lower-cased owner segment of an OpenRouter model id
// (the part before the first '/'). "z-ai/glm-5.2" -> "z-ai".
func ownerSlug(modelID string) string {
	if i := strings.IndexByte(modelID, '/'); i >= 0 {
		return strings.ToLower(modelID[:i])
	}
	return strings.ToLower(modelID)
}

// alnumLower lowercases and strips to [a-z0-9] so that the provider name
// "Z.AI" and the owner slug "z-ai" both collapse to "zai" for comparison.
func alnumLower(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type orEndpoint struct {
	ProviderName string `json:"provider_name"`
	Pricing      struct {
		Prompt    string `json:"prompt"`
		Completion string `json:"completion"`
		InCacheRd string `json:"input_cache_read"`
	} `json:"pricing"`
}

// selectFirstPartyEndpoint returns the endpoint whose provider_name identifies
// the model's first-party/owner provider. ownerSlugNorm is the alnum-normalized
// owner slug (e.g. "zai"). Pure for testability.
func selectFirstPartyEndpoint(endpoints []orEndpoint, ownerSlugNorm string) (orEndpoint, bool) {
	for _, ep := range endpoints {
		if strings.Contains(alnumLower(ep.ProviderName), ownerSlugNorm) {
			return ep, true
		}
	}
	return orEndpoint{}, false
}

// firstPartyPrice fetches /api/v1/models/{id}/endpoints and returns the entry
// priced at the first-party (owner) provider. Returns ok=false (no error) when
// no first-party endpoint exists — the caller then keeps the blended price.
func (s *OpenRouterSource) firstPartyPrice(ctx context.Context, modelID string) (Entry, bool, error) {
	ownerNorm := alnumLower(ownerSlug(modelID))
	u := strings.TrimRight(s.URL, "/") + "/" + modelID + "/endpoints"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return Entry{}, false, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Entry{}, false, fmt.Errorf("openrouter endpoints fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Entry{}, false, fmt.Errorf("openrouter endpoints: HTTP %d", resp.StatusCode)
	}

	var body struct {
		Data struct {
			Endpoints []orEndpoint `json:"endpoints"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Entry{}, false, fmt.Errorf("parse openrouter endpoints: %w", err)
	}

	ep, ok := selectFirstPartyEndpoint(body.Data.Endpoints, ownerNorm)
	if !ok {
		return Entry{}, false, nil
	}
	input, errIn := strconv.ParseFloat(ep.Pricing.Prompt, 64)
	output, errOut := strconv.ParseFloat(ep.Pricing.Completion, 64)
	if errIn != nil || errOut != nil || input <= 0 || output <= 0 {
		return Entry{}, false, nil
	}
	cacheRd, _ := strconv.ParseFloat(ep.Pricing.InCacheRd, 64)
	now := time.Now().Unix()
	key := Normalize(modelID)
	return Entry{
		Model:     key,
		Input:     input,
		Output:    output,
		CacheRead: cacheRd,
		Source:    s.Name(),
		FetchedAt: now,
		UpdatedAt: now,
	}, true, nil
}
