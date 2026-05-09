package pricing

import (
	"regexp"
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
