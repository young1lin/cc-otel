package dbmerge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ncruces/go-sqlite3"
)

var defaultRetryDelays = []time.Duration{
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
}

type batchStats struct {
	scanned    int64
	inserted   int64
	duplicates int64
	tables     map[string]*TableStats
}

type batchOutcome struct {
	stats     batchStats
	verified  int64
	committed bool
}

func newBatchStats() batchStats {
	return batchStats{tables: make(map[string]*TableStats)}
}

func (s *batchStats) table(name string) *TableStats {
	stats := s.tables[name]
	if stats == nil {
		stats = &TableStats{Name: name}
		s.tables[name] = stats
	}
	return stats
}

func Import(
	ctx context.Context,
	target *sql.DB,
	source RowSource,
	options Options,
) (Result, error) {
	started := time.Now()
	result := Result{StartedAt: started.Unix()}
	if err := ensureLedger(ctx, target); err != nil {
		return result, &MergeError{Code: ErrImport, Err: err}
	}
	delays := options.retryDelays
	if delays == nil {
		delays = defaultRetryDelays
	}
	scratchDir := os.TempDir()
	if provider, ok := source.(scratchDirProvider); ok && provider.scratchDir() != "" {
		scratchDir = provider.scratchDir()
	}
	seen, err := newSeenStore(scratchDir)
	if err != nil {
		return result, &MergeError{Code: ErrImport, Err: err}
	}
	defer seen.Close()

	var batchNumber int
	err = scanBatches(ctx, source, defaultBatchLimits(options), func(batch rowBatch) error {
		batchNumber++
		outcome, err := runStagedBatchWithRetry(ctx, target, batch, seen, options, batchNumber, delays)
		if outcome.committed {
			result.ScannedRows += outcome.stats.scanned
			result.InsertedRows += outcome.stats.inserted
			result.DuplicateRows += outcome.stats.duplicates
			result.VerifiedIdentities += outcome.verified
			mergeTableStats(&result, outcome.stats)
			emitProgress(options.Progress, Progress{
				Phase:         PhaseImporting,
				CurrentTable:  batch.Table,
				ProcessedRows: result.ScannedRows,
				TotalRows:     options.TotalRows,
				InsertedRows:  result.InsertedRows,
				DuplicateRows: result.DuplicateRows,
			})
			emitProgress(options.Progress, Progress{
				Phase:         PhaseVerifying,
				CurrentTable:  batch.Table,
				ProcessedRows: result.ScannedRows,
				TotalRows:     options.TotalRows,
				InsertedRows:  result.InsertedRows,
				DuplicateRows: result.DuplicateRows,
			})
		}
		return err
	})
	result.FinishedAt = time.Now().Unix()
	if err != nil {
		var mergeErr *MergeError
		if errors.As(err, &mergeErr) {
			return result, err
		}
		return result, &MergeError{Code: ErrImport, Err: err}
	}
	return result, nil
}

func ensureLedger(ctx context.Context, target *sql.DB) error {
	_, err := target.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS import_ledger (
			uuid TEXT PRIMARY KEY,
			imported_at INTEGER NOT NULL,
			source_db TEXT NOT NULL,
			table_name TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_import_ledger_table_time
			ON import_ledger(table_name, imported_at);
	`)
	return err
}

func runStagedBatchWithRetry(
	ctx context.Context,
	target *sql.DB,
	batch rowBatch,
	seen *seenStore,
	options Options,
	batchNumber int,
	delays []time.Duration,
) (batchOutcome, error) {
	stage, err := newStagedBatch(ctx, target, batch, stageOptions{})
	if err != nil {
		return batchOutcome{}, err
	}
	var outcome batchOutcome
	defer func() {
		_ = stage.Close(context.Background())
		if options.metrics != nil {
			options.metrics(stage.metrics)
		}
	}()
	for attempt := 0; ; attempt++ {
		outcome, err = runStagedBatchOnce(ctx, stage, seen, options, batchNumber)
		if err == nil || outcome.committed || !isBusyError(err) || attempt >= len(delays) {
			closeErr := stage.Close(context.Background())
			if err == nil && closeErr != nil {
				err = closeErr
			}
			return outcome, err
		}
		timer := time.NewTimer(delays[attempt])
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return batchOutcome{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func isBusyError(err error) bool {
	return errors.Is(err, sqlite3.BUSY) || errors.Is(err, sqlite3.BUSY_SNAPSHOT)
}

func runStagedBatchOnce(
	ctx context.Context,
	stage *stagedBatch,
	seen *seenStore,
	options Options,
	batchNumber int,
) (outcome batchOutcome, err error) {
	writerStarted := time.Now()
	if _, err = stage.conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		stage.metrics.WriterDuration += time.Since(writerStarted)
		return outcome, err
	}
	stage.metrics.TargetStatements++
	committed := false
	defer func() {
		if !committed {
			_, _ = stage.conn.ExecContext(context.Background(), "ROLLBACK")
			stage.metrics.WriterDuration += time.Since(writerStarted)
		}
	}()

	stats := newBatchStats()
	stats.scanned = stage.batch.SourceRows
	stats.table(stage.spec.Name).SourceRows = stage.batch.SourceRows
	if err := stage.markNew(ctx); err != nil {
		return outcome, err
	}
	newCount, err := stage.countNew(ctx)
	if err != nil {
		return outcome, err
	}
	claude := map[aggKey]*claudeDelta{}
	codex := map[aggKey]*codexDelta{}
	if stage.spec.Kind == KindClaudeRequest || stage.spec.Kind == KindCodexRequest {
		rows, err := stage.newRows(ctx)
		if err != nil {
			return outcome, err
		}
		for _, row := range rows {
			addAggregateDelta(options.Location, row, claude, codex)
		}
	}
	inserted, err := stage.insertNew(ctx)
	if err != nil {
		return outcome, err
	}
	if inserted != newCount {
		return outcome, fmt.Errorf("staged new count changed from %d to %d", newCount, inserted)
	}
	stats.inserted = inserted
	stats.duplicates = stage.batch.SourceRows - inserted
	tableStats := stats.table(stage.spec.Name)
	tableStats.InsertedRows = inserted
	tableStats.DuplicateRows = stats.duplicates
	stage.metrics.TargetStatements += len(claude) + len(codex)
	if err := applyAggregateDeltas(ctx, stage.conn, claude, codex); err != nil {
		return outcome, err
	}
	if err := stage.writeLedger(ctx, options.SourceID, time.Now().Unix()); err != nil {
		return outcome, err
	}
	if options.beforeCommit != nil {
		if err := options.beforeCommit(batchNumber); err != nil {
			return outcome, err
		}
	}
	if _, err := stage.conn.ExecContext(ctx, "COMMIT"); err != nil {
		return outcome, err
	}
	stage.metrics.TargetStatements++
	committed = true
	stage.metrics.WriterDuration += time.Since(writerStarted)
	outcome = batchOutcome{stats: stats, committed: true}
	missing, err := stage.missingDigest(ctx)
	if err != nil {
		return outcome, err
	}
	if missing != "" {
		var ordinal int64
		for _, candidate := range stage.batch.Candidates {
			if candidate.Identity.Digest == missing {
				ordinal = candidate.Ordinal
				break
			}
		}
		return outcome, &MergeError{
			Code: ErrVerification, Table: stage.spec.Name, Row: ordinal,
			Err: fmt.Errorf("source identity is missing from target"),
		}
	}
	first, err := seen.AddBatch(ctx, stage.batch.digests())
	if err != nil {
		return outcome, err
	}
	outcome.verified = int64(len(first))
	return outcome, nil
}

func mergeTableStats(result *Result, batch batchStats) {
	byName := make(map[string]*TableStats, len(result.Tables))
	for index := range result.Tables {
		byName[result.Tables[index].Name] = &result.Tables[index]
	}
	for _, spec := range ImportSpecs() {
		incoming := batch.tables[spec.Name]
		if incoming == nil {
			continue
		}
		stats := byName[spec.Name]
		if stats == nil {
			result.Tables = append(result.Tables, TableStats{Name: spec.Name})
			stats = &result.Tables[len(result.Tables)-1]
			byName[spec.Name] = stats
		}
		stats.SourceRows += incoming.SourceRows
		stats.InsertedRows += incoming.InsertedRows
		stats.DuplicateRows += incoming.DuplicateRows
	}
}
