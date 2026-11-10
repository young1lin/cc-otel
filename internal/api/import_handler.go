package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxImportFileBytes   = int64(2 * 1024 * 1024 * 1024)
	maxMultipartOverhead = int64(1024 * 1024)
)

type apiErrorBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	} `json:"error"`
}

func writeAPIError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var body apiErrorBody
	body.Error.Code = code
	body.Error.Message = message
	body.Error.Details = details
	_ = json.NewEncoder(w).Encode(body)
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", nil)
	return false
}

func (h *Handler) ImportInspect(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	// The inspection reads the live pool through a pooled connection while
	// the one-click migration writes it under BEGIN IMMEDIATE batches — the
	// exact mixed read/write pattern that corrupted an import_ledger page
	// during R5 offline testing. Gate the whole upload+inspect path against
	// a running migration, exactly like ImportStart (R1 review A2-2).
	h.writerBusMu.Lock()
	h.codexMigrate.mu.Lock()
	migrationRunning := h.codexMigrate.running
	h.codexMigrate.mu.Unlock()
	h.writerBusMu.Unlock()
	if migrationRunning {
		writeAPIError(w, http.StatusConflict, "migration_in_progress", "The Codex migration is running; upload after it finishes", nil)
		return
	}
	// writerBusMu is held across this whole gate, so adding the Cursor check
	// here keeps the one-writer invariant among all three background writers.
	h.cursorImport.mu.Lock()
	cursorRunning := h.cursorImport.running
	h.cursorImport.mu.Unlock()
	if cursorRunning {
		writeAPIError(w, http.StatusConflict, "import_in_progress", "A Cursor import is running; upload after it finishes", nil)
		return
	}
	manager, ok := h.importManager(w)
	if !ok {
		return
	}
	maxFile := manager.maxFileBytes
	// Bound the whole multipart read: a client that stops feeding the body
	// stalls io.Copy/NextPart forever — goroutine + file handle pinned, and
	// on Windows the pinned handle even blocks sweep's delete (R2 A3-F1).
	// 2GB at a pessimistic 1MB/s needs ~35 minutes; the sweep's 30-minute
	// uploadStuck expiry is the backstop below that (R2 A3-F4).
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetReadDeadline(time.Now().Add(35 * time.Minute))
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFile+maxMultipartOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_multipart", "Invalid multipart upload", nil)
		return
	}

	var reservation uploadReservation
	var fileStatus ImportFileStatus
	fileSeen := false
	abort := func() {
		if reservation.JobID != "" {
			_ = manager.abortUpload(reservation.JobID)
		}
	}
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			abort()
			var maxErr *http.MaxBytesError
			if errors.As(nextErr, &maxErr) {
				writeAPIError(w, http.StatusRequestEntityTooLarge, "upload_too_large", "Database file exceeds the upload limit", nil)
			} else {
				writeAPIError(w, http.StatusBadRequest, "invalid_multipart", "Invalid multipart upload", nil)
			}
			return
		}
		isFile := part.FileName() != ""
		if part.FormName() != "file" || !isFile {
			if fileSeen && isFile {
				part.Close()
				abort()
				writeAPIError(w, http.StatusBadRequest, "invalid_multipart", "Only one database file may be uploaded", nil)
				return
			}
			_, _ = io.Copy(io.Discard, part)
			part.Close()
			continue
		}
		if fileSeen {
			part.Close()
			abort()
			writeAPIError(w, http.StatusBadRequest, "invalid_multipart", "Only one database file may be uploaded", nil)
			return
		}
		fileSeen = true
		name := filepath.Base(part.FileName())
		if !strings.EqualFold(filepath.Ext(name), ".db") {
			part.Close()
			writeAPIError(w, http.StatusBadRequest, "invalid_file_extension", "Choose a .db database file", nil)
			return
		}
		reservation, err = manager.reserveUpload(name)
		if errors.Is(err, errImportBusy) {
			part.Close()
			writeAPIError(w, http.StatusConflict, "import_busy", "Another database import is already active", nil)
			return
		}
		if err != nil {
			part.Close()
			writeAPIError(w, http.StatusInternalServerError, "storage_error", "Database upload storage is unavailable", nil)
			return
		}

		file, createErr := os.OpenFile(reservation.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			part.Close()
			abort()
			writeAPIError(w, http.StatusInternalServerError, "storage_error", "Database upload storage is unavailable", nil)
			return
		}
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(part, maxFile+1))
		partCloseErr := part.Close()
		fileCloseErr := file.Close()
		if written > maxFile {
			abort()
			writeAPIError(w, http.StatusRequestEntityTooLarge, "upload_too_large", "Database file exceeds the upload limit", nil)
			return
		}
		if r.Context().Err() != nil {
			abort()
			return
		}
		if copyErr != nil || partCloseErr != nil || fileCloseErr != nil {
			abort()
			writeAPIError(w, http.StatusInternalServerError, "storage_error", "Database upload could not be saved", nil)
			return
		}
		fileStatus = ImportFileStatus{Name: name, SizeBytes: written, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	if !fileSeen {
		writeAPIError(w, http.StatusBadRequest, "missing_file", "A database file is required", nil)
		return
	}
	if err := manager.completeUpload(reservation.JobID, fileStatus.SizeBytes, fileStatus.SHA256); err != nil {
		abort()
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Database inspection could not be started", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"job_id": reservation.JobID,
		"state":  importInspecting,
	})
}

func (h *Handler) ImportStatus(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	manager, ok := h.importManager(w)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(r.URL.Query().Get("job_id"))
	status, found := manager.status(jobID)
	if !found && jobID != "" {
		writeAPIError(w, http.StatusNotFound, "job_not_found", "Database import job not found", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"job": status})
}

func (h *Handler) ImportStart(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	manager, ok := h.importManager(w)
	if !ok {
		return
	}
	var request struct {
		JobID string `json:"job_id"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "A valid job_id is required", nil)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "A valid job_id is required", nil)
		return
	}
	request.JobID = strings.TrimSpace(request.JobID)
	// Mutual exclusion with the codex migration: both drive the staged
	// import pipeline against the same live DB. writerBusMu makes this check
	// atomic vs the migration's start decision (R4 concurrency F2).
	h.writerBusMu.Lock()
	defer h.writerBusMu.Unlock()
	h.codexMigrate.mu.Lock()
	migrationRunning := h.codexMigrate.running
	h.codexMigrate.mu.Unlock()
	if migrationRunning {
		writeAPIError(w, http.StatusConflict, "migration_in_progress", "The Codex migration is running; start the database import after it finishes", nil)
		return
	}
	h.cursorImport.mu.Lock()
	cursorRunning := h.cursorImport.running
	h.cursorImport.mu.Unlock()
	if cursorRunning {
		writeAPIError(w, http.StatusConflict, "import_in_progress", "A Cursor import is running; start the database import after it finishes", nil)
		return
	}
	status, err := manager.start(request.JobID)
	switch {
	case errors.Is(err, errImportNotFound):
		writeAPIError(w, http.StatusNotFound, "job_not_found", "Database import job not found", nil)
		return
	case errors.Is(err, errInvalidJobState):
		writeAPIError(w, http.StatusConflict, "invalid_job_state", "Database import job is not ready to start", nil)
		return
	case err != nil:
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Database import could not be started", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"job": status})
}

func (h *Handler) ImportDelete(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodDelete) {
		return
	}
	manager, ok := h.importManager(w)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(r.URL.Query().Get("job_id"))
	if jobID == "" {
		// A missing job_id is a client bug, not a missing resource — 400,
		// distinguishable from a real unknown job (R1 review A4-8).
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Parameter 'job_id' is required", nil)
		return
	}
	err := manager.delete(jobID)
	switch {
	case errors.Is(err, errImportNotFound):
		writeAPIError(w, http.StatusNotFound, "job_not_found", "Database import job not found", nil)
		return
	case errors.Is(err, errImportInProgress):
		writeAPIError(w, http.StatusConflict, "import_in_progress", "Database import is currently running", nil)
		return
	case err != nil:
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Database import could not be removed", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) importManager(w http.ResponseWriter) (*importManager, bool) {
	if err := h.InitImports(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Database import storage is unavailable", nil)
		return nil, false
	}
	h.importsMu.Lock()
	manager := h.imports
	h.importsMu.Unlock()
	if manager == nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Database import storage is unavailable", nil)
		return nil, false
	}
	return manager, true
}
