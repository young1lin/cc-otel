package pricing

import (
	"regexp"
	"sort"
	"strings"
)

// IsClaudeModel reports whether the (case-insensitive, trimmed) model name
// belongs to Anthropic's Claude family. Claude models are NEVER recomputed
// locally — Claude Code already reports an authoritative cost_usd.
//
// Examples that return true:
//
//	"claude-sonnet-4-5"
//	"Claude-Opus-4-1-20250805"   (mixed case, with date suffix)
//	"  claude-haiku-4-5  "       (whitespace)
//
// Examples that return false:
//
//	""                 — empty
//	"claude"           — no trailing dash, not a real model id
//	"glm-4.6"          — non-Anthropic
//	"gpt-5-codex"      — non-Anthropic
func IsClaudeModel(model string) bool {
	return strings.HasPrefix(Normalize(model), "claude-")
}

// ClaudeCodeService is the OTLP resource service.name reported by Claude Code,
// and the value cc-otel attributes to rows that arrive without one.
const ClaudeCodeService = "claude-code"

// CursorService is the service_name stamped on rows imported from Cursor's
// usage-events CSV exports (internal/cursorimport).
const CursorService = "cursor"

// IsUpstreamAuthoritative reports whether a row's upstream-reported cost_usd
// should be trusted as-is instead of recomputed from the local price table.
//
// The authoritative combinations, and only these:
//
//   - Claude Code on a Claude model — Anthropic computes cost_usd from canonical
//     prices and Claude Code forwards it. Recomputing could only make it worse.
//   - Claude Code on a non-Claude model — GLM/DeepSeek/Kimi behind an
//     Anthropic-compatible reverse proxy get priced by Anthropic's rates, which
//     are wrong for them. Recompute.
//   - Another client (opencode, ...) on a Claude model — no cost is reported at
//     all, so trusting upstream pins the row at $0 forever. Recompute, which
//     requires a Claude row in the price table (deliberately not seeded).
//   - Cursor (any model) — NOT authoritative since 2026-11-08 (user
//     directive): plan-included rows report $0 because the plan pool absorbed
//     them, not because the usage was free, so the dashboard shows list-price
//     estimates instead (importer computes them at import time; recompute
//     backfills). The one exception lives at the call sites, not here: a
//     cursor row whose CSV cost is POSITIVE (User API Key billing) is real
//     money Cursor actually charged and keeps its upstream value — see
//     cursorimport.Import and the recompute cursor guard.
func IsUpstreamAuthoritative(serviceName, model string) bool {
	return strings.EqualFold(strings.TrimSpace(serviceName), ClaudeCodeService) && IsClaudeModel(model)
}

// dateSuffixRe matches a trailing "-YYYYMMDD" date stamp Anthropic / others
// append to model ids. It's intentionally strict (8 digits, anchored).
var dateSuffixRe = regexp.MustCompile(`-\d{8}$`)

// trailingTagRe matches version/preview tags we strip when looking up a
// stable family entry. Conservative list — only well-known tags.
var trailingTagRe = regexp.MustCompile(`-(?:preview|latest|exp|beta|alpha)$`)

// stripVariants returns the input followed by progressively-stripped
// variants (date suffix, then preview/latest/etc, then both), in
// most-specific-first order. Both regexes are anchored to the end of the
// string, so "name-20250805-preview" must be tag-stripped before its date
// becomes trailing — that combination is handled by the final branch.
func stripVariants(model string) []string {
	out := []string{model}
	if v := dateSuffixRe.ReplaceAllString(model, ""); v != model {
		out = append(out, v)
	}
	tagStripped := trailingTagRe.ReplaceAllString(model, "")
	if tagStripped != model {
		out = append(out, tagStripped)
		if v := dateSuffixRe.ReplaceAllString(tagStripped, ""); v != tagStripped {
			out = append(out, v)
		}
	}
	return out
}

// matchKey resolves a (possibly messy) model name against the registry's
// canonical-key set, in priority order:
//
//  1. exact match on normalized name
//  2. exact match on a stripped variant (no date suffix, no preview tag)
//  3. alias reverse lookup (canonical -> aliases)
//  4. longest-prefix match against canonical keys
//
// Returns the canonical key plus the MatchKind. MatchMiss with empty key
// means no candidate satisfied the lookup.
func matchKey(query string, keys map[string]struct{}, aliasIndex map[string]string) (string, MatchKind) {
	q := Normalize(query)
	if q == "" {
		return "", MatchMiss
	}

	// 1 + 2: exact on full or stripped variants
	for _, v := range stripVariants(q) {
		if _, ok := keys[v]; ok {
			if v == q {
				return v, MatchExact
			}
			return v, MatchExact // we collapse stripped-exact into "exact" for caller simplicity
		}
	}

	// 3: alias reverse lookup
	if canonical, ok := aliasIndex[q]; ok {
		return canonical, MatchAlias
	}
	for _, v := range stripVariants(q) {
		if canonical, ok := aliasIndex[v]; ok {
			return canonical, MatchAlias
		}
	}

	// 4: longest-prefix match. We want "gpt-5-codex-mini" to find "gpt-5-codex"
	// rather than "gpt-5", so iterate candidates and pick the longest one
	// that q starts with (plus "-" boundary or full equality).
	best := ""
	for k := range keys {
		if k == "" || len(k) >= len(q) {
			continue
		}
		if !strings.HasPrefix(q, k) {
			continue
		}
		// require boundary so "gpt-5" doesn't match "gpt-50".
		next := q[len(k)]
		if next != '-' && next != '.' && next != ':' {
			continue
		}
		if len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return best, MatchPrefix
	}

	return "", MatchMiss
}

// basenameCandidates returns registry keys whose basename (the segment after
// the last '/') equals q. It is the last-resort fallback for bare model names
// that upstream sources store with a provider slug — a proxy reports
// "glm-5.2" while OpenRouter/LiteLLM/seed key it "z-ai/glm-5.2".
//
// Only invoked for bare queries: q containing '/' is provider-qualified and
// resolves via exact/alias first, so it is skipped here. Bare keys (no '/')
// are never returned either, since those already matched exactly.
//
// Results are sorted for deterministic output; the source-aware winner is
// chosen by pickBasenameWinner.
func basenameCandidates(q string, keys map[string]struct{}) []string {
	if strings.Contains(q, "/") {
		return nil
	}
	out := make([]string, 0)
	for k := range keys {
		if k == "" {
			continue
		}
		i := strings.LastIndex(k, "/")
		if i < 0 {
			continue // bare keys are handled by exact match
		}
		if k[i+1:] == q {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// pickBasenameWinner selects one candidate by the documented tiebreak:
//  1. fewest '/' segments (direct provider beats reseller wrappers),
//  2. highest source rank (user > litellm > openrouter > seed),
//  3. lexicographic.
//
// sourceRank resolves a candidate key to its source priority; the registry
// supplies it from its entry table so this function stays pure and testable.
func pickBasenameWinner(candidates []string, sourceRank func(string) int) string {
	best := ""
	for _, c := range candidates {
		if best == "" || basenameCandidateLess(c, best, sourceRank) {
			best = c
		}
	}
	return best
}

// basenameCandidateLess reports whether a is strictly preferred over b.
func basenameCandidateLess(a, b string, sourceRank func(string) int) bool {
	sa, sb := strings.Count(a, "/"), strings.Count(b, "/")
	if sa != sb {
		return sa < sb
	}
	ra, rb := sourceRank(a), sourceRank(b)
	if ra != rb {
		return ra > rb
	}
	return a < b
}
