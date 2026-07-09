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
