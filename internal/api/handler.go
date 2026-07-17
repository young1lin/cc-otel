package api

import (
	"compress/gzip"
	"context"
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
	"sync"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/pricing"
	"github.com/young1lin/cc-otel/internal/recompute"
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

// recomputeJob is the server-side state of a background cost recompute.
// One at a time; started by POST /api/pricing/recompute, polled by GET.
// Every field read/write is guarded by mu so concurrent GET/POST/snapshot
// callers see a consistent picture.
type recomputeJob struct {
	mu         sync.Mutex
	running    bool
	startedAt  int64
	finishedAt int64
	table      string
	total      int
	scanned    int
	changed    int
	err        string
	lastResult *recompute.Result
}

// recomputeStatus is the JSON shape returned by GET/POST /api/pricing/recompute.
type recomputeStatus struct {
	Running    bool              `json:"running"`
	StartedAt  int64             `json:"started_at"`
	FinishedAt int64             `json:"finished_at"`
	Table      string            `json:"table,omitempty"`
	Total      int               `json:"total,omitempty"`
	Scanned    int               `json:"scanned,omitempty"`
	Changed    int               `json:"changed,omitempty"`
	Error      string            `json:"error,omitempty"`
	LastResult *recompute.Result `json:"last_result,omitempty"`
}

// Handler serves the REST API and SSE endpoints for the cc-otel web dashboard.
type Handler struct {
	repo       *db.Repository
	broker     *Broker
	cfg        *config.Config
	configPath string
	pricer     pricing.Registry // optional; nil disables /api/pricing/lookup and the pricing block in /api/status

	pricingWriter pricing.Writer  // optional; nil disables /api/pricing CRUD
	recompute     *recomputeJob   // singleton background recompute; never nil after NewHandler
	shutdownCtx   context.Context // cancels the background recompute goroutine on shutdown

	importsMu      sync.Mutex
	imports        *importManager
	importInitErr  error
	importsStarted bool
}

// NewHandler creates a Handler with the given database repository, SSE broker, config,
// and the absolute path of the config file the daemon actually loaded.
func NewHandler(repo *db.Repository, broker *Broker, cfg *config.Config, configPath string) *Handler {
	return &Handler{
		repo:       repo,
		broker:     broker,
		cfg:        cfg,
		configPath: configPath,
		recompute:  &recomputeJob{},
	}
}

// SetPricer injects the pricing registry used by /api/status and
// /api/pricing/lookup. Done via setter (rather than another constructor
// argument) to avoid churning every existing call site.
func (h *Handler) SetPricer(p pricing.Registry) { h.pricer = p }

// SetPricingWriter injects the pricing Writer used by /api/pricing CRUD.
// nil (the default) makes the collection endpoint return 503.
func (h *Handler) SetPricingWriter(w pricing.Writer) { h.pricingWriter = w }

// SetShutdownContext supplies the context used to cancel a background
// recompute goroutine when the daemon shuts down. If unset, the recompute
// runs against context.Background.
func (h *Handler) SetShutdownContext(ctx context.Context) { h.shutdownCtx = ctx }

// InitImports initializes the singleton database-import manager. Initialization
// failure does not prevent the dashboard from starting; import endpoints report
// the stored error as unavailable.
func (h *Handler) InitImports() error {
	h.importsMu.Lock()
	defer h.importsMu.Unlock()
	if h.importsStarted {
		return h.importInitErr
	}
	h.importsStarted = true
	if h.repo == nil || h.repo.DB() == nil || h.cfg == nil || strings.TrimSpace(h.cfg.DBPath) == "" {
		h.importInitErr = fmt.Errorf("database import requires an initialized repository and database path")
		return h.importInitErr
	}
	parent := h.shutdownCtx
	if parent == nil {
		parent = context.Background()
	}
	h.imports, h.importInitErr = newImportManager(parent, h.repo.DB(), h.cfg.DBPath, h.broker, defaultImportEngine{})
	return h.importInitErr
}

// Close cancels active inspection/import work and waits for file users to exit.
func (h *Handler) Close() {
	h.importsMu.Lock()
	manager := h.imports
	h.importsMu.Unlock()
	if manager != nil {
		manager.close()
	}
}

// Register wires all API and static-file routes onto the given ServeMux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/health", h.Health)
	mux.HandleFunc("/api/status", h.Status)
	mux.HandleFunc("/api/pricing/lookup", h.PricingLookup)
	mux.HandleFunc("/api/pricing", h.PricingCollection)          // GET list / POST upsert / DELETE
	mux.HandleFunc("/api/pricing/recompute", h.PricingRecompute) // GET status / POST start
	mux.HandleFunc("/api/pricing/suggest", h.PricingSuggest)     // GET on-demand OpenRouter lookup
	mux.HandleFunc("/api/dashboard", h.Dashboard)
	mux.HandleFunc("/api/calendar", h.Calendar)
	mux.HandleFunc("/api/daily", h.DailyModel)
	mux.HandleFunc("/api/hourly", h.HourlyModel)
	mux.HandleFunc("/api/intraday", h.Intraday)
	mux.HandleFunc("/api/rate", h.Rate)
	mux.HandleFunc("/api/session/rate", h.SessionRate)
	mux.HandleFunc("/api/requests", h.Requests)
	mux.HandleFunc("/api/durations", h.Durations)
	mux.HandleFunc("/api/sessions", h.Sessions)
	mux.HandleFunc("/api/models", h.Models)
	mux.HandleFunc("/api/events", h.Events)
	mux.HandleFunc("/api/import/inspect", h.ImportInspect)
	mux.HandleFunc("/api/import/status", h.ImportStatus)
	mux.HandleFunc("/api/import/start", h.ImportStart)
	mux.HandleFunc("/api/import", h.ImportDelete)

	// Codex telemetry mirror routes (Task 11). Paths follow /api/codex/<name>.
	mux.HandleFunc("/api/codex/dashboard", h.CodexDashboard)
	mux.HandleFunc("/api/codex/calendar", h.CodexCalendar)
	mux.HandleFunc("/api/codex/daily", h.CodexDaily)
	mux.HandleFunc("/api/codex/requests", h.CodexRequests)
	mux.HandleFunc("/api/codex/sessions", h.CodexSessions)
	mux.HandleFunc("/api/codex/durations", h.CodexDurations)
	mux.HandleFunc("/api/codex/models", h.CodexModels)
	mux.HandleFunc("/api/codex/intraday", h.CodexIntraday)

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

	SSEClients  int   `json:"sse_clients"`
	LastUpdate  int64 `json:"last_update_unix"`
	NotifyCount int64 `json:"notify_count"`

	WebPort  int `json:"web_port"`
	OTELPort int `json:"otel_port"`

	OTELReceiverListening bool `json:"otel_receiver_listening"`

	// ConfigPath is the absolute path of the config file this daemon loaded at startup.
	// DBPath is the resolved SQLite path after yaml + CC_OTEL_DB_PATH env override.
	ConfigPath string `json:"config_path"`
	DBPath     string `json:"db_path"`

	// Pricing is omitted when no pricing registry is wired (legacy callers /
	// tests). The frontend Server Status popup keys off the presence of this
	// block to decide whether to render the Pricing row.
	Pricing *PricingStatus `json:"pricing,omitempty"`
}

// PricingStatus mirrors pricing.Snapshot with JSON-friendly names. Kept
// separate so the api package doesn't expose the internal struct directly.
type PricingStatus struct {
	TableSize     int      `json:"table_size"`
	UserOverrides int      `json:"user_overrides"`
	LastEditAt    int64    `json:"last_edit_at"`
	MissCount24h  int      `json:"miss_count_24h"`
	MissModelsTop []string `json:"miss_models_top"`
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

	var pricingBlock *PricingStatus
	if h.pricer != nil {
		s := h.pricer.Snapshot(r.Context())
		pricingBlock = &PricingStatus{
			TableSize:     s.TableSize,
			UserOverrides: s.UserOverrides,
			LastEditAt:    s.LastEditAt,
			MissCount24h:  s.MissCount24h,
			MissModelsTop: s.MissModelsTop,
		}
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
		Pricing:               pricingBlock,
	})
}

// PricingLookupResponse describes one model lookup against the registry.
// Returned by /api/pricing/lookup.
type PricingLookupResponse struct {
	Query      string  `json:"query"`
	Found      bool    `json:"found"`
	Kind       string  `json:"kind"` // "exact" | "alias" | "prefix" | "miss"
	MatchedKey string  `json:"matched_key,omitempty"`
	Source     string  `json:"source,omitempty"`
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_creation,omitempty"`
	IsClaude   bool    `json:"is_claude"` // Claude is intentionally absent — surfaced so callers know why
}

// PricingLookup answers "why does cc-otel think model X costs Y?". Useful
// when a row's cost_usd looks wrong: hit /api/pricing/lookup?model=glm-4.6
// to confirm whether the registry resolved at all and which entry won.
func (h *Handler) PricingLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.pricer == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "pricing registry not initialised"})
		return
	}
	model := r.URL.Query().Get("model")
	if model == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "missing model query param"})
		return
	}
	res := h.pricer.Lookup(r.Context(), model)
	out := PricingLookupResponse{
		Query:      model,
		Found:      res.Found,
		Kind:       string(res.Kind),
		MatchedKey: res.MatchedKey,
		IsClaude:   pricing.IsClaudeModel(model),
	}
	if res.Found {
		out.Source = res.Entry.Source
		out.Input = res.Entry.Input
		out.Output = res.Entry.Output
		out.CacheRead = res.Entry.CacheRead
		out.CacheWrite = res.Entry.CacheCreation
	}
	json.NewEncoder(w).Encode(out)
}

// perMtokFactor converts between USD-per-token (storage unit) and the
// USD-per-million-tokens unit the pricing UI/API exchanges on the wire.
const perMtokFactor = 1_000_000.0

// PricingCollection handles GET (list), POST (upsert), DELETE over model_pricing.
// Prices are USD/Mtok on the wire and USD/token in storage, so POST divides by
// perMtokFactor and GET multiplies back. Claude writes are rejected (400).
func (h *Handler) PricingCollection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.pricingWriter == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "pricing writer not initialised"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		pageSize, _ := strconv.Atoi(q.Get("page_size"))
		// Clamp page/pageSize so an adversarial request can't overflow the
		// (page-1)*pageSize slice arithmetic or return a giant page.
		if page < 1 {
			page = 1
		}
		if pageSize < 1 {
			pageSize = 1
		}
		if pageSize > 200 {
			pageSize = 200
		}
		// Local-seen models (from telemetry) boost to the top of the sort so
		// the user's own models surface above the ~1000-row seed catalog.
		local := map[string]bool{}
		if h.repo != nil {
			if ms, err := h.repo.GetDistinctModels(r.Context()); err == nil {
				for _, m := range ms {
					local[pricing.Normalize(m)] = true
				}
			}
		}
		// Warm the OpenRouter catalog so Claude reference prices populate on
		// this load instead of appearing blank until a reload. Blocks only when
		// the cache is cold (first load after restart / after the 10-min TTL);
		// bounded to 8s so a slow OpenRouter can't hang the page.
		if h.repo != nil {
			wctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			pricing.EnsureCatalogWarm(wctx)
			cancel()
		}
		lr, err := h.pricingWriter.List(r.Context(), pricing.ListFilter{
			Query:    q.Get("q"),
			Source:   q.Get("source"),
			Page:     page,
			PageSize: pageSize,
			Local:    local,
		})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		out := make([]map[string]any, 0, len(lr.Entries))
		for _, e := range lr.Entries {
			row := h.entryToWire(e)
			row["is_local"] = pricing.EntryIsLocal(e, local)
			out = append(out, row)
		}
		json.NewEncoder(w).Encode(map[string]any{"entries": out, "total": lr.Total})

	case http.MethodPost:
		var body struct {
			Model       string   `json:"model"`
			Input       float64  `json:"input"`
			Output      float64  `json:"output"`
			CacheRead   float64  `json:"cache_read"`
			CacheCreate float64  `json:"cache_create"`
			Aliases     []string `json:"aliases"`
			Source      string   `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
			return
		}
		// Claude is fixed-price and never recomputed; the save-side guard in
		// Upsert is authoritative, but we reject early here for a clear 400.
		if pricing.IsClaudeModel(pricing.Normalize(body.Model)) {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "claude models are fixed-price and not recomputed"})
			return
		}
		saved, err := h.pricingWriter.Upsert(r.Context(), pricing.Entry{
			Model:         body.Model,
			Input:         body.Input / perMtokFactor,
			Output:        body.Output / perMtokFactor,
			CacheRead:     body.CacheRead / perMtokFactor,
			CacheCreation: body.CacheCreate / perMtokFactor,
			Aliases:       body.Aliases,
			Source:        body.Source,
		})
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(h.entryToWire(saved))

	case http.MethodDelete:
		model := r.URL.Query().Get("model")
		if model == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "model query param required"})
			return
		}
		if err := h.pricingWriter.Delete(r.Context(), model); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// entryToWire maps a stored Entry (USD/token) to the USD/Mtok wire shape and
// flags entries whose price is overridden by the user's cc-otel.yaml pricing
// block (so the UI can badge them as non-editable here).
func (h *Handler) entryToWire(e pricing.Entry) map[string]any {
	// DB row's e.Model is already normalized; YAML pricing map keys are
	// case-preserving (e.g. "GLM-4.6"). Match the registry's behaviour by
	// normalizing each YAML key before comparing. User pricing blocks are
	// small, so O(n) here is fine.
	overridden := false
	if h.cfg != nil {
		for name := range h.cfg.Pricing {
			if pricing.Normalize(name) == e.Model {
				overridden = true
				break
			}
		}
	}
	wire := map[string]any{
		"model":              e.Model,
		"input":              e.Input * perMtokFactor,
		"output":             e.Output * perMtokFactor,
		"cache_read":         e.CacheRead * perMtokFactor,
		"cache_create":       e.CacheCreation * perMtokFactor,
		"aliases":            e.Aliases,
		"source":             e.Source,
		"updated_at":         e.UpdatedAt,
		"overridden_by_yaml": overridden,
	}
	if len(e.Variants) > 0 {
		vs := make([]map[string]any, 0, len(e.Variants))
		for _, v := range e.Variants {
			vs = append(vs, entryVariantToWire(v))
		}
		wire["variants"] = vs
	}
	return wire
}

// entryVariantToWire is a slimmed wire view of a folded variant (one of the
// other provider-prefixed entries under a canonical row). Read-only display.
func entryVariantToWire(e pricing.Entry) map[string]any {
	return map[string]any{
		"model":        e.Model,
		"input":        e.Input * perMtokFactor,
		"output":       e.Output * perMtokFactor,
		"cache_read":   e.CacheRead * perMtokFactor,
		"cache_create": e.CacheCreation * perMtokFactor,
		"source":       e.Source,
	}
}

// PricingSuggest handles GET /api/pricing/suggest — a user-initiated
// OpenRouter price lookup used to prefill the manual-entry form. It never
// writes. The response carries the default one-click price (first-party
// "Official" provider when OpenRouter lists one, else the blended minimum)
// plus a "providers" list + "providers_total" for the picker. A non-match
// returns a zero-valued SuggestResult (Matched=false).
func (h *Handler) PricingSuggest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	model := r.URL.Query().Get("model")
	if pricing.Normalize(model) == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "model query param required"})
		return
	}
	sug, err := pricing.SuggestOpenRouter(r.Context(), model)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "openrouter unreachable: " + err.Error()})
		return
	}
	json.NewEncoder(w).Encode(sug)
}

// PricingRecompute handles GET (status) / POST (start) for the background
// full-table recompute. State is server-side and singleton: GET never starts a
// job; POST while a job is already running is a no-op that returns the current
// status. One job at a time.
func (h *Handler) PricingRecompute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(h.recompute.snapshot())

	case http.MethodPost:
		if h.pricer == nil || h.repo == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "pricing registry or db not available"})
			return
		}
		h.recompute.mu.Lock()
		if h.recompute.running {
			h.recompute.mu.Unlock()
			json.NewEncoder(w).Encode(h.recompute.snapshot()) // already running — no-op
			return
		}
		h.recompute.running = true
		h.recompute.startedAt = time.Now().Unix()
		h.recompute.finishedAt = 0
		h.recompute.table = ""
		h.recompute.total, h.recompute.scanned, h.recompute.changed = 0, 0, 0
		h.recompute.err = ""
		h.recompute.lastResult = nil
		h.recompute.mu.Unlock()

		go h.runRecompute()
		json.NewEncoder(w).Encode(h.recompute.snapshot())

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// runRecompute is the background goroutine body. It uses the daemon shutdown
// context (falling back to Background) so a SIGINT during a long recompute
// cancels it. Progress is written back to the job under the mutex; the
// terminal state always sets running=false and finishedAt so a later GET sees
// completion.
func (h *Handler) runRecompute() {
	ctx := context.Background()
	if h.shutdownCtx != nil {
		ctx = h.shutdownCtx
	}
	progress := func(p recompute.Progress) {
		h.recompute.mu.Lock()
		h.recompute.table = p.Table
		h.recompute.total = p.Total
		h.recompute.scanned = p.Scanned
		h.recompute.changed = p.Changed
		h.recompute.mu.Unlock()
	}
	res, err := recompute.Run(ctx, h.repo.DB(), h.pricer, recompute.Options{}, true, progress)
	h.recompute.mu.Lock()
	h.recompute.running = false
	h.recompute.finishedAt = time.Now().Unix()
	if err != nil {
		h.recompute.err = err.Error()
	} else {
		h.recompute.lastResult = &res
	}
	h.recompute.mu.Unlock()
}

// snapshot returns a consistent, lock-held copy of the job's state for JSON
// serialisation. Callers must not mutate the returned LastResult pointer.
func (j *recomputeJob) snapshot() recomputeStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return recomputeStatus{
		Running:    j.running,
		StartedAt:  j.startedAt,
		FinishedAt: j.finishedAt,
		Table:      j.table,
		Total:      j.total,
		Scanned:    j.scanned,
		Changed:    j.changed,
		Error:      j.err,
		LastResult: j.lastResult,
	}
}

// rangeToFromTo converts a range name to (from, to) date strings in local time.
// SYNC: This logic is mirrored in app.js rangeToFromTo(). Keep both in sync:
//
//	week  = today − 6 days  (inclusive → 7 days total)
//	month = today − 29 days (inclusive → 30 days total)
//	all   = 1970-01-01 → today
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

// Durations returns per-model average duration stats for the selected date range.
// Query:
//   - from=YYYY-MM-DD&to=YYYY-MM-DD (preferred)
//   - or range=today|week|month|all
//   - optional: model=<name> to filter down to a single model
//   - optional: limit=<n> (default 2000, max 2000)
func (h *Handler) Durations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.repo == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode([]db.DurationStat{})
		return
	}

	q := r.URL.Query()
	from := q.Get("from")
	to := q.Get("to")
	if from == "" || to == "" {
		rr := q.Get("range")
		if rr == "" {
			rr = "today"
		}
		from, to = rangeToFromTo(rr)
	}
	if !isValidDate(from) || !isValidDate(to) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid from/to date; expected YYYY-MM-DD"})
		return
	}

	model := q.Get("model")
	limit := 2000
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			limit = n
		}
	}

	stats, err := h.repo.GetDurationStatsByModel(r.Context(), model, from, to, limit)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(stats)
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

type CalendarResponse struct {
	Data []db.CalendarDay `json:"data"`
}

// Calendar returns compact per-day aggregates for the dashboard usage calendar.
func (h *Handler) Calendar(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.repo == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(CalendarResponse{Data: []db.CalendarDay{}})
		return
	}

	from, to := rangeToFromTo(r.URL.Query().Get("range"))
	if v := r.URL.Query().Get("from"); v != "" {
		if !isValidDate(v) {
			http.Error(w, "invalid from date", http.StatusBadRequest)
			return
		}
		from = v
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if !isValidDate(v) {
			http.Error(w, "invalid to date", http.StatusBadRequest)
			return
		}
		to = v
	}

	data, err := h.repo.GetCalendarDays(r.Context(), from, to)
	if err != nil {
		log.Printf("calendar error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = []db.CalendarDay{}
	}
	json.NewEncoder(w).Encode(CalendarResponse{Data: data})
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

type HourlyResponse struct {
	Date string                  `json:"date"`
	Data []db.HourlyModelSummary `json:"data"`
}

// HourlyModel returns per-hour, per-model token usage and cost for a single local day.
// Query params:
// - date=YYYY-MM-DD (optional; defaults to today)
// - model=<name> (optional filter)
func (h *Handler) HourlyModel(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		from, to := rangeToFromTo(r.URL.Query().Get("range"))
		if from == to {
			date = from
		} else {
			// Default to today if range is not a single day.
			_, t := rangeToFromTo("today")
			date = t
		}
	}
	if !isValidDate(date) {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	model := r.URL.Query().Get("model")

	data, err := h.repo.GetHourlyStatsByModel(r.Context(), date, model)
	if err != nil {
		log.Printf("hourly data error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = []db.HourlyModelSummary{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HourlyResponse{Date: date, Data: data})
}

// IntradayResponse wraps Intraday for the JSON envelope; From/To are inclusive
// local YYYY-MM-DD; BucketMinutes echoes back the granularity actually used so
// the client can label its axis without re-deriving it.
type IntradayResponse struct {
	From          string                    `json:"from"`
	To            string                    `json:"to"`
	BucketMinutes int                       `json:"bucket_minutes"`
	Data          []db.IntradayModelSummary `json:"data"`
}

// Intraday returns per-(time-bucket, model) stats across a [from, to] window of
// at most 7 local days, bucketed at 15/30/60 minutes. Designed for the new
// line-chart "Intraday by Model" view; defaults to today + 30-min buckets.
//
// Query params:
//   - from=YYYY-MM-DD, to=YYYY-MM-DD (optional; default = today)
//   - range=<today|week|month|all> (used only if from/to omitted)
//   - bucket=15|30|60 (default 30)
//   - model=<name> (optional filter)
func (h *Handler) Intraday(w http.ResponseWriter, r *http.Request) {
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
	model := q.Get("model")

	data, err := h.repo.GetIntradayStatsByModel(r.Context(), from, to, bucket, model)
	if err != nil {
		log.Printf("intraday data error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = []db.IntradayModelSummary{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(IntradayResponse{From: from, To: to, BucketMinutes: bucket, Data: data})
}

// RateResponse wraps the rate-over-time buckets in a JSON envelope. From/To are
// inclusive local YYYY-MM-DD; BucketMinutes echoes the granularity actually used.
type RateResponse struct {
	From          string          `json:"from"`
	To            string          `json:"to"`
	BucketMinutes int             `json:"bucket_minutes"`
	Data          []db.RateBucket `json:"data"`
}

// Rate returns per-(bucket, model) token throughput over time for the rate chart.
func (h *Handler) Rate(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "rate range must not exceed 7 days", http.StatusBadRequest)
		return
	}

	bucket := 30
	if v := q.Get("bucket"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && db.ValidRateBucketMinutes(n) {
			bucket = n
		}
	}
	model := q.Get("model")

	data, err := h.repo.GetRateOverTime(r.Context(), from, to, bucket, model)
	if err != nil {
		log.Printf("rate data error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = []db.RateBucket{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(RateResponse{From: from, To: to, BucketMinutes: bucket, Data: data})
}

// SessionRate returns token throughput for the most recent 1-minute window in which
// the given session had API activity (duration_ms > 0).
func (h *Handler) SessionRate(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}

	snap, err := h.repo.GetSessionRecentMinuteRate(r.Context(), sessionID)
	if err != nil {
		log.Printf("session rate error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if snap == nil {
		http.Error(w, "no rate data for session", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
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
		case src, ok := <-ch:
			if !ok {
				return
			}
			if src == "" {
				src = "claude"
			}
			fmt.Fprintf(w, "data: %s\n\n", src)
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
