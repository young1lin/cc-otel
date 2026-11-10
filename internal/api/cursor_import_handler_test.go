package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const cursorFixtureCSV = "Date,Cloud Agent ID,Automation ID,Kind,Model,Max Mode,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens,Cost\n" +
	"\"2026-11-05T10:00:00.000Z\",\"\",\"\",\"Included\",\"grok-4.7-xhigh\",\"No\",\"3690\",\"2\",\"17572\",\"40\",\"21304\",\"Included\"\n" +
	"\"2026-11-05T11:00:00.000Z\",\"\",\"\",\"User API Key\",\"gpt-5.2\",\"No\",\"0\",\"100\",\"10\",\"20\",\"130\",\"0.25\"\n" +
	"\"2026-11-05T12:00:00.000Z\",\"\",\"\",\"Included\",\"gpt-5.2\",\"No\",\"0\",\"0\",\"0\",\"0\",\"0\",\"-\"\n" +
	"\"oops\",\"\",\"\",\"Included\",\"x\",\"No\",\"0\",\"1\",\"0\",\"0\",\"1\",\"Included\"\n"

func cursorUploadBody(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("files", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close multipart: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func cursorStatus(t *testing.T, mux *http.ServeMux) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/cursor/import", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET status: %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET status json: %v", err)
	}
	return out
}

func TestCursorImportLifecycle_IdempotentReupload(t *testing.T) {
	h, _ := newCodexAPITestHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	if st := cursorStatus(t, mux); st["running"] != false {
		t.Fatalf("initial status should be idle, got %v", st)
	}

	post := func() map[string]any {
		body, ctype := cursorUploadBody(t, "usage-events-2026-11-07.csv", cursorFixtureCSV)
		req := httptest.NewRequest("POST", "/api/cursor/import", body)
		req.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST start: %d body=%s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("POST json: %v", err)
		}
		return out
	}

	started := post()
	if started["running"] != true || started["total_rows"] != float64(2) {
		t.Fatalf("start snapshot: %v", started)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		st := cursorStatus(t, mux)
		if st["running"] == false {
			if st["error"] != "" && st["error"] != nil {
				t.Fatalf("terminal state carries error: %v", st["error"])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("import did not reach a terminal state in 30s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	final := cursorStatus(t, mux)
	if final["inserted_rows"] != float64(2) || final["skipped_zero_rows"] != float64(1) || final["invalid_rows"] != float64(1) {
		t.Fatalf("final counters: %v", final)
	}

	// Re-upload the SAME file: everything must collapse to duplicates.
	post()
	deadline = time.Now().Add(30 * time.Second)
	for {
		st := cursorStatus(t, mux)
		if st["running"] == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("re-import did not reach a terminal state in 30s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	again := cursorStatus(t, mux)
	if again["inserted_rows"] != float64(0) || again["duplicate_rows"] != float64(2) {
		t.Fatalf("re-import counters: %v", again)
	}

	h.Close() // joins the import goroutine; must not hang
}

// Dry-run is read-only and mirrors the real import's dedup counts: before the
// import everything counts new, after it everything counts duplicate — and
// the dry-run itself must not have written anything.
func TestCursorImportDryRunCountsAgainstPool(t *testing.T) {
	h, repo := newCodexAPITestHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	dryRun := func() map[string]any {
		body, ctype := cursorUploadBody(t, "usage-events-2026-11-07.csv", cursorFixtureCSV)
		req := httptest.NewRequest("POST", "/api/cursor/import/dry-run", body)
		req.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("dry-run: %d body=%s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("dry-run json: %v", err)
		}
		return out
	}

	before := dryRun()
	if before["total_new"] != float64(2) || before["total_duplicates"] != float64(0) {
		t.Fatalf("pre-import dry-run: %v", before)
	}
	var got int64
	_ = repo.DB().QueryRow("SELECT COUNT(*) FROM api_requests WHERE service_name='cursor'").Scan(&got)
	if got != 0 {
		t.Fatalf("dry-run wrote rows: %d", got)
	}
	if ms, ok := before["new_models"].([]any); !ok || len(ms) == 0 {
		t.Fatalf("pre-import dry-run new_models: %v", before["new_models"])
	}

	// Real import of the same fixture, then dry-run again.
	body, ctype := cursorUploadBody(t, "usage-events-2026-11-07.csv", cursorFixtureCSV)
	req := httptest.NewRequest("POST", "/api/cursor/import", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import start: %d body=%s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		st := cursorStatus(t, mux)
		if st["running"] == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("import did not finish in 30s")
		}
		time.Sleep(50 * time.Millisecond)
	}

	after := dryRun()
	if after["total_new"] != float64(0) || after["total_duplicates"] != float64(2) {
		t.Fatalf("post-import dry-run: %v", after)
	}
	if ms, ok := after["new_models"].([]any); !ok || len(ms) != 0 {
		t.Fatalf("post-import dry-run new_models: %v", after["new_models"])
	}
	h.Close()
}

func TestCursorImportRejectsNonCSVAndEmptyUploads(t *testing.T) {
	h, _ := newCodexAPITestHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	// Wrong extension.
	body, ctype := cursorUploadBody(t, "usage-events.db", "whatever")
	req := httptest.NewRequest("POST", "/api/cursor/import", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "csv") {
		t.Fatalf("non-csv upload: %d body=%s", rec.Code, rec.Body.String())
	}

	// Valid CSV that parses to zero importable rows (all zero-token).
	zeroOnly := "Date,Model,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens,Cost\n" +
		"\"2026-11-05T10:00:00.000Z\",\"m\",\"0\",\"0\",\"0\",\"0\",\"0\",\"Free\"\n"
	body, ctype = cursorUploadBody(t, "empty.csv", zeroOnly)
	req = httptest.NewRequest("POST", "/api/cursor/import", body)
	req.Header.Set("Content-Type", ctype)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no_usable_rows") {
		t.Fatalf("zero-row upload: %d body=%s", rec.Code, rec.Body.String())
	}
}
