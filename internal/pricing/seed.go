package pricing

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"
)

//go:embed embed/seed.json
var seedJSON []byte

//go:embed embed/manual_seed.json
var manualSeedJSON []byte

// seedFile is the on-disk shape of embed/seed.json. We keep _meta separate
// from entries so the file stays readable.
type seedFile struct {
	Meta    map[string]string `json:"_meta"`
	Entries []seedEntry       `json:"entries"`
}

type seedEntry struct {
	Model         string   `json:"model"`
	Input         float64  `json:"input"`
	Output        float64  `json:"output"`
	CacheRead     float64  `json:"cache_read"`
	CacheCreation float64  `json:"cache_creation"`
	Aliases       []string `json:"aliases"`
}

// loadSeedEntries returns the embedded seed (auto-generated from LiteLLM via
// tools/dump_pricing_snapshot) merged with the hand-maintained manual seed
// (embed/manual_seed.json — models no upstream catalog carries, e.g. Xiaomi
// MiMo, StepFun, not-yet-listed DeepSeek V4). Manual entries override auto on
// key conflict; they are the curated defaults and survive seed.json regen.
// Public so tests can exercise the same data the runtime uses.
func loadSeedEntries() ([]Entry, error) {
	auto, err := parseSeedFile(seedJSON)
	if err != nil {
		return nil, fmt.Errorf("parse embed/seed.json: %w", err)
	}
	manual, err := parseSeedFile(manualSeedJSON)
	if err != nil {
		return nil, fmt.Errorf("parse embed/manual_seed.json: %w", err)
	}
	return mergeSeedEntries(auto, manual), nil
}

// parseSeedFile parses one embedded seed file into Entry values.
func parseSeedFile(b []byte) ([]Entry, error) {
	var sf seedFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	out := make([]Entry, 0, len(sf.Entries))
	for _, s := range sf.Entries {
		out = append(out, Entry{
			Model:         Normalize(s.Model),
			Input:         s.Input,
			Output:        s.Output,
			CacheRead:     s.CacheRead,
			CacheCreation: s.CacheCreation,
			Aliases:       s.Aliases,
			Source:        "seed",
			FetchedAt:     now,
			UpdatedAt:     now,
		})
	}
	return out, nil
}

// mergeSeedEntries folds manual into auto, with manual winning on key conflict.
// Output order: auto entries first (manual value substituted where overridden),
// then any manual-only keys appended.
func mergeSeedEntries(auto, manual []Entry) []Entry {
	merged := make(map[string]Entry, len(auto)+len(manual))
	for _, e := range auto {
		merged[e.Model] = e
	}
	for _, e := range manual {
		merged[e.Model] = e // manual overrides auto on conflict
	}
	out := make([]Entry, 0, len(merged))
	seen := make(map[string]bool, len(merged))
	for _, e := range auto {
		if seen[e.Model] {
			continue
		}
		out = append(out, merged[e.Model])
		seen[e.Model] = true
	}
	for _, e := range manual {
		if seen[e.Model] {
			continue
		}
		out = append(out, merged[e.Model])
		seen[e.Model] = true
	}
	return out
}

// seedIfEmpty bulk-inserts the embedded seed when model_pricing has no rows.
// Idempotent: a second call after rows exist is a no-op. Returns the row
// count after seeding (or current count if no seed needed).
func seedIfEmpty(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_pricing`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count model_pricing: %w", err)
	}
	if n > 0 {
		return n, nil
	}

	entries, err := loadSeedEntries()
	if err != nil {
		return 0, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin seed tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO model_pricing
			(model, input_cost, output_cost, cache_read_cost, cache_create_cost,
			 aliases, source, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return 0, fmt.Errorf("prepare seed insert: %w", err)
	}
	defer stmt.Close()

	for _, e := range entries {
		aliasJSON, _ := json.Marshal(e.Aliases)
		if _, err := stmt.ExecContext(ctx,
			e.Model, e.Input, e.Output, e.CacheRead, e.CacheCreation,
			string(aliasJSON), e.Source, e.FetchedAt, e.UpdatedAt,
		); err != nil {
			return 0, fmt.Errorf("seed insert %s: %w", e.Model, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit seed: %w", err)
	}
	return len(entries), nil
}
