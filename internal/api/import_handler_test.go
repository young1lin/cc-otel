package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/dbmerge"
)

func TestImportInspectStreamsFileAndReturns202(t *testing.T) {
	h, mux := importHTTPHandler(t, fakeImportEngine{})
	body, contentType := multipartUpload(t, "file", "source.db", []byte("SQLite payload"))
	response := serveImport(t, mux, http.MethodPost, "/api/import/inspect", body, contentType)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var accepted map[string]any
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted["job_id"] == "" || accepted["state"] != string(importInspecting) {
		t.Fatalf("response = %v", accepted)
	}
	ready := waitImportState(t, h.imports, importReady)
	if ready.File.Name != "source.db" || ready.File.SizeBytes != int64(len("SQLite payload")) || ready.File.SHA256 == "" {
		t.Fatalf("file status = %+v", ready.File)
	}
}

func TestImportInspectRejectsMissingFileAndWrongExtension(t *testing.T) {
	_, mux := importHTTPHandler(t, fakeImportEngine{})
	tests := []struct {
		name, field, filename, code string
		wantStatus                  int
	}{
		{name: "missing", field: "note", filename: "source.db", code: "missing_file", wantStatus: 400},
		{name: "extension", field: "file", filename: "source.txt", code: "invalid_file_extension", wantStatus: 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, contentType := multipartUpload(t, test.field, test.filename, []byte("data"))
			response := serveImport(t, mux, http.MethodPost, "/api/import/inspect", body, contentType)
			assertImportError(t, response, test.wantStatus, test.code)
		})
	}
}

func TestImportInspectRejectsFileOverConfiguredLimitAndDeletesPartial(t *testing.T) {
	h, mux := importHTTPHandler(t, fakeImportEngine{})
	h.imports.maxFileBytes = 32
	body, contentType := multipartUpload(t, "file", "large.db", bytes.Repeat([]byte("x"), 64))
	response := serveImport(t, mux, http.MethodPost, "/api/import/inspect", body, contentType)
	assertImportError(t, response, http.StatusRequestEntityTooLarge, "upload_too_large")
	matches, err := filepath.Glob(filepath.Join(h.imports.dir, "*.db.upload"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("partial uploads remain: %v", matches)
	}
}

func TestImportInspectReturns409WhenAnotherJobIsActive(t *testing.T) {
	started := make(chan struct{})
	h, mux := importHTTPHandler(t, fakeImportEngine{
		inspect: func(ctx context.Context, _ string, _ *sql.DB, _ dbmerge.ProgressFunc) (dbmerge.Inspection, error) {
			close(started)
			<-ctx.Done()
			return dbmerge.Inspection{}, ctx.Err()
		},
	})
	body, contentType := multipartUpload(t, "file", "one.db", []byte("one"))
	first := serveImport(t, mux, http.MethodPost, "/api/import/inspect", body, contentType)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", first.Code)
	}
	<-started
	body, contentType = multipartUpload(t, "file", "two.db", []byte("two"))
	second := serveImport(t, mux, http.MethodPost, "/api/import/inspect", body, contentType)
	assertImportError(t, second, http.StatusConflict, "import_busy")
	status, _ := h.imports.status("")
	if status.File.Name != "one.db" {
		t.Fatalf("active file changed: %+v", status.File)
	}
}

func TestImportStatusWithoutIDRestoresCurrentJob(t *testing.T) {
	h, mux := importHTTPHandler(t, fakeImportEngine{})
	jobID, _ := uploadForTest(t, h.imports)
	waitImportState(t, h.imports, importReady)
	response := serveImport(t, mux, http.MethodGet, "/api/import/status", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Job *ImportJobStatus `json:"job"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Job == nil || body.Job.JobID != jobID {
		t.Fatalf("body = %+v", body)
	}
}

func TestImportStatusUnknownExplicitIDReturns404(t *testing.T) {
	_, mux := importHTTPHandler(t, fakeImportEngine{})
	response := serveImport(t, mux, http.MethodGet, "/api/import/status?job_id=missing", nil, "")
	assertImportError(t, response, http.StatusNotFound, "job_not_found")
}

func TestImportStartRequiresReadyOrRetryableFailed(t *testing.T) {
	h, mux := importHTTPHandler(t, fakeImportEngine{})
	reservation, err := h.imports.reserveUpload("source.db")
	if err != nil {
		t.Fatal(err)
	}
	response := startImportRequest(t, mux, reservation.JobID)
	assertImportError(t, response, http.StatusConflict, "invalid_job_state")
	response = startImportRequest(t, mux, "missing")
	assertImportError(t, response, http.StatusNotFound, "job_not_found")
}

func TestImportDeleteReturns409WhileImporting(t *testing.T) {
	release := make(chan struct{})
	h, mux := importHTTPHandler(t, fakeImportEngine{
		merge: func(ctx context.Context, _ *sql.DB, _ string, _ dbmerge.Options) (dbmerge.Result, error) {
			select {
			case <-release:
				return dbmerge.Result{}, nil
			case <-ctx.Done():
				return dbmerge.Result{}, ctx.Err()
			}
		},
	})
	jobID, _ := uploadForTest(t, h.imports)
	waitImportState(t, h.imports, importReady)
	if response := startImportRequest(t, mux, jobID); response.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", response.Code, response.Body.String())
	}
	waitImportState(t, h.imports, importImporting)
	response := serveImport(t, mux, http.MethodDelete, "/api/import?job_id="+jobID, nil, "")
	assertImportError(t, response, http.StatusConflict, "import_in_progress")
	close(release)
}

func TestImportHandlersRejectWrongMethods(t *testing.T) {
	_, mux := importHTTPHandler(t, fakeImportEngine{})
	tests := []struct {
		path, method, allow string
	}{
		{"/api/import/inspect", http.MethodGet, http.MethodPost},
		{"/api/import/status", http.MethodPost, http.MethodGet},
		{"/api/import/start", http.MethodGet, http.MethodPost},
		{"/api/import", http.MethodPost, http.MethodDelete},
	}
	for _, test := range tests {
		response := serveImport(t, mux, test.method, test.path, nil, "")
		assertImportError(t, response, http.StatusMethodNotAllowed, "method_not_allowed")
		if response.Header().Get("Allow") != test.allow {
			t.Fatalf("%s Allow = %q, want %q", test.path, response.Header().Get("Allow"), test.allow)
		}
	}
}

func TestImportErrorsNeverExposeTemporaryOrDatabasePaths(t *testing.T) {
	h, _, cleanup := setupTestHandler(t)
	t.Cleanup(cleanup)
	secret := filepath.Join(t.TempDir(), "secret.db")
	h.importsStarted = true
	h.importInitErr = errors.New("cannot create " + secret)
	mux := http.NewServeMux()
	h.Register(mux)
	body, contentType := multipartUpload(t, "file", "source.db", []byte("data"))
	response := serveImport(t, mux, http.MethodPost, "/api/import/inspect", body, contentType)
	assertImportError(t, response, http.StatusInternalServerError, "storage_error")
	if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), h.cfg.DBPath) {
		t.Fatalf("response exposed a path: %s", response.Body.String())
	}
}

func importHTTPHandler(t *testing.T, engine importEngine) (*Handler, *http.ServeMux) {
	t.Helper()
	h, repo, cleanup := setupTestHandler(t)
	manager, err := newImportManager(context.Background(), repo.DB(), h.cfg.DBPath, h.broker, engine)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	h.importsStarted = true
	h.imports = manager
	t.Cleanup(func() {
		h.Close()
		repo.Close()
		cleanup()
	})
	mux := http.NewServeMux()
	h.Register(mux)
	return h, mux
}

func multipartUpload(t *testing.T, field, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func serveImport(t *testing.T, mux http.Handler, method, path string, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body.Bytes())
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func startImportRequest(t *testing.T, mux http.Handler, jobID string) *httptest.ResponseRecorder {
	t.Helper()
	body := bytes.NewBufferString(`{"job_id":"` + jobID + `"}`)
	return serveImport(t, mux, http.MethodPost, "/api/import/start", body, "application/json")
}

func assertImportError(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, wantStatus, response.Body.String())
	}
	var body apiErrorBody
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != wantCode {
		t.Fatalf("error code = %q, want %q; body=%s", body.Error.Code, wantCode, response.Body.String())
	}
}

func TestImportStatusEmptyReturnsNull(t *testing.T) {
	_, mux := importHTTPHandler(t, fakeImportEngine{})
	response := serveImport(t, mux, http.MethodGet, "/api/import/status", nil, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"job":null`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestImportDeleteReadyRemovesFile(t *testing.T) {
	h, mux := importHTTPHandler(t, fakeImportEngine{})
	jobID, path := uploadForTest(t, h.imports)
	waitImportState(t, h.imports, importReady)
	response := serveImport(t, mux, http.MethodDelete, "/api/import?job_id="+jobID, nil, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("upload still exists: %s", path)
}
