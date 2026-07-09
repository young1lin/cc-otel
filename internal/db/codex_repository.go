package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// InsertCodexAPIRequest inserts a single codex_api_requests row and upserts the
// matching codex_daily_model_agg row in the same transaction. Returns the new
// row's id.
//
// Token columns may be zero (when called for codex.api_request) or non-zero
// (fallback path from UpdateCodexAPIRequestTokens). Either way request_count is
// incremented by 1 — the SSE-completed update path uses a separate token-only
// UPSERT so a logical request is counted exactly once.
func (r *Repository) InsertCodexAPIRequest(ctx context.Context, req *CodexAPIRequest) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin codex insert tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO codex_api_requests (
			timestamp, session_id, user_id, model,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, cost_usd, duration_ms, ttft_ms,
			http_status, endpoint, conversation_id,
			event_name, event_sequence, terminal_type,
			service_name, service_version, host_arch, os_type, os_version,
			error_message
		) VALUES (?,?,?,?,  ?,?,?,?,  ?,?,?,?,?,  ?,?,?,  ?,?,?,  ?,?,?,?,?,  ?)`,
		req.Timestamp.Unix(), req.SessionID, req.UserID, req.Model,
		req.InputTokens, req.OutputTokens, req.CacheReadTokens, req.CacheCreationTokens,
		req.ReasoningTokens, req.TotalTokens, costToInt64(req.CostUSD), req.DurationMs, req.TTFTMs,
		req.HTTPStatus, req.Endpoint, req.ConversationID,
		req.EventName, req.EventSequence, req.TerminalType,
		req.ServiceName, req.ServiceVersion, req.HostArch, req.OSType, req.OSVersion,
		req.ErrorMessage,
	)
	if err != nil {
		return 0, fmt.Errorf("insert codex_api_requests: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	dateKey := req.Timestamp.Local().Format("2006-01-02")
	if _, err := tx.StmtContext(ctx, r.stmtUpsCodexAgg).ExecContext(ctx,
		dateKey, req.Model,
		req.InputTokens, req.OutputTokens,
		req.CacheReadTokens, req.CacheCreationTokens,
		req.ReasoningTokens, req.TotalTokens,
		costToInt64(req.CostUSD),
	); err != nil {
		return 0, fmt.Errorf("upsert codex_daily_model_agg: %w", err)
	}

	return id, tx.Commit()
}

// getCodexAPIRequestByID is a test helper. Not exported.
func (r *Repository) getCodexAPIRequestByID(ctx context.Context, id int64) (*CodexAPIRequest, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, timestamp, session_id, user_id, model,
		       input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		       reasoning_tokens, total_tokens, cost_usd, duration_ms, ttft_ms,
		       http_status, endpoint, conversation_id,
		       event_name, event_sequence, terminal_type,
		       service_name, service_version, host_arch, os_type, os_version,
		       error_message
		FROM codex_api_requests WHERE id = ?`, id)

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

// UpdateCodexAPIRequestTokens looks for the newest codex_api_requests row
// matching (session_id, model) within the last 5 minutes that still has zero
// tokens. If found, updates its token columns and adds the same tokens to the
// matching codex_daily_model_agg row (date keyed off the *row's* timestamp so
// midnight drift between codex.api_request and codex.sse_event lands on the
// correct day). Otherwise inserts a new token-only row via InsertCodexAPIRequest,
// which counts the request itself.
//
// Returns true if an UPDATE happened, false if a fallback INSERT happened.
func (r *Repository) UpdateCodexAPIRequestTokens(ctx context.Context, u *CodexTokenUpdate) (bool, error) {
	cutoff := u.Timestamp.Unix() - 300

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin codex update tx: %w", err)
	}
	defer tx.Rollback()

	var rowID, rowTs int64
	err = tx.QueryRowContext(ctx, `
		SELECT id, timestamp FROM codex_api_requests
		WHERE session_id = ? AND model = ?
		  AND input_tokens = 0 AND output_tokens = 0
		  AND timestamp >= ?
		ORDER BY id DESC LIMIT 1`,
		u.SessionID, u.Model, cutoff,
	).Scan(&rowID, &rowTs)

	if err == sql.ErrNoRows {
		// No pending row → commit empty tx and fall back to InsertCodexAPIRequest,
		// which has its own transaction and bumps request_count.
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit empty codex update tx: %w", err)
		}
		_, ierr := r.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
			Timestamp:       u.Timestamp,
			SessionID:       u.SessionID,
			Model:           u.Model,
			InputTokens:     u.InputTokens,
			OutputTokens:    u.OutputTokens,
			CacheReadTokens: u.CacheReadTokens,
			ReasoningTokens: u.ReasoningTokens,
			TotalTokens:     u.TotalTokens,
			EventName:       "codex.sse_event:response.completed",
		})
		if ierr != nil {
			return false, ierr
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup codex pending row: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE codex_api_requests
		SET input_tokens = ?, output_tokens = ?, cache_read_tokens = ?,
		    reasoning_tokens = ?, total_tokens = ?
		WHERE id = ?`,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens,
		u.ReasoningTokens, u.TotalTokens, rowID,
	); err != nil {
		return false, fmt.Errorf("update codex tokens: %w", err)
	}

	dateKey := time.Unix(rowTs, 0).Local().Format("2006-01-02")
	if _, err := tx.StmtContext(ctx, r.stmtUpsCodexAggToks).ExecContext(ctx,
		dateKey, u.Model,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens,
		u.ReasoningTokens, u.TotalTokens,
	); err != nil {
		return false, fmt.Errorf("upsert codex agg tokens: %w", err)
	}

	return true, tx.Commit()
}

// InsertCodexUserPrompt inserts a row into codex_user_prompt_events.
func (r *Repository) InsertCodexUserPrompt(ctx context.Context, e *CodexEvent) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO codex_user_prompt_events
			(timestamp, session_id, conversation_id, prompt_text, prompt_length,
			 event_sequence, terminal_type, service_name, service_version)
		VALUES (?,?,?,?,?, ?,?,?,?)`,
		e.Timestamp.Unix(), e.SessionID, e.ConversationID, e.PromptText, e.PromptLength,
		e.EventSequence, e.TerminalType, e.ServiceName, e.ServiceVersion,
	)
	return err
}

// InsertCodexToolDecision inserts a row into codex_tool_decision_events.
func (r *Repository) InsertCodexToolDecision(ctx context.Context, e *CodexEvent) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO codex_tool_decision_events
			(timestamp, session_id, conversation_id, tool_name, call_id,
			 decision, source, event_sequence, terminal_type)
		VALUES (?,?,?,?,?, ?,?,?,?)`,
		e.Timestamp.Unix(), e.SessionID, e.ConversationID, e.ToolName, e.CallID,
		e.Decision, e.Source, e.EventSequence, e.TerminalType,
	)
	return err
}

// InsertCodexToolResult inserts a row into codex_tool_result_events.
func (r *Repository) InsertCodexToolResult(ctx context.Context, e *CodexEvent) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO codex_tool_result_events
			(timestamp, session_id, conversation_id, tool_name, call_id,
			 success, duration_ms, arguments_length, output_length,
			 tool_origin, mcp_server, error_message, event_sequence, terminal_type)
		VALUES (?,?,?,?,?, ?,?,?,?, ?,?,?,?,?)`,
		e.Timestamp.Unix(), e.SessionID, e.ConversationID, e.ToolName, e.CallID,
		e.Success, e.DurationMs, e.ArgumentsLength, e.OutputLength,
		e.ToolOrigin, e.MCPServer, e.ErrorMessage, e.EventSequence, e.TerminalType,
	)
	return err
}

// UpdateCodexRequestDurationBySession sets duration_ms on the most recent
// codex_api_requests row matching (session_id, model) within 10 minutes of ts
// that has duration_ms = 0.
func (r *Repository) UpdateCodexRequestDurationBySession(ctx context.Context, sessionID, model string, ts time.Time, durationMs int64) (bool, error) {
	if sessionID == "" || model == "" || durationMs <= 0 {
		return false, nil
	}
	cutoff := ts.Unix() - 600
	res, err := r.db.ExecContext(ctx, `
		UPDATE codex_api_requests
		SET duration_ms = ?
		WHERE id = (
			SELECT id FROM codex_api_requests
			WHERE session_id = ? AND model = ?
			  AND duration_ms = 0
			  AND timestamp >= ?
			ORDER BY id DESC LIMIT 1
		)`,
		durationMs, sessionID, model, cutoff,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateCodexRequestTTFT sets ttft_ms on the most recent codex_api_requests
// row matching (session_id, model) within 10 minutes of ts that has ttft_ms = 0.
func (r *Repository) UpdateCodexRequestTTFT(ctx context.Context, sessionID, model string, ts time.Time, ttftMs int64) error {
	if sessionID == "" || model == "" || ttftMs <= 0 {
		return nil
	}
	cutoff := ts.Unix() - 600
	_, err := r.db.ExecContext(ctx, `
		UPDATE codex_api_requests
		SET ttft_ms = ?
		WHERE id = (
			SELECT id FROM codex_api_requests
			WHERE session_id = ? AND model = ?
			  AND (ttft_ms = 0 OR ttft_ms IS NULL)
			  AND timestamp >= ?
			ORDER BY id DESC LIMIT 1
		)`,
		ttftMs, sessionID, model, cutoff,
	)
	return err
}

// InsertCodexEvent inserts a fallback Codex log record into codex_events.
func (r *Repository) InsertCodexEvent(ctx context.Context, e *CodexEvent) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO codex_events
			(timestamp, session_id, conversation_id, event_name, event_kind,
			 model, duration_ms, error_message, raw_attrs_json)
		VALUES (?,?,?,?,?, ?,?,?,?)`,
		e.Timestamp.Unix(), e.SessionID, e.ConversationID, e.EventName, e.EventKind,
		e.Model, e.DurationMs, e.ErrorMessage, e.RawAttrsJSON,
	)
	return err
}

// InsertCodexRawEvent inserts the raw OTLP payload into codex_raw_otlp_events.
func (r *Repository) InsertCodexRawEvent(ctx context.Context, eventType string, timestampUnix int64, rawJSON string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO codex_raw_otlp_events (timestamp, event_type, raw_json) VALUES (?, ?, ?)`,
		timestampUnix, eventType, rawJSON,
	)
	return err
}

// GetCodexDashboard mirrors GetDashboardForRange but reads from codex_api_requests.
// Codex input_tokens already includes cached input; cache_read_tokens is a subset.
func (r *Repository) GetCodexDashboard(ctx context.Context, from, to string) (*Dashboard, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, err
	}

	d := &Dashboard{}
	var costUnits int64
	err = r.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COUNT(*)
		FROM codex_api_requests
		WHERE timestamp >= ? AND timestamp < ?`,
		fromUnix, toUnix,
	).Scan(&costUnits, &d.TotalInputTokens, &d.TotalCacheReadTokens, &d.TotalOutputTokens, &d.RequestCount)
	if err != nil {
		return nil, err
	}
	d.TotalCostUSD = costToFloat64(costUnits)
	if d.TotalInputTokens > 0 {
		d.CacheHitRate = float64(d.TotalCacheReadTokens) / float64(d.TotalInputTokens)
	}
	return d, nil
}

// GetCodexDailyStatsByModel returns per-(date, model) rollup rows with total count.
// Reads codex_api_requests directly. codex_daily_model_agg is now kept in sync by
// the write paths but the read side hasn't switched to it yet — doing so would
// require a startup rebuild step like RebuildDailyAggregates.
func (r *Repository) GetCodexDailyStatsByModel(ctx context.Context, from, to string, limit, offset int, granularity string) ([]DailyModelSummary, int64, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, 0, err
	}
	dateExpr := dateExprForGranularity(granularity)

	var total int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT 1 FROM codex_api_requests
			WHERE timestamp >= ? AND timestamp < ?
			GROUP BY `+dateExpr+`, model
		)`, fromUnix, toUnix).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT `+dateExpr+` AS d, model,
		       COALESCE(SUM(cost_usd), 0),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0),
		       COALESCE(SUM(cache_creation_tokens), 0),
		       COUNT(*)
		FROM codex_api_requests
		WHERE timestamp >= ? AND timestamp < ?
		GROUP BY d, model
		ORDER BY d DESC, model
		LIMIT ? OFFSET ?`,
		fromUnix, toUnix, limit, offset,
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

// GetCodexIntradayStatsByModel returns per-(bucket, model) Codex stats. Codex
// input_tokens already includes cached input; cache_read_tokens is a subset.
func (r *Repository) GetCodexIntradayStatsByModel(ctx context.Context, fromYMD, toYMD string, bucketMinutes int, model string) ([]IntradayModelSummary, error) {
	if bucketMinutes != 15 && bucketMinutes != 30 && bucketMinutes != 60 {
		return nil, fmt.Errorf("bucket_minutes must be 15, 30, or 60 (got %d)", bucketMinutes)
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
		FROM codex_api_requests
		WHERE timestamp >= ? AND timestamp < ?
	`
	args := []any{bucketSec, fromUnix, toExclusiveUnix}
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

// GetCodexRecentRequests returns paginated CodexAPIRequest rows in the date range,
// optionally filtered by model.
func (r *Repository) GetCodexRecentRequests(ctx context.Context, limit, offset int, model, from, to string) ([]CodexAPIRequest, int64, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, 0, err
	}

	args := []interface{}{fromUnix, toUnix}
	where := "timestamp >= ? AND timestamp < ?"
	if model != "" {
		where += " AND model = ?"
		args = append(args, model)
	}

	var total int64
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM codex_api_requests WHERE "+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, timestamp, session_id, user_id, model,
		       input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		       reasoning_tokens, total_tokens, cost_usd, duration_ms, ttft_ms,
		       http_status, endpoint
		FROM codex_api_requests
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
			&rec.HTTPStatus, &rec.Endpoint,
		); err != nil {
			return nil, 0, err
		}
		rec.Timestamp = time.Unix(tsUnix, 0)
		rec.CostUSD = costToFloat64(costUnits)
		out = append(out, rec)
	}
	return out, total, rows.Err()
}

// GetCodexSessionStats reuses the existing SessionStat type. StartTime maps to
// MIN(timestamp); InputTokens / OutputTokens / CostUSD aggregate per session.
func (r *Repository) GetCodexSessionStats(ctx context.Context, from, to string, limit, offset int) ([]SessionStat, int64, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT 1 FROM codex_api_requests
			WHERE timestamp >= ? AND timestamp < ?
			  AND session_id != ''
			GROUP BY session_id
		)`, fromUnix, toUnix).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT session_id,
		       MIN(timestamp),
		       COUNT(*),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cost_usd), 0)
		FROM codex_api_requests
		WHERE timestamp >= ? AND timestamp < ?
		  AND session_id != ''
		GROUP BY session_id
		ORDER BY MAX(timestamp) DESC
		LIMIT ? OFFSET ?`, fromUnix, toUnix, limit, offset)
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

// GetCodexDurationStatsByModel returns per-model latency stats. Codex does not
// emit token-throughput, so AvgOutTokensPS/AvgTotTokensPS stay at 0.
func (r *Repository) GetCodexDurationStatsByModel(ctx context.Context, model, from, to string) ([]DurationStat, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, err
	}

	args := []interface{}{fromUnix, toUnix}
	where := "timestamp >= ? AND timestamp < ? AND duration_ms > 0"
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
		FROM codex_api_requests
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
