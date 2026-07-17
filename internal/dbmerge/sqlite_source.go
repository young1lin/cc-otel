package dbmerge

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
)

var sqliteHeader = []byte("SQLite format 3\x00")

type SchemaInfo struct {
	Present       map[string]map[string]bool
	IgnoredTables []string
	Warnings      []string
}

type SQLiteSource struct {
	path   string
	schema SchemaInfo
	window *TimeWindow
}

func NewSQLiteSource(path string, schema SchemaInfo, window *TimeWindow) *SQLiteSource {
	return &SQLiteSource{path: path, schema: schema, window: window}
}

func (s *SQLiteSource) scratchDir() string {
	return filepath.Dir(s.path)
}

func openImmutable(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?mode=ro&immutable=1&_busy_timeout=5000",
		filepath.ToSlash(filepath.Clean(path)),
	)
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	return d, nil
}

func ValidateSQLite(ctx context.Context, path string) (SchemaInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return SchemaInfo{}, &MergeError{Code: ErrInvalidSQLite, Err: err}
	}
	header := make([]byte, len(sqliteHeader))
	_, readErr := io.ReadFull(file, header)
	closeErr := file.Close()
	if readErr != nil || !bytes.Equal(header, sqliteHeader) {
		if readErr == nil {
			readErr = fmt.Errorf("SQLite header does not match")
		}
		return SchemaInfo{}, &MergeError{Code: ErrInvalidSQLite, Err: readErr}
	}
	if closeErr != nil {
		return SchemaInfo{}, &MergeError{Code: ErrInvalidSQLite, Err: closeErr}
	}

	d, err := openImmutable(path)
	if err != nil {
		return SchemaInfo{}, &MergeError{Code: ErrInvalidSQLite, Err: err}
	}
	defer d.Close()
	if err := d.PingContext(ctx); err != nil {
		return SchemaInfo{}, &MergeError{Code: ErrInvalidSQLite, Err: err}
	}
	if err := checkIntegrity(ctx, d); err != nil {
		return SchemaInfo{}, err
	}

	tables, err := readSchema(ctx, d)
	if err != nil {
		return SchemaInfo{}, &MergeError{Code: ErrUnsupportedSchema, Err: err}
	}
	return validateSchema(tables)
}

func checkIntegrity(ctx context.Context, d *sql.DB) error {
	rows, err := d.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return &MergeError{Code: ErrIntegrityCheck, Err: err}
	}
	defer rows.Close()
	var results []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return &MergeError{Code: ErrIntegrityCheck, Err: err}
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return &MergeError{Code: ErrIntegrityCheck, Err: err}
	}
	if len(results) != 1 || results[0] != "ok" {
		return &MergeError{Code: ErrIntegrityCheck, Err: fmt.Errorf("quick_check returned %q", results)}
	}
	return nil
}

func readSchema(ctx context.Context, d *sql.DB) (map[string][]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	tables := make(map[string][]string, len(names))
	for _, name := range names {
		if strings.HasPrefix(name, "sqlite_") {
			tables[name] = nil
			continue
		}
		columnRows, err := d.QueryContext(ctx, `PRAGMA table_info("`+strings.ReplaceAll(name, `"`, `""`)+`")`)
		if err != nil {
			return nil, err
		}
		var columns []string
		for columnRows.Next() {
			var cid, notNull, primaryKey int
			var column, dataType string
			var defaultValue any
			if err := columnRows.Scan(&cid, &column, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
				columnRows.Close()
				return nil, err
			}
			columns = append(columns, column)
		}
		if err := columnRows.Close(); err != nil {
			return nil, err
		}
		tables[name] = columns
	}
	return tables, nil
}

func validateSchema(tables map[string][]string) (SchemaInfo, error) {
	info := SchemaInfo{Present: make(map[string]map[string]bool)}
	known := make(map[string]TableSpec, len(registry))
	for _, spec := range registry {
		known[spec.Name] = spec
	}

	for name, actual := range tables {
		if strings.HasPrefix(name, "sqlite_") {
			if name == "sqlite_sequence" {
				info.IgnoredTables = append(info.IgnoredTables, name)
			}
			continue
		}
		if spec, ok := known[name]; ok {
			allowed := append([]string{"id"}, spec.Columns...)
			if err := validateColumns(name, actual, allowed); err != nil {
				if name == "pending_ttft_spans" {
					withoutRequestID := removeString(allowed, "request_id")
					if legacyErr := validateColumns(name, actual, withoutRequestID); legacyErr == nil {
						info.Warnings = append(info.Warnings, "Legacy pending_ttft_spans has no request_id; empty values will be used.")
						info.Present[name] = columnSet(actual)
						continue
					}
				}
				if name == "codex_api_requests" {
					if isLegacyCodexColumns(actual, allowed) {
						info.Warnings = appendUnique(info.Warnings, "Legacy Codex tool_tokens will be mapped to total_tokens.")
						info.Present[name] = columnSet(actual)
						continue
					}
				}
				return SchemaInfo{}, err
			}
			info.Present[name] = columnSet(actual)
			continue
		}

		expected, ok := ignoredColumns(name)
		if !ok {
			return SchemaInfo{}, unsupported(name, "unknown table")
		}
		if name == "codex_daily_model_agg" && isLegacyCodexColumns(actual, expected) {
			info.Warnings = appendUnique(info.Warnings, "Legacy Codex tool_tokens will be mapped to total_tokens.")
		} else if err := validateColumns(name, actual, expected); err != nil {
			return SchemaInfo{}, err
		}
		info.IgnoredTables = append(info.IgnoredTables, name)
	}

	claudeCore := []string{
		"api_requests", "user_prompt_events", "tool_decision_events", "tool_result_events",
		"api_error_events", "otel_metric_points", "events", "raw_otlp_events",
	}
	for _, name := range append(claudeCore, "daily_model_agg") {
		if _, ok := tables[name]; !ok {
			return SchemaInfo{}, unsupported(name, "missing Claude core table")
		}
	}

	codexGroup := []string{
		"codex_api_requests", "codex_daily_model_agg", "codex_user_prompt_events",
		"codex_tool_decision_events", "codex_tool_result_events", "codex_events", "codex_raw_otlp_events",
	}
	presentCodex := 0
	for _, name := range codexGroup {
		if _, ok := tables[name]; ok {
			presentCodex++
		}
	}
	if presentCodex != 0 && presentCodex != len(codexGroup) {
		return SchemaInfo{}, unsupported("codex", fmt.Sprintf("incomplete Codex table group (%d/%d)", presentCodex, len(codexGroup)))
	}

	_, geminiRequests := tables["gemini_api_requests"]
	_, geminiDaily := tables["gemini_daily_model_agg"]
	if geminiRequests != geminiDaily {
		return SchemaInfo{}, unsupported("gemini", "incomplete Gemini table pair")
	}
	if geminiRequests {
		info.Warnings = append(info.Warnings, "Legacy Gemini tables are recognized but will not be imported.")
	}

	sort.Strings(info.IgnoredTables)
	return info, nil
}

func validateColumns(table string, actual, expected []string) error {
	want := make(map[string]bool, len(expected))
	for _, column := range expected {
		want[column] = true
	}
	got := columnSet(actual)
	for _, column := range actual {
		if !want[column] {
			return unsupported(table+"."+column, "unknown column")
		}
	}
	for _, column := range expected {
		if !got[column] {
			return unsupported(table+"."+column, "missing column")
		}
	}
	return nil
}

func isLegacyCodexColumns(actual, canonical []string) bool {
	actualSet := columnSet(actual)
	if !actualSet["tool_tokens"] {
		return false
	}
	want := append([]string(nil), canonical...)
	if !actualSet["total_tokens"] {
		want = removeString(want, "total_tokens")
	}
	want = append(want, "tool_tokens")
	return validateColumns("codex", actual, want) == nil
}

func unsupported(name, reason string) error {
	return &MergeError{Code: ErrUnsupportedSchema, Err: fmt.Errorf("%s: %s", name, reason)}
}

func columnSet(columns []string) map[string]bool {
	out := make(map[string]bool, len(columns))
	for _, column := range columns {
		out[column] = true
	}
	return out
}

func removeString(values []string, target string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func selectExpressions(spec TableSpec, columns map[string]bool) []string {
	out := make([]string, 0, len(spec.Columns))
	for _, column := range spec.Columns {
		switch {
		case spec.Name == "pending_ttft_spans" && column == "request_id" && !columns[column]:
			out = append(out, `'' AS request_id`)
		case spec.Name == "codex_api_requests" && column == "total_tokens" && columns["total_tokens"] && columns["tool_tokens"]:
			out = append(out, `CASE WHEN total_tokens != 0 THEN total_tokens WHEN tool_tokens > 0 THEN tool_tokens ELSE total_tokens END AS total_tokens`)
		case spec.Name == "codex_api_requests" && column == "total_tokens" && !columns["total_tokens"] && columns["tool_tokens"]:
			out = append(out, "tool_tokens AS total_tokens")
		default:
			out = append(out, quoteIdent(column))
		}
	}
	return out
}

func (s *SQLiteSource) Scan(ctx context.Context, yield func(Row) error) error {
	d, err := openImmutable(s.path)
	if err != nil {
		return err
	}
	defer d.Close()

	for _, spec := range ImportSpecs() {
		columns, present := s.schema.Present[spec.Name]
		if !present {
			continue
		}
		query, args := scanQuery(spec, columns, s.window)
		rows, err := d.QueryContext(ctx, query, args...)
		if err != nil {
			return &MergeError{Code: ErrInspection, Table: spec.Name, Err: err}
		}
		err = scanRows(rows, spec, yield)
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (s *SQLiteSource) Count(ctx context.Context) (map[string]int64, error) {
	d, err := openImmutable(s.path)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	counts := make(map[string]int64)
	for _, spec := range ImportSpecs() {
		if _, present := s.schema.Present[spec.Name]; !present {
			continue
		}
		query, args := countQuery(spec, s.window)
		var count int64
		if err := d.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			return nil, &MergeError{Code: ErrInspection, Table: spec.Name, Err: err}
		}
		counts[spec.Name] = count
	}
	return counts, nil
}

func scanQuery(spec TableSpec, columns map[string]bool, window *TimeWindow) (string, []any) {
	query := "SELECT " + strings.Join(selectExpressions(spec, columns), ", ") + " FROM " + quoteIdent(spec.Name)
	where, args := windowClause(spec, window)
	query += where
	query += " ORDER BY id ASC"
	return query, args
}

func countQuery(spec TableSpec, window *TimeWindow) (string, []any) {
	where, args := windowClause(spec, window)
	return "SELECT COUNT(*) FROM " + quoteIdent(spec.Name) + where, args
}

func windowClause(spec TableSpec, window *TimeWindow) (string, []any) {
	if window == nil {
		return "", nil
	}
	if spec.Kind == KindPendingTTFT {
		return " WHERE (span_end_unix BETWEEN ? AND ?) OR (created_unix BETWEEN ? AND ?)", []any{
			window.FromUnix, window.ToUnix, window.FromUnix, window.ToUnix,
		}
	}
	return " WHERE timestamp BETWEEN ? AND ?", []any{window.FromUnix, window.ToUnix}
}

func scanRows(rows *sql.Rows, spec TableSpec, yield func(Row) error) error {
	for rows.Next() {
		values := make([]any, len(spec.Columns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return &MergeError{Code: ErrInspection, Table: spec.Name, Err: err}
		}
		row := Row{Table: spec.Name, Values: make(map[string]any, len(values))}
		for i, column := range spec.Columns {
			row.Values[column] = normalizeScannedValue(values[i])
		}
		if err := yield(row); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return &MergeError{Code: ErrInspection, Table: spec.Name, Err: err}
	}
	return nil
}

func normalizeScannedValue(value any) any {
	switch value := value.(type) {
	case []byte:
		return string(value)
	case float64:
		if !math.IsInf(value, 0) && !math.IsNaN(value) && math.Trunc(value) == value && value >= math.MinInt64 && value <= math.MaxInt64 {
			return int64(value)
		}
	}
	return value
}
