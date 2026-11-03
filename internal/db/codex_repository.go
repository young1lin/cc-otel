package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// This file implements the AllClient unified Codex paths. Since the two pools
// merged, every function here reads and writes the SHARED tables
// (api_requests, daily_model_agg, user_prompt_events, tool_decision_events,
// tool_result_events, raw_otlp_events) with service_name='codex'. The legacy
// codex_* tables remain in the schema as a read-only archive of pre-merge
// history; the one-click migration (internal/dbmerge codex_to_all) copies
// them into the shared pool and never deletes them.

// InsertCodexAPIRequest inserts a single Codex row into api_requests and
// upserts the matching daily_model_agg row in the same transaction. Returns
// the new row's id, or 0 when the insert was ignored as a duplicate
// (request_id already present).
//
// Token columns may be zero (when called for codex.api_request) or non-zero
// (fallback path from UpdateCodexAPIRequestTokens). Either way request_count
// is incremented by 1 — the SSE-completed update path uses a separate
// token-only UPSERT so a logical request is counted exactly once.
//
// Input tokens must ALREADY be normalized to Claude semantics (uncached:
// raw input minus cache_read minus cache_creation) by the caller — the
// receiver does this before it builds the struct.
// InsertCodexAPIRequest wraps insertCodexAPIRequestOnce in the same BUSY
// retry as InsertRequest (R4 concurrency F5): a migration batch holds the
// writer under BEGIN IMMEDIATE for up to a whole batch, a live insert can
// hit BUSY past busy_timeout, and a PartialSuccess answer is NOT retried by
// most OTLP SDKs — that record would be silently dropped. The request_id
// dedup inside makes the retry idempotent.
func (r *Repository) InsertCodexAPIRequest(ctx context.Context, req *CodexAPIRequest) (int64, error) {
	for attempt := 0; ; attempt++ {
		id, err := r.insertCodexAPIRequestOnce(ctx, req)
		if err == nil || attempt >= 3 || !isBusyErr(err) {
			return id, err
		}
		select {
		case <-ctx.Done():
			return id, ctx.Err()
		case <-time.After(time.Duration(25<<attempt) * time.Millisecond):
		}
	}
}

func (r *Repository) insertCodexAPIRequestOnce(ctx context.Context, req *CodexAPIRequest) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin codex insert tx: %w", err)
	}
	defer tx.Rollback()

	id, stored, err := r.insertCodexRowInTx(ctx, tx, req)
	if err != nil {
		return 0, err
	}
	if !stored {
		// Duplicate redelivery: discard the no-op tx. Committing here as well
		// would double-commit (ErrTxDone) — the R5 rollback P1 regression.
		return 0, tx.Rollback()
	}
	return id, tx.Commit()
}

// insertCodexRowInTx performs the api_requests INSERT + agg upsert inside a
// CALLER-OWNED transaction so the orphan-completion path can share one tx
// with its duplicate check (R4 concurrency F3: check-then-insert across two
// autocommit transactions let two concurrent orphan redeliveries both pass
// the check and double-insert).
//
// The helper NEVER commits — including on the duplicate-request_id path,
// which returns stored=false with the tx still open (R5 rollback P1: the
// previous shape committed inside the dup branch, and both callers' own
// Commit then failed with sql.ErrTxDone, turning a routine at-least-once
// redelivery into HTTP 500 / PartialSuccess).
func (r *Repository) insertCodexRowInTx(ctx context.Context, tx *sql.Tx, req *CodexAPIRequest) (id int64, stored bool, err error) {
	res, err := tx.StmtContext(ctx, r.stmtInsReq).ExecContext(ctx,
		req.Timestamp.Unix(), req.SessionID, req.UserID, "", 0,
		req.Model, "",
		req.InputTokens, req.OutputTokens, req.CacheReadTokens, req.CacheCreationTokens,
		costToInt64(req.CostUSD), req.DurationMs, req.TTFTMs, req.RequestID,
		req.EventName, req.EventSequence, "", req.TerminalType, "", "", "",
		CodexServiceName, req.ServiceVersion, req.HostArch, req.OSType, req.OSVersion,
		"", req.ErrorMessage, 0, 0,
		req.ReasoningTokens, req.TotalTokens, req.HTTPStatus, req.Endpoint, req.ConversationID,
	)
	if err != nil {
		return 0, false, fmt.Errorf("insert codex api_requests: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Duplicate request_id: the row is already stored; do not touch the
		// aggregate either, or the dashboard would count it twice. The tx is
		// left OPEN for the caller to roll back / commit as it sees fit.
		return 0, false, nil
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, false, err
	}

	dateKey := req.Timestamp.Local().Format("2006-01-02")
	if _, err := tx.StmtContext(ctx, r.stmtUpsAgg).ExecContext(ctx,
		dateKey, req.Model, CodexServiceName,
		req.InputTokens, req.OutputTokens,
		req.CacheReadTokens, req.CacheCreationTokens,
		costToInt64(req.CostUSD),
	); err != nil {
		return 0, false, fmt.Errorf("upsert daily_model_agg (codex): %w", err)
	}

	return id, true, nil
}

// getCodexAPIRequestByID is a test helper reading a unified Codex row.
func (r *Repository) getCodexAPIRequestByID(ctx context.Context, id int64) (*CodexAPIRequest, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, timestamp, session_id, user_id, model,
		       input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		       reasoning_tokens, total_tokens, cost_usd, duration_ms, ttft_ms,
		       http_status, endpoint, conversation_id,
		       event_name, event_sequence, terminal_type,
		       service_name, service_version, host_arch, os_type, os_version,
		       error_message
		FROM api_requests WHERE id = ? AND service_name = ?`, id, CodexServiceName)

	out := &CodexAPIRequest{}
	var tsUnix int64
	var costUnits int64
	if err := row.Scan(&out.ID, &tsUnix, &out.SessionID, &out.UserID, &out.Model,
		&out.InputTokens, &out.OutputTokens, &out.CacheReadTokens, &out.CacheCreationTokens,
		&out.ReasoningTokens, &out.TotalTokens, &costUnits, &out.DurationMs, &out.TTFTMs,
		&out.HTTPStatus, &out.Endpoint, &out.ConversationID,
		&out.EventName, &out.EventSequence, &out.TerminalType,
		&out.ServiceName, &out.ServiceVersion, &out.HostArch, &out.OSType, &out.OSVersion,
		&out.ErrorMessage,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	out.Timestamp = time.Unix(tsUnix, 0)
	out.CostUSD = costToFloat64(costUnits)
	return out, nil
}

// UpdateCodexAPIRequestTokens finalizes a unified Codex request row with
// completion accounting. When u.RequestRowID > 0 it targets that exact row
// first (and only that row), otherwise it falls back to the newest zero-token
// Codex row matching (session_id, model) within the last 30 minutes (aligned
// with the receiver tracker's eviction window so a tracker lost to a restart
// mid-stream still finds its pending row). The matched row is updated with
// token columns, cost, full duration, and direct TTFT, and the same deltas
// are added to daily_model_agg under service_name='codex' (date keyed off the
// *row's* timestamp so midnight drift lands on the correct day, request count
// unchanged).
//
// An exact row that already carries token data is treated as
// already-finalized: the transaction commits as a no-op and returns true
// without re-applying deltas. If no pending row exists at all, a token-only
// fallback row is inserted via InsertCodexAPIRequest (which bumps
// request_count) and the method returns false.
//
// Returns true for an exact/fallback UPDATE or an idempotent exact-row no-op,
// and false for the fallback INSERT. Either value lets the receiver drop the
// matched in-memory tracker state.
func (r *Repository) UpdateCodexAPIRequestTokens(ctx context.Context, u *CodexTokenUpdate) (bool, error) {
	// Same deferred read-then-write BUSY_SNAPSHOT race InsertRequest documents
	// (repository.go): during an online import/migration a completion tx that
	// straddles a batch commit gets SQLITE_BUSY immediately. Re-snapshotting
	// is safe: every branch is idempotent (exact-row no-op, guarded fallback
	// UPDATE, identity-checked fallback INSERT).
	for attempt := 0; ; attempt++ {
		handled, err := r.updateCodexTokensOnce(ctx, u)
		if err == nil || attempt >= 3 || !isBusyErr(err) {
			return handled, err
		}
		select {
		case <-ctx.Done():
			return handled, ctx.Err()
		case <-time.After(time.Duration(25<<attempt) * time.Millisecond):
		}
	}
}

func (r *Repository) updateCodexTokensOnce(ctx context.Context, u *CodexTokenUpdate) (bool, error) {
	// Defense in depth against token-less completions (the per-poll SSE log
	// record): an update that carries nothing must not fall through to the
	// fallback INSERT and mint a phantom zero-token row.
	if u.InputTokens == 0 && u.OutputTokens == 0 &&
		u.CacheReadTokens == 0 && u.CacheCreationTokens == 0 &&
		u.ReasoningTokens == 0 && u.TotalTokens == 0 &&
		u.TTFTMs == 0 && u.DurationMs == 0 && u.CostUSD == 0 {
		return true, nil
	}
	// 30 minutes: match the receiver-side tracker eviction window so a
	// tracker miss after a restart during a long stream still finds its
	// pending row instead of double-inserting.
	cutoff := u.Timestamp.Unix() - 1800

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin codex update tx: %w", err)
	}
	defer tx.Rollback()

	var rowID, rowTs int64
	if u.RequestRowID > 0 {
		var input, output, cacheRead, cacheCreate, reasoning, total int64
		err = tx.QueryRowContext(ctx, `
			SELECT id, timestamp, input_tokens, output_tokens, cache_read_tokens,
			       cache_creation_tokens, reasoning_tokens, total_tokens
			FROM api_requests
			WHERE id = ? AND session_id = ? AND model = ? AND service_name = ?`,
			u.RequestRowID, u.SessionID, u.Model, CodexServiceName,
		).Scan(&rowID, &rowTs, &input, &output, &cacheRead, &cacheCreate, &reasoning, &total)
		if err == nil && (input != 0 || output != 0 || cacheRead != 0 ||
			cacheCreate != 0 || reasoning != 0 || total != 0) {
			if commitErr := tx.Commit(); commitErr != nil {
				return false, fmt.Errorf("commit idempotent codex update: %w", commitErr)
			}
			return true, nil
		}
		if err != nil && err != sql.ErrNoRows {
			return false, fmt.Errorf("lookup exact codex row: %w", err)
		}
		if err == sql.ErrNoRows {
			rowID = 0
			rowTs = 0
		}
	}

	if rowID == 0 {
		// Pending-row predicate must be as strict as the exact-row idempotency
		// check above: input is stored NORMALIZED, so a fully-cached completion
		// legitimately persists input=0; requiring all six token columns to be
		// zero keeps finalized rows out of this match.
		err = tx.QueryRowContext(ctx, `
			SELECT id, timestamp FROM api_requests
			WHERE session_id = ? AND model = ? AND service_name = ?
			  AND input_tokens = 0 AND output_tokens = 0
			  AND cache_read_tokens = 0 AND cache_creation_tokens = 0
			  AND reasoning_tokens = 0 AND total_tokens = 0
			  AND timestamp >= ?
			ORDER BY id DESC LIMIT 1`,
			u.SessionID, u.Model, CodexServiceName, cutoff,
		).Scan(&rowID, &rowTs)
	}

	if err == sql.ErrNoRows {
		// Idempotent replay guard: OTLP delivery is at-least-once, and after
		// the first completion finalizes its row (and the tracker entry is
		// evicted), a re-delivery would otherwise arrive here and insert a full
		// duplicate — double-counting request_count, every token category, and
		// cost in both api_requests and daily_model_agg. The completion's
		// identity is (session, model, full token vector); its timestamp alone
		// cannot be matched exactly because the finalized row keeps the
		// API-REQUEST's timestamp (up to one tracker window earlier than the
		// completion), while an orphan-path first insert keeps the completion's
		// own. Match a window covering both shapes.
		//
		// DOCUMENTED LIMITATIONS of token-vector identity (R4 integrity F2/F3,
		// protocol F6 — accepted after review, fixing them needs wire-level
		// identity Codex does not send):
		//  (a) a DISTINCT second request in the same session whose six token
		//      fields exactly equal an earlier one within the 31-minute window
		//      is treated as a replay and dropped (silent undercount; realistic
		//      only for deterministic scripted loops — conversation input grows
		//      per turn, so vectors differ);
		//  (b) two DIFFERENT authoritative completions for one span both land
		//      (exact replays are no-ops, but a conflicting vector inserts);
		//  (c) an archive copy of an orphan-fallback row never identity-matches
		//      the live row (different ts/event_shape) — a cross-source double
		//      count the sanity check cannot see.
		// The check AND the fallback
		// INSERT run inside the SAME tx: two concurrent orphan redeliveries
		// both passing an autocommit check was a double-insert TOCTOU (R4
		// concurrency F3); if the sibling commits first, this tx's COMMIT
		// surfaces BUSY_SNAPSHOT and the caller's retry re-runs everything.
		var dupID int64
		dupErr := tx.QueryRowContext(ctx, `
			SELECT id FROM api_requests
			WHERE session_id = ? AND model = ? AND service_name = ?
			  AND timestamp BETWEEN ? AND ?
			  AND input_tokens = ? AND output_tokens = ?
			  AND cache_read_tokens = ? AND cache_creation_tokens = ?
			  AND reasoning_tokens = ? AND total_tokens = ?
			LIMIT 1`,
			u.SessionID, u.Model, CodexServiceName,
			u.Timestamp.Unix()-1860, u.Timestamp.Unix()+60,
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens,
			u.ReasoningTokens, u.TotalTokens,
		).Scan(&dupID)
		if dupErr == nil {
			// Already applied — treat like the exact-row no-op.
			if commitErr := tx.Commit(); commitErr != nil {
				return false, fmt.Errorf("commit duplicate-noop codex tx: %w", commitErr)
			}
			return true, nil
		}
		if dupErr != sql.ErrNoRows {
			return false, fmt.Errorf("codex completion identity check: %w", dupErr)
		}
		// ttft/duration-only orphan (no pending row, no token field): minting
		// a zero-token api_request would bump request_count for a request that
		// may never have existed (per-poll SSE record racing ahead of its
		// api_request export). No-op instead (R4 protocol F5, live-confirmed
		// phantom under session r4adv-ttftonly).
		if u.InputTokens == 0 && u.OutputTokens == 0 &&
			u.CacheReadTokens == 0 && u.CacheCreationTokens == 0 &&
			u.ReasoningTokens == 0 && u.TotalTokens == 0 {
			if commitErr := tx.Commit(); commitErr != nil {
				return false, fmt.Errorf("commit ttft-only-noop codex tx: %w", commitErr)
			}
			return true, nil
		}
		if _, _, insertErr := r.insertCodexRowInTx(ctx, tx, &CodexAPIRequest{
			Timestamp:           u.Timestamp,
			SessionID:           u.SessionID,
			Model:               u.Model,
			InputTokens:         u.InputTokens,
			OutputTokens:        u.OutputTokens,
			CacheReadTokens:     u.CacheReadTokens,
			CacheCreationTokens: u.CacheCreationTokens,
			ReasoningTokens:     u.ReasoningTokens,
			TotalTokens:         u.TotalTokens,
			CostUSD:             u.CostUSD,
			DurationMs:          u.DurationMs,
			TTFTMs:              u.TTFTMs,
			EventName:           "codex.sse_event:response.completed",
		}); insertErr != nil {
			return false, insertErr
		}
		// stored=false (duplicate identity) falls through to the same commit:
		// the tx is still open — only this caller commits it.
		if commitErr := tx.Commit(); commitErr != nil {
			return false, commitErr
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup codex pending row: %w", err)
	}

	// Guard the fallback UPDATE: two concurrent completions can select the
	// same pending row under WAL (readers don't block). Last-write-wins on the
	// row is acceptable, but the agg deltas must apply EXACTLY once, so gate
	// them on RowsAffected. When the loser simply returned "success" its
	// token vector was dropped silently and the tracker entry evicted (R1
	// review A2-3) — for the fallback path, retry the NEXT-newest pending row
	// (the loser's own anchor) before giving up.
	excluded := int64(0)
	for {
		if u.RequestRowID == 0 && excluded != 0 {
			err = tx.QueryRowContext(ctx, `
				SELECT id, timestamp FROM api_requests
				WHERE session_id = ? AND model = ? AND service_name = ?
				  AND input_tokens = 0 AND output_tokens = 0
				  AND cache_read_tokens = 0 AND cache_creation_tokens = 0
				  AND reasoning_tokens = 0 AND total_tokens = 0
				  AND timestamp >= ? AND id != ?
				ORDER BY id DESC LIMIT 1`,
				u.SessionID, u.Model, CodexServiceName, cutoff, excluded,
			).Scan(&rowID, &rowTs)
			if err == sql.ErrNoRows {
				// No other pending row: treat as an idempotent no-op.
				if commitErr := tx.Commit(); commitErr != nil {
					return false, fmt.Errorf("commit raced codex update: %w", commitErr)
				}
				return true, nil
			}
			if err != nil {
				return false, fmt.Errorf("reselect codex pending row: %w", err)
			}
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE api_requests
			SET input_tokens = ?, output_tokens = ?, cache_read_tokens = ?,
			    cache_creation_tokens = ?, reasoning_tokens = ?, total_tokens = ?,
			    cost_usd = ?,
			    duration_ms = CASE WHEN ? > 0 THEN ? ELSE duration_ms END,
			    ttft_ms = CASE WHEN ? > 0 THEN ? ELSE ttft_ms END
			WHERE id = ? AND input_tokens = 0 AND output_tokens = 0
			  AND cache_read_tokens = 0 AND cache_creation_tokens = 0
			  AND reasoning_tokens = 0 AND total_tokens = 0`,
			u.InputTokens, u.OutputTokens, u.CacheReadTokens,
			u.CacheCreationTokens, u.ReasoningTokens, u.TotalTokens,
			costToInt64(u.CostUSD),
			u.DurationMs, u.DurationMs,
			u.TTFTMs, u.TTFTMs,
			rowID,
		)
		if err != nil {
			return false, fmt.Errorf("update codex tokens: %w", err)
		}
		affected, _ := res.RowsAffected()
		if affected > 0 {
			break
		}
		// Someone else finalized this row between our SELECT and UPDATE.
		if u.RequestRowID != 0 {
			// Pinned path: a duplicate delivery racing its own first apply —
			// commit as an idempotent no-op without agg deltas.
			if commitErr := tx.Commit(); commitErr != nil {
				return false, fmt.Errorf("commit raced codex update: %w", commitErr)
			}
			return true, nil
		}
		excluded = rowID
	}

	dateKey := time.Unix(rowTs, 0).Local().Format("2006-01-02")
	if _, err := tx.StmtContext(ctx, r.stmtUpsAggToks).ExecContext(ctx,
		dateKey, u.Model, CodexServiceName,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens,
		costToInt64(u.CostUSD),
	); err != nil {
		return false, fmt.Errorf("upsert daily_model_agg tokens (codex): %w", err)
	}

	return true, tx.Commit()
}

// InsertCodexUserPrompt inserts a Codex prompt into the shared
// user_prompt_events table.
func (r *Repository) InsertCodexUserPrompt(ctx context.Context, e *CodexEvent) error {
	_, err := r.execWithBusyRetry(ctx, `
		INSERT INTO user_prompt_events
			(timestamp, session_id, user_id, prompt_id, prompt_text, prompt_length,
			 event_sequence, terminal_type, service_name, service_version,
			 host_arch, os_type, os_version, conversation_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.Timestamp.Unix(), e.SessionID, "", "", e.PromptText, e.PromptLength,
		e.EventSequence, e.TerminalType, CodexServiceName, e.ServiceVersion,
		"", "", "", e.ConversationID,
	)
	return err
}

// InsertCodexToolDecision inserts a Codex approval into the shared
// tool_decision_events table.
func (r *Repository) InsertCodexToolDecision(ctx context.Context, e *CodexEvent) error {
	_, err := r.execWithBusyRetry(ctx, `
		INSERT INTO tool_decision_events
			(timestamp, session_id, user_id, prompt_id, event_sequence, tool_name,
			 decision, source, terminal_type, conversation_id, call_id, service_name)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.Timestamp.Unix(), e.SessionID, "", "", e.EventSequence, e.ToolName,
		e.Decision, e.Source, e.TerminalType, e.ConversationID, e.CallID,
		CodexServiceName,
	)
	return err
}

// InsertCodexToolResult inserts a Codex tool outcome into the shared
// tool_result_events table. output_length maps onto the shared
// tool_result_size_bytes column; Codex-only detail (arguments_length,
// mcp_server, tool_origin) rides in the dedicated columns.
func (r *Repository) InsertCodexToolResult(ctx context.Context, e *CodexEvent) error {
	_, err := r.execWithBusyRetry(ctx, `
		INSERT INTO tool_result_events
			(timestamp, session_id, user_id, prompt_id, event_sequence, tool_name,
			 success, duration_ms, tool_result_size_bytes, decision_source, decision_type,
			 terminal_type, conversation_id, call_id, arguments_length,
			 mcp_server, tool_origin, error_message, service_name)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.Timestamp.Unix(), e.SessionID, "", "", e.EventSequence, e.ToolName,
		e.Success, e.DurationMs, e.OutputLength, "", "",
		e.TerminalType, e.ConversationID, e.CallID, e.ArgumentsLength,
		e.MCPServer, e.ToolOrigin, e.ErrorMessage, CodexServiceName,
	)
	return err
}

// UpdateCodexRequestDuration sets duration_ms for a unified Codex request
// row. When requestRowID > 0 it targets that exact (id, session_id, model)
// row first, replacing duration only when it is still zero; a positive
// existing duration is treated as authoritative and left untouched. If no
// exact row matches it falls back to the newest zero-duration Codex row for
// (session_id, model) within 10 minutes of ts. Returns true when an exact
// identity was handled (updated or already authoritative) or a fallback row
// was updated; false only for a genuine no-match, which lets the caller
// decide to insert a duration-only row.
func (r *Repository) UpdateCodexRequestDuration(
	ctx context.Context,
	requestRowID int64,
	sessionID, model string,
	ts time.Time,
	durationMs int64,
) (bool, error) {
	if sessionID == "" || model == "" || durationMs <= 0 {
		return false, nil
	}
	if requestRowID > 0 {
		res, err := r.execWithBusyRetry(ctx, `
			UPDATE api_requests
			SET duration_ms = ?
			WHERE id = ? AND session_id = ? AND model = ? AND service_name = ?
			  AND duration_ms = 0`,
			durationMs, requestRowID, sessionID, model, CodexServiceName,
		)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return true, nil
		}
		var exists int
		if err := r.db.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM api_requests
				WHERE id = ? AND session_id = ? AND model = ? AND service_name = ?
			)`, requestRowID, sessionID, model, CodexServiceName).Scan(&exists); err != nil {
			return false, err
		}
		if exists == 1 {
			return true, nil
		}
	}

	cutoff := ts.Unix() - 600
	res, err := r.execWithBusyRetry(ctx, `
		UPDATE api_requests
		SET duration_ms = ?
		WHERE id = (
			SELECT id FROM api_requests
			WHERE session_id = ? AND model = ? AND service_name = ?
			  AND duration_ms = 0
			  AND timestamp >= ?
			ORDER BY id DESC LIMIT 1
		)`,
		durationMs, sessionID, model, CodexServiceName, cutoff,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateCodexRequestTTFT sets ttft_ms for a unified Codex request row. When
// requestRowID > 0 it targets that exact (id, session_id, model) row first,
// filling TTFT only when it is zero/null; a positive existing TTFT is
// authoritative and left untouched. Otherwise it falls back to the newest
// zero/null-TTFT Codex row for (session_id, model) within 10 minutes of ts.
func (r *Repository) UpdateCodexRequestTTFT(
	ctx context.Context,
	requestRowID int64,
	sessionID, model string,
	ts time.Time,
	ttftMs int64,
) error {
	if sessionID == "" || model == "" || ttftMs <= 0 {
		return nil
	}
	if requestRowID > 0 {
		res, err := r.execWithBusyRetry(ctx, `
			UPDATE api_requests
			SET ttft_ms = ?
			WHERE id = ? AND session_id = ? AND model = ? AND service_name = ?
			  AND (ttft_ms = 0 OR ttft_ms IS NULL)`,
			ttftMs, requestRowID, sessionID, model, CodexServiceName,
		)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		var exists int
		if err := r.db.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM api_requests
				WHERE id = ? AND session_id = ? AND model = ? AND service_name = ?
			)`, requestRowID, sessionID, model, CodexServiceName).Scan(&exists); err != nil {
			return err
		}
		if exists == 1 {
			return nil
		}
	}

	cutoff := ts.Unix() - 600
	_, err := r.execWithBusyRetry(ctx, `
		UPDATE api_requests
		SET ttft_ms = ?
		WHERE id = (
			SELECT id FROM api_requests
			WHERE session_id = ? AND model = ? AND service_name = ?
			  AND (ttft_ms = 0 OR ttft_ms IS NULL)
			  AND timestamp >= ?
			ORDER BY id DESC LIMIT 1
		)`,
		ttftMs, sessionID, model, CodexServiceName, cutoff,
	)
	return err
}

// UpdateCodexTurnTTFT fills ttft_ms from Codex's dedicated turn-level
// codex.turn_ttft event (turn_timing.rs record_turn_ttft: duration from turn
// start to the FIRST output signal of any kind). The completion's ttft_ms
// attr is Option — absent for non-streamed/instant responses — so this event
// is often the ONLY ttft signal a turn produces. Target selection: the newest
// zero/null-TTFT codex anchor (event_name='codex.api_request') for
// (conversation_id, model) whose timestamp is at or before the event time —
// in a tool-loop turn only the FIRST request's anchor can precede the first
// output, so the join cannot steal a later request. Fill-only: an existing
// positive ttft (websocket reconstruction or an earlier authoritative
// completion) is left untouched, and a later authoritative completion with
// ttft_ms > 0 still overwrites via the finalize path's CASE-WHEN.
func (r *Repository) UpdateCodexTurnTTFT(
	ctx context.Context,
	conversationID, model string,
	ts time.Time,
	ttftMs int64,
) (bool, error) {
	if conversationID == "" || model == "" || ttftMs <= 0 {
		return false, nil
	}
	res, err := r.execWithBusyRetry(ctx, `
		UPDATE api_requests
		SET ttft_ms = ?
		WHERE id = (
			SELECT id FROM api_requests
			WHERE conversation_id = ? AND model = ? AND service_name = ?
			  AND event_name = 'codex.api_request'
			  AND (ttft_ms = 0 OR ttft_ms IS NULL)
			  AND timestamp <= ?
			  AND timestamp >= ?
			ORDER BY timestamp DESC, id DESC LIMIT 1
		)`,
		ttftMs, conversationID, model, CodexServiceName,
		ts.Unix(), ts.Unix()-3600,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// InsertCodexRawEvent inserts the raw OTLP payload into the shared
// raw_otlp_events table (identical schema to the legacy codex one).
func (r *Repository) InsertCodexRawEvent(ctx context.Context, eventType string, timestampUnix int64, rawJSON string) error {
	_, err := r.execWithBusyRetry(ctx, `
		INSERT INTO raw_otlp_events (timestamp, event_type, raw_json) VALUES (?, ?, ?)`,
		timestampUnix, eventType, rawJSON,
	)
	return err
}

// GetCodexDashboard mirrors GetDashboardForRange but filtered to Codex
// rows. It reads daily_model_agg (service_name='codex' rows are maintained
// transactionally by the live receiver and the migration) instead of
// scanning raw api_requests — the same trade the Claude dashboard makes
// (agg rows are pruned to the retention window by Cleanup). Since the merge,
// input_tokens is stored normalized (uncached), so the cache-hit denominator
// matches the Claude-side formula input+cache_read.
func (r *Repository) GetCodexDashboard(ctx context.Context, from, to string) (*Dashboard, error) {
	d := &Dashboard{}
	var costUnits int64
	var cacheCreate int64
	err := r.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(request_count), 0),
			COALESCE(SUM(cache_creation_tokens), 0)
		FROM daily_model_agg
		WHERE service_name = ? AND date >= ? AND date <= ?`,
		CodexServiceName, from, to,
	).Scan(&costUnits, &d.TotalInputTokens, &d.TotalCacheReadTokens, &d.TotalOutputTokens, &d.RequestCount, &cacheCreate)
	d.TotalCacheCreationTokens = cacheCreate
	if err != nil {
		return nil, err
	}
	d.TotalCostUSD = costToFloat64(costUnits)
	// Fold the full input side, exactly like GetDashboardForRange: the same
	// h-input KPI card renders both endpoints, so total_input_tokens must
	// mean the same thing (input + cache_read + cache_creation) on both
	// tabs or the two dashboards cannot be reconciled (R1 review A5-2).
	d.TotalInputTokens += d.TotalCacheReadTokens + cacheCreate
	// Same denominator as the Claude dashboard (GetDailyStats) and the
	// frontend token-math: the full input side, cache_creation included.
	// The old input+cache_read form diverged for Codex, which reports
	// large cache_creation volumes (docs review A6).
	if d.TotalInputTokens > 0 {
		d.CacheHitRate = float64(d.TotalCacheReadTokens) / float64(d.TotalInputTokens)
	}
	return d, nil
}

// GetCodexDailyStatsByModel returns per-(date, model) rollup rows with total
// count, reading daily_model_agg filtered to Codex rows (same retention
// trade as the Claude-side daily stats) instead of scanning raw
// api_requests rows in the window.
func (r *Repository) GetCodexDailyStatsByModel(ctx context.Context, from, to string, limit, offset int, granularity string) ([]DailyModelSummary, int64, error) {
	dateExpr := aggDateExpr(granularity)

	var total int64
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*) FROM (
			SELECT 1 FROM daily_model_agg
			WHERE service_name = ? AND date >= ? AND date <= ?
			GROUP BY %s, model
		)`, dateExpr), CodexServiceName, from, to).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s AS d, model,
		       COALESCE(SUM(cost_usd), 0),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0),
		       COALESCE(SUM(cache_creation_tokens), 0),
		       COALESCE(SUM(request_count), 0)
		FROM daily_model_agg
		WHERE service_name = ? AND date >= ? AND date <= ?
		GROUP BY d, model
		ORDER BY d DESC, model
		LIMIT ? OFFSET ?`, dateExpr),
		CodexServiceName, from, to, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []DailyModelSummary
	for rows.Next() {
		var s DailyModelSummary
		var costUnits int64
		if err := rows.Scan(&s.Date, &s.Model, &costUnits,
			&s.InputTokens, &s.OutputTokens, &s.CacheReadTokens, &s.CacheCreationTokens,
			&s.RequestCount,
		); err != nil {
			return nil, 0, err
		}
		s.CostUSD = costToFloat64(costUnits)
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// GetCodexCalendarDays returns compact per-day aggregates for the Codex usage
// calendar, read from the shared daily_model_agg under service_name='codex'.
// Totals use Claude semantics (input+output+cache are disjoint columns since
// the merge normalization) and now match the Claude-side calendar.
func (r *Repository) GetCodexCalendarDays(ctx context.Context, from, to string) ([]CalendarDay, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			date,
			model,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(request_count), 0)
		FROM daily_model_agg
		WHERE service_name = ? AND date >= ? AND date <= ?
		GROUP BY date, model
		ORDER BY date ASC, model ASC`, CodexServiceName, from, to)
	if err != nil {
		return nil, fmt.Errorf("codex calendar days query: %w", err)
	}
	defer rows.Close()

	byDate := make(map[string]*CalendarDay)
	var dates []string
	topTokens := make(map[string]int64)
	for rows.Next() {
		var date, model string
		var input, output, cacheRead, cacheCreate, costUnits, reqs int64
		if err := rows.Scan(&date, &model, &input, &output, &cacheRead, &cacheCreate, &costUnits, &reqs); err != nil {
			return nil, err
		}

		day := byDate[date]
		if day == nil {
			day = &CalendarDay{Date: date}
			byDate[date] = day
			dates = append(dates, date)
			topTokens[date] = -1
		}
		modelTokens := input + output + cacheRead + cacheCreate
		day.TotalTokens += modelTokens
		day.InputTokens += input
		day.OutputTokens += output
		day.CacheReadTokens += cacheRead
		day.CacheCreationTokens += cacheCreate
		day.CostUSD += costToFloat64(costUnits)
		day.RequestCount += reqs
		if modelTokens > topTokens[date] || (modelTokens == topTokens[date] && (day.TopModel == "" || model < day.TopModel)) {
			topTokens[date] = modelTokens
			day.TopModel = model
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]CalendarDay, 0, len(dates))
	for _, date := range dates {
		out = append(out, *byDate[date])
	}
	return out, nil
}

// GetCodexIntradayStatsByModel returns per-(bucket, model) Codex stats from
// the unified pool.
func (r *Repository) GetCodexIntradayStatsByModel(ctx context.Context, fromYMD, toYMD string, bucketMinutes int, model string) ([]IntradayModelSummary, error) {
	if !ValidRateBucketMinutes(bucketMinutes) {
		return nil, fmt.Errorf("bucket_minutes must be 5, 10, 15, 30, or 60 (got %d)", bucketMinutes)
	}
	fromUnix, toExclusiveUnix, err := localDateRangeToUnix(fromYMD, toYMD)
	if err != nil {
		return nil, err
	}
	bucketSec := int64(bucketMinutes) * 60

	query := `
		SELECT
			(timestamp - (timestamp % ?)) AS bucket_start,
			model,
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(COUNT(*), 0)
		FROM api_requests
		WHERE service_name = ? AND timestamp >= ? AND timestamp < ?
	`
	args := []any{bucketSec, CodexServiceName, fromUnix, toExclusiveUnix}
	if model != "" {
		query += ` AND model = ?`
		args = append(args, model)
	}
	query += `
		GROUP BY bucket_start, model
		ORDER BY bucket_start ASC, SUM(cost_usd) DESC
	`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("codex intraday model stats query: %w", err)
	}
	defer rows.Close()

	loc := time.Local
	var result []IntradayModelSummary
	for rows.Next() {
		var s IntradayModelSummary
		var totalCost int64
		if err := rows.Scan(&s.BucketStartUnix, &s.Model, &totalCost, &s.InputTokens, &s.OutputTokens,
			&s.CacheReadTokens, &s.CacheCreationTokens, &s.RequestCount); err != nil {
			return nil, err
		}
		s.CostUSD = costToFloat64(totalCost)
		s.BucketMinutes = bucketMinutes
		s.BucketLabel = time.Unix(s.BucketStartUnix, 0).In(loc).Format("01-02 15:04")
		result = append(result, s)
	}
	return result, rows.Err()
}

// GetCodexRecentRequests returns paginated unified Codex rows in the date
// range, optionally filtered by model. conversation_id is carried through as
// the legacy codex tab expects it.
func (r *Repository) GetCodexRecentRequests(ctx context.Context, limit, offset int, model, from, to string) ([]CodexAPIRequest, int64, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, 0, err
	}

	args := []interface{}{CodexServiceName, fromUnix, toUnix}
	where := "service_name = ? AND timestamp >= ? AND timestamp < ?"
	if model != "" {
		where += " AND model = ?"
		args = append(args, model)
	}

	var total int64
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM api_requests WHERE "+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, timestamp, session_id, user_id, model,
		       input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		       reasoning_tokens, total_tokens, cost_usd, duration_ms, ttft_ms,
		       http_status, endpoint, conversation_id,
		       service_name
		FROM api_requests
		WHERE `+where+`
		ORDER BY timestamp DESC
		LIMIT ? OFFSET ?`, args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []CodexAPIRequest
	for rows.Next() {
		var rec CodexAPIRequest
		var tsUnix, costUnits int64
		if err := rows.Scan(&rec.ID, &tsUnix, &rec.SessionID, &rec.UserID, &rec.Model,
			&rec.InputTokens, &rec.OutputTokens, &rec.CacheReadTokens, &rec.CacheCreationTokens,
			&rec.ReasoningTokens, &rec.TotalTokens, &costUnits, &rec.DurationMs, &rec.TTFTMs,
			&rec.HTTPStatus, &rec.Endpoint, &rec.ConversationID,
			&rec.ServiceName,
		); err != nil {
			return nil, 0, err
		}
		rec.Timestamp = time.Unix(tsUnix, 0)
		rec.CostUSD = costToFloat64(costUnits)
		out = append(out, rec)
	}
	return out, total, rows.Err()
}

// GetCodexSessionStats reuses the existing SessionStat type against the
// unified pool. StartTime maps to MIN(timestamp); InputTokens / OutputTokens
// / CostUSD aggregate per session.
func (r *Repository) GetCodexSessionStats(ctx context.Context, from, to string, limit, offset int) ([]SessionStat, int64, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT 1 FROM api_requests
			WHERE service_name = ? AND timestamp >= ? AND timestamp < ?
			  AND session_id != ''
			GROUP BY session_id
		)`, CodexServiceName, fromUnix, toUnix).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT session_id,
		       MIN(timestamp),
		       COUNT(*),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cost_usd), 0)
		FROM api_requests
		WHERE service_name = ? AND timestamp >= ? AND timestamp < ?
		  AND session_id != ''
		GROUP BY session_id
		ORDER BY MAX(timestamp) DESC
		LIMIT ? OFFSET ?`, CodexServiceName, fromUnix, toUnix, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []SessionStat
	for rows.Next() {
		var s SessionStat
		var minTs, costUnits int64
		if err := rows.Scan(&s.SessionID, &minTs, &s.RequestCount,
			&s.InputTokens, &s.OutputTokens, &costUnits,
		); err != nil {
			return nil, 0, err
		}
		s.StartTime = time.Unix(minTs, 0)
		s.CostUSD = costToFloat64(costUnits)
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// GetCodexDurationStatsByModel returns per-model latency stats from the
// unified pool. Codex does not emit token-throughput, so AvgOutTokensPS /
// AvgTotTokensPS stay at 0.
func (r *Repository) GetCodexDurationStatsByModel(ctx context.Context, model, from, to string) ([]DurationStat, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, err
	}

	args := []interface{}{CodexServiceName, fromUnix, toUnix}
	where := "service_name = ? AND timestamp >= ? AND timestamp < ? AND duration_ms > 0"
	if model != "" {
		where += " AND model = ?"
		args = append(args, model)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT model,
		       COUNT(*),
		       AVG(duration_ms), AVG(ttft_ms),
		       0.0, 0.0,
		       MAX(duration_ms), MIN(duration_ms)
		FROM api_requests
		WHERE `+where+`
		GROUP BY model
		ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DurationStat
	for rows.Next() {
		var d DurationStat
		if err := rows.Scan(&d.Model, &d.RequestCount,
			&d.AvgDurationMs, &d.AvgTTFTMs,
			&d.AvgOutTokensPS, &d.AvgTotTokensPS,
			&d.MaxDurationMs, &d.MinDurationMs,
		); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
