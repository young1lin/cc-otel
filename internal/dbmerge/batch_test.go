package dbmerge

import (
	"context"
	"strings"
	"testing"
)

func TestDefaultBatchLimitsUseTenThousandRowsAnd128MiB(t *testing.T) {
	got := defaultBatchLimits(Options{})
	if got.Rows != 10_000 || got.Bytes != 128<<20 {
		t.Fatalf("limits = %+v", got)
	}
}

func TestScanBatchesFlushesOnRowsAndTableChange(t *testing.T) {
	rows := sliceSource{
		requestRow("a", 1), requestRow("b", 1), requestRow("c", 1),
		{Table: "events", Values: map[string]any{"timestamp": int64(4), "event_name": "event"}},
	}
	var got []rowBatch
	err := scanBatches(context.Background(), rows, batchLimits{Rows: 2, Bytes: 1 << 20}, func(batch rowBatch) error {
		got = append(got, batch)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].SourceRows != 2 || got[1].SourceRows != 1 || got[2].Table != "events" {
		t.Fatalf("batches = %+v", got)
	}
}

func TestScanBatchesFlushesBeforeByteLimitAndAllowsOversizedRow(t *testing.T) {
	row := Row{Table: "raw_otlp_events", Values: map[string]any{
		"timestamp": int64(1), "event_type": "log", "raw_json": strings.Repeat("x", 256),
	}}
	var got []rowBatch
	err := scanBatches(context.Background(), sliceSource{row, row}, batchLimits{Rows: 10, Bytes: 128}, func(batch rowBatch) error {
		got = append(got, batch)
		return nil
	})
	if err != nil || len(got) != 2 || got[0].SourceRows != 1 || got[1].SourceRows != 1 {
		t.Fatalf("batches=%+v err=%v", got, err)
	}
}

func TestScanBatchesCollapsesIdentityButCountsOccurrences(t *testing.T) {
	row := requestRow("same", 1)
	var got rowBatch
	err := scanBatches(context.Background(), sliceSource{row, row}, batchLimits{Rows: 10, Bytes: 1 << 20}, func(batch rowBatch) error {
		got = batch
		return nil
	})
	if err != nil || got.SourceRows != 2 || len(got.Candidates) != 1 || got.Candidates[0].Occurrences != 2 {
		t.Fatalf("batch=%+v err=%v", got, err)
	}
}
