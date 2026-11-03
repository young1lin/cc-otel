package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
)

// CostScale is the cost unit scale: 1 USD = 100000 int64 units (0.00001
// precision). Exported so recompute and any future writer share one
// constant (R3 review A1-F3).
const CostScale = 100000

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

	// Unified-client (AllClient) columns. Claude rows leave these zero/empty;
	// Codex rows (service_name='codex') carry them since the two pools merged.
	ReasoningTokens int64  `json:"reasoning_tokens"`
	TotalTokens     int64  `json:"total_tokens"`
	HTTPStatus      int64  `json:"http_status"`
	Endpoint        string `json:"endpoint"`
	ConversationID  string `json:"conversation_id"`
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
// NOTE on total_input_tokens: /api/dashboard fills it with the FOLDED input
// side (SUM(input+cache_read+cache_creation)); the legacy codex shim
// (GetCodexDashboard) fills it with RAW SUM(input_tokens) and exposes the
// cache parts separately — consumers must not mix the two endpoints.
type Dashboard struct {
	TotalCostUSD             float64 `json:"total_cost_usd"`
	TotalInputTokens         int64   `json:"total_input_tokens"`          // folded input side on /api/dashboard; raw SUM(input) on the codex shim
	TotalCacheReadTokens     int64   `json:"total_cache_read_tokens"`     // SUM(cache_read_tokens) — matches column "Cache Read"
	TotalCacheCreationTokens int64   `json:"total_cache_creation_tokens"` // codex shim only; keeps cache_hit_rate payload-reproducible
	TotalOutputTokens        int64   `json:"total_output_tokens"`
	CacheHitRate             float64 `json:"cache_hit_rate"`
	RequestCount             int64   `json:"request_count"`
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

// DefaultServiceName is the telemetry client attributed to rows that arrive
// without an OTLP resource service.name. Historically every row in these tables
// came from Claude Code, so that stays the back-compatible default.
const DefaultServiceName = "claude-code"

// NormalizeServiceName folds a missing/blank service_name onto DefaultServiceName.
// Every write path (receiver, merge/import, aggregate rebuild) must agree on
// this, otherwise the same client shows up as both ” and 'claude-code'.
func NormalizeServiceName(serviceName string) string {
	if strings.TrimSpace(serviceName) == "" {
		return DefaultServiceName
	}
	return serviceName
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

// CalendarDay holds one local-day aggregate for the usage calendar heatmap.
type CalendarDay struct {
	Date                string  `json:"date"`
	TotalTokens         int64   `json:"total_tokens"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CostUSD             float64 `json:"cost_usd"`
	RequestCount        int64   `json:"request_count"`
	TopModel            string  `json:"top_model"`
}

// ClientSummary is one telemetry client's (service_name) aggregate for a date
// range — the per-client token accounting behind /api/clients and the Clients
// panel. Rows that predate service_name attribution fold onto DefaultServiceName.
type ClientSummary struct {
	ServiceName         string  "json:\"service_name\""
	ModelCount          int64   "json:\"model_count\""
	InputTokens         int64   "json:\"input_tokens\""
	OutputTokens        int64   "json:\"output_tokens\""
	CacheReadTokens     int64   "json:\"cache_read_tokens\""
	CacheCreationTokens int64   "json:\"cache_creation_tokens\""
	TotalTokens         int64   "json:\"total_tokens\""
	CostUSD             float64 "json:\"cost_usd\""
	RequestCount        int64   "json:\"request_count\""
	CacheHitRate        float64 "json:\"cache_hit_rate\""
	FirstDate           string  "json:\"first_date\""
	LastDate            string  "json:\"last_date\""
}

// DurationStat holds per-model latency and throughput stats for a time range.
//
// duration_ms is the end-to-end Claude API request latency reported by Claude Code:
// it covers server-side queueing + TTFT + streaming generation, but NOT local tool
// execution (Claude Code reports tool time on a separate tool_result event/table).
// So every tokens/s figure below inherently EXCLUDES local tool execution time.
//
// Two throughput flavors are provided for the same output/total token counts:
//   - Avg*TokensPS      = AVG(tokens*1000/duration_ms): arithmetic mean of each
//     request's tok/s; every request weighted equally.
//   - Weighted*TokensPS = SUM(tokens)*1000/SUM(duration_ms): overall throughput,
//     i.e. total tokens / total time (long/slow queued requests dominate).
type DurationStat struct {
	Model               string  `json:"model"`
	RequestCount        int64   `json:"request_count"`
	AvgDurationMs       float64 `json:"avg_duration_ms"`
	AvgTTFTMs           float64 `json:"avg_ttft_ms"`
	AvgOutTokensPS      float64 `json:"avg_out_tokens_per_s"`
	AvgTotTokensPS      float64 `json:"avg_total_tokens_per_s"`
	WeightedOutTokensPS float64 `json:"weighted_out_tokens_per_s"`
	WeightedTotTokensPS float64 `json:"weighted_total_tokens_per_s"`
	MaxDurationMs       int64   `json:"max_duration_ms"`
	MinDurationMs       int64   `json:"min_duration_ms"`
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

// IntradayModelSummary holds per-(time-bucket, model) aggregated stats for the
// intraday bar-chart view, supporting 5/10/15/30/60-minute buckets and a span
// of up to 7 days. BucketStartUnix is the inclusive start of the bucket (UTC
// seconds); BucketLabel is the same instant rendered in local time as
// "MM-DD HH:MM" and is what the frontend displays.
type IntradayModelSummary struct {
	BucketStartUnix     int64   `json:"bucket_start_unix"`
	BucketLabel         string  `json:"bucket_label"`
	BucketMinutes       int     `json:"bucket_minutes"`
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
	db             *sql.DB
	stmtInsReq     *sql.Stmt // prepared: INSERT OR IGNORE INTO api_requests
	stmtUpsAgg     *sql.Stmt // prepared: UPSERT INTO daily_model_agg
	stmtUpsAggToks *sql.Stmt // prepared: UPSERT INTO daily_model_agg (token deltas only)
}

// NewRepository returns a Repository backed by the given database connection.
// Prepared statements are created for the hot-path InsertRequest to avoid
// repeated SQL parsing on every call.
func NewRepository(db *sql.DB) *Repository {
	r := &Repository{db: db}

	// A Prepare failure leaves a nil *sql.Stmt; every later Exec panics.
	// There is no per-call fallback anywhere: schema drift must surface as a
	// loud startup panic naming the statement, not a first-request nil deref
	// mid-flight (R3 error-recovery F6 — the old log line claimed a fallback
	// that never existed; R4 regression review confirmed the zombie path
	// crashed on first write anyway).
	prepare := func(target **sql.Stmt, label, query string) {
		stmt, err := db.Prepare(query)
		if err != nil {
			panic(fmt.Sprintf("repository: prepare %s failed: %v", label, err))
		}
		*target = stmt
	}
	prepare(&r.stmtInsReq, "insert request", `
		INSERT OR IGNORE INTO api_requests
			(timestamp, session_id, user_id, prompt_id, prompt_length,
			 model, actual_model, input_tokens, output_tokens,
			 cache_read_tokens, cache_creation_tokens, cost_usd, duration_ms, ttft_ms, request_id,
			 event_name, event_sequence, speed, terminal_type, tool_name, decision, source,
			 service_name, service_version, host_arch, os_type, os_version,
			 error_type, error_message, error_code, error_retryable,
			 reasoning_tokens, total_tokens, http_status, endpoint, conversation_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)

	prepare(&r.stmtUpsAgg, "upsert daily_model_agg", `
		INSERT INTO daily_model_agg (date, model, service_name, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens, cost_usd, request_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(date, model, service_name) DO UPDATE SET
			input_tokens          = input_tokens + excluded.input_tokens,
			output_tokens         = output_tokens + excluded.output_tokens,
			cache_read_tokens     = cache_read_tokens + excluded.cache_read_tokens,
			cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
			cost_usd              = cost_usd + excluded.cost_usd,
			request_count         = request_count + 1`)

	// Unified Codex completion upsert: one logical request is written twice —
	// once for codex.api_request (INSERT OR IGNORE + request_count++ via
	// stmtInsReq/stmtUpsAgg), once for codex.sse_event(response.completed)
	// (token/cost/duration deltas, request_count unchanged). Keeping the two
	// paths on separate UPSERTs is what stops a request from double-counting.
	prepare(&r.stmtUpsAggToks, "upsert agg tokens", `
		INSERT INTO daily_model_agg (date, model, service_name, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens, cost_usd, request_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(date, model, service_name) DO UPDATE SET
			input_tokens          = input_tokens + excluded.input_tokens,
			output_tokens         = output_tokens + excluded.output_tokens,
			cache_read_tokens     = cache_read_tokens + excluded.cache_read_tokens,
			cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
			cost_usd              = cost_usd + excluded.cost_usd`)

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
	if r.stmtUpsAggToks != nil {
		r.stmtUpsAggToks.Close()
	}
}

// Ping verifies the database connection is alive.
func (r *Repository) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

// DB returns the underlying *sql.DB for ad-hoc queries (e.g. tests, custom
// admin endpoints). Production code should prefer the typed methods.
func (r *Repository) DB() *sql.DB {
	return r.db
}

func costToInt64(usd float64) int64 {
	return int64(math.Round(usd * CostScale))
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
	return fromT.Unix(), toT.AddDate(0, 0, 1).Unix(), nil // calendar next midnight: Add(24h) is wrong on DST days (R1 A1-2)
}

func costToFloat64(units int64) float64 {
	return float64(units) / CostScale
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
	var reqExists bool
	if err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM api_requests)`).Scan(&reqExists); err != nil {
		return false, err
	}
	if aggEmpty {
		return reqExists, nil
	}
	if !reqExists {
		return false, nil
	}
	// Coverage-gap self-heal: api_requests is deliberately never
	// retention-pruned while daily_model_agg used to be, so a database that
	// ever ran an older build can have detail rows OLDER than the oldest agg
	// row — the All time range then silently loses those months (live-caught:
	// detail from 2026-04, agg starting at the 90-day cutoff). Rebuild so the
	// aggregate always covers the detail's full span.
	var aggMin, reqMin string
	if err := r.db.QueryRowContext(ctx, `
		SELECT (SELECT MIN(date) FROM daily_model_agg),
		       (SELECT date(MIN(timestamp), 'unixepoch', 'localtime') FROM api_requests)`).Scan(&aggMin, &reqMin); err != nil {
		return false, err
	}
	return reqMin != "" && (aggMin == "" || aggMin > reqMin), nil
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

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(rebuildDailyAggSQL, DefaultServiceName)); err != nil {
		return fmt.Errorf("rebuild daily_model_agg: %w", err)
	}

	return tx.Commit()
}

// InsertRequest stores a single API request record, ignoring duplicates by request_id.
// It also upserts the daily_model_agg pre-aggregation table within the same transaction.
// Returns true if a new row was inserted (false for duplicates).
// InsertRequest persists a request and its daily aggregate in one transaction,
// retrying on SQLITE_BUSY(_SNAPSHOT).
//
// The tx is deferred and the insert reads before it writes (request_id dedup),
// so when a concurrent writer — an online import merging into the live DB —
// commits between this tx's snapshot and its write, SQLite returns BUSY
// immediately: busy_timeout never applies to that snapshot upgrade. A fresh
// transaction re-snapshots, and the request_id dedup makes the whole insert
// idempotent, so retrying is safe. The race window was effectively unreachable
// until CC_OTEL_TEST_NOSYNC sped test timing up (2026-08-17).
func (r *Repository) InsertRequest(ctx context.Context, req *APIRequest) (bool, error) {
	for attempt := 0; ; attempt++ {
		inserted, err := r.insertRequestOnce(ctx, req)
		if err == nil || attempt >= 3 || !isBusyErr(err) {
			return inserted, err
		}
		select {
		case <-ctx.Done():
			return inserted, ctx.Err()
		case <-time.After(time.Duration(25<<attempt) * time.Millisecond): // 25, 50, 100ms (the 200ms step is never reached)
		}
	}
}

func (r *Repository) insertRequestOnce(ctx context.Context, req *APIRequest) (bool, error) {
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
		req.ReasoningTokens, req.TotalTokens, req.HTTPStatus, req.Endpoint, req.ConversationID,
	)
	if err != nil {
		return false, err
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return false, tx.Commit()
	}

	// If TTFT wasn't present on the log event, try to apply a pending TTFT span that arrived earlier.
	// This solves the common ordering issue: traces exporter flushes before logs exporter.
	if req.TTFTMs == 0 {
		if reqID, idErr := res.LastInsertId(); idErr == nil && reqID > 0 {
			_, _ = r.applyPendingTTFTForRequestTx(ctx, tx, reqID, req.RequestID, req.SessionID, req.Model, req.Timestamp.Unix(), 120)
		}
	}

	dateKey := req.Timestamp.Local().Format("2006-01-02")
	cost := costToInt64(req.CostUSD)

	// Normalize here too: RebuildDailyAggregates and dbmerge both fold a blank
	// service_name into DefaultServiceName, so a raw '' written by this path
	// would split into a separate agg row that a later rebuild silently merges
	// away — making the same data move between rows.
	_, err = tx.StmtContext(ctx, r.stmtUpsAgg).ExecContext(ctx,
		dateKey, req.Model, NormalizeServiceName(req.ServiceName),
		req.InputTokens, req.OutputTokens,
		req.CacheReadTokens, req.CacheCreationTokens,
		cost,
	)
	if err != nil {
		return false, fmt.Errorf("upsert daily_model_agg: %w", err)
	}

	return true, tx.Commit()
}

// isBusyErr reports whether err is lock contention SQLite would not wait out
// on its own — busy_timeout does not cover deferred-tx snapshot upgrades.
// Same predicate dbmerge applies to its own writers (import.go).
func isBusyErr(err error) bool {
	return errors.Is(err, sqlite3.BUSY) || errors.Is(err, sqlite3.BUSY_SNAPSHOT)
}

func (r *Repository) applyPendingTTFTForRequestTx(ctx context.Context, tx *sql.Tx, apiRequestID int64, requestID, sessionID, model string, reqUnix int64, windowSec int64) (bool, error) {
	if r == nil || r.db == nil || tx == nil || apiRequestID <= 0 {
		return false, nil
	}
	if windowSec <= 0 {
		windowSec = 120
	}

	var pendingID int64
	var ttftMs int64
	var err error
	if requestID != "" {
		err = tx.QueryRowContext(ctx, `
			SELECT id, ttft_ms
			FROM pending_ttft_spans
			WHERE processed = 0
			  AND request_id = ?
			ORDER BY ABS(span_end_unix - ?) ASC
			LIMIT 1
		`, requestID, reqUnix).Scan(&pendingID, &ttftMs)
	}
	if requestID == "" || err == sql.ErrNoRows {
		if sessionID == "" || model == "" {
			return false, nil
		}
		err = tx.QueryRowContext(ctx, `
			SELECT id, ttft_ms
			FROM pending_ttft_spans
			WHERE processed = 0
			  AND session_id = ?
			  AND model = ?
			  AND span_end_unix BETWEEN (? - ?) AND (? + ?)
			ORDER BY ABS(span_end_unix - ?) ASC
			LIMIT 1
		`, sessionID, model, reqUnix, windowSec, reqUnix, windowSec, reqUnix).Scan(&pendingID, &ttftMs)
	}
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if ttftMs <= 0 {
		return false, nil
	}

	// Update the newly inserted row only.
	res, err := tx.ExecContext(ctx, `UPDATE api_requests SET ttft_ms = ? WHERE id = ? AND ttft_ms = 0`, ttftMs, apiRequestID)
	if err != nil {
		return false, err
	}
	aff, _ := res.RowsAffected()
	if aff <= 0 {
		return false, nil
	}

	_, _ = tx.ExecContext(ctx, `UPDATE pending_ttft_spans SET processed = 1 WHERE id = ?`, pendingID)
	return true, nil
}

// InsertRawEvent stores the complete original OTEL event as JSON for future re-processing.
// A future-dated client clock would otherwise pin the row outside every TTL
// sweep forever (CleanupRaw only deletes timestamp < before), so timestamps
// are clamped to now (R3 review: malformed clocks must not outlive retention;
// rationale reworded in the R4 docs pass — raw_otlp_events has no MIN()
// consumer, the TTL escape was the real issue).
func (r *Repository) InsertRawEvent(ctx context.Context, eventType string, timestamp int64, rawJSON string) error {
	if now := time.Now().Unix(); timestamp > now+60 {
		timestamp = now
	}
	_, err := r.execWithBusyRetry(ctx,
		`INSERT INTO raw_otlp_events (timestamp, event_type, raw_json) VALUES (?, ?, ?)`,
		timestamp, eventType, rawJSON)
	return err
}

func (r *Repository) EnqueuePendingTTFTSpan(ctx context.Context, requestID, sessionID, model string, spanEndUnix int64, ttftMs int64, rawJSON string) error {
	if r == nil || r.db == nil || spanEndUnix <= 0 || ttftMs <= 0 || rawJSON == "" {
		return nil
	}
	if requestID == "" && (sessionID == "" || model == "") {
		return nil
	}
	_, err := r.execWithBusyRetry(ctx, `
		INSERT INTO pending_ttft_spans (created_unix, session_id, model, span_end_unix, ttft_ms, raw_json, processed, request_id)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?)
	`, time.Now().Unix(), sessionID, model, spanEndUnix, ttftMs, rawJSON, requestID)
	return err
}

// BackfillTTFTByRequestID updates api_requests.ttft_ms by the exact upstream request id.
func (r *Repository) BackfillTTFTByRequestID(ctx context.Context, requestID string, ttftMs int64) (bool, error) {
	if r == nil || r.db == nil || requestID == "" || ttftMs <= 0 {
		return false, nil
	}
	res, err := r.execWithBusyRetry(ctx, `UPDATE api_requests SET ttft_ms = ? WHERE request_id = ? AND ttft_ms = 0`, ttftMs, requestID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// BackfillTTFTNearest best-effort updates api_requests.ttft_ms for the request that most likely
// corresponds to the given trace span.
//
// Strategy: match on (session_id, prompt_id, model) and choose the row whose timestamp is closest
// to spanEndUnix (seconds). Only updates when ttft_ms is currently 0 and ttftMs > 0.
func (r *Repository) BackfillTTFTNearest(ctx context.Context, sessionID, promptID, model string, spanEndUnix int64, ttftMs int64) (bool, error) {
	if r == nil || r.db == nil || ttftMs <= 0 {
		return false, nil
	}

	var id int64
	err := r.db.QueryRowContext(ctx, `
		SELECT id
		FROM claude_pool_requests
		WHERE session_id = ?
		  AND prompt_id = ?
		  AND model = ?
		  AND ttft_ms = 0
		ORDER BY ABS(timestamp - ?) ASC
		LIMIT 1
	`, sessionID, promptID, model, spanEndUnix).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	res, err := r.execWithBusyRetry(ctx, `UPDATE api_requests SET ttft_ms = ? WHERE id = ? AND ttft_ms = 0`, ttftMs, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// BackfillTTFTNearestLoose is a fallback when trace spans don't include prompt_id.
// It matches on (session_id, model) within a small time window around spanEndUnix (seconds),
// then picks the closest request. Only updates when ttft_ms is currently 0.
func (r *Repository) BackfillTTFTNearestLoose(ctx context.Context, sessionID, model string, spanEndUnix int64, windowSec int64, ttftMs int64) (bool, error) {
	if r == nil || r.db == nil || ttftMs <= 0 || sessionID == "" || model == "" {
		return false, nil
	}
	if windowSec <= 0 {
		windowSec = 120
	}

	var id int64
	err := r.db.QueryRowContext(ctx, `
		SELECT id
		FROM claude_pool_requests
		WHERE session_id = ?
		  AND model = ?
		  AND ttft_ms = 0
		  AND timestamp BETWEEN (? - ?) AND (? + ?)
		ORDER BY ABS(timestamp - ?) ASC
		LIMIT 1
	`, sessionID, model, spanEndUnix, windowSec, spanEndUnix, windowSec, spanEndUnix).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	res, err := r.execWithBusyRetry(ctx, `UPDATE api_requests SET ttft_ms = ? WHERE id = ? AND ttft_ms = 0`, ttftMs, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// InsertEvent stores any OTEL event into the events table.
func (r *Repository) InsertEvent(ctx context.Context, e *Event) error {
	successInt := 0
	if e.Success > 0 {
		successInt = 1
	}
	_, err := r.execWithBusyRetry(ctx, `
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
	_, err := r.execWithBusyRetry(ctx, `
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
	_, err := r.execWithBusyRetry(ctx, `
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
	_, err := r.execWithBusyRetry(ctx, `
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
	_, err := r.execWithBusyRetry(ctx, `
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
	_, err := r.execWithBusyRetry(ctx, `
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
		FROM claude_pool_agg
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

	// Cache hit rate = cache_read / input-side total (uncached + cache read + cache create).
	// Using the full input side (not just read+create) keeps the metric meaningful for
	// reverse-proxied providers (GLM, mimo, ...) that report cache_read but never
	// cache_creation — otherwise the ratio would collapse to a constant 100%. This matches
	// the Codex definition (cache_read / input_tokens, where their input already
	// includes the cached portion).
	if d.TotalInputTokens > 0 {
		d.CacheHitRate = float64(totalCacheRead) / float64(d.TotalInputTokens)
	}
	return d, nil
}

// GetClientStats returns per-client (service_name) token/cost aggregates for a
// date range, largest total-token consumer first. It reads api_requests —
// the deliberately never-retention-pruned ledger — rather than
// daily_model_agg, whose rows are trimmed to the retention window: a client
// that only ran months ago must not vanish from this table. The GROUP BY is
// one indexed range scan; ~250k rows aggregate in the tens of milliseconds.
func (r *Repository) GetClientStats(ctx context.Context, from, to string) ([]ClientSummary, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			COALESCE(NULLIF(service_name, ''), ?) AS svc,
			COUNT(DISTINCT model),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COUNT(*),
			MIN(date(timestamp, 'unixepoch', 'localtime')),
			MAX(date(timestamp, 'unixepoch', 'localtime'))
		FROM claude_pool_requests
		WHERE timestamp >= ? AND timestamp < ?
		GROUP BY svc
		ORDER BY (COALESCE(SUM(input_tokens), 0) + COALESCE(SUM(output_tokens), 0)
			+ COALESCE(SUM(cache_read_tokens), 0) + COALESCE(SUM(cache_creation_tokens), 0)) DESC`,
		DefaultServiceName, fromUnix, toUnix)
	if err != nil {
		return nil, fmt.Errorf("client stats query: %w", err)
	}
	defer rows.Close()

	var result []ClientSummary
	for rows.Next() {
		var s ClientSummary
		var cost int64
		if err := rows.Scan(&s.ServiceName, &s.ModelCount, &s.InputTokens, &s.OutputTokens,
			&s.CacheReadTokens, &s.CacheCreationTokens, &cost, &s.RequestCount,
			&s.FirstDate, &s.LastDate); err != nil {
			return nil, err
		}
		s.TotalTokens = s.InputTokens + s.OutputTokens + s.CacheReadTokens + s.CacheCreationTokens
		s.CostUSD = costToFloat64(cost)
		// Same cache-hit definition as the dashboard KPI (see GetDashboardForRange).
		inputSide := s.InputTokens + s.CacheReadTokens + s.CacheCreationTokens
		if inputSide > 0 {
			s.CacheHitRate = float64(s.CacheReadTokens) / float64(inputSide)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// HourOfDayDetail is one (local hour-of-day, client, model) cell aggregated
// across every day in the range — the wire shape behind the Hour of Day
// view. The client pivots these rows: stack by model, by client, or pin
// both for a single series ("how much Claude Opus at 13:00, from Claude
// Code"). Complements the Intraday chart: that one is chronological
// per-bucket detail for a short span; this one folds the whole range into
// 24 hour buckets.
type HourOfDayDetail struct {
	Hour                int     "json:\"hour\""
	ServiceName         string  "json:\"service_name\""
	Model               string  "json:\"model\""
	InputTokens         int64   "json:\"input_tokens\""
	OutputTokens        int64   "json:\"output_tokens\""
	CacheReadTokens     int64   "json:\"cache_read_tokens\""
	CacheCreationTokens int64   "json:\"cache_creation_tokens\""
	TotalTokens         int64   "json:\"total_tokens\""
	CostUSD             float64 "json:\"cost_usd\""
	RequestCount        int64   "json:\"request_count\""
}

// GetHourOfDayStats aggregates api_requests into (local hour-of-day,
// client, model) cells for the date range. The client-side pivot turns
// these into "stack by model / by client / pinned single series" views.
// api_requests carries exact timestamps; daily_model_agg is date-granular
// and cannot answer hour-of-day questions.
func (r *Repository) GetHourOfDayStats(ctx context.Context, from, to string) ([]HourOfDayDetail, error) {
	fromUnix, toUnix, err := localDateRangeToUnix(from, to)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			CAST(strftime('%H', timestamp, 'unixepoch', 'localtime') AS INTEGER) AS h,
			COALESCE(NULLIF(service_name, ''), ?),
			model,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COUNT(*)
		FROM claude_pool_requests
		WHERE timestamp >= ? AND timestamp < ?
		GROUP BY h, COALESCE(NULLIF(service_name, ''), ?), model
		ORDER BY h, COALESCE(NULLIF(service_name, ''), ?), model`,
		DefaultServiceName, fromUnix, toUnix, DefaultServiceName, DefaultServiceName)
	if err != nil {
		return nil, fmt.Errorf("hour-of-day stats query: %w", err)
	}
	defer rows.Close()

	var result []HourOfDayDetail
	for rows.Next() {
		var s HourOfDayDetail
		var cost int64
		if err := rows.Scan(&s.Hour, &s.ServiceName, &s.Model, &s.InputTokens, &s.OutputTokens,
			&s.CacheReadTokens, &s.CacheCreationTokens, &cost, &s.RequestCount); err != nil {
			return nil, err
		}
		s.TotalTokens = s.InputTokens + s.OutputTokens + s.CacheReadTokens + s.CacheCreationTokens
		s.CostUSD = costToFloat64(cost)
		result = append(result, s)
	}
	return result, rows.Err()
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
			COALESCE(SUM(input_tokens + cache_read_tokens + cache_creation_tokens), 0)
		FROM claude_pool_agg
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
		var totalCost, cacheRead, inputSide int64
		if err := rows.Scan(&s.Date, &s.TotalInputTokens, &s.TotalOutputTokens, &totalCost, &s.RequestCount, &cacheRead, &inputSide); err != nil {
			return nil, err
		}
		s.TotalCostUSD = costToFloat64(totalCost)
		// cache_read / input-side total (see GetDashboardForRange for rationale).
		if inputSide > 0 {
			s.CacheHitRate = float64(cacheRead) / float64(inputSide)
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
		FROM claude_pool_agg
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

// GetCalendarDays returns compact per-day aggregates for the dashboard usage calendar.
func (r *Repository) GetCalendarDays(ctx context.Context, from, to string) ([]CalendarDay, error) {
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
		FROM claude_pool_agg
		WHERE date >= ? AND date <= ?
		GROUP BY date, model
		ORDER BY date ASC, model ASC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("calendar days query: %w", err)
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
		FROM claude_pool_requests
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

// GetIntradayStatsByModel returns per-(bucket, model) stats for [fromYMD, toYMD]
// (inclusive local days), using a fixed bucket size in minutes (5, 10, 15, 30, or 60).
// Buckets are computed by flooring api_requests.timestamp to the nearest
// bucket boundary in UTC seconds; for whole-hour-offset timezones (the common
// case) this aligns to local clock buckets exactly. The label is rendered in
// the server's time.Local zone so the frontend gets a display-ready string.
func (r *Repository) GetIntradayStatsByModel(ctx context.Context, fromYMD, toYMD string, bucketMinutes int, model string) ([]IntradayModelSummary, error) {
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
		FROM claude_pool_requests
		WHERE timestamp >= ? AND timestamp < ?
	`
	args := []any{bucketSec, fromUnix, toExclusiveUnix}
	if strings.TrimSpace(model) != "" {
		query += ` AND model = ?`
		args = append(args, model)
	}
	query += `
		GROUP BY bucket_start, model
		ORDER BY bucket_start ASC, SUM(cost_usd) DESC
	`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("intraday model stats query: %w", err)
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

// RateBucket holds per-(time-bucket, model) token throughput for the rate-over-time
// line chart. Only requests with duration_ms > 0 are counted, matching the durations
// table. duration_ms is the Claude API request latency (server queue + TTFT + streaming);
// local tool execution is a separate event, so it is excluded from every rate here.
//
// Two flavors are provided for each of output/total tokens:
//   - Avg*PerS      = AVG(tokens*1000/duration_ms) within the bucket: mean of each
//     request's tok/s, every request weighted equally.
//   - Weighted*PerS = SUM(tokens)*1000/SUM(duration_ms): the bucket's overall throughput.
type RateBucket struct {
	BucketStartUnix   int64   `json:"bucket_start_unix"`
	BucketLabel       string  `json:"bucket_label"`
	BucketMinutes     int     `json:"bucket_minutes"`
	Model             string  `json:"model"`
	RequestCount      int64   `json:"request_count"`
	OutTokens         int64   `json:"out_tokens"`
	TotalTokens       int64   `json:"total_tokens"`
	DurationMsSum     int64   `json:"duration_ms_sum"`
	AvgOutPerS        float64 `json:"avg_out_per_s"`
	AvgTotalPerS      float64 `json:"avg_total_per_s"`
	WeightedOutPerS   float64 `json:"weighted_out_per_s"`
	WeightedTotalPerS float64 `json:"weighted_total_per_s"`
}

// ValidRateBucketMinutes reports whether n is an allowed rate-chart bucket size.
func ValidRateBucketMinutes(n int) bool {
	return n == 5 || n == 10 || n == 15 || n == 30 || n == 60
}

// GetRateOverTime returns per-(bucket, model) token throughput for [fromYMD, toYMD]
// (inclusive local days) with a fixed bucket size in minutes (5, 10, 15, 30, or 60).
// Only requests with duration_ms > 0 participate. Empty buckets are omitted. If model
// is non-empty, results are restricted to that model.
func (r *Repository) GetRateOverTime(ctx context.Context, fromYMD, toYMD string, bucketMinutes int, model string) ([]RateBucket, error) {
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
			COUNT(*) AS request_count,
			COALESCE(SUM(output_tokens), 0) AS out_tokens,
			COALESCE(SUM(input_tokens + cache_read_tokens + cache_creation_tokens + output_tokens), 0) AS total_tokens,
			COALESCE(SUM(duration_ms), 0) AS duration_ms_sum,
			AVG(CAST(output_tokens AS REAL) * 1000.0 / CAST(duration_ms AS REAL)) AS avg_out_per_s,
			AVG(CAST((input_tokens + cache_read_tokens + cache_creation_tokens + output_tokens) AS REAL) * 1000.0 / CAST(duration_ms AS REAL)) AS avg_total_per_s
		FROM claude_pool_requests
		WHERE timestamp >= ? AND timestamp < ? AND duration_ms > 0
	`
	args := []any{bucketSec, fromUnix, toExclusiveUnix}
	if strings.TrimSpace(model) != "" {
		query += ` AND model = ?`
		args = append(args, model)
	}
	query += `
		GROUP BY bucket_start, model
		ORDER BY bucket_start ASC, model ASC
	`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("rate over time query: %w", err)
	}
	defer rows.Close()

	loc := time.Local
	var result []RateBucket
	for rows.Next() {
		var s RateBucket
		if err := rows.Scan(&s.BucketStartUnix, &s.Model, &s.RequestCount,
			&s.OutTokens, &s.TotalTokens, &s.DurationMsSum,
			&s.AvgOutPerS, &s.AvgTotalPerS); err != nil {
			return nil, err
		}
		if s.DurationMsSum > 0 {
			s.WeightedOutPerS = float64(s.OutTokens) * 1000.0 / float64(s.DurationMsSum)
			s.WeightedTotalPerS = float64(s.TotalTokens) * 1000.0 / float64(s.DurationMsSum)
		}
		s.BucketMinutes = bucketMinutes
		s.BucketLabel = time.Unix(s.BucketStartUnix, 0).In(loc).Format("01-02 15:04")
		result = append(result, s)
	}
	return result, rows.Err()
}

// SessionRateSnapshot is the token throughput for a session's most recent 1-minute
// window that had API activity (duration_ms > 0). Rates exclude local tool time,
// matching GetRateOverTime / GetDurationStatsByModel semantics.
type SessionRateSnapshot struct {
	SessionID         string  `json:"session_id"`
	BucketStartUnix   int64   `json:"bucket_start_unix"`
	BucketLabel       string  `json:"bucket_label"`
	BucketMinutes     int     `json:"bucket_minutes"`
	LastActiveUnix    int64   `json:"last_active_unix"`
	RequestCount      int64   `json:"request_count"`
	OutTokens         int64   `json:"out_tokens"`
	TotalTokens       int64   `json:"total_tokens"`
	DurationMsSum     int64   `json:"duration_ms_sum"`
	AvgOutPerS        float64 `json:"avg_out_per_s"`
	AvgTotalPerS      float64 `json:"avg_total_per_s"`
	WeightedOutPerS   float64 `json:"weighted_out_per_s"`
	WeightedTotalPerS float64 `json:"weighted_total_per_s"`
}

// GetSessionRecentMinuteRate returns throughput for the latest 1-minute bucket in
// which sessionID had at least one request with duration_ms > 0. Returns (nil, nil)
// when the session has no qualifying activity.
func (r *Repository) GetSessionRecentMinuteRate(ctx context.Context, sessionID string) (*SessionRateSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id required")
	}

	const bucketSec int64 = 60
	var lastActive sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT MAX(timestamp) FROM claude_pool_requests
		WHERE session_id = ? AND duration_ms > 0`, sessionID,
	).Scan(&lastActive)
	if err == sql.ErrNoRows || !lastActive.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session recent minute rate: %w", err)
	}

	bucketStart := lastActive.Int64 - (lastActive.Int64 % bucketSec)
	bucketEnd := bucketStart + bucketSec

	query := `
		SELECT
			COUNT(*) AS request_count,
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(input_tokens + cache_read_tokens + cache_creation_tokens + output_tokens), 0),
			COALESCE(SUM(duration_ms), 0),
			AVG(CAST(output_tokens AS REAL) * 1000.0 / CAST(duration_ms AS REAL)),
			AVG(CAST((input_tokens + cache_read_tokens + cache_creation_tokens + output_tokens) AS REAL) * 1000.0 / CAST(duration_ms AS REAL))
		FROM claude_pool_requests
		WHERE session_id = ? AND duration_ms > 0
		  AND timestamp >= ? AND timestamp < ?
	`

	var s SessionRateSnapshot
	var avgOut, avgTotal sql.NullFloat64
	err = r.db.QueryRowContext(ctx, query, sessionID, bucketStart, bucketEnd).Scan(
		&s.RequestCount, &s.OutTokens, &s.TotalTokens, &s.DurationMsSum,
		&avgOut, &avgTotal,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session recent minute rate: %w", err)
	}
	if s.RequestCount == 0 {
		return nil, nil
	}

	s.SessionID = sessionID
	s.BucketStartUnix = bucketStart
	s.LastActiveUnix = lastActive.Int64
	s.BucketMinutes = 1
	loc := time.Local
	s.BucketLabel = time.Unix(s.BucketStartUnix, 0).In(loc).Format("01-02 15:04")
	if avgOut.Valid {
		s.AvgOutPerS = avgOut.Float64
	}
	if avgTotal.Valid {
		s.AvgTotalPerS = avgTotal.Float64
	}
	if s.DurationMsSum > 0 {
		s.WeightedOutPerS = float64(s.OutTokens) * 1000.0 / float64(s.DurationMsSum)
		s.WeightedTotalPerS = float64(s.TotalTokens) * 1000.0 / float64(s.DurationMsSum)
	}
	return &s, nil
}

// CountDailyStatsByModel returns the number of distinct (date, model) groups.
func (r *Repository) CountDailyStatsByModel(ctx context.Context, from, to string, granularity string) (int64, error) {
	dateExpr := aggDateExpr(granularity)
	var count int64
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT 1 FROM claude_pool_agg
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
	// R5 API P1: reasoning_tokens / total_tokens / http_status / endpoint /
	// conversation_id MUST be selected — APIRequest serializes them, and the
	// codex twin (GetCodexRecentRequests) already does; omitting them served
	// structural zeros that contradicted /api/codex/requests for the same row.
	query := `SELECT id, timestamp, session_id, user_id, prompt_id, prompt_length,
		model, actual_model, input_tokens, output_tokens,
		cache_read_tokens, cache_creation_tokens, cost_usd, duration_ms, ttft_ms, request_id,
		event_name, event_sequence, speed, terminal_type, tool_name, decision, source,
		service_name, service_version, host_arch, os_type, os_version,
		error_type, error_message, error_code, error_retryable,
		reasoning_tokens, total_tokens, http_status, endpoint, conversation_id
		FROM claude_pool_requests` + whereClause + ` ORDER BY timestamp DESC LIMIT ? OFFSET ?`
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
			&req.ErrorType, &req.ErrorMessage, &req.ErrorCode, &req.ErrorRetryable,
			&req.ReasoningTokens, &req.TotalTokens, &req.HTTPStatus, &req.Endpoint, &req.ConversationID); err != nil {
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
		`SELECT DISTINCT model FROM claude_pool_agg WHERE model != '' ORDER BY model`)
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
	err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM claude_pool_requests`+whereClause, args...).Scan(&count)
	return count, err
}

// GetDurationStatsByModel returns per-model duration stats for API requests in the given date range.
// If model is non-empty, results are restricted to that model.
func (r *Repository) GetDurationStatsByModel(ctx context.Context, model, from, to string, limit int) ([]DurationStat, error) {
	whereClause, args, err := requestWhereClause(model, from, to)
	if err != nil {
		return nil, err
	}
	// Exclude missing/zero durations (some events may not have duration_ms).
	if whereClause == "" {
		whereClause = " WHERE duration_ms > 0"
	} else {
		whereClause += " AND duration_ms > 0"
	}

	lim := limit
	if lim <= 0 || lim > 2000 {
		lim = 2000
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT
			model,
			COUNT(*) AS request_count,
			AVG(duration_ms) AS avg_duration_ms,
			AVG(ttft_ms) AS avg_ttft_ms,
			AVG(CAST(output_tokens AS REAL) * 1000.0 / CAST(duration_ms AS REAL)) AS avg_out_tokens_per_s,
			AVG(CAST((input_tokens + cache_read_tokens + cache_creation_tokens + output_tokens) AS REAL) * 1000.0 / CAST(duration_ms AS REAL)) AS avg_total_tokens_per_s,
			SUM(CAST(output_tokens AS REAL)) * 1000.0 / NULLIF(SUM(duration_ms), 0) AS weighted_out_tokens_per_s,
			SUM(CAST((input_tokens + cache_read_tokens + cache_creation_tokens + output_tokens) AS REAL)) * 1000.0 / NULLIF(SUM(duration_ms), 0) AS weighted_total_tokens_per_s,
			MAX(duration_ms) AS max_duration_ms,
			MIN(duration_ms) AS min_duration_ms
		FROM claude_pool_requests`+whereClause+`
		GROUP BY model
		ORDER BY avg_duration_ms DESC
		LIMIT ?`, append(args, lim)...)
	if err != nil {
		return nil, fmt.Errorf("duration stats query: %w", err)
	}
	defer rows.Close()

	var out []DurationStat
	for rows.Next() {
		var s DurationStat
		if err := rows.Scan(&s.Model, &s.RequestCount, &s.AvgDurationMs, &s.AvgTTFTMs, &s.AvgOutTokensPS, &s.AvgTotTokensPS, &s.WeightedOutTokensPS, &s.WeightedTotTokensPS, &s.MaxDurationMs, &s.MinDurationMs); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
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
		FROM claude_pool_requests
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
		SELECT COUNT(DISTINCT session_id) FROM claude_pool_requests
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

	// import_ledger: append-only audit trail of merged/imported rows, one entry
	// per source row. Nothing reads it — dedup keys on target-table identity
	// (dbmerge.identityExistsExpression), not the ledger — so it only ever
	// grows, and a few large merges dwarf every other table on disk.
	//
	// NOTE: imported_at is when the *import ran*, not how old the data is, and
	// api_requests is deliberately never retention-pruned. So this drops audit
	// history for rows that are still live. That is the intended trade: bounded
	// disk over an indefinite provenance record.
	ilRes, ilErr := r.db.ExecContext(ctx, `DELETE FROM import_ledger WHERE imported_at < ?`, beforeUnix)
	if ilErr != nil {
		return total, fmt.Errorf("cleanup import_ledger: %w", ilErr)
	}
	ilN, _ := ilRes.RowsAffected()
	total += ilN

	// daily_model_agg is deliberately NOT retention-pruned (anymore):
	// api_requests — its only source — is never pruned either, so pruning the
	// aggregate left All time showing a 90-day window while the detail kept
	// months more (live-caught 2026-11: agg started at the cutoff, detail at
	// 2026-04, four months silently missing from every All time view). The
	// table holds at most a few rows per model per day, so keeping it forever
	// costs nothing meaningful. NeedsAggRebuild self-heals databases whose agg
	// was already pruned by an older build.

	r.db.ExecContext(ctx, "PRAGMA incremental_vacuum")
	return total, nil
}

// CleanupRaw deletes records older than beforeUnix from raw event tables only.
// These tables store the full OTLP JSON and grow quickly; they are safe to prune
// aggressively since the structured tables already hold the parsed data.
// pending_ttft_spans rides the same sweep: processed rows are dead weight, and
// unprocessed rows older than the window are dropped — the request_id match
// path itself is unbounded in time, so without this purge a stale unprocessed
// row could in principle pair with a future request reusing its request_id
// (R3 security/observability finding: nothing else ever deleted them).
// codex_raw_otlp_events is deliberately EXEMPT (R4 concurrency F4): it is the
// frozen pre-merge archive (no production writer; only the migration reads
// it), and its "archive tables are never modified or deleted" invariant
// (dbmerge/codex_to_all.go) assumes freeze-after-write,
// and TTL-pruning it made every migration re-run re-insert the whole archive
// (misleading inserted_rows + WAL churn).
func (r *Repository) CleanupRaw(ctx context.Context, beforeUnix int64) (int64, error) {
	var total int64

	for _, tbl := range []string{"raw_otlp_events"} {
		res, err := r.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE timestamp < ?`, tbl), beforeUnix)
		if err != nil {
			return total, fmt.Errorf("cleanup raw %s: %w", tbl, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM pending_ttft_spans WHERE processed = 1 OR created_unix < ?`, beforeUnix)
	if err != nil {
		return total, fmt.Errorf("cleanup pending_ttft_spans: %w", err)
	}
	n, _ := res.RowsAffected()
	total += n

	r.db.ExecContext(ctx, "PRAGMA incremental_vacuum")
	return total, nil
}

// execWithBusyRetry runs a single-statement write with the same BUSY
// retry policy as InsertRequest: a migration batch holding the writer
// under BEGIN IMMEDIATE can exhaust busy_timeout, and the sub-event tables
// have no idempotent key — but a statement that failed with BUSY never
// committed, so replaying it cannot double-insert (R1 review A3-3).
func (r *Repository) execWithBusyRetry(ctx context.Context, query string, args ...any) (sql.Result, error) {
	for attempt := 0; ; attempt++ {
		res, err := r.db.ExecContext(ctx, query, args...)
		if err == nil || attempt >= 3 || !isBusyErr(err) {
			return res, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(25<<attempt) * time.Millisecond):
		}
	}
}
