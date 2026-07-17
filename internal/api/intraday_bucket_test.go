package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
)

func newIntradayBucketHandler(t *testing.T) *Handler {
	t.Helper()
	d, err := db.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return NewHandler(db.NewRepository(d), NewBroker(), &config.Config{}, "")
}

// Both intraday paths must honour 5 and 10. The Codex path is reached by the
// same UI control whenever the Codex tab is active, so a whitelist that drifts
// between the two would silently coerce the user's choice back to 30.
func TestIntradayBucketEchoedNotCoerced(t *testing.T) {
	h := newIntradayBucketHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	cases := []struct {
		bucket string
		want   int
	}{
		{"5", 5}, {"10", 10}, {"15", 15}, {"30", 30}, {"60", 60},
		{"7", 30}, {"45", 30}, {"abc", 30},
	}

	for _, base := range []string{"/api/intraday", "/api/codex/intraday"} {
		for _, tc := range cases {
			url := base + "?from=2026-01-01&to=2026-01-01&bucket=" + tc.bucket
			req := httptest.NewRequest("GET", url, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != 200 {
				t.Fatalf("%s: expected 200, got %d body=%s", url, rec.Code, rec.Body.String())
			}
			var resp IntradayResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("%s: decode: %v", url, err)
			}
			if resp.BucketMinutes != tc.want {
				t.Errorf("%s: bucket_minutes = %d, want %d", url, resp.BucketMinutes, tc.want)
			}
		}
	}
}

func TestIntradaySevenDayCapStillReturns400(t *testing.T) {
	h := newIntradayBucketHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	for _, base := range []string{"/api/intraday", "/api/codex/intraday"} {
		url := base + "?from=2026-01-01&to=2026-01-30&bucket=30"
		req := httptest.NewRequest("GET", url, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("%s: expected 400 for a 30-day span, got %d", url, rec.Code)
		}
	}
}
