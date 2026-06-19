// dump_pricing_snapshot fetches the BerriAI/litellm model_prices JSON from
// GitHub raw, filters to providers cc-otel users actually see, and writes a
// trimmed snapshot to internal/pricing/embed/seed.json.
//
// Run before each release to refresh the embedded fallback so first-boot
// (or fully offline) installs have current prices. The runtime Refresher
// (Phase 3) will keep the SQLite table fresh after that.
//
// Anthropic / Claude entries are unconditionally dropped — Claude Code
// reports authoritative cost_usd and cc-otel never recomputes Claude.
//
// Hand-maintained entries for models NO upstream catalog carries (Xiaomi MiMo,
// StepFun, not-yet-listed DeepSeek V4) live in embed/manual_seed.json, which
// seed.go merges ON TOP of this file (manual wins on key conflict). This tool
// does NOT touch manual_seed.json — add such models there, never here, or the
// next run wipes them.
//
// Usage:
//
//	go run ./tools/dump_pricing_snapshot
//	# optional: -url, -out
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const defaultURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

const defaultOut = "internal/pricing/embed/seed.json"

// Provider whitelist: only providers cc-otel users plausibly hit. Bedrock
// and Azure are skipped on purpose — they pull in dozens of region-prefixed
// duplicates ("us.", "eu.", "global.", etc.) that bloat the embed without
// matching anything Claude Code or Codex actually sends as model_id.
//
// "openrouter" is the most useful catch-all — it carries GLM / Kimi /
// Qwen / DeepSeek and others under a single namespace.
var allowedProviders = map[string]bool{
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
	"vertex_ai":    true, // Gemini family
	"gemini":       true,
}

// Anthropic provider names — every shape that carries Claude entries we
// want to drop, regardless of whether the model id starts with "claude-".
var anthropicProviders = map[string]bool{
	"anthropic":                  true,
	"bedrock_converse":           true,
	"vertex_ai-anthropic_models": true,
}

type litellmEntry struct {
	InputCostPerToken           *float64 `json:"input_cost_per_token,omitempty"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token,omitempty"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost,omitempty"`
	LiteLLMProvider             string   `json:"litellm_provider,omitempty"`
	Mode                        string   `json:"mode,omitempty"`
}

type seedEntry struct {
	Model         string   `json:"model"`
	Input         float64  `json:"input"`
	Output        float64  `json:"output"`
	CacheRead     float64  `json:"cache_read,omitempty"`
	CacheCreation float64  `json:"cache_creation,omitempty"`
	Aliases       []string `json:"aliases,omitempty"`
}

type seedFile struct {
	Meta    map[string]string `json:"_meta"`
	Entries []seedEntry       `json:"entries"`
}

func main() {
	url := flag.String("url", defaultURL, "LiteLLM raw JSON URL")
	out := flag.String("out", defaultOut, "output path (relative to repo root)")
	flag.Parse()

	body, err := fetch(*url)
	if err != nil {
		log.Fatalf("fetch: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		log.Fatalf("parse top level: %v", err)
	}

	entries := make([]seedEntry, 0, 256)
	skipped := struct {
		anthropic    int
		nonChat      int
		noPricing    int
		notWhitelist int
		claudeName   int
	}{}

	for name, payload := range raw {
		if name == "sample_spec" {
			continue
		}
		var le litellmEntry
		if err := json.Unmarshal(payload, &le); err != nil {
			continue
		}

		if anthropicProviders[le.LiteLLMProvider] {
			skipped.anthropic++
			continue
		}
		// Defense in depth: drop anything whose name screams Claude even
		// if some other provider re-hosts it.
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "claude-") || strings.Contains(lower, "/claude-") || strings.Contains(lower, "anthropic.claude") {
			skipped.claudeName++
			continue
		}

		if !allowedProviders[le.LiteLLMProvider] {
			skipped.notWhitelist++
			continue
		}
		// Only chat / completion models — embeddings, image, audio entries
		// in this file have prices but cc-otel never sees them.
		if le.Mode != "" && le.Mode != "chat" && le.Mode != "completion" && le.Mode != "responses" {
			skipped.nonChat++
			continue
		}

		if le.InputCostPerToken == nil || le.OutputCostPerToken == nil ||
			*le.InputCostPerToken <= 0 || *le.OutputCostPerToken <= 0 {
			skipped.noPricing++
			continue
		}

		e := seedEntry{
			Model:  strings.ToLower(strings.TrimSpace(name)),
			Input:  *le.InputCostPerToken,
			Output: *le.OutputCostPerToken,
		}
		if le.CacheReadInputTokenCost != nil {
			e.CacheRead = *le.CacheReadInputTokenCost
		}
		if le.CacheCreationInputTokenCost != nil {
			e.CacheCreation = *le.CacheCreationInputTokenCost
		}
		entries = append(entries, e)
	}

	// Auto-aliases: many entries are namespaced like "openrouter/z-ai/glm-4.6"
	// or "deepseek/deepseek-v3.2", but the bare model name ("glm-4.6",
	// "deepseek-v3.2") is what reverse-proxied Claude Code / Codex traffic
	// actually carries. For each bare name, exactly one entry "wins" the
	// alias: shortest-path entry first, then provider preference, then
	// alphabetical for determinism. Losing entries get no alias.
	canonicalSet := map[string]bool{}
	for _, e := range entries {
		canonicalSet[e.Model] = true
	}
	candidatesByBare := map[string][]int{}
	for i, e := range entries {
		if !strings.Contains(e.Model, "/") {
			continue
		}
		parts := strings.Split(e.Model, "/")
		bare := parts[len(parts)-1]
		if bare == "" || bare == e.Model || canonicalSet[bare] {
			continue
		}
		candidatesByBare[bare] = append(candidatesByBare[bare], i)
	}
	for bare, idxs := range candidatesByBare {
		winner := idxs[0]
		for _, i := range idxs[1:] {
			if aliasRank(entries[i].Model) < aliasRank(entries[winner].Model) {
				winner = i
			}
		}
		entries[winner].Aliases = append(entries[winner].Aliases, bare)
		_ = bare
	}

	// Stable order — easier diffs in git history when the file is regenerated.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Model < entries[j].Model })

	sf := seedFile{
		Meta: map[string]string{
			"version":   time.Now().UTC().Format("2006-01-02"),
			"source":    *url,
			"generator": "tools/dump_pricing_snapshot",
			"currency":  "USD",
			"unit":      "per token",
			"note":      "Generated from BerriAI/litellm (MIT). Anthropic/Claude intentionally omitted — Claude Code reports authoritative cost_usd; cc-otel never recomputes Claude.",
		},
		Entries: entries,
	}
	pretty, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	pretty = append(pretty, '\n')

	if err := os.WriteFile(*out, pretty, 0644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}

	fmt.Printf("wrote %d entries to %s\n", len(entries), *out)
	fmt.Printf("skipped: anthropic=%d claude_name=%d non_chat=%d no_pricing=%d not_whitelisted=%d\n",
		skipped.anthropic, skipped.claudeName, skipped.nonChat, skipped.noPricing, skipped.notWhitelist)
}

// providerRank assigns a numeric score to a model id's leading provider
// segment so the alias-resolution tie-breaker is deterministic. Lower is
// better. The order reflects "most likely to be what users hit through a
// reverse proxy or direct API."
var providerRank = map[string]int{
	"openai":       0,
	"deepseek":     1,
	"moonshot":     2,
	"vertex_ai":    3,
	"openrouter":   4,
	"groq":         5,
	"mistral":      6,
	"perplexity":   7,
	"xai":          8,
	"together_ai":  9,
	"fireworks_ai": 10,
}

// aliasRank returns a sortable score for an entry id. Shorter paths beat
// longer paths; within the same length, providerRank wins; ties fall back
// to alphabetical (handled by caller via stable input order).
func aliasRank(model string) int {
	parts := strings.Split(model, "/")
	provider := ""
	if len(parts) > 0 {
		provider = parts[0]
	}
	pr, ok := providerRank[provider]
	if !ok {
		pr = 100
	}
	// Path length is the dominant factor: 100 per extra segment >> any provider delta.
	return len(parts)*100 + pr
}

func fetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
