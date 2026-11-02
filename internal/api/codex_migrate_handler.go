package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/young1lin/cc-otel/internal/dbmerge"
)

// codexMigrationJob is the server-side state of the one-click AllClient
// migration. One at a time; started by POST /api/codex/migrate, polled by GET.
// Every field read/write is guarded by mu so concurrent GET/POST/snapshot
// callers see a consistent picture.
type codexMigrationJob struct {
	mu         sync.Mutex
	running    bool
	startedAt  int64
	finishedAt int64
	phase      dbmerge.Phase
	table      string
	total      int64
	processed  int64
	inserted   int64
	duplicates int64
	preview    []dbmerge.CodexMigrationCounts
	lastResult *dbmerge.Result
	err        string
}

type codexMigrationStatus struct {
	Running    bool                             `json:"running"`
	StartedAt  int64                            `json:"started_at"`
	FinishedAt int64                            `json:"finished_at"`
	Phase      dbmerge.Phase                    `json:"phase,omitempty"`
	Table      string                           `json:"table,omitempty"`
	TotalRows  int64                            `json:"total_rows,omitempty"`
	Processed  int64                            `json:"processed_rows,omitempty"`
	Inserted   int64                            `json:"inserted_rows,omitempty"`
	Duplicates int64                            `json:"duplicate_rows,omitempty"`
	Preview    []dbmerge.CodexMigrationCounts   `json:"preview,omitempty"`
	LastResult *dbmerge.Result                  `json:"last_result,omitempty"`
	Error      string                           `json:"error,omitempty"`
}

func (j *codexMigrationJob) snapshot() codexMigrationStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return codexMigrationStatus{
		Running: j.running, StartedAt: j.startedAt, FinishedAt: j.finishedAt,
		Phase: j.phase, Table: j.table, TotalRows: j.total,
		Processed: j.processed, Inserted: j.inserted, Duplicates: j.duplicates,
		Preview: j.preview, LastResult: j.lastResult, Error: j.err,
	}
}

// CodexMigrate serves GET (status) and POST (start) for the AllClient
// migration. The POST body is ignored; the migration always runs against the
// live database and is idempotent (identity-based dedup via import_ledger).
func (h *Handler) CodexMigrate(w http.ResponseWriter, r *http.Request) {
	job := h.codexMigrate
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.snapshot())
		return
	case http.MethodPost:
		job.mu.Lock()
		if job.running {
			job.mu.Unlock()
			writeAPIError(w, http.StatusConflict, "migration_in_progress", "A Codex migration is already running", nil)
			return
		}
		job.running = true
		job.startedAt = time.Now().Unix()
		job.finishedAt = 0
		job.phase = dbmerge.PhaseInspecting
		job.table = ""
		job.total, job.processed, job.inserted, job.duplicates = 0, 0, 0, 0
		job.err = ""
		job.mu.Unlock()
		go h.runCodexMigration()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.snapshot())
		return
	default:
		w.Header().Set("Allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", nil)
	}
}

func (h *Handler) runCodexMigration() {
	job := h.codexMigrate
	ctx := h.shutdownCtx
	if ctx == nil {
		ctx = context.Background()
	}
	fail := func(err error) {
		job.mu.Lock()
		job.running = false
		job.finishedAt = time.Now().Unix()
		job.err = err.Error()
		job.mu.Unlock()
	}

	preview, err := dbmerge.InspectCodexToAll(ctx, h.repo.DB())
	if err != nil {
		fail(err)
		return
	}
	job.mu.Lock()
	job.preview = preview
	job.phase = dbmerge.PhaseImporting
	for _, counts := range preview {
		job.total += counts.SourceRows
	}
	job.mu.Unlock()

	progress := func(p dbmerge.Progress) {
		job.mu.Lock()
		job.phase = p.Phase
		job.table = p.CurrentTable
		job.processed = p.ProcessedRows
		job.inserted = p.InsertedRows
		job.duplicates = p.DuplicateRows
		job.mu.Unlock()
	}
	result, err := dbmerge.MigrateCodexToAll(ctx, h.repo.DB(), dbmerge.Options{
		Progress: progress,
	})
	if err != nil {
		fail(err)
		return
	}
	job.mu.Lock()
	job.running = false
	job.finishedAt = time.Now().Unix()
	job.phase = dbmerge.PhaseVerifying
	job.lastResult = &result
	job.mu.Unlock()

	// The unified pool changed: refresh every open dashboard.
	h.broker.NotifySource("all")
}
