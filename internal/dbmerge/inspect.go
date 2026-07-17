package dbmerge

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type seenStore struct {
	path string
	db   *sql.DB
	once sync.Once
}

func newSeenStore(directory string) (*seenStore, error) {
	file, err := os.CreateTemp(directory, ".inspect-*.db")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(path)
		return nil, err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	d, err := sql.Open("sqlite3", filepath.ToSlash(path))
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	d.SetMaxOpenConns(1)
	if _, err := d.Exec(`
		PRAGMA journal_mode=OFF;
		PRAGMA synchronous=OFF;
		CREATE TABLE seen (digest TEXT PRIMARY KEY) WITHOUT ROWID;
	`); err != nil {
		d.Close()
		os.Remove(path)
		return nil, err
	}
	return &seenStore{path: path, db: d}, nil
}

func (s *seenStore) Add(ctx context.Context, digest string) (bool, error) {
	first, err := s.AddBatch(ctx, []string{digest})
	if err != nil {
		return false, err
	}
	_, ok := first[digest]
	return ok, nil
}

func (s *seenStore) AddBatch(ctx context.Context, digests []string) (map[string]struct{}, error) {
	first := make(map[string]struct{})
	if len(digests) == 0 {
		return first, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	limit, err := variableLimit(conn)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for start := 0; start < len(digests); start += limit {
		end := min(start+limit, len(digests))
		args := make([]any, end-start)
		values := make([]string, end-start)
		for i, digest := range digests[start:end] {
			args[i] = digest
			values[i] = "(?)"
		}
		rows, err := tx.QueryContext(ctx,
			`INSERT OR IGNORE INTO seen(digest) VALUES `+strings.Join(values, ",")+` RETURNING digest`,
			args...,
		)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var digest string
			if err := rows.Scan(&digest); err != nil {
				rows.Close()
				return nil, err
			}
			first[digest] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return first, nil
}

func (s *seenStore) Close() error {
	var closeErr error
	s.once.Do(func() {
		closeErr = s.db.Close()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(s.path + suffix); err != nil && !os.IsNotExist(err) && closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

func InspectSQLite(
	ctx context.Context,
	path string,
	target *sql.DB,
	progress ProgressFunc,
) (Inspection, error) {
	return inspectSQLite(ctx, path, target, progress, nil)
}

func inspectSQLite(
	ctx context.Context,
	path string,
	target *sql.DB,
	progress ProgressFunc,
	metrics func(batchMetrics),
) (Inspection, error) {
	schema, err := ValidateSQLite(ctx, path)
	if err != nil {
		return Inspection{}, err
	}
	source := NewSQLiteSource(path, schema, nil)
	counts, err := source.Count(ctx)
	if err != nil {
		return Inspection{}, &MergeError{Code: ErrInspection, Err: err}
	}
	total := sumCounts(counts)
	seen, err := newSeenStore(filepath.Dir(path))
	if err != nil {
		return Inspection{}, &MergeError{Code: ErrInspection, Err: err}
	}
	defer seen.Close()

	out := Inspection{
		IgnoredTables: append([]string(nil), schema.IgnoredTables...),
		Warnings:      append([]string(nil), schema.Warnings...),
	}
	byTable := make(map[string]*TableStats)
	err = scanBatches(ctx, source, defaultBatchLimits(Options{}), func(batch rowBatch) error {
		stats := byTable[batch.Table]
		if stats == nil {
			stats = &TableStats{Name: batch.Table}
			byTable[batch.Table] = stats
		}
		first, err := seen.AddBatch(ctx, batch.digests())
		if err != nil {
			return err
		}
		firstBatch := batch.firstCandidates(first)
		var newRows int64
		batchMetric := batchMetrics{LogicalRows: batch.SourceRows}
		if len(firstBatch.Candidates) > 0 {
			stage, err := newStagedBatch(ctx, target, firstBatch, stageOptions{})
			if err != nil {
				return err
			}
			stageErr := stage.markNew(ctx)
			if stageErr == nil {
				newRows, stageErr = stage.countNew(ctx)
			}
			closeErr := stage.Close(ctx)
			batchMetric = stage.metrics
			if stageErr != nil {
				return stageErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		if metrics != nil {
			metrics(batchMetric)
		}
		duplicates := batch.SourceRows - newRows
		stats.SourceRows += batch.SourceRows
		stats.NewRows += newRows
		stats.DuplicateRows += duplicates
		out.SourceRows += batch.SourceRows
		out.NewRows += newRows
		out.DuplicateRows += duplicates
		emitProgress(progress, Progress{
			Phase:         PhaseInspecting,
			CurrentTable:  batch.Table,
			ProcessedRows: out.SourceRows,
			TotalRows:     total,
			DuplicateRows: out.DuplicateRows,
		})
		return nil
	})
	out.Tables = orderedStats(byTable)
	if err != nil {
		return Inspection{}, &MergeError{Code: ErrInspection, Err: err}
	}
	return out, nil
}

func sumCounts(counts map[string]int64) int64 {
	var total int64
	for _, count := range counts {
		total += count
	}
	return total
}

func orderedStats(byTable map[string]*TableStats) []TableStats {
	out := make([]TableStats, 0, len(byTable))
	for _, spec := range ImportSpecs() {
		if stats := byTable[spec.Name]; stats != nil {
			out = append(out, *stats)
		}
	}
	return out
}

func emitProgress(progress ProgressFunc, value Progress) {
	if progress == nil {
		return
	}
	if value.TotalRows > 0 {
		value.Percent = float64(value.ProcessedRows) * 100 / float64(value.TotalRows)
	} else if value.ProcessedRows > 0 {
		value.Percent = 100
	}
	if math.IsNaN(value.Percent) || math.IsInf(value.Percent, 0) {
		value.Percent = 0
	}
	value.Percent = math.Max(0, math.Min(100, value.Percent))
	progress(value)
}
