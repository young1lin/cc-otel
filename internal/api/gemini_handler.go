package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/young1lin/cc-otel/internal/db"
)

// GeminiDashboard returns aggregated KPI cards for the Gemini tab.
func (h *Handler) GeminiDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	from, to, ok := codexResolveRange(r)
	if !ok {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	d, err := h.repo.GetGeminiDashboard(r.Context(), from, to)
	if err != nil {
		log.Printf("gemini dashboard error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(d)
}

// GeminiCalendar returns compact per-day aggregates for the Gemini usage calendar.
func (h *Handler) GeminiCalendar(w http.ResponseWriter, r *http.Request) {
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
	rows, err := h.repo.GetGeminiCalendarDays(r.Context(), from, to)
	if err != nil {
		log.Printf("gemini calendar error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.CalendarDay{}
	}
	json.NewEncoder(w).Encode(CalendarResponse{Data: rows})
}

// GeminiDaily returns paged per-(date, model) rollup rows for the Gemini tab.
func (h *Handler) GeminiDaily(w http.ResponseWriter, r *http.Request) {
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
	rows, total, err := h.repo.GetGeminiDailyStatsByModel(r.Context(), from, to, pageSize, (page-1)*pageSize, gran)
	if err != nil {
		log.Printf("gemini daily error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.DailyModelSummary{}
	}
	json.NewEncoder(w).Encode(PagedResponse{Data: rows, Total: total, Page: page, PageSize: pageSize})
}

// GeminiIntraday returns per-(time-bucket, model) Gemini stats.
func (h *Handler) GeminiIntraday(w http.ResponseWriter, r *http.Request) {
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
		if err == nil && (n == 15 || n == 30 || n == 60) {
			bucket = n
		}
	}

	data, err := h.repo.GetGeminiIntradayStatsByModel(r.Context(), from, to, bucket, q.Get("model"))
	if err != nil {
		log.Printf("gemini intraday data error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = []db.IntradayModelSummary{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(IntradayResponse{From: from, To: to, BucketMinutes: bucket, Data: data})
}

// GeminiRequests returns paged Gemini API request rows.
func (h *Handler) GeminiRequests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	from, to, ok := codexResolveRange(r)
	if !ok {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	page, pageSize := parsePage(r)
	model := r.URL.Query().Get("model")
	rows, total, err := h.repo.GetGeminiRecentRequests(r.Context(), pageSize, (page-1)*pageSize, model, from, to)
	if err != nil {
		log.Printf("gemini requests error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.GeminiAPIRequest{}
	}
	json.NewEncoder(w).Encode(PagedResponse{Data: rows, Total: total, Page: page, PageSize: pageSize})
}

// GeminiSessions returns paged session aggregates.
func (h *Handler) GeminiSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	from, to, ok := codexResolveRange(r)
	if !ok {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	page, pageSize := parsePage(r)
	rows, total, err := h.repo.GetGeminiSessionStats(r.Context(), from, to, pageSize, (page-1)*pageSize)
	if err != nil {
		log.Printf("gemini sessions error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.SessionStat{}
	}
	json.NewEncoder(w).Encode(PagedResponse{Data: rows, Total: total, Page: page, PageSize: pageSize})
}

// GeminiDurations returns per-model latency stats for Gemini CLI telemetry.
func (h *Handler) GeminiDurations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	from, to, ok := codexResolveRange(r)
	if !ok {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	rows, err := h.repo.GetGeminiDurationStatsByModel(r.Context(), r.URL.Query().Get("model"), from, to)
	if err != nil {
		log.Printf("gemini durations error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.DurationStat{}
	}
	json.NewEncoder(w).Encode(rows)
}

// GeminiModels returns the distinct list of Gemini models seen so far.
func (h *Handler) GeminiModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, err := h.repo.DB().QueryContext(r.Context(),
		`SELECT DISTINCT model FROM gemini_api_requests WHERE model != '' ORDER BY model`)
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
