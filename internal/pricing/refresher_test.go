package pricing

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
)

// stubSource returns canned data without hitting the network.
type stubSource struct {
	name     string
	priority int
	entries  map[string]Entry
	err      error
	calls    int
}

func (s *stubSource) Name() string  { return s.name }
func (s *stubSource) Priority() int { return s.priority }
func (s *stubSource) Fetch(_ context.Context) (map[string]Entry, error) {
	s.calls++
	return s.entries, s.err
}

func newRefreshTestRegistry(t *testing.T) (Reloader, *stubSource, *Refresher) {
	t.Helper()
	db := newTestDB(t)
	reg, err := NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reloader := reg.(Reloader)

	src := &stubSource{
		name:     "stub",
		priority: 100,
		entries: map[string]Entry{
			"stub-model-a": {Model: "stub-model-a", Input: 1e-6, Output: 4e-6, Source: "stub"},
		},
	}

	logger := log.New(io.Discard, "", 0)
	r := &Refresher{
		db:       db,
		reg:      reloader,
		sources:  []Source{src},
		interval: time.Hour,
		timeout:  time.Second,
		logger:   logger,
	}
	return reloader, src, r
}

func TestRefresher_FirstTickInsertsNewRows(t *testing.T) {
	reg, src, r := newRefreshTestRegistry(t)

	if err := r.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if src.calls != 1 {
		t.Errorf("source.Fetch call count = %d, want 1", src.calls)
	}

	// New model should be in the registry now (registry was reloaded).
	res := reg.Lookup(context.Background(), "stub-model-a")
	if !res.Found {
		t.Fatal("stub-model-a not found after refresh")
	}
	if res.Entry.Source != "stub" {
		t.Errorf("source field = %q, want stub", res.Entry.Source)
	}
}

func TestRefresher_SecondTickWithSameDataIsNoOp(t *testing.T) {
	_, _, r := newRefreshTestRegistry(t)

	if err := r.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}

	// Snapshot updated_at on the row from the first tick.
	var firstUpdatedAt int64
	r.db.QueryRowContext(context.Background(),
		`SELECT updated_at FROM model_pricing WHERE model = 'stub-model-a'`,
	).Scan(&firstUpdatedAt)
	if firstUpdatedAt == 0 {
		t.Fatal("row not present after first tick")
	}

	// Second tick same payload — diff says hash unchanged, no UPDATE.
	time.Sleep(1100 * time.Millisecond) // make sure unix-second clock advanced
	if err := r.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	var secondUpdatedAt int64
	r.db.QueryRowContext(context.Background(),
		`SELECT updated_at FROM model_pricing WHERE model = 'stub-model-a'`,
	).Scan(&secondUpdatedAt)
	if secondUpdatedAt != firstUpdatedAt {
		t.Errorf("updated_at changed despite identical price: first=%d second=%d (refresher should skip equal hashes)",
			firstUpdatedAt, secondUpdatedAt)
	}
}

func TestRefresher_PriceChangeUpdatesRow(t *testing.T) {
	reg, src, r := newRefreshTestRegistry(t)

	if err := r.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}

	// Now mutate the source to simulate an upstream price change.
	src.entries["stub-model-a"] = Entry{Model: "stub-model-a", Input: 2e-6, Output: 5e-6, Source: "stub"}

	if err := r.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	res := reg.Lookup(context.Background(), "stub-model-a")
	if !res.Found {
		t.Fatal("model lost after refresh")
	}
	if res.Entry.Input != 2e-6 || res.Entry.Output != 5e-6 {
		t.Errorf("price not updated, got input=%v output=%v", res.Entry.Input, res.Entry.Output)
	}
}

func TestRefresher_AllSourcesFailKeepsSnapshot(t *testing.T) {
	reg, src, r := newRefreshTestRegistry(t)

	src.err = errors.New("simulated network error")
	src.entries = nil

	err := r.Tick(context.Background())
	if err == nil {
		t.Fatal("expected error when all sources fail")
	}
	if !strings.Contains(err.Error(), "all pricing sources failed") {
		t.Errorf("unexpected error: %v", err)
	}

	// The snapshot status should record the failure.
	snap := reg.Snapshot(context.Background())
	if snap.LastRefreshErr == "" {
		t.Error("LastRefreshErr should record source failure")
	}
}

func TestRefresher_HighPriorityWinsMerge(t *testing.T) {
	db := newTestDB(t)
	reg, err := NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reloader := reg.(Reloader)

	low := &stubSource{
		name:     "low",
		priority: 1,
		entries: map[string]Entry{
			"shared-model": {Model: "shared-model", Input: 9e-6, Output: 9e-6, Source: "low"},
		},
	}
	high := &stubSource{
		name:     "high",
		priority: 100,
		entries: map[string]Entry{
			"shared-model": {Model: "shared-model", Input: 1e-6, Output: 4e-6, Source: "high"},
		},
	}
	r := &Refresher{
		db:       db,
		reg:      reloader,
		sources:  []Source{low, high},
		interval: time.Hour,
		timeout:  time.Second,
		logger:   log.New(io.Discard, "", 0),
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	res := reg.Lookup(context.Background(), "shared-model")
	if !res.Found {
		t.Fatal("shared-model not in registry")
	}
	if res.Entry.Source != "high" {
		t.Errorf("expected high-priority source to win, got source=%q", res.Entry.Source)
	}
	if res.Entry.Input != 1e-6 {
		t.Errorf("input should be from high source, got %v", res.Entry.Input)
	}
}

func TestRefresher_PartialFailureStillUpdates(t *testing.T) {
	db := newTestDB(t)
	reg, err := NewRegistry(context.Background(), db, &config.Config{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reloader := reg.(Reloader)

	good := &stubSource{
		name:     "good",
		priority: 10,
		entries: map[string]Entry{
			"good-only-model": {Model: "good-only-model", Input: 1e-6, Output: 1e-6, Source: "good"},
		},
	}
	broken := &stubSource{
		name:     "broken",
		priority: 5,
		err:      errors.New("502"),
	}
	r := &Refresher{
		db:       db,
		reg:      reloader,
		sources:  []Source{good, broken},
		interval: time.Hour,
		timeout:  time.Second,
		logger:   log.New(io.Discard, "", 0),
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	res := reg.Lookup(context.Background(), "good-only-model")
	if !res.Found {
		t.Fatal("good source's entry must reach the registry despite broken neighbour")
	}
	snap := reg.Snapshot(context.Background())
	if snap.LastRefreshMsg != "partial" {
		t.Errorf("LastRefreshMsg = %q, want \"partial\"", snap.LastRefreshMsg)
	}
	if snap.LastRefreshErr == "" {
		t.Error("LastRefreshErr should mention broken source")
	}
}

func TestEntryHash_StableAndDistinct(t *testing.T) {
	a := Entry{Input: 1e-6, Output: 4e-6, CacheRead: 0, CacheCreation: 0}
	b := Entry{Input: 1e-6, Output: 4e-6, CacheRead: 0, CacheCreation: 0}
	if entryHash(a) != entryHash(b) {
		t.Error("identical entries must have identical hashes")
	}
	c := Entry{Input: 1.000001e-6, Output: 4e-6, CacheRead: 0, CacheCreation: 0}
	if entryHash(a) == entryHash(c) {
		t.Error("differing input price must produce a different hash")
	}
}
