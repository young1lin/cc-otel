package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/young1lin/cc-otel/internal/db"
)

// codexResolveRange resolves the (from, to) date pair from query params,
// preferring explicit ?from=&to= over ?range=. An explicitly supplied but
// invalid from/to is REJECTED (ok=false → 400): silently substituting the
// range default would return data the caller never asked for. Only when no
// explicit value is given does ?range= (default "30d") apply.
func codexResolveRange(r *http.Request) (string, string, bool) {
	q := r.URL.Query()
	from, to := rangeToFromTo(q.Get("range"))
	if v := q.Get("from"); v != "" {
		if !isValidDate(v) {
			return "", "", false
		}
		from = v
	}
	if v := q.Get("to"); v != "" {
		if !isValidDate(v) {
			return "", "", false
		}
		to = v
	}
	if !isValidDate(from) || !isValidDate(to) {
		return "", "", false
	}
	return from, to, true
}

// codexRejectRequest centralizes the two preconditions every codex shim
// endpoint shares: a repository must exist, and the date range must resolve.
func (h *Handler) codexRejectRequest(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if h.repo == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "repository_unavailable", "repository not initialized", nil)
		return "", "", false
	}
	from, to, ok := codexResolveRange(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "invalid_date", "from/to must be YYYY-MM-DD dates (or use range=)", nil)
		return "", "", false
	}
	return from, to, true
}

// CodexDashboard returns aggregated KPI cards for the Codex tab.
func (h *Handler) CodexDashboard(w http.ResponseWriter, r *http.Request) {
	from, to, ok := h.codexRejectRequest(w, r)
	if !ok {
		return
	}
	d, err := h.repo.GetCodexDashboard(r.Context(), from, to)
	if err != nil {
		log.Printf("codex dashboard error: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load codex dashboard", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d)
}

// CodexCalendar returns compact per-day aggregates for the Codex usage calendar.
func (h *Handler) CodexCalendar(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CalendarResponse{Data: []db.CalendarDay{}})
		return
	}
	from, to, ok := codexResolveRange(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "invalid_date", "from/to must be YYYY-MM-DD dates (or use range=)", nil)
		return
	}
	rows, err := h.repo.GetCodexCalendarDays(r.Context(), from, to)
	if err != nil {
		log.Printf("codex calendar error: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load codex calendar", nil)
		return
	}
	if rows == nil {
		rows = []db.CalendarDay{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CalendarResponse{Data: rows})
}

// CodexDaily returns paged per-(date, model) rollup rows for the Codex tab.
func (h *Handler) CodexDaily(w http.ResponseWriter, r *http.Request) {
	from, to, ok := h.codexRejectRequest(w, r)
	if !ok {
		return
	}
	page, pageSize := parsePage(r)
	gran := r.URL.Query().Get("granularity")
	if gran != "month" {
		gran = "day"
	}
	rows, total, err := h.repo.GetCodexDailyStatsByModel(r.Context(), from, to, pageSize, (page-1)*pageSize, gran)
	if err != nil {
		log.Printf("codex daily error: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load codex daily stats", nil)
		return
	}
	if rows == nil {
		rows = []db.DailyModelSummary{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PagedResponse{Data: rows, Total: total, Page: page, PageSize: pageSize})
}

// CodexIntraday returns per-(time-bucket, model) Codex stats.
func (h *Handler) CodexIntraday(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "repository_unavailable", "repository not initialized", nil)
		return
	}
	q := r.URL.Query()
	from := q.Get("from")
	to := q.Get("to")
	if from == "" || to == "" {
		rng := q.Get("range")
		if rng == "" {
			rng = "today"
		}
		from, to = rangeToFromTo(rng)
	}
	if !isValidDate(from) || !isValidDate(to) {
		writeAPIError(w, http.StatusBadRequest, "invalid_date", "from/to must be YYYY-MM-DD dates", nil)
		return
	}
	loc := time.Local
	fromT, errF := time.ParseInLocation("2006-01-02", from, loc)
	toT, errT := time.ParseInLocation("2006-01-02", to, loc)
	if errF != nil || errT != nil || toT.Before(fromT) {
		writeAPIError(w, http.StatusBadRequest, "invalid_date_range", "to must not be before from", nil)
		return
	}
	if toT.Sub(fromT) > 6*24*time.Hour {
		writeAPIError(w, http.StatusBadRequest, "range_too_wide", "intraday range must not exceed 7 days", nil)
		return
	}

	bucket := 30
	if v := q.Get("bucket"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && db.ValidRateBucketMinutes(n) {
			bucket = n
		}
	}

	data, err := h.repo.GetCodexIntradayStatsByModel(r.Context(), from, to, bucket, q.Get("model"))
	if err != nil {
		log.Printf("codex intraday data error: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load codex intraday stats", nil)
		return
	}
	if data == nil {
		data = []db.IntradayModelSummary{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(IntradayResponse{From: from, To: to, BucketMinutes: bucket, Data: data})
}

// CodexRequests returns paged Codex API request rows.
func (h *Handler) CodexRequests(w http.ResponseWriter, r *http.Request) {
	from, to, ok := h.codexRejectRequest(w, r)
	if !ok {
		return
	}
	page, pageSize := parsePage(r)
	model := r.URL.Query().Get("model")
	rows, total, err := h.repo.GetCodexRecentRequests(r.Context(), pageSize, (page-1)*pageSize, model, from, to)
	if err != nil {
		log.Printf("codex requests error: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load codex requests", nil)
		return
	}
	if rows == nil {
		rows = []db.CodexAPIRequest{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PagedResponse{Data: rows, Total: total, Page: page, PageSize: pageSize})
}

// CodexSessions returns paged session aggregates.
func (h *Handler) CodexSessions(w http.ResponseWriter, r *http.Request) {
	from, to, ok := h.codexRejectRequest(w, r)
	if !ok {
		return
	}
	page, pageSize := parsePage(r)
	rows, total, err := h.repo.GetCodexSessionStats(r.Context(), from, to, pageSize, (page-1)*pageSize)
	if err != nil {
		log.Printf("codex sessions error: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load codex sessions", nil)
		return
	}
	if rows == nil {
		rows = []db.SessionStat{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PagedResponse{Data: rows, Total: total, Page: page, PageSize: pageSize})
}

// CodexDurations returns per-model latency stats. Token-throughput columns are
// always zero since Codex does not emit per-request token rates.
func (h *Handler) CodexDurations(w http.ResponseWriter, r *http.Request) {
	from, to, ok := h.codexRejectRequest(w, r)
	if !ok {
		return
	}
	rows, err := h.repo.GetCodexDurationStatsByModel(r.Context(), r.URL.Query().Get("model"), from, to)
	if err != nil {
		log.Printf("codex durations error: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load codex durations", nil)
		return
	}
	if rows == nil {
		rows = []db.DurationStat{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rows)
}

// CodexModels returns the distinct list of Codex models seen so far. It reads
// daily_model_agg (service_name='codex') instead of the legacy archive table:
// the unified pool is the source of truth after the merge, and the agg index
// makes this an index-only scan.
func (h *Handler) CodexModels(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil || h.repo.DB() == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "repository_unavailable", "repository not initialized", nil)
		return
	}
	rows, err := h.repo.DB().QueryContext(r.Context(),
		`SELECT DISTINCT model FROM daily_model_agg WHERE service_name = ? AND model != '' ORDER BY model`,
		db.CodexServiceName)
	if err != nil {
		log.Printf("codex models error: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load codex models", nil)
		return
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			log.Printf("codex models scan error: %v", err)
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load codex models", nil)
			return
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		log.Printf("codex models rows error: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load codex models", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
