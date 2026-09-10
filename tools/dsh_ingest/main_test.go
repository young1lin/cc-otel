package main

import (
	"context"
	"strings"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
	appdb "github.com/young1lin/cc-otel/internal/db"
)

const usageLine = `{"type":"assistant/message","seq":17,"time":1788795586538,"data":{"turn":1,"step":1,"usage":{"inputTokens":5660,"outputTokens":408,"cacheReadTokens":9792},"message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi"}],"source":{"kind":"model","provider":"zai-coding-cn","model":"glm-5.3"}}}}`

const noUsageLine = `{"type":"assistant/message","seq":18,"time":1788795587000,"data":{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"no usage"}],"source":{"kind":"model","model":"glm-5.3"}}}}`

const chunkLine = `{"type":"assistant/chunk","seq":19,"time":1788795587100,"data":{"turn":1,"step":1,"chunk":{"type":"block-end","index":0}}}`

const noSeqLine = `{"type":"text-chunks","seq0":305077,"time0":1788969340617,"data":{"turn":1208,"step":34}}`

func TestCollectRows(t *testing.T) {
	rows, err := collectRows("session-x", strings.NewReader(usageLine+"\n"+noUsageLine+"\n"+chunkLine+"\n"+noSeqLine+"\n"))
	if err != nil {
		t.Fatalf("collectRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row (only usage-bearing assistant/message), got %d", len(rows))
	}
	r := rows[0]
	if r.RequestID != "dsh-v3-session-x-17" {
		t.Errorf("request id = %q, want dsh-v3-session-x-17", r.RequestID)
	}
	if r.Model != "glm-5.3" || r.InputTokens != 5660 || r.OutputTokens != 408 || r.CacheReadTokens != 9792 || r.CacheCreationTokens != 0 {
		t.Errorf("row fields wrong: %+v", r)
	}
	if r.ServiceName != "deepseek-harness" || r.EventSequence != 17 || r.EventName != "api_request" {
		t.Errorf("attribution wrong: %+v", r)
	}
	if r.Timestamp.UnixMilli() != 1788795586538 {
		t.Errorf("timestamp = %v, want 1788795586538ms", r.Timestamp.UnixMilli())
	}
}

func TestCollectRowsEmpty(t *testing.T) {
	rows, err := collectRows("session-x", strings.NewReader(""))
	if err != nil {
		t.Fatalf("collectRows empty: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want 0 rows, got %d", len(rows))
	}
}

// deleteSessionRows must clear BOTH key shapes for the session — the legacy
// versionless rows the FULL-era exporter stored and the generation-versioned
// rows this tool writes — or a re-run would double-count alongside whichever
// shape survived. Other sessions' rows must stay untouched.
func TestDeleteSessionRows_CoversBothKeyShapes(t *testing.T) {
	d, err := appdb.Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	insert := func(sessionID, requestID string) {
		t.Helper()
		ev := &dshEvent{Seq: 1, Time: 1788795586538}
		ev.Data.Usage = &dshUsage{InputTokens: 10, OutputTokens: 5}
		ev.Data.Message.Source.Model = "glm-5.3"
		row := usageRow(sessionID, ev)
		row.RequestID = requestID
		if _, err := appdb.NewRepository(d).InsertRequest(ctx, row); err != nil {
			t.Fatalf("insert %s: %v", requestID, err)
		}
	}
	insert("session-x", "dsh-session-x-7")    // v0-era key
	insert("session-x", "dsh-v3-session-x-7") // this tool's key
	insert("session-y", "dsh-v3-session-y-7") // another session

	n, err := deleteSessionRows(ctx, d, "session-x", sessionFormatVersion)
	if err != nil {
		t.Fatalf("deleteSessionRows: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d rows, want 2 (both key shapes)", n)
	}

	var left, other int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_requests WHERE session_id = 'session-x'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("session-x rows left = %d, want 0", left)
	}
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_requests WHERE session_id = 'session-y'`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if other != 1 {
		t.Errorf("session-y rows = %d, want 1 — the range scan leaked across sessions", other)
	}
}
