package dbmerge

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
)

// TestSourceColumnsIncludesPresentOptional pins the bin->global copy fix:
// Optional unified columns that exist in the source MUST be carried — using
// spec.Columns alone silently strips reasoning_tokens/total_tokens/... on
// every merge (R3 schema finding F1).
func TestSourceColumnsIncludesPresentOptional(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: filepath.Join(t.TempDir(), "src.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	spec, ok := LookupSpec("api_requests")
	if !ok {
		t.Fatal("api_requests spec missing")
	}
	cols, err := SourceColumns(context.Background(), d, spec)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, c := range cols {
		have[c] = true
	}
	for _, want := range []string{"reasoning_tokens", "total_tokens", "http_status", "endpoint", "conversation_id"} {
		if !have[want] {
			t.Fatalf("SourceColumns dropped unified column %q (F1 regression): %v", want, cols)
		}
	}
	for _, want := range spec.Columns {
		if !have[want] {
			t.Fatalf("SourceColumns dropped required column %q: %v", want, cols)
		}
	}
}

// TestSourceColumnsSkipsAbsentOptional: a pre-AllClient source without the
// unified columns must still export (missing optional skipped, not an error).
func TestSourceColumnsSkipsAbsentOptional(t *testing.T) {
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("CREATE TABLE old_requests (timestamp INTEGER, session_id TEXT)"); err != nil {
		t.Fatal(err)
	}
	spec := TableSpec{Name: "old_requests", Columns: []string{"timestamp", "session_id"}, Optional: []string{"conversation_id", "reasoning_tokens"}}
	cols, err := SourceColumns(context.Background(), d, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[0] != "timestamp" || cols[1] != "session_id" {
		t.Fatalf("SourceColumns = %v, want base columns only when source predates Optional", cols)
	}
}

// TestRowColumnsDerivesFromRowKeys pins the import side: the receiving end
// derives the insert column set from the exported row's keys.
func TestRowColumnsDerivesFromRowKeys(t *testing.T) {
	spec := TableSpec{Name: "api_requests", Columns: []string{"timestamp", "session_id"}, Optional: []string{"conversation_id", "reasoning_tokens"}}
	full := RowColumns(spec, map[string]any{"timestamp": 1, "session_id": "s", "conversation_id": "c", "reasoning_tokens": 2})
	if len(full) != 4 {
		t.Fatalf("RowColumns = %v, want all 4 for a post-merge row", full)
	}
	old := RowColumns(spec, map[string]any{"timestamp": 1, "session_id": "s"})
	if len(old) != 2 {
		t.Fatalf("RowColumns = %v, want base 2 for a pre-AllClient row", old)
	}
}
