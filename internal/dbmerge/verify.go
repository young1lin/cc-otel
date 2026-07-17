package dbmerge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

type scratchDirProvider interface {
	scratchDir() string
}

func Verify(
	ctx context.Context,
	target *sql.DB,
	source RowSource,
	progress ProgressFunc,
) (int64, error) {
	return verifyBatches(ctx, target, source, progress, nil)
}

func verifyBatches(
	ctx context.Context,
	target *sql.DB,
	source RowSource,
	progress ProgressFunc,
	metrics func(batchMetrics),
) (int64, error) {
	scratchDir := os.TempDir()
	if provider, ok := source.(scratchDirProvider); ok && provider.scratchDir() != "" {
		scratchDir = provider.scratchDir()
	}
	seen, err := newSeenStore(scratchDir)
	if err != nil {
		return 0, &MergeError{Code: ErrVerification, Err: err}
	}
	defer seen.Close()

	var scanned int64
	var verified int64
	err = scanBatches(ctx, source, defaultBatchLimits(Options{}), func(batch rowBatch) error {
		scanned += batch.SourceRows
		first, err := seen.AddBatch(ctx, batch.digests())
		if err != nil {
			return err
		}
		firstBatch := batch.firstCandidates(first)
		batchMetric := batchMetrics{LogicalRows: batch.SourceRows}
		if len(firstBatch.Candidates) > 0 {
			stage, err := newStagedBatch(ctx, target, firstBatch, stageOptions{})
			if err != nil {
				return err
			}
			missing, verifyErr := stage.missingDigest(ctx)
			closeErr := stage.Close(context.Background())
			batchMetric = stage.metrics
			if metrics != nil {
				metrics(batchMetric)
			}
			if verifyErr != nil {
				return verifyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if missing != "" {
				var ordinal int64
				for _, candidate := range firstBatch.Candidates {
					if candidate.Identity.Digest == missing {
						ordinal = candidate.Ordinal
						break
					}
				}
				return &MergeError{
					Code: ErrVerification, Table: batch.Table, Row: ordinal,
					Err: fmt.Errorf("source identity is missing from target"),
				}
			}
			verified += int64(len(firstBatch.Candidates))
		} else if metrics != nil {
			metrics(batchMetric)
		}
		emitProgress(progress, Progress{
			Phase: PhaseVerifying, CurrentTable: batch.Table,
			ProcessedRows: scanned,
		})
		return nil
	})
	if err != nil {
		var mergeErr *MergeError
		if errors.As(err, &mergeErr) {
			return verified, err
		}
		return verified, &MergeError{Code: ErrVerification, Err: err}
	}
	return verified, nil
}

func MergeSQLite(
	ctx context.Context,
	target *sql.DB,
	path string,
	options Options,
) (Result, error) {
	schema, err := ValidateSQLite(ctx, path)
	if err != nil {
		return Result{}, err
	}
	source := NewSQLiteSource(path, schema, options.Window)
	return Import(ctx, target, source, options)
}
