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
	want := map[string]float64{
		"mimo-v2.5-pro":     0.000000435, // $0.435 / M
		"deepseek-v4-pro":   0.00000174,
		"deepseek-v4-flash": 0.00000014,
		"step-3.5-flash":    0.0000001,
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
	// glm-5.2 is auto-fetched (OpenRouter) and intentionally NOT in the seed.
	if _, ok := byModel["glm-5.2"]; ok {
		t.Error("glm-5.2 should not be in seed (auto-fetched, would block refresh)")
	}
}
