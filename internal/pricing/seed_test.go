package pricing

import "testing"

func TestMergeSeedEntries(t *testing.T) {
	auto := []Entry{
		{Model: "a", Input: 1},
		{Model: "b", Input: 2},
	}
	manual := []Entry{
		{Model: "b", Input: 99}, // overrides auto
		{Model: "c", Input: 3},  // manual-only
	}
	got := mergeSeedEntries(auto, manual)
	byKey := map[string]Entry{}
	for _, e := range got {
		byKey[e.Model] = e
	}
	if byKey["b"].Input != 99 {
		t.Errorf("manual should override auto for b: got %v", byKey["b"].Input)
	}
	if _, ok := byKey["a"]; !ok {
		t.Error("auto-only entry a missing")
	}
	if _, ok := byKey["c"]; !ok {
		t.Error("manual-only entry c missing")
	}
	if len(got) != 3 {
		t.Errorf("want 3 merged entries, got %d", len(got))
	}
}

func TestLoadSeedEntriesIncludesManual(t *testing.T) {
	entries, err := loadSeedEntries()
	if err != nil {
		t.Fatalf("loadSeedEntries: %v", err)
	}
	byModel := map[string]Entry{}
	for _, e := range entries {
		byModel[e.Model] = e
	}
	// manual_seed.json entries must be present with curated (not stale) prices.
	// 2026-08-16: refreshed against OpenRouter first-party quotes; deepseek-v4-pro
	// was cut 4x by DeepSeek (1.74e-6 → 4.35e-7). glm-* entries are now curated
	// here deliberately (user decision): the auto seed is a LiteLLM snapshot that
	// lags new releases, and pinning beats a $0 row on fresh installs.
	want := map[string]float64{
		"mimo-v2.5-pro":          0.000000435, // $0.435 / M
		"deepseek-v4-pro":        0.000000435, // official DeepSeek, 2026-08 cut
		"deepseek-v4-pro-0813":   0.000000435,
		"deepseek-v4-flash":      0.00000014,
		"deepseek-v4-flash-0731": 0.00000014,
		"step-3.5-flash":         0.0000001,
		"glm-5.2":                0.0000014, // Z.AI official $1.4 / $4.4
		"glm-5.3":                0.0000014, // inferred from 5.2 (no published price)
		"kimi-k3":                0.000003, // Moonshot official $3 / $15
		"kimi-k2.7-code":         0.00000095,
		"qwen3.8-max":            0.000002, // OpenRouter blend (no first-party)
		"minimax-m3":             0.0000003,
	}
	for m, in := range want {
		e, ok := byModel[m]
		if !ok {
			t.Errorf("manual entry %q missing from merged seed", m)
			continue
		}
		if e.Input != in {
			t.Errorf("%q input = %v, want %v", m, e.Input, in)
		}
	}
}
