package dbmerge

import (
	"context"
	"database/sql"
	"maps"
	"strings"
	"testing"
)

func TestSeenStoreAddBatchReturnsOnlyFirstDigests(t *testing.T) {
	store, err := newSeenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, err := store.AddBatch(context.Background(), []string{"a", "b", "a"})
	if err != nil || !maps.Equal(first, map[string]struct{}{"a": {}, "b": {}}) {
		t.Fatalf("first=%v err=%v", first, err)
	}
	first, err = store.AddBatch(context.Background(), []string{"b", "c"})
	if err != nil || !maps.Equal(first, map[string]struct{}{"c": {}}) {
		t.Fatalf("first=%v err=%v", first, err)
	}
}

func TestStagedBatchMarksConflictsAndInsertsNewSet(t *testing.T) {
	target := importTarget(t)
	existing := requestRow("existing", 1)
	insertTestRow(t, target, existing)

	sourceExisting := requestRow("existing", 999)
	sourceExisting.Values["model"] = "source-must-not-win"
	batch := mustBatch(t, sliceSource{sourceExisting, requestRow("new", 2)})
	stage, err := newStagedBatch(context.Background(), target, batch, stageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close(context.Background())

	if err = stage.markNew(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := stage.countNew(context.Background()); err != nil || got != 1 {
		t.Fatalf("new=%d err=%v", got, err)
	}
	if got, err := stage.insertNew(context.Background()); err != nil || got != 1 {
		t.Fatalf("inserted=%d err=%v", got, err)
	}

	var cost int64
	var model string
	if err := target.QueryRow(`SELECT cost_usd, model FROM api_requests WHERE request_id='existing'`).Scan(&cost, &model); err != nil {
		t.Fatal(err)
	}
	if cost != 1 || model != "claude" {
		t.Fatalf("existing target row changed: cost=%d model=%q", cost, model)
	}
	assertCounts(t, target, "api_requests", 2)
}

func TestStagedBatchUsesNullSafeIdentity(t *testing.T) {
	target := importTarget(t)
	existing := Row{Table: "events", Values: map[string]any{
		"timestamp": int64(1), "event_name": "same", "prompt_id": nil,
	}}
	insertTestRow(t, target, existing)
	stage, err := newStagedBatch(context.Background(), target, mustBatch(t, sliceSource{existing}), stageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close(context.Background())
	if err := stage.markNew(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := stage.countNew(context.Background()); err != nil || got != 0 {
		t.Fatalf("NULL-safe match count=%d err=%v", got, err)
	}
}

func TestIdentityPredicateUsesISForEveryKind(t *testing.T) {
	for _, table := range []string{"api_requests", "pending_ttft_spans", "codex_api_requests", "events"} {
		spec, ok := LookupSpec(table)
		if !ok {
			t.Fatalf("missing spec %s", table)
		}
		predicate := identityPredicate(spec, "t", "s")
		if predicate == "" || !strings.Contains(predicate, " IS ") {
			t.Fatalf("%s predicate=%q", table, predicate)
		}
	}
}

func TestStagedBatchSplitsTransportAtVariableLimit(t *testing.T) {
	target := importTarget(t)
	batch := mustBatch(t, sliceSource{
		requestRow("one", 1), requestRow("two", 2), requestRow("three", 3),
	})
	stage, err := newStagedBatch(context.Background(), target, batch, stageOptions{variableLimit: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close(context.Background())
	if stage.metrics.LogicalRows != 3 || stage.metrics.TransportStatements < 2 {
		t.Fatalf("metrics=%+v, want one logical batch split across transport statements", stage.metrics)
	}
	if err := stage.markNew(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := stage.countNew(context.Background()); err != nil || got != 3 {
		t.Fatalf("new=%d err=%v", got, err)
	}
}

func TestStagedBatchNewRowsLedgerAndContainment(t *testing.T) {
	ctx := context.Background()
	target := importTarget(t)
	row := requestRow("staged", 7)
	batch := mustBatch(t, sliceSource{row})
	stage, err := newStagedBatch(ctx, target, batch, stageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.markNew(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := stage.newRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Values["request_id"] != "staged" {
		t.Fatalf("new rows=%+v", rows)
	}
	wantMissing := batch.Candidates[0].Identity.Digest
	if missing, err := stage.missingDigest(ctx); err != nil || missing != wantMissing {
		t.Fatalf("missing=%q want=%q err=%v", missing, wantMissing, err)
	}
	if inserted, err := stage.insertNew(ctx); err != nil || inserted != 1 {
		t.Fatalf("inserted=%d err=%v", inserted, err)
	}
	if missing, err := stage.missingDigest(ctx); err != nil || missing != "" {
		t.Fatalf("missing after insert=%q err=%v", missing, err)
	}
	if err := ensureLedger(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := stage.writeLedger(ctx, "upload:test", 123); err != nil {
		t.Fatal(err)
	}
	assertCounts(t, target, "import_ledger", 1)
	if err := stage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestStagedBatchRequestLookupUsesRequestIDIndex(t *testing.T) {
	target := importTarget(t)
	insertTestRow(t, target, requestRow("existing", 1))
	stage, err := newStagedBatch(context.Background(), target, mustBatch(t, sliceSource{requestRow("existing", 2)}), stageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close(context.Background())

	for name, query := range map[string]string{
		"mark new":    stage.markNewStatement(),
		"containment": stage.missingStatement(),
	} {
		plan := explainQueryPlan(t, stage.conn, query)
		if !strings.Contains(plan, "idx_requests_request_id") {
			t.Fatalf("%s query does not use request_id index:\n%s", name, plan)
		}
	}
}

func TestStagedBatchOrdinaryLookupUsesTimeIndex(t *testing.T) {
	target := importTarget(t)
	row := Row{Table: "tool_decision_events", Values: map[string]any{
		"timestamp": int64(1), "session_id": "s", "tool_name": "Read", "decision": "allow",
	}}
	insertTestRow(t, target, row)
	stage, err := newStagedBatch(context.Background(), target, mustBatch(t, sliceSource{row}), stageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close(context.Background())

	for name, query := range map[string]string{
		"mark new":    stage.markNewStatement(),
		"containment": stage.missingStatement(),
	} {
		plan := explainQueryPlan(t, stage.conn, query)
		if !strings.Contains(plan, "idx_tool_decision_time") {
			t.Fatalf("%s query does not use timestamp index:\n%s", name, plan)
		}
	}
}

func explainQueryPlan(t *testing.T, conn *sql.Conn, query string) string {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "\n")
}

func mustBatch(t *testing.T, source RowSource) rowBatch {
	t.Helper()
	var batches []rowBatch
	err := scanBatches(context.Background(), source, defaultBatchLimits(Options{}), func(batch rowBatch) error {
		batches = append(batches, batch)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	return batches[0]
}
