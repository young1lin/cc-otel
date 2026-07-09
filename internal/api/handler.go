package api

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/web"
)

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

// GzipMiddleware compresses responses when the client accepts gzip.
// SSE (/api/events) is excluded because EventSource cannot handle compressed streams.
func GzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.URL.Path == "/api/events" {
			next.ServeHTTP(w, r)
			return
		}
		gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		defer gz.Close()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
		next.ServeHTTP(&gzipResponseWriter{Writer: gz, ResponseWriter: w}, r)
	})
}

type PagedResponse struct {
	Data     interface{} `json:"data"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func isValidDate(s string) bool {
	return dateRe.MatchString(s)
}

func parsePage(r *http.Request) (page, pageSize int) {
	page, pageSize = 1, 20
	if v := r.URL.Query().Get("page"); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			page = n
		}
	}
	if v := r.URL.Query().Get("page_size"); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 && n <= 2000 {
			pageSize = n
		}
	}
	return
}

// Handler serves the REST API and SSE endpoints for the cc-otel web dashboard.
type Handler struct {
	repo       *db.Repository
	broker     *Broker
	cfg        *config.Config
	configPath string
}

// NewHandler creates a Handler with the given database repository, SSE broker, config,
// and the absolute path of the config file the daemon actually loaded.
func NewHandler(repo *db.Repository, broker *Broker, cfg *config.Config, configPath string) *Handler {
	return &Handler{repo: repo, broker: broker, cfg: cfg, configPath: configPath}
}

// Register wires all API and static-file routes onto the given ServeMux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/health", h.Health)
	mux.HandleFunc("/api/status", h.Status)
	mux.HandleFunc("/api/dashboard", h.Dashboard)
	mux.HandleFunc("/api/daily", h.DailyModel)
	mux.HandleFunc("/api/requests", h.Requests)
	mux.HandleFunc("/api/sessions", h.Sessions)
	mux.HandleFunc("/api/models", h.Models)
	mux.HandleFunc("/api/events", h.Events)

	// Embedded static files reflect the tree at compile time. For local UI edits without
	// rebuilding, set CC_OTEL_STATIC_DIR to the static folder (e.g. internal/web/static).
	if dir := strings.TrimSpace(os.Getenv("CC_OTEL_STATIC_DIR")); dir != "" {
		log.Printf("web UI: serving static from disk %q (CC_OTEL_STATIC_DIR)", dir)
		mux.Handle("/", http.FileServer(http.Dir(dir)))
		return
	}

	staticFS, err := fs.Sub(web.FS(), "static")
	if err != nil {
		log.Printf("failed to create static FS: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
}

// Health reports database connectivity (200 ok, 503 error).
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.repo == nil || h.repo.Ping(r.Context()) != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"status": "error"})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type StatusResponse struct {
	ServerTimeUnix int64 `json:"server_time_unix"`

	DBOK bool `json:"db_ok"`

	SSEClients int   `json:"sse_clients"`
	LastUpdate int64 `json:"last_update_unix"`
	NotifyCount int64 `json:"notify_count"`

	WebPort  int `json:"web_port"`
	OTELPort int `json:"otel_port"`

	OTELReceiverListening bool `json:"otel_receiver_listening"`

	// ConfigPath is the absolute path of the config file this daemon loaded at startup.
	// DBPath is the resolved SQLite path after yaml + CC_OTEL_DB_PATH env override.
	ConfigPath string `json:"config_path"`
	DBPath     string `json:"db_path"`
}

// Status returns server health details: DB, SSE, OTEL receiver, ports, and last update time.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	webPort := config.DefaultWebPort()
	otelPort := config.DefaultOTELPort()
	if h.cfg != nil {
		if h.cfg.WebPort != 0 {
			webPort = h.cfg.WebPort
		}
		if h.cfg.OTELPort != 0 {
			otelPort = h.cfg.OTELPort
		}
	}

	dbOK := false
	if h.repo != nil && h.repo.Ping(r.Context()) == nil {
		dbOK = true
	}

	sseClients := 0
	lastUpdate := int64(0)
	notifyCount := int64(0)
	if h.broker != nil {
		sseClients = h.broker.ClientCount()
		lastUpdate = h.broker.LastNotifyUnix()
		notifyCount = h.broker.NotifyCount()
	}

	otelListening := false
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", otelPort), 700*time.Millisecond)
	if err == nil {
		otelListening = true
		conn.Close()
	}

	dbPath := ""
	if h.cfg != nil {
		dbPath = h.cfg.DBPath
	}

	json.NewEncoder(w).Encode(StatusResponse{
		ServerTimeUnix:        time.Now().Unix(),
		DBOK:                  dbOK,
		SSEClients:            sseClients,
		LastUpdate:            lastUpdate,
		NotifyCount:           notifyCount,
		WebPort:               webPort,
		OTELPort:              otelPort,
		OTELReceiverListening: otelListening,
		ConfigPath:            h.configPath,
		DBPath:                dbPath,
	})
}

// rangeToFromTo converts a range name to (from, to) date strings in local time.
func rangeToFromTo(rangeParam string) (from, to string) {
	now := time.Now().Local()
	today := now.Format("2006-01-02")
	switch rangeParam {
	case "week":
		return now.AddDate(0, 0, -6).Format("2006-01-02"), today
	case "month":
		return now.AddDate(0, 0, -29).Format("2006-01-02"), today
	case "all":
		return "1970-01-01", today
	default: // "today" or empty
		return today, today
	}
}

// Dashboard returns aggregated token usage and cost KPIs for a date range.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	from, to := rangeToFromTo(r.URL.Query().Get("range"))
	// Allow explicit from/to to override range param
	if v := r.URL.Query().Get("from"); v != "" && isValidDate(v) {
		from = v
	}
	if v := r.URL.Query().Get("to"); v != "" && isValidDate(v) {
		to = v
	}
	data, err := h.repo.GetDashboardForRange(r.Context(), from, to)
	if err != nil {
		log.Printf("dashboard error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// DailyModel returns per-day, per-model token usage and cost with pagination.
func (h *Handler) DailyModel(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" || !isValidDate(from) || !isValidDate(to) {
		f, t := rangeToFromTo(r.URL.Query().Get("range"))
		if from == "" || !isValidDate(from) {
			from = f
		}
		if to == "" || !isValidDate(to) {
			to = t
		}
	}
	page, pageSize := parsePage(r)
	offset := (page - 1) * pageSize
	granularity := r.URL.Query().Get("granularity")
	if granularity != "month" {
		granularity = "day"
	}

	total, err := h.repo.CountDailyStatsByModel(r.Context(), from, to, granularity)
	if err != nil {
		log.Printf("daily count error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data, err := h.repo.GetDailyStatsByModel(r.Context(), from, to, pageSize, offset, granularity)
	if err != nil {
		log.Printf("daily data error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = []db.DailyModelSummary{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PagedResponse{Data: data, Total: total, Page: page, PageSize: pageSize})
}

// Requests returns individual API request records with optional model and date filters.
func (h *Handler) Requests(w http.ResponseWriter, r *http.Request) {
	page, pageSize := parsePage(r)
	offset := (page - 1) * pageSize
	model := r.URL.Query().Get("model")
	from, to := rangeToFromTo(r.URL.Query().Get("range"))
	if v := r.URL.Query().Get("from"); v != "" && isValidDate(v) {
		from = v
	}
	if v := r.URL.Query().Get("to"); v != "" && isValidDate(v) {
		to = v
	}

	total, err := h.repo.CountRecentRequests(r.Context(), model, from, to)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data, err := h.repo.GetRecentRequests(r.Context(), pageSize, offset, model, from, to)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = []db.APIRequest{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PagedResponse{Data: data, Total: total, Page: page, PageSize: pageSize})
}

// Sessions returns per-session aggregated stats with pagination.
func (h *Handler) Sessions(w http.ResponseWriter, r *http.Request) {
	from, to := rangeToFromTo(r.URL.Query().Get("range"))
	if v := r.URL.Query().Get("from"); v != "" && isValidDate(v) {
		from = v
	}
	if v := r.URL.Query().Get("to"); v != "" && isValidDate(v) {
		to = v
	}
	page, pageSize := parsePage(r)
	offset := (page - 1) * pageSize

	total, err := h.repo.CountSessionStats(r.Context(), from, to)
	if err != nil {
		log.Printf("sessions count error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data, err := h.repo.GetSessionStats(r.Context(), from, to, pageSize, offset)
	if err != nil {
		log.Printf("sessions data error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = []db.SessionStat{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PagedResponse{Data: data, Total: total, Page: page, PageSize: pageSize})
}

// Events streams Server-Sent Events to the client, pushing "update" on each new OTEL record.
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// No CORS header needed — same-origin only (local tool)

	fmt.Fprintf(w, "data: connected\n\n")
	flusher.Flush()

	ch := h.broker.Subscribe()
	defer h.broker.Unsubscribe(ch)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ch:
			fmt.Fprintf(w, "data: update\n\n")
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// Models returns the distinct model names seen in the database.
func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	models, err := h.repo.GetDistinctModels(r.Context())
	if err != nil {
		log.Printf("models error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if models == nil {
		models = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models)
}
