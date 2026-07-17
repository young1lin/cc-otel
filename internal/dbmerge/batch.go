package dbmerge

import "context"

type batchLimits struct {
	Rows  int
	Bytes int64
}

type batchCandidate struct {
	Row         Row
	Identity    Identity
	Ordinal     int64
	Occurrences int64
}

type rowBatch struct {
	Table        string
	Candidates   []batchCandidate
	SourceRows   int64
	PayloadBytes int64
}

func defaultBatchLimits(options Options) batchLimits {
	rows := options.BatchSize
	if rows <= 0 || rows > MaxBatchSize {
		rows = MaxBatchSize
	}
	bytes := options.batchBytes
	if bytes <= 0 || bytes > MaxBatchPayloadBytes {
		bytes = MaxBatchPayloadBytes
	}
	return batchLimits{Rows: rows, Bytes: bytes}
}

func estimateRowBytes(row Row) int64 {
	size := int64(64 + len(row.Table))
	for key, value := range row.Values {
		size += int64(32 + len(key))
		switch value := value.(type) {
		case string:
			size += int64(len(value))
		case []byte:
			size += int64(len(value))
		default:
			size += 16
		}
	}
	return size
}

func scanBatches(
	ctx context.Context,
	source RowSource,
	limits batchLimits,
	yield func(rowBatch) error,
) error {
	if limits.Rows <= 0 || limits.Rows > MaxBatchSize {
		limits.Rows = MaxBatchSize
	}
	if limits.Bytes <= 0 || limits.Bytes > MaxBatchPayloadBytes {
		limits.Bytes = MaxBatchPayloadBytes
	}

	var batch rowBatch
	indexes := make(map[string]int)
	var ordinal int64
	flush := func() error {
		if batch.SourceRows == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(batch); err != nil {
			return err
		}
		batch = rowBatch{}
		indexes = make(map[string]int)
		return nil
	}

	err := source.Scan(ctx, func(row Row) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		rowBytes := estimateRowBytes(row)
		if batch.SourceRows != 0 && (batch.Table != row.Table || batch.SourceRows >= int64(limits.Rows) || batch.PayloadBytes+rowBytes > limits.Bytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		identity, err := IdentityFor(row)
		if err != nil {
			return err
		}
		if batch.SourceRows == 0 {
			batch.Table = row.Table
		}
		batch.SourceRows++
		batch.PayloadBytes += rowBytes
		ordinal++
		if index, ok := indexes[identity.Digest]; ok {
			batch.Candidates[index].Occurrences++
			return nil
		}
		indexes[identity.Digest] = len(batch.Candidates)
		batch.Candidates = append(batch.Candidates, batchCandidate{
			Row: row, Identity: identity, Ordinal: ordinal, Occurrences: 1,
		})
		return nil
	})
	if err != nil {
		return err
	}
	return flush()
}

func (b rowBatch) digests() []string {
	digests := make([]string, len(b.Candidates))
	for i := range b.Candidates {
		digests[i] = b.Candidates[i].Identity.Digest
	}
	return digests
}

func (b rowBatch) firstCandidates(first map[string]struct{}) rowBatch {
	out := rowBatch{Table: b.Table, SourceRows: b.SourceRows, PayloadBytes: b.PayloadBytes}
	for _, candidate := range b.Candidates {
		if _, ok := first[candidate.Identity.Digest]; ok {
			out.Candidates = append(out.Candidates, candidate)
		}
	}
	return out
}
