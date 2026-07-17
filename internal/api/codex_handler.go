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
// preferring explicit ?from=&to= over ?range=. Returns false if the resolved
// dates are invalid; in that case the caller writes 400 and returns.
func codexResolveRange(r *http.Request) (string, string, bool) {
	from, to := rangeToFromTo(r.URL.Query().Get("range"))
	if v := r.URL.Query().Get("from"); v != "" && isValidDate(v) {
		from = v
	}
	if v := r.URL.Query().Get("to"); v != "" && isValidDate(v) {
		to = v
	}
	if !isValidDate(from) || !isValidDate(to) {
		return "", "", false
	}
	return from, to, true
}

// CodexDashboard returns aggregated KPI cards for the Codex tab.
func (h *Handler) CodexDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	from, to, ok := codexResolveRange(r)
	if !ok {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	d, err := h.repo.GetCodexDashboard(r.Context(), from, to)
	if err != nil {
		log.Printf("codex dashboard error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(d)
}

// CodexCalendar returns compact per-day aggregates for the Codex usage calendar.
func (h *Handler) CodexCalendar(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.repo == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(CalendarResponse{Data: []db.CalendarDay{}})
		return
	}
	from, to, ok := codexResolveRange(r)
	if !ok {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	rows, err := h.repo.GetCodexCalendarDays(r.Context(), from, to)
	if err != nil {
		log.Printf("codex calendar error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.CalendarDay{}
	}
	json.NewEncoder(w).Encode(CalendarResponse{Data: rows})
}

// CodexDaily returns paged per-(date, model) rollup rows for the Codex tab.
func (h *Handler) CodexDaily(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	from, to, ok := codexResolveRange(r)
	if !ok {
		http.Error(w, "invalid date", http.StatusBadRequest)
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
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.DailyModelSummary{}
	}
	json.NewEncoder(w).Encode(PagedResponse{Data: rows, Total: total, Page: page, PageSize: pageSize})
}

// CodexIntraday returns per-(time-bucket, model) Codex stats.
func (h *Handler) CodexIntraday(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	loc := time.Local
	fromT, errF := time.ParseInLocation("2006-01-02", from, loc)
	toT, errT := time.ParseInLocation("2006-01-02", to, loc)
	if errF != nil || errT != nil || toT.Before(fromT) {
		http.Error(w, "invalid date range", http.StatusBadRequest)
		return
	}
	if toT.Sub(fromT) > 6*24*time.Hour {
		http.Error(w, "intraday range must not exceed 7 days", http.StatusBadRequest)
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
		http.Error(w, "internal error", http.StatusInternalServerError)
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
	w.Header().Set("Content-Type", "application/json")
	from, to, ok := codexResolveRange(r)
	if !ok {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	page, pageSize := parsePage(r)
	model := r.URL.Query().Get("model")
	rows, total, err := h.repo.GetCodexRecentRequests(r.Context(), pageSize, (page-1)*pageSize, model, from, to)
	if err != nil {
		log.Printf("codex requests error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.CodexAPIRequest{}
	}
	json.NewEncoder(w).Encode(PagedResponse{Data: rows, Total: total, Page: page, PageSize: pageSize})
}

// CodexSessions returns paged session aggregates.
func (h *Handler) CodexSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	from, to, ok := codexResolveRange(r)
	if !ok {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	page, pageSize := parsePage(r)
	rows, total, err := h.repo.GetCodexSessionStats(r.Context(), from, to, pageSize, (page-1)*pageSize)
	if err != nil {
		log.Printf("codex sessions error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.SessionStat{}
	}
	json.NewEncoder(w).Encode(PagedResponse{Data: rows, Total: total, Page: page, PageSize: pageSize})
}

// CodexDurations returns per-model latency stats. Token-throughput columns are
// always zero since Codex does not emit per-request token rates.
func (h *Handler) CodexDurations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	from, to, ok := codexResolveRange(r)
	if !ok {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	rows, err := h.repo.GetCodexDurationStatsByModel(r.Context(), r.URL.Query().Get("model"), from, to)
	if err != nil {
		log.Printf("codex durations error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.DurationStat{}
	}
	json.NewEncoder(w).Encode(rows)
}

// CodexModels returns the distinct list of Codex models seen so far.
func (h *Handler) CodexModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, err := h.repo.DB().QueryContext(r.Context(),
		`SELECT DISTINCT model FROM codex_api_requests WHERE model != '' ORDER BY model`)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		out = append(out, m)
	}
	json.NewEncoder(w).Encode(out)
}
