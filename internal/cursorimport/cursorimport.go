// Package cursorimport parses Cursor's usage-events CSV exports and imports
// them into cc-otel's unified api_requests pool.
//
// Cursor's export has no request id, so every row gets a deterministic
// content-hash key: "cursor-" + sha256(date|model|kind|token counts)[:16].
// The partial unique index on api_requests(request_id) then makes re-imports
// — overlapping month exports, duplicate downloads, repeated clicks —
// collapse into no-ops: identical rows always map to the same key, and
// INSERT OR IGNORE skips what is already stored.
//
// Column mapping (verified against 20683 real export rows, 2026-11-07):
// every row satisfies Total = Input(w/o CacheWrite) + Input(w/ CacheWrite) +
// Cache Read + Output, i.e. the four columns are exactly cc-otel's uncached
// input / cache creation / cache read / output vocabulary. Input (w/o Cache
// Write) does NOT include cache read — otherwise the identity would
// double-count it.
//
// Model names are stored as their BASE identity: Cursor's picker suffixes
// (fast = speed tier, max/thinking/xhigh/high/medium/low = thinking level)
// are stripped by NormalizeCursorModel before InsertRequest, so the model
// dimension stays clean. The RAW slug keeps feeding the dedup hash — see
// NormalizeCursorModel for the whitelist rules.
package cursorimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/young1lin/cc-otel/internal/db"
)

// ServiceName is the attribution label every imported row carries. It must
// not contain "codex" (case-insensitive) or exactly "deepseek-harness" —
// those route rows into other parsers downstream. "cursor" is safe.
const ServiceName = "cursor"

// requestIDPrefix namespaces content-hash keys on api_requests(request_id),
// disjoint from dsh- / dshp- / dsh-v3- and bare provider request ids.
const requestIDPrefix = "cursor-"

// Export column names (Cursor changes these rarely; parse fails loudly with
// the missing names if it does).
const (
	colDate    = "Date"
	colAgentID = "Cloud Agent ID"
	// Automation ID appeared in the 2026-11-08 export format alongside
	// "Max Mode" (older exports lack both). Optional, like Cloud Agent ID.
	colAutomationID = "Automation ID"
	colKind         = "Kind"
	colModel        = "Model"
	colInW          = "Input (w/ Cache Write)"
	colInWO         = "Input (w/o Cache Write)"
	colCacheRead    = "Cache Read"
	colOutput       = "Output Tokens"
	colTotal        = "Total Tokens"
	colCost         = "Cost"
)

// Row is one parsed usage-events record, already mapped onto cc-otel's token
// vocabulary.
type Row struct {
	Timestamp           time.Time
	RawDate             string // millisecond-precision source string; feeds the dedup hash
	Model               string // RAW Cursor slug; RequestID hashes this, Import stores the normalized base
	Kind                string
	SessionID           string // "cursor-agent-<id>" for cloud-agent rows, else ""
	InputTokens         int64  // Input (w/o Cache Write) — uncached input
	CacheCreationTokens int64  // Input (w/ Cache Write) — cache-write tokens
	CacheReadTokens     int64  // Cache Read
	OutputTokens        int64
	TotalTokens         int64 // stored as the derived sum so component queries reconcile
	CostUSD             float64
	RequestID           string
	File                string
}

// ParseStats counts everything Parse saw but did not keep, so the import UI
// can surface data-quality signals without failing the whole file.
type ParseStats struct {
	Rows               int // data lines read
	SkippedZeroToken   int // all-zero token rows dropped (unless KeepZeroToken)
	IdentityViolations int // Total != component sum, or unparseable Cost
	InvalidRows        int // dropped: bad timestamp / token value / empty model
}

// Parse reads one usage-events CSV export. Single malformed rows are counted
// and skipped — a 20k-row history must not die on one odd line — but a
// missing required column is fatal for the whole file.
func Parse(fileName string, r io.Reader, keepZeroToken bool) ([]Row, ParseStats, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true

	header, err := cr.Read()
	if err != nil {
		return nil, ParseStats{}, fmt.Errorf("read header: %w", err)
	}
	idx := make(map[string]int, len(header))
	for i, name := range header {
		idx[strings.TrimSpace(name)] = i
	}
	var missing []string
	for _, name := range []string{colDate, colModel, colInW, colInWO, colCacheRead, colOutput} {
		if _, ok := idx[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, ParseStats{}, fmt.Errorf("missing required column(s): %s", strings.Join(missing, ", "))
	}

	col := func(rec []string, name string) string {
		if i, ok := idx[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}

	var rows []Row
	var stats ParseStats
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return rows, stats, fmt.Errorf("line %d: %w", line, err)
		}
		stats.Rows++

		ts, err := time.Parse(time.RFC3339, col(rec, colDate))
		if err != nil {
			stats.InvalidRows++
			continue
		}
		inW, ok1 := parseTokenCount(col(rec, colInW))
		inWO, ok2 := parseTokenCount(col(rec, colInWO))
		cacheRead, ok3 := parseTokenCount(col(rec, colCacheRead))
		out, ok4 := parseTokenCount(col(rec, colOutput))
		if !ok1 || !ok2 || !ok3 || !ok4 {
			stats.InvalidRows++
			continue
		}
		model := col(rec, colModel)
		if model == "" {
			stats.InvalidRows++
			continue
		}

		derived := inWO + inW + cacheRead + out
		if csvTotal, ok := parseTokenCount(col(rec, colTotal)); ok && csvTotal != derived {
			// Counted, not fatal. The four components are what every chart
			// and KPI sums, so the derived total is what gets stored.
			stats.IdentityViolations++
		}

		if derived == 0 && !keepZeroToken {
			stats.SkippedZeroToken++
			continue
		}

		cost, costOK := parseCost(col(rec, colCost))
		if !costOK {
			// Token data survives; the cost anomaly is surfaced instead of
			// silently storing a guess.
			stats.IdentityViolations++
		}

		rawDate := col(rec, colDate)
		kind := col(rec, colKind)
		row := Row{
			Timestamp:           ts,
			RawDate:             rawDate,
			Model:               model,
			Kind:                kind,
			InputTokens:         inWO,
			CacheCreationTokens: inW,
			CacheReadTokens:     cacheRead,
			OutputTokens:        out,
			TotalTokens:         derived,
			CostUSD:             cost,
			RequestID:           RequestID(rawDate, model, kind, inWO, inW, cacheRead, out),
			File:                fileName,
		}
		// Session attribution: a Cloud Agent run is the stronger identity and
		// wins when both IDs are present; otherwise an Automation ID groups
		// grok-bot / Bugbot rows per automation in the Sessions panel. Neither
		// ID feeds the dedup hash, so a format that later adds or changes these
		// columns never breaks idempotency.
		if agent := col(rec, colAgentID); agent != "" {
			row.SessionID = "cursor-agent-" + agent
		} else if automation := col(rec, colAutomationID); automation != "" {
			row.SessionID = "cursor-automation-" + automation
		}
		rows = append(rows, row)
	}
	return rows, stats, nil
}

// modeSegments are Cursor's picker-mode suffix SEGMENTS (speed tiers and
// thinking/effort levels) — attributes of a request, not the model's
// identity. Whole-segment match only, so real names containing these words
// as parts (grok-code-fast-1 ends in "1"; flash is not a Cursor mode and
// never appears here) are untouched. Verified against all 49 distinct
// slugs in the 2026-11-07 exports.
var modeSegments = map[string]bool{
	"fast": true, "thinking": true, "xhigh": true,
	"high": true, "medium": true, "low": true, "max": true,
}

// NormalizeCursorModel reduces a raw Cursor slug to its base model:
//
//	cursor-grok-4.6-high-fast → grok-4.6
//	gpt-5.1-codex-max         → gpt-5.1-codex
//	claude-4.5-sonnet-thinking → claude-4.5-sonnet
//	gpt-5.5-extra-high        → gpt-5.5
//	grok-code-fast-1           → grok-code-fast-1 (real xAI name, tail is "1")
//	gemini-3-flash-preview     → unchanged ("flash" is a model name, not a mode)
//	auto / composer-* / grok-bot-* / github_bugbot / agent_review → unchanged
//
// The "cursor-" prefix is Cursor's namespace marker on hosted grok variants
// and is stripped so those rows join the plain grok-N family. Combination
// order varies in the wild (-xhigh-fast and -fast-xhigh both occur), so
// segments are stripped in a loop. The RAW slug still feeds RequestID,
// keeping dedup stable no matter how the mapping evolves.
func NormalizeCursorModel(raw string) string {
	m := strings.TrimSpace(raw)
	m = strings.TrimPrefix(m, "cursor-")
	// Two-segment mode first: stripping "high" alone would strand "extra".
	if strings.HasSuffix(m, "-extra-high") {
		m = strings.TrimSuffix(m, "-extra-high")
	}
	for {
		i := strings.LastIndex(m, "-")
		if i <= 0 || !modeSegments[m[i+1:]] {
			break
		}
		m = m[:i]
	}
	if m == "" {
		return strings.TrimSpace(raw)
	}
	return m
}

// RequestID derives the deterministic dedup key for one usage row. The raw
// millisecond-precision date string participates, so two distinct requests
// only collide when they share model, kind, every token count, AND the exact
// millisecond — measured zero collisions across 20683 real rows.
// Exported for tests; callers normally go through Parse.
func RequestID(date, model, kind string, inWO, inW, cacheRead, out int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d|%d|%d|%d", date, model, kind, inWO, inW, cacheRead, out)))
	return requestIDPrefix + hex.EncodeToString(sum[:])[:16]
}

// parseCost maps the export's Cost column. "Included", "Free", "-" and ""
// mean nothing was billed → 0. Anything else must parse as a dollar amount.
func parseCost(s string) (float64, bool) {
	switch s {
	case "", "-", "Included", "Free":
		return 0, true
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

func parseTokenCount(s string) (int64, bool) {
	if s == "" {
		return 0, true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, true
	}
	// Defensive: a "1234.0" style value still carries the count.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f), true
	}
	return 0, false
}

// Result is the accounting of one import run, mirrored to the import UI.
type Result struct {
	Files              []string `json:"files,omitempty"`
	Scanned            int64    `json:"scanned_rows"`        // candidate rows handed to the writer
	Inserted           int64    `json:"inserted_rows"`       // new rows stored
	Duplicates         int64    `json:"duplicate_rows"`      // already present (request_id hit)
	SkippedZeroToken   int64    `json:"skipped_zero_rows"`   // dropped at parse (default policy)
	IdentityViolations int64    `json:"identity_violations"` // Total-mismatch / unparseable cost
	InvalidRows        int64    `json:"invalid_rows"`        // dropped at parse
}

// Pricer estimates a row's list-price cost; implemented by pricing.Registry
// (and test stubs). Kept minimal so the package stays decoupled from the
// pricing internals.
type Pricer interface {
	Calc(ctx context.Context, model string, input, output, cacheRead, cacheCreate int64) float64
}

// Progress is called periodically with running counters (never decreasing
// within one run).
type Progress func(processed, inserted, duplicates int64)

// Import writes parsed rows through the repository's own hot path
// (InsertRequest): request_id dedup, the daily_model_agg upsert in the same
// transaction, and the built-in BUSY retry — identical semantics to live
// OTLP ingestion, so a running daemon stays consistent throughout.
//
// cost_usd (user directive 2026-11-08, superseding DP-11): a POSITIVE CSV
// cost is real money Cursor charged a User API Key — store it verbatim. A
// zero/absent cost means the plan pool absorbed the row; the dashboard still
// wants to know what the usage was worth, so it is ESTIMATED at list price
// via the Pricer (nil keeps the CSV value — the CSV-only test path). Unknown
// models price to $0 and stay $0, matching recompute's miss handling.
//
// Idempotency: re-importing the same rows inserts nothing and leaves
// daily_model_agg unchanged (duplicates skip the agg upsert too; the final
// rebuild levels against the pool regardless).
func Import(ctx context.Context, repo *db.Repository, pricer Pricer, rows []Row, progress Progress) (Result, error) {
	res := Result{Scanned: int64(len(rows))}
	seen := map[string]bool{}
	for _, row := range rows {
		if !seen[row.File] {
			seen[row.File] = true
			res.Files = append(res.Files, row.File)
		}
	}

	var processed int64
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("cancelled after %d rows: %w", processed, err)
		}
		cost := row.CostUSD
		base := NormalizeCursorModel(row.Model)
		if cost <= 0 && pricer != nil {
			cost = pricer.Calc(ctx, base, row.InputTokens, row.OutputTokens,
				row.CacheReadTokens, row.CacheCreationTokens)
		}
		inserted, err := repo.InsertRequest(ctx, &db.APIRequest{
			Timestamp:           row.Timestamp,
			SessionID:           row.SessionID,
			Model:               base,
			InputTokens:         row.InputTokens,
			OutputTokens:        row.OutputTokens,
			CacheReadTokens:     row.CacheReadTokens,
			CacheCreationTokens: row.CacheCreationTokens,
			CostUSD:             cost,
			RequestID:           row.RequestID,
			EventName:           "api_request",
			ServiceName:         ServiceName,
			TotalTokens:         row.TotalTokens,
		})
		if err != nil {
			return res, fmt.Errorf("insert %s (file %s): %w", row.RequestID, row.File, err)
		}
		processed++
		if inserted {
			res.Inserted++
		} else {
			res.Duplicates++
		}
		if progress != nil && processed%250 == 0 {
			progress(processed, res.Inserted, res.Duplicates)
		}
	}

	// Audit trail, same shape the dbmerge pipeline writes: one ledger row per
	// stored row, keyed by the request_id uuid so re-runs are no-ops too.
	if err := writeLedger(ctx, repo.DB(), rows, time.Now().Unix()); err != nil {
		return res, fmt.Errorf("import ledger: %w", err)
	}

	// Level the aggregate calendar against the pool (InsertRequest already
	// upserted per row; this rebuild is the belt-and-braces dsh_ingest uses).
	if err := repo.RebuildDailyAggregates(ctx); err != nil {
		return res, fmt.Errorf("rebuild daily aggregates: %w", err)
	}
	if progress != nil {
		progress(res.Scanned, res.Inserted, res.Duplicates)
	}
	return res, nil
}

func writeLedger(ctx context.Context, d *sql.DB, rows []Row, now int64) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		"INSERT OR IGNORE INTO import_ledger (uuid, imported_at, source_db, table_name) VALUES (?, ?, ?, 'api_requests')")
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx, row.RequestID, now, "cursor-csv:"+row.File); err != nil {
			return fmt.Errorf("ledger %s: %w", row.RequestID, err)
		}
	}
	return tx.Commit()
}
