package pricing

import "testing"

func TestSelectFirstPartyEndpoint(t *testing.T) {
	// Mirrors the live z-ai/glm-5.2 endpoint list: fp4 discounters first, the
	// first-party "Z.AI" buried mid-list, a pricier outlier last.
	mk := func(provider, in, out, cr string) orEndpoint {
		e := orEndpoint{ProviderName: provider}
		e.Pricing.Prompt = in
		e.Pricing.Completion = out
		e.Pricing.InCacheRd = cr
		return e
	}
	eps := []orEndpoint{
		mk("Wafer", "0.0000012", "0.0000041", "0.0000002"),   // fp4 — wrong to pick
		mk("DeepInfra", "0.0000012", "0.0000042", "0.0000002"),
		mk("Z.AI", "0.0000014", "0.0000044", "0.00000026"),    // first-party
		mk("Io Net", "0.00000168", "0.00000528", "0.0000005"),
	}
	ep, ok := selectFirstPartyEndpoint(eps, "zai")
	if !ok || ep.ProviderName != "Z.AI" {
		t.Fatalf("want first-party Z.AI, got ok=%v provider=%q", ok, ep.ProviderName)
	}
	if ep.Pricing.Prompt != "0.0000014" {
		t.Errorf("got prompt %q, want 0.0000014", ep.Pricing.Prompt)
	}

	// No first-party present -> not found.
	if _, ok := selectFirstPartyEndpoint([]orEndpoint{mk("Wafer", "1", "1", "1")}, "zai"); ok {
		t.Error("want false when no first-party endpoint matches")
	}
}

func TestOwnerSlugAndAlnumLower(t *testing.T) {
	cases := map[string]string{
		"z-ai/glm-5.2":          "z-ai",
		"openrouter/z-ai/x":     "openrouter",
		"deepseek-chat":         "deepseek-chat",
		"":                      "",
	}
	for in, want := range cases {
		if got := ownerSlug(in); got != want {
			t.Errorf("ownerSlug(%q) = %q, want %q", in, got, want)
		}
	}
	// alnumLower collapses separators so "Z.AI" matches slug "z-ai".
	pairs := map[string]string{
		"Z.AI":      "zai",
		"z-ai":      "zai",
		"Deep Seek": "deepseek",
		"gpt-4o":    "gpt4o",
	}
	for in, want := range pairs {
		if got := alnumLower(in); got != want {
			t.Errorf("alnumLower(%q) = %q, want %q", in, got, want)
		}
	}
}
