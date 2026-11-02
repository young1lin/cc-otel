package api

import (
	"context"
	"encoding/json"
	"log"
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

// Terminal migration phases. dbmerge's own phase values are all mid-run;
// pollers need to distinguish a finished job from one stuck in verify.
const (
	PhaseMigrateDone   dbmerge.Phase = "done"
	PhaseMigrateFailed dbmerge.Phase = "failed"
)

type codexMigrationStatus struct {
	Running    bool                           `json:"running"`
	StartedAt  int64                          `json:"started_at"`
	FinishedAt int64                          `json:"finished_at"`
	Phase      dbmerge.Phase                  `json:"phase,omitempty"`
	Table      string                         `json:"table,omitempty"`
	TotalRows  int64                          `json:"total_rows,omitempty"`
	Processed  int64                          `json:"processed_rows,omitempty"`
	Inserted   int64                          `json:"inserted_rows,omitempty"`
	Duplicates int64                          `json:"duplicate_rows,omitempty"`
	Preview    []dbmerge.CodexMigrationCounts `json:"preview,omitempty"`
	LastResult *dbmerge.Result                `json:"last_result,omitempty"`
	Error      string                         `json:"error,omitempty"`
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
// retentionDays reports the configured retention window (0 = keep
// everything, the all-time sanity check).
func (h *Handler) retentionDays() int {
	if h.cfg == nil {
		return 0
	}
	return h.cfg.RetentionDays
}

func (h *Handler) CodexMigrate(w http.ResponseWriter, r *http.Request) {
	job := h.codexMigrate
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.snapshot())
		return
	case http.MethodPost:
		if h.repo == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "repository_unavailable", "repository not initialized", nil)
			return
		}
		// The codex migration and file imports both write the live DB through
		// the same staged pipeline; run one at a time (each pins a pool conn
		// and holds BEGIN IMMEDIATE batches — same-file concurrent writers are
		// the exact pattern the dedicated read-only source guards against).
		h.importsMu.Lock()
		imports := h.imports
		h.importsMu.Unlock()
		// Serialize the two writers' start decisions (R4 concurrency F2):
		// hold writerBusMu across BOTH the import check and the running-flag
		// flip so a concurrent import start cannot slip between them.
		h.writerBusMu.Lock()
		defer h.writerBusMu.Unlock()
		if imports != nil && imports.HasActiveJob() {
			writeAPIError(w, http.StatusConflict, "import_in_progress", "A database import is running; start the Codex migration after it finishes", nil)
			return
		}
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
		job.preview = nil
		job.lastResult = nil
		job.err = ""
		job.mu.Unlock()
		h.migrateWG.Add(1)
		go func() {
			defer h.migrateWG.Done()
			h.runCodexMigration()
		}()
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
	// Hard deadline: a wedged migration must not hold running=true (and 409
	// every future POST) forever. 30 minutes covers the largest archives.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	fail := func(err error, result *dbmerge.Result) {
		job.mu.Lock()
		job.running = false
		job.finishedAt = time.Now().Unix()
		job.phase = PhaseMigrateFailed
		if result != nil {
			// Batches committed before the failure are durable and
			// idempotent-safe; surface what actually landed.
			job.lastResult = result
		}
		job.err = err.Error()
		job.mu.Unlock()
		log.Printf("codex migration: FAILED: %v (batches committed before the failure are durable)", err)
		// Partial commits may have changed the pool: refresh dashboards.
		h.broker.NotifySource("all")
	}

	log.Printf("codex migration: starting (archive inspection)")
	preview, err := dbmerge.InspectCodexToAll(ctx, h.repo.DB())
	if err != nil {
		log.Printf("codex migration: inspection failed: %v", err)
		fail(err, nil)
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
	// TotalRows from the preview above: skips MigrateCodexToAll's internal
	// full-archive inspection. RetentionDays scopes the post-migration agg
	// sanity check to the retention window (Cleanup prunes daily_model_agg
	// older than this while api_requests is never pruned).
	result, err := dbmerge.MigrateCodexToAll(ctx, h.repo.DB(), dbmerge.Options{
		Progress:      progress,
		TotalRows:     job.total,
		RetentionDays: h.retentionDays(),
	})
	if err != nil {
		fail(err, &result)
		return
	}
	// Re-run the inspection AFTER the copy so the finished card shows the
	// post-migration truth (all rows already merged) instead of the stale
	// pre-run preview that promised them as new. Bounded deadline: these run
	// AFTER the migration ctx is done (a shutdown may have cancelled it), so
	// the post-inspection and the WAL checkpoints anchor to the SHUTDOWN ctx,
	// not Background() — Close() joins this goroutine via migrateWG and a
	// detached 2-minute context would stall that join past cancel (R4
	// concurrency P3). No-op re-runs (nothing inserted) keep the pre-run
	// preview — it already showed new_rows=0 for every table, and skipping the
	// staging pass saves its full-archive cost (~3s on the production archive).
	postParent := h.shutdownCtx
	if postParent == nil { // unit tests build Handlers without SetShutdownContext
		postParent = context.Background()
	}
	postCtx, postCancel := context.WithTimeout(postParent, 2*time.Minute)
	defer postCancel()
	if result.InsertedRows > 0 {
		if post, perr := dbmerge.InspectCodexToAll(postCtx, h.repo.DB()); perr == nil {
			job.mu.Lock()
			job.preview = post
			job.mu.Unlock()
		} else {
			log.Printf("codex migration: post-run inspection failed (card keeps the pre-run preview): %v", perr)
		}
	}
	job.mu.Lock()
	job.running = false
	job.finishedAt = time.Now().Unix()
	job.phase = PhaseMigrateDone
	job.lastResult = &result
	job.mu.Unlock()
	log.Printf("codex migration: done — scanned=%d inserted=%d duplicates=%d verified=%d",
		result.ScannedRows, result.InsertedRows, result.DuplicateRows, result.VerifiedIdentities)

	// The migration wrote a large burst through WAL. A passive checkpoint
	// folds the frames back into the main DB when idle (readers never
	// blocked); the follow-up TRUNCATE reclaims the -wal file, which neither
	// passive checkpoints nor autocheckpoint ever shrink on their own — the
	// production file stayed frozen at 340MB after the first big migration
	// (R3 perf finding). Busy readers simply leave it for the next attempt.
	if _, err := h.repo.DB().ExecContext(postCtx, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		log.Printf("codex migration wal checkpoint: %v", err)
	}
	if _, err := h.repo.DB().ExecContext(postCtx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		log.Printf("codex migration wal truncate: %v", err)
	}

	// The unified pool changed: refresh every open dashboard.
	h.broker.NotifySource("all")
}
