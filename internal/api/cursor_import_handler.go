package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/young1lin/cc-otel/internal/cursorimport"
)

// cursorImportJob is the server-side state of the Cursor usage-events CSV
// import. One at a time; started by POST /api/cursor/import (multipart CSV
// upload), polled by GET. Every field read/write is guarded by mu, mirroring
// codexMigrationJob.
type cursorImportJob struct {
	mu                 sync.Mutex
	running            bool
	startedAt          int64
	finishedAt         int64
	files              []string
	total              int64
	processed          int64
	inserted           int64
	duplicates         int64
	skippedZeroToken   int64
	identityViolations int64
	invalidRows        int64
	// supersededHint counts pool rows that share an exact (timestamp, model)
	// pair with another cursor row — the observable footprint of Cursor
	// REWRITING a row between exports (same instant+model, different token
	// counts → different request_id → both variants kept by design).
	supersededHint int64
	lastResult     *cursorimport.Result
	err            string
}

type cursorImportStatus struct {
	Running            bool                 `json:"running"`
	StartedAt          int64                `json:"started_at"`
	FinishedAt         int64                `json:"finished_at"`
	Files              []string             `json:"files,omitempty"`
	TotalRows          int64                `json:"total_rows"`
	ProcessedRows      int64                `json:"processed_rows"`
	InsertedRows       int64                `json:"inserted_rows"`
	DuplicateRows      int64                `json:"duplicate_rows"`
	SkippedZeroRows    int64                `json:"skipped_zero_rows"`
	IdentityViolations int64                `json:"identity_violations"`
	InvalidRows        int64                `json:"invalid_rows"`
	SupersededHint     int64                `json:"superseded_hint"`
	LastResult         *cursorimport.Result `json:"last_result,omitempty"`
	Error              string               `json:"error,omitempty"`
	// History is derived per request from import_ledger (not job state), so a
	// page reload shows past imports even before any job ran this session.
	History []cursorImportHistoryEntry `json:"history"`
}

// cursorImportHistoryEntry is one previously imported file: how many distinct
// rows it ever contributed and when it was first imported.
type cursorImportHistoryEntry struct {
	File       string `json:"file"`
	Rows       int64  `json:"rows"`
	ImportedAt int64  `json:"imported_at"`
}

func (j *cursorImportJob) snapshot() cursorImportStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return cursorImportStatus{
		Running: j.running, StartedAt: j.startedAt, FinishedAt: j.finishedAt,
		Files: j.files, TotalRows: j.total, ProcessedRows: j.processed,
		InsertedRows: j.inserted, DuplicateRows: j.duplicates,
		SkippedZeroRows: j.skippedZeroToken, IdentityViolations: j.identityViolations,
		InvalidRows: j.invalidRows, SupersededHint: j.supersededHint,
		LastResult: j.lastResult, Error: j.err,
	}
}

// Public upload bounds. A month export is ~350KB and the whole 11-month
// history is 1.9MB; 64MB covers many redundant re-downloads with room to
// spare while staying trivial to buffer in memory (rows are ~100B each).
const (
	maxCursorUploadBytes = 64 << 20
	maxCursorFileCount   = 100
)

// publicCursorImportError maps an import failure to a short, generic
// message; internal details (SQL errors, paths) stay in the server log only.
func publicCursorImportError(err error) string {
	if err == nil {
		return "the import failed"
	}
	if strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		return "the import timed out; committed rows are kept"
	}
	return "the import failed; committed rows are kept"
}

// cursorImportDryRunFile is the per-file preview breakdown.
type cursorImportDryRunFile struct {
	File          string `json:"file"`
	CandidateRows int64  `json:"candidate_rows"`
	NewRows       int64  `json:"new_rows"`
	DuplicateRows int64  `json:"duplicate_rows"`
}

type cursorImportDryRunResult struct {
	Files              []cursorImportDryRunFile `json:"files"`
	TotalCandidates    int64                    `json:"total_candidates"`
	TotalNew           int64                    `json:"total_new"`
	TotalDuplicates    int64                    `json:"total_duplicates"`
	SkippedZeroRows    int64                    `json:"skipped_zero_rows"`
	IdentityViolations int64                    `json:"identity_violations"`
	InvalidRows        int64                    `json:"invalid_rows"`
	FirstDate          string                   `json:"first_date"`
	LastDate           string                   `json:"last_date"`
	// NewModels lists base model names in the upload that have no cursor rows
	// yet — the models whose first appearance this import would be.
	NewModels []string `json:"new_models"`
}

// CursorImportDryRun serves POST /api/cursor/import/dry-run: the exact same
// multipart parse as the real import (same guards, same column mapping, same
// zero-token skip), but READ-ONLY — it never writes, never takes the
// one-writer gate, and never touches job state. Existence is checked against
// the live pool, so a preview concurrent with a running import is approximate
// (a row the import is about to fold still counts as new); that is fine for a
// confirmation hint and is the reason this is not gated.
func (h *Handler) CursorImportDryRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only", nil)
		return
	}
	if h.repo == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "repository_unavailable", "repository not initialized", nil)
		return
	}
	ctx := r.Context()
	rows, stats, _, publicErr := h.parseCursorUpload(w, r)
	if publicErr != "" {
		return // parseCursorUpload already wrote the API error
	}

	// Which request_ids already exist? Chunked IN probes keep well under the
	// SQLite parameter ceiling; 500 is instant at real-history scale.
	exists := make(map[string]bool, len(rows))
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.RequestID
	}
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		q := "SELECT request_id FROM api_requests WHERE request_id IN (" +
			strings.TrimRight(strings.Repeat("?,", len(chunk)), ",") + ")"
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		found, err := h.repo.DB().QueryContext(ctx, q, args...)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "dry_run_failed", "the preview failed", nil)
			return
		}
		for found.Next() {
			var id string
			_ = found.Scan(&id)
			exists[id] = true
		}
		found.Close()
	}

	// Models the pool already has under cursor — base names, same as Import stores.
	poolModels := map[string]bool{}
	if mrows, err := h.repo.DB().QueryContext(ctx,
		"SELECT DISTINCT model FROM api_requests WHERE service_name='cursor'"); err == nil {
		for mrows.Next() {
			var m string
			_ = mrows.Scan(&m)
			poolModels[m] = true
		}
		mrows.Close()
	}

	res := cursorImportDryRunResult{
		SkippedZeroRows:    int64(stats.SkippedZeroToken),
		IdentityViolations: int64(stats.IdentityViolations),
		InvalidRows:        int64(stats.InvalidRows),
		NewModels:          []string{},
	}
	perFile := map[string]*cursorImportDryRunFile{}
	seenNewModels := map[string]bool{}
	var firstTS, lastTS int64
	for i, row := range rows {
		if i == 0 || row.Timestamp.Unix() < firstTS {
			firstTS = row.Timestamp.Unix()
		}
		if i == 0 || row.Timestamp.Unix() > lastTS {
			lastTS = row.Timestamp.Unix()
		}
		f := perFile[row.File]
		if f == nil {
			f = &cursorImportDryRunFile{File: row.File}
			perFile[row.File] = f
		}
		f.CandidateRows++
		res.TotalCandidates++
		if exists[row.RequestID] {
			f.DuplicateRows++
			res.TotalDuplicates++
		} else {
			f.NewRows++
			res.TotalNew++
		}
		base := cursorimport.NormalizeCursorModel(row.Model)
		if !poolModels[base] && !seenNewModels[base] {
			seenNewModels[base] = true
			res.NewModels = append(res.NewModels, base)
		}
	}
	for _, f := range perFile {
		res.Files = append(res.Files, *f)
	}
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].File < res.Files[j].File })
	sort.Strings(res.NewModels)
	res.FirstDate = time.Unix(firstTS, 0).Local().Format("2006-01-02")
	res.LastDate = time.Unix(lastTS, 0).Local().Format("2006-01-02")
	if len(rows) == 0 {
		// Mirror the real import's rejection shape so the UI can show why
		// nothing is importable before the user ever clicks Import.
		writeAPIError(w, http.StatusBadRequest, "no_usable_rows",
			"No importable rows found in the uploaded CSV files", map[string]any{
				"skipped_zero_rows":   stats.SkippedZeroToken,
				"identity_violations": stats.IdentityViolations,
				"invalid_rows":        stats.InvalidRows,
			})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// cursorImportHistory reads the per-file rollup of every Cursor CSV import
// from import_ledger (newest first, capped). Ledger rows are INSERT OR IGNORE
// keyed by request_id, so Rows counts each file's distinct contributions ever
// and a re-import of the same file does not double it.
func (h *Handler) cursorImportHistory(ctx context.Context) []cursorImportHistoryEntry {
	if h.repo == nil {
		return nil
	}
	rows, err := h.repo.DB().QueryContext(ctx, `
		SELECT source_db, COUNT(*), MIN(imported_at)
		  FROM import_ledger
		 WHERE source_db LIKE 'cursor-csv:%'
		 GROUP BY source_db
		 ORDER BY MIN(imported_at) DESC
		 LIMIT 20`)
	if err != nil {
		return nil // history is a nicety; never fail the status endpoint on it
	}
	defer rows.Close()
	out := make([]cursorImportHistoryEntry, 0, 8)
	for rows.Next() {
		var e cursorImportHistoryEntry
		if err := rows.Scan(&e.File, &e.Rows, &e.ImportedAt); err != nil {
			return out
		}
		e.File = strings.TrimPrefix(e.File, "cursor-csv:")
		out = append(out, e)
	}
	return out
}

// countSupersededRows counts cursor pool rows sharing an exact (timestamp,
// model) pair with another INTERACTIVE-model cursor row. Background agent
// families (grok-bot-*, Bugbot, agent_review) legitimately fire parallel
// requests at the same millisecond — on the 18899 rehearsal they accounted
// for 320 of 333 collisions — so they are excluded; a surviving same-instant
// collision on an interactive model has different token counts (identical
// counts would have deduped into one row) and is the observable footprint of
// Cursor re-exporting a revised row, which the hash-keyed import stores as
// BOTH variants by design. The count is a hint, not an audit: composer fan-
// out can still collide, so the UI words it "possibly revised".
func countSupersededRows(ctx context.Context, db *sql.DB) int64 {
	var n int64
	_ = db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(c - 1), 0) FROM (
			SELECT COUNT(*) AS c FROM api_requests
			 WHERE service_name = 'cursor'
			   AND model NOT LIKE 'grok-bot-%'
			   AND model NOT IN ('github_bugbot', 'agent_review')
			 GROUP BY timestamp, model HAVING c > 1)`).Scan(&n)
	return n
}

// CursorImport serves GET (status) and POST (start) for the Cursor CSV
// import. POST carries the CSV files as multipart parts (any part with a
// filename; .csv only) plus an optional keep_zero form value. Parsing is
// synchronous (seconds for realistic histories); row insertion runs as a
// background job so the UI can poll progress.
func (h *Handler) CursorImport(w http.ResponseWriter, r *http.Request) {
	job := h.cursorImport
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		st := job.snapshot()
		st.History = h.cursorImportHistory(r.Context())
		_ = json.NewEncoder(w).Encode(st)
		return
	case http.MethodPost:
		if h.repo == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "repository_unavailable", "repository not initialized", nil)
			return
		}
		// Same one-writer bus as the Codex migration and database import:
		// every background writer drives writes against the live DB, so
		// start decisions must serialize under writerBusMu.
		h.importsMu.Lock()
		imports := h.imports
		h.importsMu.Unlock()
		h.writerBusMu.Lock()
		defer h.writerBusMu.Unlock()
		if imports != nil && imports.HasActiveJob() {
			writeAPIError(w, http.StatusConflict, "import_in_progress", "A database import is running; start the Cursor import after it finishes", nil)
			return
		}
		h.codexMigrate.mu.Lock()
		migrationRunning := h.codexMigrate.running
		h.codexMigrate.mu.Unlock()
		if migrationRunning {
			writeAPIError(w, http.StatusConflict, "migration_in_progress", "The Codex migration is running; start the Cursor import after it finishes", nil)
			return
		}
		job.mu.Lock()
		if job.running {
			job.mu.Unlock()
			writeAPIError(w, http.StatusConflict, "import_in_progress", "A Cursor import is already running", nil)
			return
		}

		rows, stats, files, publicErr := h.parseCursorUpload(w, r)
		if publicErr != "" {
			job.mu.Unlock()
			return // parseCursorUpload already wrote the API error
		}
		if len(rows) == 0 {
			job.mu.Unlock()
			writeAPIError(w, http.StatusBadRequest, "no_usable_rows",
				"No importable rows found in the uploaded CSV files", map[string]any{
					"skipped_zero_rows":   stats.SkippedZeroToken,
					"identity_violations": stats.IdentityViolations,
					"invalid_rows":        stats.InvalidRows,
				})
			return
		}
		job.running = true
		job.startedAt = time.Now().Unix()
		job.finishedAt = 0
		job.files = files
		job.total = int64(len(rows))
		job.processed, job.inserted, job.duplicates = 0, 0, 0
		job.supersededHint = 0
		job.skippedZeroToken = int64(stats.SkippedZeroToken)
		job.identityViolations = int64(stats.IdentityViolations)
		job.invalidRows = int64(stats.InvalidRows)
		job.lastResult = nil
		job.err = ""
		rowsToWrite := rows
		job.mu.Unlock()
		h.migrateWG.Add(1)
		go func() {
			defer h.migrateWG.Done()
			h.runCursorImport(rowsToWrite)
		}()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(job.snapshot())
		return
	default:
		w.Header().Set("Allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", nil)
	}
}

// parseCursorUpload reads the multipart body and parses every .csv part.
// It returns the merged rows, the summed parse stats, and the file names;
// on failure it writes the API error itself and returns a non-empty
// publicErr so the caller only unwinds.
func (h *Handler) parseCursorUpload(w http.ResponseWriter, r *http.Request) ([]cursorimport.Row, cursorimport.ParseStats, []string, string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCursorUploadBytes+maxMultipartOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_multipart", "Invalid multipart upload", nil)
		return nil, cursorimport.ParseStats{}, nil, "invalid_multipart"
	}
	var rows []cursorimport.Row
	var stats cursorimport.ParseStats
	var files []string
	keepZero := false
	parts := 0
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_multipart", "Invalid multipart upload", nil)
			return nil, stats, nil, "invalid_multipart"
		}
		if part.FileName() == "" {
			if strings.EqualFold(part.FormName(), "keep_zero") {
				v, _ := io.ReadAll(io.LimitReader(part, 16))
				sv := strings.ToLower(strings.TrimSpace(string(v)))
				keepZero = sv == "1" || sv == "true" || sv == "on" || sv == "yes"
			} else {
				_, _ = io.Copy(io.Discard, part)
			}
			part.Close()
			continue
		}
		parts++
		if parts > maxCursorFileCount {
			part.Close()
			writeAPIError(w, http.StatusBadRequest, "too_many_files",
				"At most 100 CSV files may be uploaded at once", nil)
			return nil, stats, nil, "too_many_files"
		}
		name := filepath.Base(part.FileName())
		if !strings.EqualFold(filepath.Ext(name), ".csv") {
			part.Close()
			writeAPIError(w, http.StatusBadRequest, "invalid_file_extension",
				"Choose Cursor usage-events .csv files", nil)
			return nil, stats, nil, "invalid_file_extension"
		}
		body, err := io.ReadAll(part)
		part.Close()
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_multipart", "Invalid multipart upload", nil)
			return nil, stats, nil, "invalid_multipart"
		}
		fileRows, fileStats, err := cursorimport.Parse(name, strings.NewReader(string(body)), keepZero)
		if err != nil {
			// Parse errors name missing columns / line numbers only — safe
			// to surface; they are the user's actionable signal.
			writeAPIError(w, http.StatusBadRequest, "invalid_csv",
				name+": "+err.Error(), nil)
			return nil, stats, nil, "invalid_csv"
		}
		rows = append(rows, fileRows...)
		stats.Rows += fileStats.Rows
		stats.SkippedZeroToken += fileStats.SkippedZeroToken
		stats.IdentityViolations += fileStats.IdentityViolations
		stats.InvalidRows += fileStats.InvalidRows
		files = append(files, name)
	}
	if len(files) == 0 {
		writeAPIError(w, http.StatusBadRequest, "missing_file", "At least one .csv file is required", nil)
		return nil, stats, nil, "missing_file"
	}
	return rows, stats, files, ""
}

func (h *Handler) runCursorImport(rows []cursorimport.Row) {
	job := h.cursorImport
	ctx := h.shutdownCtx
	if ctx == nil {
		ctx = context.Background()
	}
	// Hard deadline so a wedged import cannot hold running=true (and 409
	// every future POST) forever. 20k rows insert in seconds; 15 minutes
	// covers histories two orders of magnitude larger.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	fail := func(err error) {
		job.mu.Lock()
		job.running = false
		job.finishedAt = time.Now().Unix()
		job.err = publicCursorImportError(err)
		job.mu.Unlock()
		log.Printf("cursor import: FAILED: %v", err)
		h.broker.NotifySource("all")
	}

	log.Printf("cursor import: starting (%d candidate rows from %d file(s))", len(rows), len(job.files))
	progress := func(processed, inserted, duplicates int64) {
		job.mu.Lock()
		job.processed = processed
		job.inserted = inserted
		job.duplicates = duplicates
		job.mu.Unlock()
	}
	result, err := cursorimport.Import(ctx, h.repo, h.pricer, rows, progress)
	if err != nil {
		fail(err)
		return
	}
	// Compute outside the job lock: one group-by over the cursor pool (ms at
	// real-history scale) that must not block a concurrent status poll.
	superseded := countSupersededRows(ctx, h.repo.DB())
	job.mu.Lock()
	job.running = false
	job.finishedAt = time.Now().Unix()
	job.processed = result.Scanned
	job.inserted = result.Inserted
	job.duplicates = result.Duplicates
	job.lastResult = &result
	// Parse-time counters live on the job (Import's Result only sees write-
	// time numbers), so the summary log reads them from there.
	logSkippedZero, logViolations, logInvalid := job.skippedZeroToken, job.identityViolations, job.invalidRows
	job.supersededHint = superseded
	job.mu.Unlock()
	log.Printf("cursor import: done — scanned=%d inserted=%d duplicates=%d skippedZero=%d violations=%d invalid=%d superseded=%d",
		result.Scanned, result.Inserted, result.Duplicates, logSkippedZero, logViolations, logInvalid, superseded)

	// Mirror the codex migration's WAL hygiene: fold the burst back and let
	// TRUNCATE reclaim the -wal file.
	postParent := h.shutdownCtx
	if postParent == nil {
		postParent = context.Background()
	}
	postCtx, postCancel := context.WithTimeout(postParent, 2*time.Minute)
	defer postCancel()
	if _, err := h.repo.DB().ExecContext(postCtx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		log.Printf("cursor import wal checkpoint: %v", err)
	}
	if _, err := h.repo.DB().ExecContext(postCtx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("cursor import wal truncate: %v", err)
	}

	// The unified pool changed: refresh every open dashboard.
	h.broker.NotifySource("all")
}
