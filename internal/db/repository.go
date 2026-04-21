package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

const costScale = 100000 // 1 USD = 100000 units, 0.00001 precision

// APIRequest represents a single Claude Code API request record stored in the database.
type APIRequest struct {
	ID                  int64     `json:"id"`
	Timestamp           time.Time `json:"timestamp"`
	SessionID           string    `json:"session_id"`
	UserID              string    `json:"user_id"`
	PromptID            string    `json:"prompt_id"`
	PromptLength        int64     `json:"prompt_length"`
	Model               string    `json:"model"`
	ActualModel         string    `json:"actual_model"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	CostUSD             float64   `json:"cost_usd"`
	DurationMs          int64     `json:"duration_ms"`
	TTFTMs              int64     `json:"ttft_ms"`
	RequestID           string    `json:"request_id"`
	EventName           string    `json:"event_name"`
	EventSequence       int64     `json:"event_sequence"`
	Speed               string    `json:"speed"`
	TerminalType        string    `json:"terminal_type"`
	ToolName            string    `json:"tool_name"`
	Decision            string    `json:"decision"`
	Source              string    `json:"source"`
	ServiceName         string    `json:"service_name"`
	ServiceVersion      string    `json:"service_version"`
	HostArch            string    `json:"host_arch"`
	OSType              string    `json:"os_type"`
	OSVersion           string    `json:"os_version"`
	ErrorType           string    `json:"error_type"`
	ErrorMessage        string    `json:"error_message"`
	ErrorCode           int64     `json:"error_code"`
	ErrorRetryable      int       `json:"error_retryable"`
}

// Event represents any OTEL event (user_prompt, api_request, tool_decision, tool_result).
// All unrecognized event names are stored here; known types go to dedicated tables.
type Event struct {
	ID                  int64     `json:"id"`
	Timestamp           time.Time `json:"timestamp"`
	SessionID           string    `json:"session_id"`
	UserID              string    `json:"user_id"`
	PromptID            string    `json:"prompt_id"`
	PromptText          string    `json:"prompt_text"`
	PromptLength        int64     `json:"prompt_length"`
	EventName           string    `json:"event_name"` // user_prompt / api_request / tool_decision / tool_result
	EventSequence       int64     `json:"event_sequence"`
	Model               string    `json:"model"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	CostUSD             float64   `json:"cost_usd"`
	DurationMs          int64     `json:"duration_ms"`
	TTFTMs              int64     `json:"ttft_ms"`
	Speed               string    `json:"speed"` // normal / fast
	TerminalType        string    `json:"terminal_type"`
	ToolName            string    `json:"tool_name"`       // Agent / Bash / Read / Write ...
	Decision            string    `json:"decision"`        // accept / deny
	Source              string    `json:"source"`          // config / user
	DecisionSource      string    `json:"decision_source"` // tool_result (e.g. config)
	DecisionType        string    `json:"decision_type"`   // tool_result (e.g. accept)
	Success             int       `json:"success"`         // 1/0 for tool_result
	ToolResultSizeBytes int64     `json:"tool_result_size_bytes"`
	ErrorType           string    `json:"error_type"`    // for api_error events
	ErrorMessage        string    `json:"error_message"` // for api_error events
	ErrorCode           int64     `json:"error_code"`    // HTTP status / error code
	RequestID           string    `json:"request_id"`
	ErrorRetryable      int       `json:"error_retryable"` // 1/0 for api_error
	ServiceName         string    `json:"service_name"`
	ServiceVersion      string    `json:"service_version"`
	HostArch            string    `json:"host_arch"`
	OSType              string    `json:"os_type"`
	OSVersion           string    `json:"os_version"`
}

// Dashboard holds aggregated token usage and cost statistics for a date range.
type Dashboard struct {
	TotalCostUSD         float64 `json:"total_cost_usd"`
	TotalInputTokens     int64   `json:"total_input_tokens"`      // SUM(input+cache_read+cache_creation) — input-side total (matches chart KPI Input)
	TotalCacheReadTokens int64   `json:"total_cache_read_tokens"` // SUM(cache_read_tokens) — matches column "Cache Read"
	TotalOutputTokens    int64   `json:"total_output_tokens"`
	CacheHitRate         float64 `json:"cache_hit_rate"`
	RequestCount         int64   `json:"request_count"`
}

// DailySummary holds per-day aggregated token and cost statistics.
type DailySummary struct {
	Date              string  `json:"date"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	TotalCostUSD      float64 `json:"total_cost_usd"`
	RequestCount      int64   `json:"request_count"`
	CacheHitRate      float64 `json:"cache_hit_rate"`
}

// DailyModelSummary holds per-day, per-model aggregated token and cost statistics.
type DailyModelSummary struct {
	Date                string  `json:"date"`
	Model               string  `json:"model"`
	CostUSD             float64 `json:"cost_usd"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	RequestCount        int64   `json:"request_count"`
}

// HourlyModelSummary holds per-(hour, model) aggregated token and cost statistics for a single local day.
type HourlyModelSummary struct {
	Hour                int     `json:"hour"` // 0..23 in local time
	Model               string  `json:"model"`
	CostUSD             float64 `json:"cost_usd"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	RequestCount        int64   `json:"request_count"`
}

// Repository provides data access methods for the SQLite database.
type Repository struct {
	db         *sql.DB
	stmtInsReq *sql.Stmt // prepared: INSERT OR IGNORE INTO api_requests
	stmtUpsAgg *sql.Stmt // prepared: UPSERT INTO daily_model_agg
}

// NewRepository returns a Repository backed by the given database connection.
// Prepared statements are created for the hot-path InsertRequest to avoid
// repeated SQL parsing on every call.
func NewRepository(db *sql.DB) *Repository {
	r := &Repository{db: db}

	r.stmtInsReq, _ = db.Prepare(`
		INSERT OR IGNORE INTO api_requests
			(timestamp, session_id, user_id, prompt_id, prompt_length,
			 model, actual_model, input_tokens, output_tokens,
			 cache_read_tokens, cache_creation_tokens, cost_usd, duration_ms, ttft_ms, request_id,
			 event_name, event_sequence, speed, terminal_type, tool_name, decision, source,
			 service_name, service_version, host_arch, os_type, os_version,
			 error_type, error_message, error_code, error_retryable)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)

	r.stmtUpsAgg, _ = db.Prepare(`
		INSERT INTO daily_model_agg (date, model, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens, cost_usd, request_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(date, model) DO UPDATE SET
			input_tokens          = input_tokens + excluded.input_tokens,
			output_tokens         = output_tokens + excluded.output_tokens,
			cache_read_tokens     = cache_read_tokens + excluded.cache_read_tokens,
			cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
			cost_usd              = cost_usd + excluded.cost_usd,
			request_count         = request_count + 1`)

	return r
}

// Close releases prepared statements held by the repository.
func (r *Repository) Close() {
	if r.stmtInsReq != nil {
		r.stmtInsReq.Close()
	}
	if r.stmtUpsAgg != nil {
		r.stmtUpsAgg.Close()
	}
}

// Ping verifies the database connection is alive.
func (r *Repository) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

func costToInt64(usd float64) int64 {
	return int64(math.Round(usd * costScale))
}

// dateExprForGranularity returns a whitelisted SQL date expression for the given granularity.
func dateExprForGranularity(granularity string) string {
	switch granularity {
	case "month":
		return "strftime('%Y-%m', timestamp, 'unixepoch', 'localtime')"
	default:
		return "date(timestamp, 'unixepoch', 'localtime')"
	}
}

// localDateRangeToUnix converts a pair of YYYY-MM-DD local dates into the Unix
// timestamp half-open range [fromUnix, toExclusiveUnix) covering the full local
// days inclusive of both endpoints. Using raw timestamp bounds lets the query
// planner hit idx_requests_timestamp instead of scanning every row through
// date(timestamp, 'unixepoch', 'localtime').
func localDateRangeToUnix(from, to string) (int64, int64, error) {
	loc := time.Local
	fromT, err := time.ParseInLocation("2006-01-02", from, loc)
	if err != nil {
		return 0, 0, fmt.Errorf("parse from %q: %w", from, err)
	}
	toT, err := time.ParseInLocation("2006-01-02", to, loc)
	if err != nil {
		return 0, 0, fmt.Errorf("parse to %q: %w", to, err)
	}
	return fromT.Unix(), toT.Add(24 * time.Hour).Unix(), nil
}

func costToFloat64(units int64) float64 {
	return float64(units) / costScale
}

// aggDateExpr returns the SQL expression to group daily_model_agg.date by granularity.
// Separate from dateExprForGranularity which targets Unix timestamp columns.
func aggDateExpr(granularity string) string {
	switch granularity {
	case "month":
		return "strftime('%Y-%m', date)"
	default:
		return "date"
	}
}

// NeedsAggRebuild returns true when daily_model_agg is empty but api_requests has data.
func (r *Repository) NeedsAggRebuild(ctx context.Context) (bool, error) {
	var aggEmpty bool
	if err := r.db.QueryRowContext(ctx,
		`SELECT NOT EXISTS(SELECT 1 FROM daily_model_agg)`).Scan(&aggEmpty); err != nil {
		return false, err
	}
	if !aggEmpty {
		return false, nil
	}
	var reqExists bool
	if err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM api_requests)`).Scan(&reqExists); err != nil {
		return false, err
	}
	return reqExists, nil
}

// RebuildDailyAggregates drops and rebuilds daily_model_agg from api_requests.
func (r *Repository) RebuildDailyAggregates(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rebuild tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM daily_model_agg`); err != nil {
		return fmt.Errorf("clear daily_model_agg: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO daily_model_agg (date, model, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens, cost_usd, request_count)
		SELECT
			date(timestamp, 'unixepoch', 'localtime'),
			model,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COUNT(*)
		FROM api_requests
		GROUP BY date(timestamp, 'unixepoch', 'localtime'), model`); err != nil {
		return fmt.Errorf("rebuild daily_model_agg: %w", err)
	}

	return tx.Commit()
}

// InsertRequest stores a single API request record, ignoring duplicates by request_id.
// It also upserts the daily_model_agg pre-aggregation table within the same transaction.
// Returns true if a new row was inserted (false for duplicates).
func (r *Repository) InsertRequest(ctx context.Context, req *APIRequest) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Use prepared statement within the transaction to skip SQL re-parsing.
	res, err := tx.StmtContext(ctx, r.stmtInsReq).ExecContext(ctx,
		req.Timestamp.Unix(),
		req.SessionID, req.UserID, req.PromptID, req.PromptLength,
		req.Model, req.ActualModel,
		req.InputTokens, req.OutputTokens,
		req.CacheReadTokens, req.CacheCreationTokens,
		costToInt64(req.CostUSD), req.DurationMs, req.TTFTMs, req.RequestID,
		req.EventName, req.EventSequence, req.Speed, req.TerminalType, req.ToolName, req.Decision, req.Source,
		req.ServiceName, req.ServiceVersion, req.HostArch, req.OSType, req.OSVersion,
		req.ErrorType, req.ErrorMessage, req.ErrorCode, req.ErrorRetryable,
	)
	if err != nil {
		return false, err
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return false, tx.Commit()
	}

	dateKey := req.Timestamp.Local().Format("2006-01-02")
	cost := costToInt64(req.CostUSD)

	_, err = tx.StmtContext(ctx, r.stmtUpsAgg).ExecContext(ctx,
		dateKey, req.Model,
		req.InputTokens, req.OutputTokens,
		req.CacheReadTokens, req.CacheCreationTokens,
		cost,
	)
	if err != nil {
		return false, fmt.Errorf("upsert daily_model_agg: %w", err)
	}

	return true, tx.Commit()
}

// InsertRawEvent stores the complete original OTEL event as JSON for future re-processing.
func (r *Repository) InsertRawEvent(ctx context.Context, eventType string, timestamp int64, rawJSON string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO raw_otlp_events (timestamp, event_type, raw_json) VALUES (?, ?, ?)`,
		timestamp, eventType, rawJSON)
	return err
}

// InsertEvent stores any OTEL event into the events table.
func (r *Repository) InsertEvent(ctx context.Context, e *Event) error {
	successInt := 0
	if e.Success > 0 {
		successInt = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO events
			(timestamp, session_id, user_id, prompt_id, prompt_length,
			 event_name, event_sequence, model, input_tokens, output_tokens,
			 cache_read_tokens, cache_creation_tokens, cost_usd, duration_ms,
			 speed, terminal_type, tool_name, decision, source, success, tool_result_size_bytes,
			 service_name, service_version, host_arch, os_type, os_version,
			 error_type, error_message, error_code, error_retryable)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Timestamp.Unix(),
		e.SessionID, e.UserID, e.PromptID, e.PromptLength,
		e.EventName, e.EventSequence, e.Model,
		e.InputTokens, e.OutputTokens,
		e.CacheReadTokens, e.CacheCreationTokens,
		e.CostUSD, e.DurationMs,
		e.Speed, e.TerminalType, e.ToolName, e.Decision, e.Source,
		successInt, e.ToolResultSizeBytes,
		e.ServiceName, e.ServiceVersion, e.HostArch, e.OSType, e.OSVersion,
		e.ErrorType, e.ErrorMessage, e.ErrorCode, e.ErrorRetryable,
	)
	return err
}

// InsertUserPrompt stores a user_prompt event into the dedicated table.
func (r *Repository) InsertUserPrompt(ctx context.Context, e *Event) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_prompt_events
			(timestamp, session_id, user_id, prompt_id, prompt_text, prompt_length, event_sequence,
			 terminal_type, service_name, service_version, host_arch, os_type, os_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Timestamp.Unix(),
		e.SessionID, e.UserID, e.PromptID, e.PromptText, e.PromptLength, e.EventSequence,
		e.TerminalType, e.ServiceName, e.ServiceVersion, e.HostArch, e.OSType, e.OSVersion,
	)
	return err
}

// InsertToolDecision stores a tool_decision event into the dedicated table.
func (r *Repository) InsertToolDecision(ctx context.Context, e *Event) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO tool_decision_events
			(timestamp, session_id, user_id, prompt_id, event_sequence,
			 tool_name, decision, source, terminal_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Timestamp.Unix(),
		e.SessionID, e.UserID, e.PromptID, e.EventSequence,
		e.ToolName, e.Decision, e.Source, e.TerminalType,
	)
	return err
}

// InsertToolResult stores a tool_result event into the dedicated table.
func (r *Repository) InsertToolResult(ctx context.Context, e *Event) error {
	successInt := 0
	if e.Success > 0 {
		successInt = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO tool_result_events
			(timestamp, session_id, user_id, prompt_id, event_sequence,
			 tool_name, success, duration_ms, tool_result_size_bytes,
			 decision_source, decision_type, terminal_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Timestamp.Unix(),
		e.SessionID, e.UserID, e.PromptID, e.EventSequence,
		e.ToolName, successInt, e.DurationMs, e.ToolResultSizeBytes,
		e.DecisionSource, e.DecisionType, e.TerminalType,
	)
	return err
}

// InsertAPIError stores an api_error event into the dedicated table.
func (r *Repository) InsertAPIError(ctx context.Context, e *Event) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO api_error_events
			(timestamp, session_id, user_id, prompt_id, event_sequence,
			 model, duration_ms, terminal_type,
			 error_type, error_message, error_code, error_retryable,
			 service_name, service_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Timestamp.Unix(),
		e.SessionID, e.UserID, e.PromptID, e.EventSequence,
		e.Model, e.DurationMs, e.TerminalType,
		e.ErrorType, e.ErrorMessage, e.ErrorCode, e.ErrorRetryable,
		e.ServiceName, e.ServiceVersion,
	)
	return err
}

// InsertMetricPoint stores one OTLP sum metric data point (all Claude Code metrics use sum).
func (r *Repository) InsertMetricPoint(ctx context.Context, timestampUnix int64, metricName string, value float64, sessionID, userID, terminalType, model, attrType string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO otel_metric_points (timestamp, metric_name, value, session_id, user_id, terminal_type, model, attr_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		timestampUnix, metricName, value, sessionID, userID, terminalType, model, attrType,
	)
	return err
}

// GetDashboard returns today's aggregated KPIs.
func (r *Repository) GetDashboard(ctx context.Context) (*Dashboard, error) {
	today := time.Now().Local().Format("2006-01-02")
	return r.GetDashboardForRange(ctx, today, today)
}

// GetDashboardForRange returns aggregated KPIs (cost, tokens, cache hit, requests) for a date range.
func (r *Repository) GetDashboardForRange(ctx context.Context, from, to string) (*Dashboard, error) {
	d := &Dashboard{}
	var totalCost, totalInput, totalOutput, totalCacheRead, totalCacheCreation int64

	err := r.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(request_count), 0)
		FROM daily_model_agg
		WHERE date >= ? AND date <= ?`, from, to).Scan(
		&totalCost, &totalInput, &totalOutput,
		&totalCacheRead, &totalCacheCreation, &d.RequestCount,
	)
	if err != nil {
		return nil, fmt.Errorf("dashboard query: %w", err)
	}

	// total_input_tokens = full input-side (uncached + cache read + cache create)
	d.TotalInputTokens = totalInput + totalCacheRead + totalCacheCreation
	d.TotalOutputTokens = totalOutput
	d.TotalCacheReadTokens = totalCacheRead
	d.TotalCostUSD = costToFloat64(totalCost)

	totalCacheTokens := totalCacheRead + totalCacheCreation
	if totalCacheTokens > 0 {
		d.CacheHitRate = float64(totalCacheRead) / float64(totalCacheTokens)
	}
	return d, nil
}

// GetDailyStats returns per-day aggregated token and cost statistics.
func (r *Repository) GetDailyStats(ctx context.Context, from, to string) ([]DailySummary, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			date,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(request_count), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_read_tokens + cache_creation_tokens), 0)
		FROM daily_model_agg
		WHERE date >= ? AND date <= ?
		GROUP BY date
		ORDER BY date`, from, to)
	if err != nil {
		return nil, fmt.Errorf("daily stats query: %w", err)
	}
	defer rows.Close()

	var result []DailySummary
	for rows.Next() {
		var s DailySummary
		var totalCost, cacheRead, cacheTotal int64
		if err := rows.Scan(&s.Date, &s.TotalInputTokens, &s.TotalOutputTokens, &totalCost, &s.RequestCount, &cacheRead, &cacheTotal); err != nil {
			return nil, err
		}
		s.TotalCostUSD = costToFloat64(totalCost)
		if cacheTotal > 0 {
			s.CacheHitRate = float64(cacheRead) / float64(cacheTotal)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// GetDailyStatsByModel returns per-(date, model) token and cost breakdown with pagination.
func (r *Repository) GetDailyStatsByModel(ctx context.Context, from, to string, limit, offset int, granularity string) ([]DailyModelSummary, error) {
	dateExpr := aggDateExpr(granularity)
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			`+dateExpr+`,
			model,
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(request_count), 0)
		FROM daily_model_agg
		WHERE date >= ? AND date <= ?
		GROUP BY `+dateExpr+`, model
		ORDER BY `+dateExpr+` DESC, SUM(cost_usd) DESC LIMIT ? OFFSET ?`, from, to, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("daily model stats query: %w", err)
	}
	defer rows.Close()

	var result []DailyModelSummary
	for rows.Next() {
		var s DailyModelSummary
		var totalCost int64
		if err := rows.Scan(&s.Date, &s.Model, &totalCost, &s.InputTokens, &s.OutputTokens,
			&s.CacheReadTokens, &s.CacheCreationTokens, &s.RequestCount); err != nil {
			return nil, err
		}
		s.CostUSD = costToFloat64(totalCost)
		result = append(result, s)
	}
	return result, rows.Err()
}

// GetHourlyStatsByModel returns per-(local-hour, model) aggregated stats for a single local day.
// date must be YYYY-MM-DD (local time). Optional model filter narrows rows for the given model.
func (r *Repository) GetHourlyStatsByModel(ctx context.Context, date string, model string) ([]HourlyModelSummary, error) {
	fromUnix, toExclusiveUnix, err := localDateRangeToUnix(date, date)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT
			CAST(strftime('%H', timestamp, 'unixepoch', 'localtime') AS INTEGER) AS hour,
			model,
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(COUNT(*), 0)
		FROM api_requests
		WHERE timestamp >= ? AND timestamp < ?
	`
	args := []interface{}{fromUnix, toExclusiveUnix}
	if strings.TrimSpace(model) != "" {
		query += ` AND model = ?`
		args = append(args, model)
	}
	query += `
		GROUP BY hour, model
		ORDER BY hour ASC, SUM(cost_usd) DESC
	`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("hourly model stats query: %w", err)
	}
	defer rows.Close()

	var result []HourlyModelSummary
	for rows.Next() {
		var s HourlyModelSummary
		var totalCost int64
		if err := rows.Scan(&s.Hour, &s.Model, &totalCost, &s.InputTokens, &s.OutputTokens,
			&s.CacheReadTokens, &s.CacheCreationTokens, &s.RequestCount); err != nil {
			return nil, err
		}
		s.CostUSD = costToFloat64(totalCost)
		result = append(result, s)
	}
	return result, rows.Err()
}

// CountDailyStatsByModel returns the number of distinct (date, model) groups.
func (r *Repository) CountDailyStatsByModel(ctx context.Context, from, to string, granularity string) (int64, error) {
	dateExpr := aggDateExpr(granularity)
	var count int64
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT 1 FROM daily_model_agg
			WHERE date >= ? AND date <= ?
			GROUP BY `+dateExpr+`, model
		)`, from, to).Scan(&count)
	return count, err
}

// requestWhereClause builds a shared WHERE clause + args for api_requests queries
// filtered by optional model and local date range [from, to].
func requestWhereClause(model, from, to string) (string, []interface{}, error) {
	var where []string
	var args []interface{}
	if model != "" {
		where = append(where, "model = ?")
		args = append(args, model)
	}
	if from != "" && to != "" {
		fromUnix, toExclusiveUnix, err := localDateRangeToUnix(from, to)
		if err != nil {
			return "", nil, err
		}
		where = append(where, "timestamp >= ?", "timestamp < ?")
		args = append(args, fromUnix, toExclusiveUnix)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	return clause, args, nil
}

// GetRecentRequests returns individual API request records with optional model and date filters.
func (r *Repository) GetRecentRequests(ctx context.Context, limit, offset int, model, from, to string) ([]APIRequest, error) {
	whereClause, args, err := requestWhereClause(model, from, to)
	if err != nil {
		return nil, err
	}
	query := `SELECT id, timestamp, session_id, user_id, prompt_id, prompt_length,
		model, actual_model, input_tokens, output_tokens,
		cache_read_tokens, cache_creation_tokens, cost_usd, duration_ms, ttft_ms, request_id,
		event_name, event_sequence, speed, terminal_type, tool_name, decision, source,
		service_name, service_version, host_arch, os_type, os_version,
		error_type, error_message, error_code, error_retryable
		FROM api_requests` + whereClause + ` ORDER BY timestamp DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("recent requests query: %w", err)
	}
	defer rows.Close()

	var result []APIRequest
	for rows.Next() {
		var req APIRequest
		var ts, cost int64
		if err := rows.Scan(&req.ID, &ts, &req.SessionID, &req.UserID, &req.PromptID, &req.PromptLength,
			&req.Model, &req.ActualModel,
			&req.InputTokens, &req.OutputTokens,
			&req.CacheReadTokens, &req.CacheCreationTokens,
			&cost, &req.DurationMs, &req.TTFTMs, &req.RequestID,
			&req.EventName, &req.EventSequence, &req.Speed, &req.TerminalType, &req.ToolName, &req.Decision, &req.Source,
			&req.ServiceName, &req.ServiceVersion, &req.HostArch, &req.OSType, &req.OSVersion,
			&req.ErrorType, &req.ErrorMessage, &req.ErrorCode, &req.ErrorRetryable); err != nil {
			return nil, err
		}
		req.Timestamp = time.Unix(ts, 0)
		req.CostUSD = costToFloat64(cost)
		result = append(result, req)
	}
	return result, rows.Err()
}

// GetDistinctModels returns all unique model names in the database.
func (r *Repository) GetDistinctModels(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT model FROM daily_model_agg WHERE model != '' ORDER BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var models []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

// CountRecentRequests returns the total number of API request records matching the filters.
func (r *Repository) CountRecentRequests(ctx context.Context, model, from, to string) (int64, error) {
	whereClause, args, err := requestWhereClause(model, from, to)
	if err != nil {
		return 0, err
	}
	var count int64
	err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_requests`+whereClause, args...).Scan(&count)
	return count, err
}

type SessionStat struct {
	SessionID    string    `json:"session_id"`
	UserID       string    `json:"user_id"`
	StartTime    time.Time `json:"start_time"`
	RequestCount int64     `json:"request_count"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
}

// GetSessionStats returns per-session aggregated stats ordered by cost descending.
func (r *Repository) GetSessionStats(ctx context.Context, from, to string, limit, offset int) ([]SessionStat, error) {
	fromUnix, toExclusiveUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			session_id,
			MAX(user_id),
			MIN(timestamp),
			COUNT(*),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cost_usd), 0)
		FROM api_requests
		WHERE session_id != ''
		  AND timestamp >= ? AND timestamp < ?
		GROUP BY session_id
		ORDER BY SUM(cost_usd) DESC
		LIMIT ? OFFSET ?`, fromUnix, toExclusiveUnix, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("session stats query: %w", err)
	}
	defer rows.Close()

	var result []SessionStat
	for rows.Next() {
		var s SessionStat
		var ts, cost int64
		if err := rows.Scan(&s.SessionID, &s.UserID, &ts, &s.RequestCount, &s.InputTokens, &s.OutputTokens, &cost); err != nil {
			return nil, err
		}
		s.StartTime = time.Unix(ts, 0)
		s.CostUSD = costToFloat64(cost)
		result = append(result, s)
	}
	return result, rows.Err()
}

// CountSessionStats returns the number of distinct sessions in the date range.
func (r *Repository) CountSessionStats(ctx context.Context, from, to string) (int64, error) {
	fromUnix, toExclusiveUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return 0, err
	}
	var count int64
	err = r.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT session_id) FROM api_requests
		WHERE session_id != ''
		  AND timestamp >= ? AND timestamp < ?`, fromUnix, toExclusiveUnix).Scan(&count)
	return count, err
}

// Cleanup deletes records older than beforeUnix from all event tables and daily_model_agg.
func (r *Repository) Cleanup(ctx context.Context, beforeUnix int64) (int64, error) {
	var total int64

	// Timestamp-based tables (unix epoch column "timestamp")
	tsTable := []string{
		"raw_otlp_events",
		"events",
		"user_prompt_events",
		"tool_decision_events",
		"tool_result_events",
		"api_error_events",
	}
	for _, tbl := range tsTable {
		res, err := r.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE timestamp < ?`, tbl), beforeUnix)
		if err != nil {
			return total, fmt.Errorf("cleanup %s: %w", tbl, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}

	// Date-string based table
	beforeDate := time.Unix(beforeUnix, 0).Local().Format("2006-01-02")
	res, err := r.db.ExecContext(ctx, `DELETE FROM daily_model_agg WHERE date < ?`, beforeDate)
	if err != nil {
		return total, fmt.Errorf("cleanup daily_model_agg: %w", err)
	}
	n, _ := res.RowsAffected()
	total += n

	r.db.ExecContext(ctx, "PRAGMA incremental_vacuum")
	return total, nil
}
