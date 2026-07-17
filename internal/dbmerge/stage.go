package dbmerge

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

const stageTable = `_dbmerge_batch`

type stageOptions struct {
	variableLimit int
}

type stagedBatch struct {
	conn     *sql.Conn
	spec     TableSpec
	batch    rowBatch
	metrics  batchMetrics
	once     sync.Once
	closeErr error
}

func variableLimit(conn *sql.Conn) (int, error) {
	var limit int
	err := conn.Raw(func(driverConn any) error {
		raw, ok := driverConn.(sqliteDriver.Conn)
		if !ok {
			return fmt.Errorf("sqlite connection has unexpected type %T", driverConn)
		}
		limit = raw.Raw().Limit(sqlite3.LIMIT_VARIABLE_NUMBER, -1)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, fmt.Errorf("invalid SQLite variable limit %d", limit)
	}
	return limit, nil
}

func newStagedBatch(
	ctx context.Context,
	target *sql.DB,
	batch rowBatch,
	options stageOptions,
) (*stagedBatch, error) {
	spec, ok := LookupSpec(batch.Table)
	if !ok {
		return nil, fmt.Errorf("unknown import table %q", batch.Table)
	}
	conn, err := target.Conn(ctx)
	if err != nil {
		return nil, err
	}
	stage := &stagedBatch{
		conn:  conn,
		spec:  spec,
		batch: batch,
		metrics: batchMetrics{
			LogicalRows: batch.SourceRows,
		},
	}
	fail := func(err error) (*stagedBatch, error) {
		_ = stage.Close(context.Background())
		return nil, err
	}
	limit, err := variableLimit(conn)
	if err != nil {
		return fail(err)
	}
	if options.variableLimit > 0 && options.variableLimit < limit {
		limit = options.variableLimit
	}

	definitions := []string{
		`_merge_digest TEXT PRIMARY KEY`,
		`_merge_ordinal INTEGER NOT NULL`,
		`_merge_occurrences INTEGER NOT NULL`,
		`_merge_new INTEGER NOT NULL DEFAULT 0`,
		`_merge_key_kind INTEGER NOT NULL`,
	}
	for _, column := range spec.Columns {
		definitions = append(definitions, quoteIdent(column))
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS temp.`+stageTable); err != nil {
		return fail(err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE `+stageTable+` (`+strings.Join(definitions, ",")+`)`); err != nil {
		return fail(err)
	}
	if len(batch.Candidates) == 0 {
		return stage, nil
	}

	columnsPerRow := len(spec.Columns) + 4
	rowsPerStatement := limit / columnsPerRow
	if rowsPerStatement < 1 {
		return fail(fmt.Errorf("SQLite variable limit %d is below the %d values required for %s", limit, columnsPerRow, spec.Name))
	}
	insertColumns := []string{`_merge_digest`, `_merge_ordinal`, `_merge_occurrences`, `_merge_key_kind`}
	for _, column := range spec.Columns {
		insertColumns = append(insertColumns, quoteIdent(column))
	}
	rowPlaceholder := "(" + strings.TrimSuffix(strings.Repeat("?,", columnsPerRow), ",") + ")"
	for start := 0; start < len(batch.Candidates); start += rowsPerStatement {
		end := min(start+rowsPerStatement, len(batch.Candidates))
		placeholders := make([]string, end-start)
		args := make([]any, 0, (end-start)*columnsPerRow)
		for i, candidate := range batch.Candidates[start:end] {
			placeholders[i] = rowPlaceholder
			args = append(args,
				candidate.Identity.Digest,
				candidate.Ordinal,
				candidate.Occurrences,
				identityKeyKind(candidate.Identity),
			)
			for _, column := range spec.Columns {
				args = append(args, candidate.Row.Values[column])
			}
		}
		query := `INSERT INTO temp.` + stageTable + ` (` + strings.Join(insertColumns, ",") + `) VALUES ` + strings.Join(placeholders, ",")
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return fail(err)
		}
		stage.metrics.TransportStatements++
	}
	return stage, nil
}

func identityKeyKind(identity Identity) int {
	if len(identity.Columns) == 1 && identity.Columns[0] == "request_id" {
		return 1
	}
	return 0
}

func identityPredicate(spec TableSpec, targetAlias, stageAlias string) string {
	switch spec.Kind {
	case KindClaudeRequest:
		return "((" + stageAlias + "._merge_key_kind = 1 AND " + identityColumnsPredicate([]string{"request_id"}, targetAlias, stageAlias) + ") OR (" +
			stageAlias + "._merge_key_kind = 0 AND " + identityColumnsPredicate(cols("timestamp session_id prompt_id event_sequence model input_tokens output_tokens duration_ms"), targetAlias, stageAlias) + "))"
	case KindPendingTTFT:
		return "((" + stageAlias + "._merge_key_kind = 1 AND " + identityColumnsPredicate([]string{"request_id"}, targetAlias, stageAlias) + ") OR (" +
			stageAlias + "._merge_key_kind = 0 AND " + identityColumnsPredicate(cols("session_id model span_end_unix ttft_ms"), targetAlias, stageAlias) + "))"
	case KindCodexRequest:
		return identityColumnsPredicate(cols("timestamp session_id model input_tokens output_tokens duration_ms"), targetAlias, stageAlias)
	default:
		return identityColumnsPredicate(spec.Columns, targetAlias, stageAlias)
	}
}

func identityColumnsPredicate(columns []string, targetAlias, stageAlias string) string {
	clauses := make([]string, len(columns))
	for i, column := range columns {
		quoted := quoteIdent(column)
		clauses[i] = targetAlias + "." + quoted + " IS " + stageAlias + "." + quoted
	}
	return strings.Join(clauses, " AND ")
}

func identityExistsExpression(spec TableSpec, targetAlias, stageAlias string) string {
	table := `main.` + quoteIdent(spec.Name)
	exists := func(predicate, index string) string {
		from := table + ` AS ` + targetAlias
		if index != "" {
			from += ` INDEXED BY "` + index + `"`
		}
		return `EXISTS (SELECT 1 FROM ` + from + ` WHERE ` + predicate + `)`
	}
	switch spec.Kind {
	case KindClaudeRequest:
		request := identityColumnsPredicate([]string{"request_id"}, targetAlias, stageAlias) + ` AND ` + targetAlias + `.` + quoteIdent("request_id") + ` != ''`
		fallback := identityColumnsPredicate(cols("timestamp session_id prompt_id event_sequence model input_tokens output_tokens duration_ms"), targetAlias, stageAlias)
		return `CASE WHEN ` + stageAlias + `._merge_key_kind = 1 THEN ` + exists(request, "idx_requests_request_id") + ` ELSE ` + exists(fallback, "idx_requests_timestamp") + ` END`
	case KindPendingTTFT:
		request := identityColumnsPredicate([]string{"request_id"}, targetAlias, stageAlias)
		fallback := identityColumnsPredicate(cols("session_id model span_end_unix ttft_ms"), targetAlias, stageAlias)
		return `CASE WHEN ` + stageAlias + `._merge_key_kind = 1 THEN ` + exists(request, "") + ` ELSE ` + exists(fallback, "") + ` END`
	case KindCodexRequest:
		return exists(identityColumnsPredicate(cols("timestamp session_id model input_tokens output_tokens duration_ms"), targetAlias, stageAlias), identityLookupIndex(spec.Name))
	default:
		return exists(identityColumnsPredicate(spec.Columns, targetAlias, stageAlias), identityLookupIndex(spec.Name))
	}
}

var identityLookupIndexes = map[string]string{
	"user_prompt_events":         "idx_user_prompt_time",
	"tool_decision_events":       "idx_tool_decision_time",
	"tool_result_events":         "idx_tool_result_time",
	"api_error_events":           "idx_api_error_time",
	"otel_metric_points":         "idx_metric_points_time",
	"events":                     "idx_events_time",
	"raw_otlp_events":            "idx_raw_otlp_time",
	"codex_api_requests":         "idx_codex_requests_timestamp",
	"codex_user_prompt_events":   "idx_codex_user_prompt_time",
	"codex_tool_decision_events": "idx_codex_tool_dec_time",
	"codex_tool_result_events":   "idx_codex_tool_res_time",
	"codex_raw_otlp_events":      "idx_codex_raw_time",
}

func identityLookupIndex(table string) string { return identityLookupIndexes[table] }

func (s *stagedBatch) markNewStatement() string {
	return `UPDATE temp.` + stageTable + ` AS s
		SET _merge_new = CASE WHEN ` + identityExistsExpression(s.spec, "t", "s") + ` THEN 0 ELSE 1 END`
}

func (s *stagedBatch) markNew(ctx context.Context) error {
	_, err := s.conn.ExecContext(ctx, s.markNewStatement())
	s.metrics.TargetStatements++
	return err
}

func (s *stagedBatch) countNew(ctx context.Context) (int64, error) {
	var count int64
	err := s.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM temp.`+stageTable+` WHERE _merge_new = 1`).Scan(&count)
	return count, err
}

func (s *stagedBatch) insertNew(ctx context.Context) (int64, error) {
	expected, err := s.countNew(ctx)
	if err != nil {
		return 0, err
	}
	columns := make([]string, len(s.spec.Columns))
	selected := make([]string, len(s.spec.Columns))
	for i, column := range s.spec.Columns {
		columns[i] = quoteIdent(column)
		selected[i] = "s." + quoteIdent(column)
	}
	query := `INSERT INTO main.` + quoteIdent(s.spec.Name) + ` (` + strings.Join(columns, ",") + `)
		SELECT ` + strings.Join(selected, ",") + ` FROM temp.` + stageTable + ` AS s
		WHERE s._merge_new = 1 ORDER BY s._merge_ordinal`
	result, err := s.conn.ExecContext(ctx, query)
	s.metrics.TargetStatements++
	if err != nil {
		return 0, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if inserted != expected {
		return inserted, fmt.Errorf("inserted %d %s rows, expected %d", inserted, s.spec.Name, expected)
	}
	return inserted, nil
}

func (s *stagedBatch) newRows(ctx context.Context) ([]Row, error) {
	selected := make([]string, len(s.spec.Columns))
	for i, column := range s.spec.Columns {
		selected[i] = quoteIdent(column)
	}
	rows, err := s.conn.QueryContext(ctx,
		`SELECT `+strings.Join(selected, ",")+` FROM temp.`+stageTable+` WHERE _merge_new = 1 ORDER BY _merge_ordinal`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		values := make([]any, len(s.spec.Columns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := Row{Table: s.spec.Name, Values: make(map[string]any, len(values))}
		for i, column := range s.spec.Columns {
			row.Values[column] = normalizeScannedValue(values[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *stagedBatch) writeLedger(ctx context.Context, sourceID string, importedAt int64) error {
	_, err := s.conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO main.import_ledger (uuid, imported_at, source_db, table_name)
		SELECT _merge_digest, ?, ?, ? FROM temp.`+stageTable,
		importedAt, sourceID, s.spec.Name,
	)
	s.metrics.TargetStatements++
	return err
}

func (s *stagedBatch) missingDigest(ctx context.Context) (string, error) {
	var digest string
	err := s.conn.QueryRowContext(ctx, s.missingStatement()).Scan(&digest)
	s.metrics.TargetStatements++
	if err == sql.ErrNoRows {
		return "", nil
	}
	return digest, err
}

func (s *stagedBatch) missingStatement() string {
	return `SELECT s._merge_digest FROM temp.` + stageTable + ` AS s
		WHERE NOT (` + identityExistsExpression(s.spec, "t", "s") + `)
		ORDER BY s._merge_ordinal LIMIT 1`
}

func (s *stagedBatch) Close(ctx context.Context) error {
	s.once.Do(func() {
		if s.conn == nil {
			return
		}
		_, dropErr := s.conn.ExecContext(ctx, `DROP TABLE IF EXISTS temp.`+stageTable)
		closeErr := s.conn.Close()
		if dropErr != nil {
			s.closeErr = dropErr
		} else {
			s.closeErr = closeErr
		}
	})
	return s.closeErr
}
