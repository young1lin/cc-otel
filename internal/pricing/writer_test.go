package pricing

import (
	"context"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
)

func newReg(t *testing.T) (Registry, Writer) {
	t.Helper()
	reg, err := NewRegistry(context.Background(), newTestDB(t), &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg, reg.(Writer)
}

func TestUpsert_InsertsAndReloads(t *testing.T) {
	_, w := newReg(t)
	got, err := w.Upsert(context.Background(), Entry{Model: "glm-4.6", Input: 6e-7, Output: 2.2e-6, CacheRead: 6e-8, Aliases: []string{"z-ai/glm-4.6"}})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got.Source != "manual" || got.Model != "glm-4.6" {
		t.Fatalf("got %+v", got)
	}
}

func TestUpsert_UpdatesExisting(t *testing.T) {
	reg, w := newReg(t)
	_, _ = w.Upsert(context.Background(), Entry{Model: "x", Input: 1e-6, Output: 1e-6})
	_, err := w.Upsert(context.Background(), Entry{Model: "x", Input: 5e-6, Output: 5e-6})
	if err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if v := reg.Calc(context.Background(), "x", 1_000_000, 0, 0, 0); v != 5.0 { // 1M * $5/Mtok
		t.Fatalf("Calc after update = %v, want 5", v)
	}
}

func TestUpsert_RejectsClaudeAndZero(t *testing.T) {
	_, w := newReg(t)
	if _, err := w.Upsert(context.Background(), Entry{Model: "claude-sonnet-5", Input: 1, Output: 1}); err == nil {
		t.Fatal("claude upsert should fail")
	}
	if _, err := w.Upsert(context.Background(), Entry{Model: "x", Input: 0, Output: 1}); err == nil {
		t.Fatal("zero input should fail")
	}
}

// TestUpsert_SourceManualOrOpenRouter verifies that a saved price records its
// provenance: "openrouter" when accepted from the picker, "manual" otherwise
// (including unknown values, which are normalized). This is what lets the UI
// distinguish hand-typed prices from OpenRouter-sourced ones.
func TestUpsert_SourceManualOrOpenRouter(t *testing.T) {
	reg, w := newReg(t)
	got, err := w.Upsert(context.Background(), Entry{Model: "glm-x", Input: 1e-6, Output: 2e-6, Source: "openrouter"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got.Source != "openrouter" {
		t.Fatalf("source = %q, want openrouter", got.Source)
	}
	// An unknown/empty source is normalized to manual.
	got2, _ := w.Upsert(context.Background(), Entry{Model: "glm-y", Input: 1e-6, Output: 2e-6, Source: "bogus"})
	if got2.Source != "manual" {
		t.Fatalf("unknown source = %q, want manual", got2.Source)
	}
	// The openrouter source round-trips through Reload (Lookup).
	if res := reg.Lookup(context.Background(), "glm-x"); !res.Found || res.Entry.Source != "openrouter" {
		t.Fatalf("lookup glm-x: %+v", res)
	}
}

func TestDelete_RemovesAndReloads(t *testing.T) {
	reg, w := newReg(t)
	_, _ = w.Upsert(context.Background(), Entry{Model: "x", Input: 1e-6, Output: 1e-6})
	if err := w.Delete(context.Background(), "x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res := reg.Lookup(context.Background(), "x"); res.Found {
		t.Fatal("x still found after delete")
	}
}

// TestList_SearchSourcePaging exercises substring search, source filtering,
// and pagination. It is intentionally seed-independent: the seed snapshot
// contains ~620 real models (including z-ai/glm-* and openai/gpt-*), so
// filtering by Source:"manual" isolates only the entries upserted below,
// and the uniquely-named "list-*" models avoid colliding with any seed row.
func TestList_SearchSourcePaging(t *testing.T) {
	_, w := newReg(t)
	_, _ = w.Upsert(context.Background(), Entry{Model: "list-a", Input: 1e-6, Output: 1e-6})
	_, _ = w.Upsert(context.Background(), Entry{Model: "list-b", Input: 2e-6, Output: 2e-6})

	// Substring + source filter: only our two manual "list-*" rows.
	res, err := w.List(context.Background(), ListFilter{Query: "list-", Source: "manual"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Total != 2 {
		t.Fatalf("got total=%d, want 2 (manual list-* entries)", res.Total)
	}
	// Keys are sorted alphabetically, so list-a is first.
	if res.Entries[0].Model != "list-a" {
		t.Fatalf("first entry = %q, want list-a", res.Entries[0].Model)
	}

	// Page 2 (1-based) of manual entries: offset path, single entry, list-b.
	paged, _ := w.List(context.Background(), ListFilter{Source: "manual", Page: 2, PageSize: 1})
	if paged.Total != 2 || len(paged.Entries) != 1 {
		t.Fatalf("paging got total=%d len=%d", paged.Total, len(paged.Entries))
	}
	if paged.Entries[0].Model != "list-b" {
		t.Fatalf("page 2 entry = %q, want list-b", paged.Entries[0].Model)
	}
}

func TestList_FoldsDuplicatesByBasename(t *testing.T) {
	_, w := newReg(t)
	// Three provider-variants of the same model, all source "manual" to stay
	// isolated from the seed catalog.
	_, _ = w.Upsert(context.Background(), Entry{Model: "glm-4.7", Input: 6e-7, Output: 2.2e-6})
	_, _ = w.Upsert(context.Background(), Entry{Model: "z-ai/glm-4.7", Input: 7e-7, Output: 3e-6})
	_, _ = w.Upsert(context.Background(), Entry{Model: "openrouter/z-ai/glm-4.7", Input: 8e-7, Output: 4e-6})

	res, err := w.List(context.Background(), ListFilter{Source: "manual", PageSize: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got []string
	for _, e := range res.Entries {
		got = append(got, e.Model)
	}
	// All three share basename "glm-4.7" -> folded to one row; the bare key wins.
	if res.Total != 1 || len(got) != 1 || got[0] != "glm-4.7" {
		t.Fatalf("folded = total %d %v, want 1 [glm-4.7]", res.Total, got)
	}
	// The other two provider-variants are attached for the expand UI.
	if len(res.Entries[0].Variants) != 2 {
		t.Fatalf("rep variants = %d, want 2", len(res.Entries[0].Variants))
	}
}

func TestDisplayRank(t *testing.T) {
	cases := map[string]int{
		"manual": 100, "user": 100,
		"openrouter": 80, "litellm": 60, "seed": 40,
		"": 0, "unknown": 0,
	}
	for src, want := range cases {
		if got := DisplayRank(src); got != want {
			t.Errorf("DisplayRank(%q) = %d, want %d", src, got, want)
		}
	}
}

// TestList_LocalFirstThenName verifies the sort: local-seen entries first
// (alphabetical among themselves), then the rest (alphabetical). All three
// entries are source "manual" so DisplayRank ties and only local/name matter.
func TestList_LocalFirstThenName(t *testing.T) {
	_, w := newReg(t)
	_, _ = w.Upsert(context.Background(), Entry{Model: "aaa-notlocal", Input: 1e-6, Output: 1e-6})
	_, _ = w.Upsert(context.Background(), Entry{Model: "zzz-local", Input: 1e-6, Output: 1e-6})
	_, _ = w.Upsert(context.Background(), Entry{Model: "mmm-local", Input: 1e-6, Output: 1e-6})

	local := map[string]bool{"zzz-local": true, "mmm-local": true}
	res, err := w.List(context.Background(), ListFilter{Source: "manual", Local: local, PageSize: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"mmm-local", "zzz-local", "aaa-notlocal"}
	got := make([]string, len(res.Entries))
	for i, e := range res.Entries {
		got[i] = e.Model
	}
	// Source:"manual" isolates our 3 upserts from the seed catalog.
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestList_IncludesReportedClaudeRowsReadOnly verifies that a Claude model the
// user has run (present in Local telemetry) but that has no model_pricing row
// (Claude is priced upstream by design) still shows up in the table as a
// read-only "upstream" row.
func TestList_IncludesReportedClaudeRowsReadOnly(t *testing.T) {
	_, w := newReg(t)
	local := map[string]bool{"claude-opus-4-8": true}
	res, err := w.List(context.Background(), ListFilter{Local: local, PageSize: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got *Entry
	for i := range res.Entries {
		if res.Entries[i].Model == "claude-opus-4-8" {
			got = &res.Entries[i]
			break
		}
	}
	if got == nil {
		t.Fatal("reported Claude model should be listed (read-only)")
	}
	if !IsClaudeModel(got.Model) || got.Source != "upstream" {
		t.Fatalf("Claude row should be read-only/upstream, got %+v", got)
	}
}

// TestCachedCatalogPrice_ReadsWarmCache verifies the read-only price lookup used
// to populate Claude rows reads the in-memory catalog cache (no network) and
// matches fuzzily: telemetry dashes/date-suffixes vs OpenRouter dots
// ("claude-opus-4-8" -> "anthropic/claude-opus-4.8").
func TestCachedCatalogPrice_ReadsWarmCache(t *testing.T) {
	srv := orRoutingServer(t, `{"data":[
		{"id":"anthropic/claude-opus-4.8","pricing":{"prompt":"0.000015","completion":"0.000075","input_cache_read":"0.0000015"}},
		{"id":"anthropic/claude-haiku-4.5","pricing":{"prompt":"0.000001","completion":"0.000005","input_cache_read":"0.0000001"}}
	]}`, emptyEndpoints, nil)
	defer srv.Close()
	redirectBoth(t, srv.URL)

	if _, err := fetchCatalog(context.Background()); err != nil {
		t.Fatalf("warm: %v", err)
	}
	// dash telemetry name matches dot catalog id
	in, out, cr, ok := CachedCatalogPrice("claude-opus-4-8")
	if !ok || !approx(in, 15) || !approx(out, 75) || !approx(cr, 1.5) {
		t.Fatalf("opus = %v/%v/%v ok=%v, want 15/75/1.5", in, out, cr, ok)
	}
	// date-suffixed telemetry name matches the undated catalog id
	if in, _, _, ok := CachedCatalogPrice("claude-haiku-4-5-20251001"); !ok || !approx(in, 1) {
		t.Fatalf("haiku = %v ok=%v, want 1", in, ok)
	}
	if _, _, _, ok := CachedCatalogPrice("no-such-model"); ok {
		t.Fatal("expected no match for an unknown model")
	}
}

// TestList_ClaudeCacheCreateDefaultsToFiveMinuteTier verifies the injected
// Claude row fills cache_create from Anthropic's 5-min prompt-caching write
// tier (1.25x input), since OpenRouter publishes no cache-write field.
func TestList_ClaudeCacheCreateDefaultsToFiveMinuteTier(t *testing.T) {
	srv := orRoutingServer(t, `{"data":[
		{"id":"anthropic/claude-sonnet-5","pricing":{"prompt":"0.000002","completion":"0.00001","input_cache_read":"0.0000002"}}
	]}`, emptyEndpoints, nil)
	defer srv.Close()
	redirectBoth(t, srv.URL)
	if _, err := fetchCatalog(context.Background()); err != nil {
		t.Fatalf("warm: %v", err)
	}
	_, w := newReg(t)
	res, err := w.List(context.Background(), ListFilter{Local: map[string]bool{"claude-sonnet-5": true}, PageSize: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got *Entry
	for i := range res.Entries {
		if res.Entries[i].Model == "claude-sonnet-5" {
			got = &res.Entries[i]
			break
		}
	}
	if got == nil {
		t.Fatal("claude-sonnet-5 row missing")
	}
	if !approx(got.CacheCreation, got.Input*1.25) {
		t.Fatalf("cache_create = %v, want %v (1.25x input %v)", got.CacheCreation, got.Input*1.25, got.Input)
	}
}
