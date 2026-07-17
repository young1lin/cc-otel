package db

import "context"

// CleanupLegacyCodexEventsBatch deletes at most limit compatibility events.
func (r *Repository) CleanupLegacyCodexEventsBatch(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}

	result, err := r.db.ExecContext(ctx, `
		DELETE FROM codex_events
		WHERE id IN (
			SELECT id FROM codex_events ORDER BY id LIMIT ?
		)`, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
