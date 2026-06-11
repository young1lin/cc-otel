package db

import (
	"context"
	"fmt"
	"time"
)

// InsertGeminiAPIRequest inserts a single gemini_api_requests row and upserts the
// matching gemini_daily_model_agg row in the same transaction.
// Returns (0, nil) for duplicates (RowsAffected == 0), or (id, nil) on success.
func (r *Repository) InsertGeminiAPIRequest(ctx context.Context, req *GeminiAPIRequest) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin gemini insert tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO gemini_api_requests (
			timestamp, session_id, model,
			input_tokens, output_tokens, cache_read_tokens,
			thoughts_tokens, tool_tokens, total_tokens,
			duration_ms, cost_usd, http_status_code,
			prompt_id, event_name,
			service_name, service_version
		) VALUES (?,?,?, ?,?,?, ?,?,?, ?,?,?, ?,?, ?,?)`,
		req.Timestamp.Unix(), req.SessionID, req.Model,
		req.InputTokens, req.OutputTokens, req.CacheReadTokens,
		req.ThoughtsTokens, req.ToolTokens, req.TotalTokens,
		req.DurationMs, costToInt64(req.CostUSD), req.HTTPStatusCode,
		req.PromptID, req.EventName,
		req.ServiceName, req.ServiceVersion,
	)
	if err != nil {
		return 0, fmt.Errorf("insert gemini_api_requests: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		// Duplicate — commit empty tx and return.
		return 0, tx.Commit()
	}

	id, _ := res.LastInsertId()

	dateKey := req.Timestamp.Local().Format("2006-01-02")
	if _, err := tx.StmtContext(ctx, r.stmtUpsGeminiAgg).ExecContext(ctx,
		dateKey, req.Model,
		req.InputTokens, req.OutputTokens,
		req.CacheReadTokens,
		req.ThoughtsTokens, req.ToolTokens, req.TotalTokens,
		costToInt64(req.CostUSD),
		req.DurationMs,
	); err != nil {
		return 0, fmt.Errorf("upsert gemini_daily_model_agg: %w", err)
	}

	return id, tx.Commit()
}

// GetGeminiDashboard mirrors GetDashboardForRange but reads from gemini_api_requests.
func (r *Repository) GetGeminiDashboard(ctx context.Context, from, to string) (*Dashboard, error) {
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
		FROM gemini_api_requests
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

// GetGeminiDailyStatsByModel returns per-(date, model) rollup rows with total count.
// Gemini has no cache_creation_tokens, so that column is always 0.
func (r *Repository) GetGeminiDailyStatsByModel(ctx context.Context, from, to string, limit, offset int, granularity string) ([]DailyModelSummary, int64, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, 0, err
	}
	dateExpr := dateExprForGranularity(granularity)

	var total int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT 1 FROM gemini_api_requests
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
		       0,
		       COUNT(*)
		FROM gemini_api_requests
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

// GetGeminiCalendarDays returns compact per-day aggregates for the Gemini usage calendar.
// Reads from gemini_daily_model_agg. Uses total_tokens when present, falls back to
// input + output. Aggregates per date, picks TopModel by token count.
func (r *Repository) GetGeminiCalendarDays(ctx context.Context, from, to string) ([]CalendarDay, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			date,
			model,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(total_requests), 0)
		FROM gemini_daily_model_agg
		WHERE date >= ? AND date <= ?
		GROUP BY date, model
		ORDER BY date ASC, model ASC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("gemini calendar days query: %w", err)
	}
	defer rows.Close()

	byDate := make(map[string]*CalendarDay)
	var dates []string
	topTokens := make(map[string]int64)
	for rows.Next() {
		var date, model string
		var input, output, cacheRead, reportedTotal, costUnits, reqs int64
		if err := rows.Scan(&date, &model, &input, &output, &cacheRead, &reportedTotal, &costUnits, &reqs); err != nil {
			return nil, err
		}

		day := byDate[date]
		if day == nil {
			day = &CalendarDay{Date: date}
			byDate[date] = day
			dates = append(dates, date)
			topTokens[date] = -1
		}
		// Prefer reported total_tokens when present, fall back to input+output.
		modelTokens := reportedTotal
		if modelTokens <= 0 {
			modelTokens = input + output
		}
		day.TotalTokens += modelTokens
		day.InputTokens += input
		day.OutputTokens += output
		day.CacheReadTokens += cacheRead
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

// GetGeminiRecentRequests returns paginated GeminiAPIRequest rows in the date range,
// optionally filtered by model.
func (r *Repository) GetGeminiRecentRequests(ctx context.Context, limit, offset int, model, from, to string) ([]GeminiAPIRequest, int64, error) {
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
		"SELECT COUNT(*) FROM gemini_api_requests WHERE "+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, timestamp, session_id, model,
		       input_tokens, output_tokens, cache_read_tokens,
		       thoughts_tokens, tool_tokens, total_tokens,
		       duration_ms, cost_usd, http_status_code,
		       prompt_id, event_name,
		       service_name, service_version
		FROM gemini_api_requests
		WHERE `+where+`
		ORDER BY timestamp DESC
		LIMIT ? OFFSET ?`, args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []GeminiAPIRequest
	for rows.Next() {
		var rec GeminiAPIRequest
		var tsUnix, costUnits int64
		if err := rows.Scan(&rec.ID, &tsUnix, &rec.SessionID, &rec.Model,
			&rec.InputTokens, &rec.OutputTokens, &rec.CacheReadTokens,
			&rec.ThoughtsTokens, &rec.ToolTokens, &rec.TotalTokens,
			&rec.DurationMs, &costUnits, &rec.HTTPStatusCode,
			&rec.PromptID, &rec.EventName,
			&rec.ServiceName, &rec.ServiceVersion,
		); err != nil {
			return nil, 0, err
		}
		rec.Timestamp = time.Unix(tsUnix, 0)
		rec.CostUSD = costToFloat64(costUnits)
		out = append(out, rec)
	}
	return out, total, rows.Err()
}

// GetGeminiSessionStats returns per-session aggregated stats ordered by most recent first.
func (r *Repository) GetGeminiSessionStats(ctx context.Context, from, to string, limit, offset int) ([]SessionStat, int64, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT 1 FROM gemini_api_requests
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
		FROM gemini_api_requests
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

// GetGeminiDurationStatsByModel returns per-model latency stats. Gemini does not
// emit token-throughput, so AvgOutTokensPS/AvgTotTokensPS stay at 0.
func (r *Repository) GetGeminiDurationStatsByModel(ctx context.Context, model, from, to string) ([]DurationStat, error) {
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
		       AVG(duration_ms), AVG(0),
		       0.0, 0.0,
		       MAX(duration_ms), MIN(duration_ms)
		FROM gemini_api_requests
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

// GetGeminiIntradayStatsByModel returns per-(bucket, model) Gemini stats.
// Gemini has no cache_creation_tokens, so that column is always 0.
func (r *Repository) GetGeminiIntradayStatsByModel(ctx context.Context, fromYMD, toYMD string, bucketMinutes int, model string) ([]IntradayModelSummary, error) {
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
			0,
			COALESCE(COUNT(*), 0)
		FROM gemini_api_requests
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
		return nil, fmt.Errorf("gemini intraday model stats query: %w", err)
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
