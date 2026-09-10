// dsh_ingest backfills DeepSeek Harness (dsh) usage rows into cc-otel's
// api_requests directly from dsh's local session logs (~/.dsh/sessions),
// without any dsh-side changes.
//
// Why: dsh 0.1.5 removed the FULL telemetry mode — the OTLP exporter now
// uploads only when the user explicitly submits feedback, so ordinary
// conversation never reaches cc-otel. The session logs remain the complete
// durable record (every assistant/message carries the step's usage), so this
// tool ingests them locally instead.
//
// What it reads: session.v3.jsonl.zstd — the current-format generation that
// every session resumed under dsh 0.1.5 has. It contains the full migrated
// history plus new events, so one file per session is the whole truth. Old
// v0-only sessions (never resumed since the upgrade) are skipped on purpose:
// the FULL-mode era exporter already stored those rows.
//
// Format notes (verified against dsh 0.1.5-alpha.2):
//   - multi-frame zstd; frame 1 holds only the header line, but a plain
//     stream reader over the whole file yields the concatenated JSONL.
//   - "seq" equals the event count (the contiguity contract); usage-bearing
//     assistant/message events are the only ones that become rows.
//   - v3 renumbers seqs relative to the v0 generation, so both this tool and
//     the receiver fold the format generation into request_id
//     (dsh-v3-<session>-<v3seq> vs the legacy v0-era dsh-<session>-<v0seq>);
//     without the generation segment the unique index silently swallows
//     same-session collisions. For every ingested session this tool REPLACES
//     the session's old rows first — both key shapes (-keep-existing
//     disables that), keeping counts exact and re-runs idempotent.
//
// Cost is left at 0: run the Pricing Table's ↻ 重算历史 (or
// tools/recompute_cost --apply) afterwards, same as any other backfill.
//
// Usage:
//
//	go run ./tools/dsh_ingest -db <path> [-sessions ~/.dsh/sessions]
//	    [-dry-run] [-keep-existing] [-v]
package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/klauspost/compress/zstd"

	appdb "github.com/young1lin/cc-otel/internal/db"
)

// dshUsage mirrors the usage object on a v3 assistant/message event.
type dshUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

// dshEvent is the subset of one session-log line this tool reads. Bodies carry
// whole tool outputs and file contents; only the fields below are decoded.
type dshEvent struct {
	Seq  int64  `json:"seq"`
	Time int64  `json:"time"`
	Type string `json:"type"`
	Data struct {
		Usage   *dshUsage `json:"usage"`
		Message struct {
			Source struct {
				Model string `json:"model"`
			} `json:"source"`
		} `json:"message"`
	} `json:"data"`
}

// sessionFormatVersion is the ledger format generation of the
// session.v3.jsonl.zstd files this tool reads. request_id construction must
// stay in lockstep with the receiver's dshRequestID (internal/receiver/dsh.go),
// which reads the same value from the session.format_version record attribute —
// rows this tool writes and rows the feedback-triggered exporter writes must
// collapse onto the same index.
const sessionFormatVersion = "3"

// usageRow maps one usage-bearing assistant/message event onto an
// api_requests row.
func usageRow(sessionID string, ev *dshEvent) *appdb.APIRequest {
	return &appdb.APIRequest{
		Timestamp:           time.UnixMilli(ev.Time),
		SessionID:           sessionID,
		Model:               ev.Data.Message.Source.Model,
		ActualModel:         ev.Data.Message.Source.Model,
		InputTokens:         ev.Data.Usage.InputTokens,
		OutputTokens:        ev.Data.Usage.OutputTokens,
		CacheReadTokens:     ev.Data.Usage.CacheReadTokens,
		CacheCreationTokens: ev.Data.Usage.CacheWriteTokens,
		EventName:           "api_request",
		EventSequence:       ev.Seq,
		ServiceName:         "deepseek-harness",
		RequestID:           fmt.Sprintf("dsh-v%s-%s-%d", sessionFormatVersion, sessionID, ev.Seq),
	}
}

// deleteSessionRows removes every api_requests row keyed to one DSH session,
// covering both request_id shapes that can exist for it: the legacy
// versionless rows stored by the FULL-era exporter ("dsh-<sid>-…") and the
// generation-versioned rows this tool and the receiver write
// ("dsh-v<ver>-<sid>-…"). The prefixes sort into disjoint ranges, so the
// replace step must clear both — deleting only one shape would double-count
// the session alongside the other generation's rows.
func deleteSessionRows(ctx context.Context, execer *sql.DB, sessionID, formatVersion string) (int64, error) {
	var total int64
	for _, bounds := range [][2]string{
		{"dsh-" + sessionID + "-", "dsh-" + sessionID + "."},
		{"dsh-v" + formatVersion + "-" + sessionID + "-", "dsh-v" + formatVersion + "-" + sessionID + "."},
	} {
		res, err := execer.ExecContext(ctx,
			"DELETE FROM api_requests WHERE request_id >= ? AND request_id < ?",
			bounds[0], bounds[1])
		if err != nil {
			return total, fmt.Errorf("delete %s…: %w", bounds[0], err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// collectRows streams one decompressed session log and returns its usage
// rows. Lines without "assistant/message" are skipped before JSON decoding —
// the interesting lines are a small fraction and bodies can be megabytes.
func collectRows(sessionID string, r io.Reader) ([]*appdb.APIRequest, error) {
	var rows []*appdb.APIRequest
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, []byte(`"assistant/message"`)) {
			var ev dshEvent
			if json.Unmarshal(line, &ev) == nil && ev.Type == "assistant/message" && ev.Data.Usage != nil {
				rows = append(rows, usageRow(sessionID, &ev))
			}
		}
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return rows, err
		}
	}
}

func main() {
	dbPath := flag.String("db", "", "target cc-otel database (required)")
	sessionsDir := flag.String("sessions", filepath.Join(os.Getenv("USERPROFILE"), ".dsh", "sessions"), "dsh sessions root")
	dryRun := flag.Bool("dry-run", false, "parse and report without writing")
	keepExisting := flag.Bool("keep-existing", false, "do not delete the session's pre-existing dsh rows first (v0-era and prior-run keys)")
	verbose := flag.Bool("v", false, "per-session detail")
	flag.Parse()
	if *dbPath == "" {
		log.Fatal("--db is required")
	}

	files, err := filepath.Glob(filepath.Join(*sessionsDir, "*", "*", "session.v3.jsonl.zstd"))
	if err != nil {
		log.Fatalf("glob sessions: %v", err)
	}

	type result struct {
		sessionID string
		rows      []*appdb.APIRequest
	}
	var results []result
	var totalRows, todayRows int
	today := time.Now().Format("2006-01-02")

	for _, path := range files {
		sessionID := filepath.Base(filepath.Dir(path))
		f, err := os.Open(path)
		if err != nil {
			if *verbose {
				log.Printf("skip %s: %v", sessionID, err)
			}
			continue
		}
		zr, err := zstd.NewReader(f)
		if err != nil {
			f.Close()
			log.Printf("zstd %s: %v", sessionID, err)
			continue
		}
		rows, err := collectRows(sessionID, zr)
		zr.Close()
		f.Close()
		if err != nil {
			log.Printf("parse %s: %v (keeping %d rows parsed so far)", sessionID, err, len(rows))
		}
		if len(rows) == 0 {
			continue
		}
		nToday := 0
		for _, row := range rows {
			if row.Timestamp.Format("2006-01-02") == today {
				nToday++
			}
		}
		results = append(results, result{sessionID, rows})
		totalRows += len(rows)
		todayRows += nToday
		if *verbose {
			fmt.Printf("  %-50s %5d rows (%d today)\n", sessionID, len(rows), nToday)
		}
	}
	fmt.Printf("parsed %d v3 sessions: %d usage rows total, %d today\n", len(results), totalRows, todayRows)
	if *dryRun {
		fmt.Println("dry-run: nothing written")
		return
	}

	db, err := sql.Open("sqlite3", appdb.DSN(*dbPath))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	repo := appdb.NewRepository(db)
	ctx := context.Background()

	var inserted, replaced int
	for _, res := range results {
		if !*keepExisting {
			// Old rows for the same logical history, in either key shape:
			// replace, or the renumbered v3 rows would double-count alongside.
			n, err := deleteSessionRows(ctx, db, res.sessionID, sessionFormatVersion)
			if err != nil {
				log.Fatalf("delete old rows for %s: %v", res.sessionID, err)
			}
			if n > 0 {
				replaced += int(n)
				if *verbose {
					fmt.Printf("  %-50s replaced %d old-keyed rows\n", res.sessionID, n)
				}
			}
		}
		for _, row := range res.rows {
			ok, err := repo.InsertRequest(ctx, row)
			if err != nil {
				log.Fatalf("insert %s: %v", row.RequestID, err)
			}
			if ok {
				inserted++
			}
		}
	}
	if err := repo.RebuildDailyAggregates(ctx); err != nil {
		log.Fatalf("rebuild daily aggregates: %v", err)
	}
	fmt.Printf("written: %d rows inserted, %d old-keyed rows replaced (v0-era + prior v3 keys), aggregates rebuilt\n", inserted, replaced)
	fmt.Println("costs are 0 — run the Pricing Table's ↻ 重算历史 or tools/recompute_cost --apply")
}
