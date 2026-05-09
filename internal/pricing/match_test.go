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
