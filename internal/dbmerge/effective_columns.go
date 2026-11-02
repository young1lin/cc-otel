package dbmerge

import (
	"context"
	"database/sql"
)

// SourceColumns returns the effective column list a copy tool should read
// from a source table for this spec: the required identity columns plus
// whichever Optional unified columns actually exist in the source schema.
//
// Pre-AllClient sources lack the Optional columns entirely (reading them
// would fail); post-merge sources carry them and silently dropping them —
// as spec.Columns alone does, because Optional is never part of Columns —
// strips reasoning_tokens/total_tokens/http_status/endpoint/conversation_id
// and the tool-event attribution columns on every copy. This mirrors what
// stagedBatch.columns does for the import pipeline (stage.go).
func SourceColumns(ctx context.Context, src *sql.DB, spec TableSpec) ([]string, error) {
	present, err := existingColumns(ctx, src, spec.Name)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(spec.Columns)+len(spec.Optional))
	out = append(out, spec.Columns...)
	for _, c := range spec.Optional {
		if present[c] {
			out = append(out, c)
		}
	}
	return out, nil
}

// RowColumns returns spec.Columns plus the Optional columns whose values are
// present in a single exported row map. The receiving side of a copy cannot
// ask the (absent) source schema, so it derives the set from the row itself.
func RowColumns(spec TableSpec, row map[string]any) []string {
	out := make([]string, 0, len(spec.Columns)+len(spec.Optional))
	out = append(out, spec.Columns...)
	for _, c := range spec.Optional {
		if _, ok := row[c]; ok {
			out = append(out, c)
		}
	}
	return out
}

func existingColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}
