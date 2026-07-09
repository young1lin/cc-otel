package pricing

import "testing"

func TestIsClaudeModel(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"claude-sonnet-4-5", true},
		{"Claude-Opus-4-1-20250805", true},
		{"  claude-haiku-4-5  ", true},
		{"CLAUDE-SONNET-4", true},
		{"claude-", true}, // technically a prefix match — caller still gets miss because not a real model
		{"", false},
		{"claude", false}, // no trailing dash
		{"glm-4.6", false},
		{"gpt-5-codex", false},
		{"deepseek-v3.2", false},
		{"my-claude-clone", false},
	}
	for _, c := range cases {
		if got := IsClaudeModel(c.in); got != c.want {
			t.Errorf("IsClaudeModel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":                "",
		" GPT-5 ":         "gpt-5",
		"GLM-4.6":         "glm-4.6",
		"\tdeepseek-v3\n": "deepseek-v3",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripVariants(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"gpt-5", []string{"gpt-5"}},
		{"claude-sonnet-4-5-20250929", []string{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5"}},
		{"gpt-5-preview", []string{"gpt-5-preview", "gpt-5"}},
		{"claude-opus-4-1-20250805-preview", []string{
			"claude-opus-4-1-20250805-preview",
			"claude-opus-4-1-20250805", // strip preview tag (date now trailing)
			"claude-opus-4-1",          // then strip the date
		}},
	}
	for _, c := range cases {
		got := stripVariants(c.in)
		if !equalSlice(got, c.want) {
			t.Errorf("stripVariants(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMatchKey(t *testing.T) {
	keys := map[string]struct{}{
		"gpt-5":         {},
		"gpt-5-codex":   {},
		"glm-4.6":       {},
		"deepseek-v3.2": {},
	}
	aliases := map[string]string{
		"openai/gpt-5":    "gpt-5",
		"zai-org/glm-4.6": "glm-4.6",
	}

	tests := []struct {
		query    string
		wantKey  string
		wantKind MatchKind
	}{
		{"gpt-5", "gpt-5", MatchExact},
		{"GPT-5", "gpt-5", MatchExact},                         // normalize first
		{"gpt-5-codex-2025-09-12", "gpt-5-codex", MatchPrefix}, // prefix wins over plain gpt-5
		{"openai/gpt-5", "gpt-5", MatchAlias},
		{"glm-4.6", "glm-4.6", MatchExact},
		{"glm-4.6-20251201", "glm-4.6", MatchExact}, // date-stripped exact (collapsed)
		{"unknown-model-xyz", "", MatchMiss},
		{"", "", MatchMiss},
		{"gpt-50", "", MatchMiss}, // boundary check rejects sub-token
	}
	for _, tc := range tests {
		gotKey, gotKind := matchKey(tc.query, keys, aliases)
		if gotKey != tc.wantKey || gotKind != tc.wantKind {
			t.Errorf("matchKey(%q) = (%q, %s), want (%q, %s)",
				tc.query, gotKey, gotKind, tc.wantKey, tc.wantKind)
		}
	}
}

func TestBasenameCandidates(t *testing.T) {
	keys := map[string]struct{}{
		"z-ai/glm-5.1":                {},
		"openrouter/z-ai/glm-5.1":     {},
		"z-ai/glm-4.6":                {},
		"openrouter/z-ai/glm-4.6":     {},
		"together_ai/zai-org/glm-4.6": {},
		"glm-4.6":                     {}, // bare key — never a basename candidate
		"gpt-5":                       {},
	}
	tests := []struct {
		q    string
		want []string // sorted ascending
	}{
		{"glm-5.1", []string{"openrouter/z-ai/glm-5.1", "z-ai/glm-5.1"}},
		{"glm-4.6", []string{"openrouter/z-ai/glm-4.6", "together_ai/zai-org/glm-4.6", "z-ai/glm-4.6"}},
		{"glm-5", []string{}},        // tail must equal exactly (glm-5 != glm-5.1)
		{"z-ai/glm-5.1", nil},        // provider-qualified query -> skip (exact/alias handle it)
		{"gpt-5", nil},               // only bare key exists, no prefixed one -> none
		{"unknown-model", nil},
	}
	for _, tc := range tests {
		got := basenameCandidates(tc.q, keys)
		if !equalSlice(got, tc.want) {
			t.Errorf("basenameCandidates(%q) = %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestPickBasenameWinner(t *testing.T) {
	// ranks mirror SourceRank (user>litellm>openrouter>seed) but are stubbed
	// per key so the test stays independent of the registry.
	ranks := map[string]int{
		"z-ai/glm-4.6":                20, // openrouter
		"openrouter/z-ai/glm-4.6":     10, // seed
		"together_ai/zai-org/glm-4.6": 10, // seed
		"litellm/x":                   30,
		"seed/x":                      10,
		"openrouter/x":                20,
		"a/x":                         10,
		"b/x":                         10,
	}
	rank := func(k string) int { return ranks[k] }
	tests := []struct {
		name  string
		cands []string
		want  string
	}{
		{"fewest segment wins", []string{"openrouter/z-ai/glm-4.6", "together_ai/zai-org/glm-4.6", "z-ai/glm-4.6"}, "z-ai/glm-4.6"},
		{"tie segment, higher source wins", []string{"seed/x", "litellm/x"}, "litellm/x"},
		{"full tie -> lexicographic", []string{"b/x", "a/x"}, "a/x"},
		{"single candidate", []string{"z-ai/glm-5.2"}, "z-ai/glm-5.2"},
		{"empty", nil, ""},
	}
	for _, tc := range tests {
		if got := pickBasenameWinner(tc.cands, rank); got != tc.want {
			t.Errorf("%s: pickBasenameWinner = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSourceRank(t *testing.T) {
	cases := map[string]int{
		"user":       40,
		"litellm":    30,
		"openrouter": 20,
		"seed":       10,
		"":           0,
		"weird":      0,
	}
	for src, want := range cases {
		if got := SourceRank(src); got != want {
			t.Errorf("SourceRank(%q) = %d, want %d", src, got, want)
		}
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
