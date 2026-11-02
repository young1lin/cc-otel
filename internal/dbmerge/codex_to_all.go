package dbmerge

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/young1lin/cc-otel/internal/db"
)

// Codex-to-All migration ("AllClient"). Copies the read-only codex_* archive
// tables inside the LIVE database into the unified pool (api_requests,
// user_prompt_events, tool_decision_events, tool_result_events,
// raw_otlp_events) with service_name='codex', reusing the standard Import
// pipeline: staged identity dedup, import_ledger UUIDs (source
// "codex-migration"), and incremental daily_model_agg deltas. The archive
// tables are never modified or deleted, so the migration is repeatable and
// reversible.
//
// Token semantics are normalized during the copy: the Codex archive stores
// input_tokens INCLUSIVE of cached input, the unified pool stores the four
// categories disjointly (Claude semantics), so input becomes
// max(0, input - cache_read - cache_creation) exactly like the live receiver
// path does since the merge.

// codexSourceID labels every import_ledger row this migration writes.
const codexSourceID = "codex-migration"

type codexTablePlan struct {
	source string
	target string
}

var codexToAllPlans = []codexTablePlan{
	{"codex_api_requests", "api_requests"},
	{"codex_user_prompt_events", "user_prompt_events"},
	{"codex_tool_decision_events", "tool_decision_events"},
	{"codex_tool_result_events", "tool_result_events"},
	{"codex_raw_otlp_events", "raw_otlp_events"},
}

// codexToAllSource streams the archive tables from the live DB, transforming
// every row into its unified-table shape. It implements RowSource so the
// standard Import pipeline (batching, staging, dedup, ledger, agg deltas)
// applies unchanged.
//
// Reads go through a DEDICATED read-only connection, never the target's
// pool. The migration reads and writes the same file; streaming the archive
// SELECT on one pooled connection while staged INSERTs run on another
// reproducibly corrupts import_ledger pages with the ncruces driver
// (offline-verified: PRAGMA quick_check reports "Rowid out of order" and
// the affected ledger rows vanish). A separate read-only handle — the same
// convention SQLiteSource uses for imports from other files — sidesteps the
// concurrent same-pool access pattern and is safe under WAL. immutable=1 is
// deliberately NOT used: the file is being written while it is read.
type codexToAllSource struct {
	db  *sql.DB
	own bool // db was opened here and must be closed by Close
}

// newCodexToAllSource opens the migration source. When the target is a file
// database it reopens that file read-only on a dedicated connection; for
// :memory: targets (tests) it falls back to the target handle, where the
// corruption pattern cannot arise from a separate connection.
func newCodexToAllSource(ctx context.Context, target *sql.DB) (*codexToAllSource, error) {
	rows, err := target.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var file string
	for rows.Next() {
		var seq int
		var name, f string
		if err := rows.Scan(&seq, &name, &f); err != nil {
			return nil, err
		}
		if name == "main" {
			file = f
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if file == "" {
		return &codexToAllSource{db: target}, nil
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", filepath.ToSlash(filepath.Clean(file)))
	ro, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	ro.SetMaxOpenConns(1)
	return &codexToAllSource{db: ro, own: true}, nil
}

// Close releases the dedicated read-only connection, if one was opened.
func (s *codexToAllSource) Close() error {
	if !s.own {
		return nil
	}
	return s.db.Close()
}

func tableExists(ctx context.Context, d *sql.DB, name string) (bool, error) {
	var one int
	err := d.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *codexToAllSource) Scan(ctx context.Context, yield func(Row) error) error {
	for _, plan := range codexToAllPlans {
		exists, err := tableExists(ctx, s.db, plan.source)
		if err != nil {
			return &MergeError{Code: ErrInspection, Table: plan.source, Err: err}
		}
		if !exists {
			continue
		}
		var scan func(*sql.Rows) (Row, error)
		var query string
		switch plan.source {
		case "codex_api_requests":
			query = `
				SELECT timestamp, session_id, user_id, model,
				       input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
				       reasoning_tokens, total_tokens, cost_usd, duration_ms, ttft_ms,
				       http_status, endpoint, conversation_id,
				       event_name, event_sequence, terminal_type,
				       service_version, host_arch, os_type, os_version, error_message
				FROM codex_api_requests ORDER BY id ASC`
			scan = func(rows *sql.Rows) (Row, error) {
				var v struct {
					Timestamp, Input, Output, CacheRead, CacheCreate                 int64
					Reasoning, Total, Cost, Duration, TTFT, HTTPStatus, EventSequence int64
					SessionID, UserID, Model                                          string
					Endpoint, ConversationID, EventName, TerminalType                 string
					ServiceVersion, HostArch, OSType, OSVersion, ErrorMessage         string
				}
				if err := rows.Scan(&v.Timestamp, &v.SessionID, &v.UserID, &v.Model,
					&v.Input, &v.Output, &v.CacheRead, &v.CacheCreate,
					&v.Reasoning, &v.Total, &v.Cost, &v.Duration, &v.TTFT,
					&v.HTTPStatus, &v.Endpoint, &v.ConversationID,
					&v.EventName, &v.EventSequence, &v.TerminalType,
					&v.ServiceVersion, &v.HostArch, &v.OSType, &v.OSVersion, &v.ErrorMessage,
				); err != nil {
					return Row{}, err
				}
				input := v.Input - v.CacheRead - v.CacheCreate
				if input < 0 {
					input = 0
				}
				return Row{Table: plan.target, Values: map[string]any{
					"timestamp": v.Timestamp, "session_id": v.SessionID, "user_id": v.UserID,
					"prompt_id": "", "prompt_length": int64(0),
					"model": v.Model, "actual_model": "",
					"input_tokens": input, "output_tokens": v.Output,
					"cache_read_tokens": v.CacheRead, "cache_creation_tokens": v.CacheCreate,
					"cost_usd": v.Cost, "duration_ms": v.Duration, "ttft_ms": v.TTFT, "request_id": "",
					"event_name": v.EventName, "event_sequence": v.EventSequence, "speed": "",
					"terminal_type": v.TerminalType, "tool_name": "", "decision": "", "source": "",
					"service_name": db.CodexServiceName, "service_version": v.ServiceVersion,
					"host_arch": v.HostArch, "os_type": v.OSType, "os_version": v.OSVersion,
					"error_type": "", "error_message": v.ErrorMessage, "error_code": int64(0), "error_retryable": 0,
					"reasoning_tokens": v.Reasoning, "total_tokens": v.Total,
					"http_status": v.HTTPStatus, "endpoint": v.Endpoint, "conversation_id": v.ConversationID,
				}}, nil
			}
		case "codex_user_prompt_events":
			query = `
				SELECT timestamp, session_id, conversation_id, prompt_text, prompt_length,
				       event_sequence, terminal_type, service_version
				FROM codex_user_prompt_events ORDER BY id ASC`
			scan = func(rows *sql.Rows) (Row, error) {
				var timestamp, promptLength, eventSequence int64
				var sessionID, conversationID, promptText, terminalType, serviceVersion string
				if err := rows.Scan(&timestamp, &sessionID, &conversationID, &promptText, &promptLength,
					&eventSequence, &terminalType, &serviceVersion); err != nil {
					return Row{}, err
				}
				return Row{Table: plan.target, Values: map[string]any{
					"timestamp": timestamp, "session_id": sessionID, "user_id": "", "prompt_id": "",
					"prompt_text": promptText, "prompt_length": promptLength,
					"event_sequence": eventSequence, "terminal_type": terminalType,
					"service_name": db.CodexServiceName, "service_version": serviceVersion,
					"host_arch": "", "os_type": "", "os_version": "",
					"conversation_id": conversationID,
				}}, nil
			}
		case "codex_tool_decision_events":
			query = `
				SELECT timestamp, session_id, conversation_id, tool_name, call_id,
				       decision, source, event_sequence, terminal_type
				FROM codex_tool_decision_events ORDER BY id ASC`
			scan = func(rows *sql.Rows) (Row, error) {
				var timestamp, eventSequence int64
				var sessionID, conversationID, toolName, callID, decision, source, terminalType string
				if err := rows.Scan(&timestamp, &sessionID, &conversationID, &toolName, &callID,
					&decision, &source, &eventSequence, &terminalType); err != nil {
					return Row{}, err
				}
				return Row{Table: plan.target, Values: map[string]any{
					"timestamp": timestamp, "session_id": sessionID, "user_id": "", "prompt_id": "",
					"event_sequence": eventSequence, "tool_name": toolName,
					"decision": decision, "source": source, "terminal_type": terminalType,
					"conversation_id": conversationID, "call_id": callID,
					"service_name": db.CodexServiceName,
				}}, nil
			}
		case "codex_tool_result_events":
			query = `
				SELECT timestamp, session_id, conversation_id, tool_name, call_id,
				       success, duration_ms, arguments_length, output_length,
				       tool_origin, mcp_server, event_sequence, terminal_type
				FROM codex_tool_result_events ORDER BY id ASC`
			scan = func(rows *sql.Rows) (Row, error) {
				var timestamp, eventSequence, success, duration, argumentsLength, outputLength int64
				var sessionID, conversationID, toolName, callID, toolOrigin, mcpServer, terminalType string
				if err := rows.Scan(&timestamp, &sessionID, &conversationID, &toolName, &callID,
					&success, &duration, &argumentsLength, &outputLength,
					&toolOrigin, &mcpServer, &eventSequence, &terminalType); err != nil {
					return Row{}, err
				}
				return Row{Table: plan.target, Values: map[string]any{
					"timestamp": timestamp, "session_id": sessionID, "user_id": "", "prompt_id": "",
					"event_sequence": eventSequence, "tool_name": toolName,
					"success": success, "duration_ms": duration, "tool_result_size_bytes": outputLength,
					"decision_source": "", "decision_type": "", "terminal_type": terminalType,
					"conversation_id": conversationID, "call_id": callID,
					"arguments_length": argumentsLength, "mcp_server": mcpServer,
					"tool_origin": toolOrigin, "service_name": db.CodexServiceName,
				}}, nil
			}
		default: // codex_raw_otlp_events
			query = `SELECT timestamp, event_type, raw_json FROM codex_raw_otlp_events ORDER BY id ASC`
			scan = func(rows *sql.Rows) (Row, error) {
				var timestamp int64
				var eventType, rawJSON string
				if err := rows.Scan(&timestamp, &eventType, &rawJSON); err != nil {
					return Row{}, err
				}
				return Row{Table: plan.target, Values: map[string]any{
					"timestamp": timestamp, "event_type": eventType, "raw_json": rawJSON,
				}}, nil
			}
		}

		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return &MergeError{Code: ErrInspection, Table: plan.source, Err: err}
		}
		for rows.Next() {
			row, scanErr := scan(rows)
			if scanErr != nil {
				rows.Close()
				return &MergeError{Code: ErrInspection, Table: plan.source, Err: scanErr}
			}
			if err := yield(row); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return &MergeError{Code: ErrInspection, Table: plan.source, Err: err}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// CodexMigrationCounts is the per-table preview of what a migration would do.
type CodexMigrationCounts struct {
	SourceTable string `json:"source_table"`
	TargetTable string `json:"target_table"`
	SourceRows  int64 `json:"source_rows"`
	NewRows     int64 `json:"new_rows"`
	// ExistingRows counts source rows whose unified identity already exists
	// in the target table (previously migrated, or an in-source duplicate of
	// a row that will land first).
	ExistingRows int64 `json:"existing_rows"`
}

// InspectCodexToAll previews the migration against the live DB: per-source
// row counts plus identity-based new/duplicate estimates. Absent codex
// tables are skipped, so a fresh install reports an empty (all-zero) preview.
func InspectCodexToAll(ctx context.Context, target *sql.DB) ([]CodexMigrationCounts, error) {
	out := make([]CodexMigrationCounts, 0, len(codexToAllPlans))
	for _, plan := range codexToAllPlans {
		counts := CodexMigrationCounts{SourceTable: plan.source, TargetTable: plan.target}
		exists, err := tableExists(ctx, target, plan.source)
		if err != nil {
			return nil, &MergeError{Code: ErrInspection, Table: plan.source, Err: err}
		}
		if !exists {
			out = append(out, counts)
			continue
		}
		if err := target.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+quoteIdent(plan.source)).Scan(&counts.SourceRows); err != nil {
			return nil, &MergeError{Code: ErrInspection, Table: plan.source, Err: err}
		}
		if counts.SourceRows == 0 {
			out = append(out, counts)
			continue
		}
		// Only the request table gets a precise identity estimate; the
		// sub-event archive schemas map onto the unified columns via the
		// transform, so an EXISTS join cannot be expressed directly. Their
		// preview reports the source count as an upper bound — the migration
		// itself still dedups every row exactly through staging.
		newRows := counts.SourceRows
		if plan.source == "codex_api_requests" {
			var err error
			newRows, err = countCodexNewRows(ctx, target, plan)
			if err != nil {
				return nil, err
			}
		}
		counts.NewRows = newRows
		counts.ExistingRows = counts.SourceRows - newRows
		out = append(out, counts)
	}
	return out, nil
}

// countCodexNewRows counts codex_api_requests archive rows whose unified
// identity (the KindClaudeRequest fallback tuple — the archive has no
// request_id column) does NOT already exist in api_requests. Input is
// compared in normalized form: input - cache_read - cache_creation (>= 0).
func countCodexNewRows(ctx context.Context, target *sql.DB, plan codexTablePlan) (int64, error) {
	query := `
		SELECT COUNT(*) FROM codex_api_requests AS c
		WHERE NOT EXISTS (
			SELECT 1 FROM api_requests AS t
			WHERE t.timestamp = c.timestamp
			  AND t.session_id = c.session_id
			  AND t.prompt_id = ''
			  AND t.event_sequence = c.event_sequence
			  AND t.model = c.model
			  AND t.input_tokens = MAX(c.input_tokens - c.cache_read_tokens - c.cache_creation_tokens, 0)
			  AND t.output_tokens = c.output_tokens
			  AND t.duration_ms = c.duration_ms
		)`
	var count int64
	if err := target.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, &MergeError{Code: ErrInspection, Table: plan.source, Err: err}
	}
	return count, nil
}

// MigrateCodexToAll runs the one-click migration: copy every archive row into
// the unified pool through the standard Import pipeline (staged dedup +
// ledger + aggregate deltas). Re-running is a no-op: identities already
// present are counted as duplicates and skipped.
func MigrateCodexToAll(ctx context.Context, target *sql.DB, options Options) (Result, error) {
	options.SourceID = codexSourceID
	if options.Location == nil {
		options.Location = time.Local
	}
	if options.TotalRows == 0 {
		preview, err := InspectCodexToAll(ctx, target)
		if err != nil {
			return Result{}, err
		}
		for _, counts := range preview {
			options.TotalRows += counts.SourceRows
		}
	}
	source, err := newCodexToAllSource(ctx, target)
	if err != nil {
		return Result{}, &MergeError{Code: ErrInspection, Err: err}
	}
	defer source.Close()
	result, err := Import(ctx, target, source, options)
	if err != nil {
		return result, err
	}

	// Keep the Codex shim calendar (daily_model_agg WHERE service_name =
	// 'codex') aligned with the archive-derived totals. The incremental
	// deltas already applied cover this; the sanity check below is cheap.
	var unifiedCount, aggCount int64
	if err := target.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM api_requests WHERE service_name = ?", db.CodexServiceName).Scan(&unifiedCount); err != nil {
		return result, err
	}
	if err := target.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(request_count), 0) FROM daily_model_agg WHERE service_name = ?", db.CodexServiceName).Scan(&aggCount); err != nil {
		return result, err
	}
	if unifiedCount != aggCount {
		return result, &MergeError{
			Code:  ErrVerification,
			Table: "daily_model_agg",
			Err:   fmt.Errorf("codex agg mismatch: %d unified rows vs %d agg requests", unifiedCount, aggCount),
		}
	}
	return result, nil
}