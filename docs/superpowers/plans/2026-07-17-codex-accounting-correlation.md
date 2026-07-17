# Codex Structured Accounting Correlation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist Codex cache-write tokens, direct TTFT, and full request-to-completion duration in structured rows and aggregates without restoring generic Codex event retention.

**Architecture:** The existing process-wide receiver tracker correlates each successful API/WebSocket request with its structured request row. Completion writes all accounting fields in one repository transaction, then removes only the matched in-memory entry; row-ID-first SQL prevents concurrent same-model requests from crossing. Frontend source-aware token math treats Codex input as input-side total and exposes Cache Create in every existing view.

**Tech Stack:** Go 1.x, OTLP protobuf logs, SQLite through database/sql, vanilla ESM JavaScript, Node 18+ test runner, embedded static assets, PowerShell, Make.

## Global Constraints

- Work from `D:\goProject\cc-otel` on branch `feat/online-database-import`; this plan targets code at commit `0c0714f`.
- The authoritative design is `docs/superpowers/specs/2026-07-17-codex-accounting-correlation-design.md`.
- Follow TDD for every behavior: add one focused failing test, run it and record the expected failure, implement the minimum change, rerun green, then commit.
- Do not add a schema migration, pending-correlation table, new background service, or durable generic Codex event store.
- Keep `codex_events` compatibility-only: live writes stay disabled and imports keep ignoring it.
- Keep current Claude Code accounting semantics unchanged.
- Codex `input_token_count` is input-side total. Uncached input is `max(input - cacheRead - cacheWrite, 0)`; never add cache categories back to Codex input.
- Map upstream `tool_token_count` to reported `total_tokens`; do not treat it as a tool-token price category.
- Cost arguments are exactly uncached input, output, cache read, and cache write.
- Positive completion `ttft_ms` and full completion duration are authoritative over WebSocket fallbacks.
- Tracker fallback window is 10 minutes; repository pending-row fallback is 5 minutes; abandoned tracker entries expire after 30 minutes.
- A structured database failure must retain tracker state. Remove it only after `UpdateCodexAPIRequestTokens` commits.
- Do not rewrite historical rows, recompute historical costs, alter pricing values, or deploy/restart production.
- Development verification uses OTLP `14317`, Web `18899`, and a database under `bin\`; never use production `4317/8899`.
- Frontend verification must rebuild `bin/cc-otel.exe`, restart the development instance, inspect API output, exercise the browser, and capture a screenshot.
- Preserve chart rules from `.claude/CLAUDE.md`: no `stack: 'total'`, no `trigger: 'axis'`, and no `emphasis: { focus: 'series' }`.
- Do not push. Each task makes only the local commit shown in that task.

## File and Responsibility Map

- Modify `internal/db/codex_types.go:38-54`: completion update payload.
- Modify `internal/db/repository.go:228-256`: Codex aggregate prepared statements.
- Modify `internal/db/codex_repository.go:98-177`: exact-row-first atomic completion write.
- Modify `internal/db/codex_repository.go:221-267`: exact-row-first WebSocket fallback timing helpers.
- Modify `internal/db/codex_repository_test.go:92-340`: repository correlation, aggregate, idempotency, and timing coverage.
- Modify `internal/receiver/codex_parser.go:53-154`: observed-time helpers and non-destructive tracker.
- Modify `internal/receiver/codex_parser.go:188-267`: successful request anchors and authoritative completion accounting.
- Modify `internal/receiver/codex_parser.go:330-390`: WebSocket timing compatibility.
- Modify `internal/receiver/codex_parser_test.go:16-283`: receiver field mapping, cost, duration, failure, tracker, and WebSocket coverage.
- Modify `internal/web/static/js/token-math.js:3-45`: Codex input/cache decomposition.
- Modify `internal/web/static/app.js:120-135`: source tab column visibility.
- Modify `internal/web/static/js/chart-main.js:11-55`: chart tooltip Cache Create row.
- Modify `internal/web/static/js/panel-daily.js:74-160,403-434`: pure row/tooltip renderers, Cache Create, and colspans.
- Modify `internal/web/static/js/panel-requests.js:103-139`: request Cache Create cell.
- Modify `internal/web/static/tests/token-math.test.mjs:1-34`: token decomposition tests.
- Modify `internal/web/static/tests/panel-requests.test.mjs:58-121`: request-row rendering test.
- Create `internal/web/static/tests/chart-main.test.mjs`: chart tooltip test.
- Create `internal/web/static/tests/panel-daily.test.mjs`: daily/intraday pure rendering tests.
- Modify `docs/otel-events.md:153-285`: final Codex telemetry contract.
- Temporarily create, run, and delete `bin/tmp/seed_codex_ui/main.go`: development-only visual fixture; never stage it.

---

### Task 1: Atomic completion payload, cache-write aggregate, and exact-row idempotency

**Files:**

- Modify: `internal/db/codex_types.go:38-54`
- Modify: `internal/db/repository.go:246-256`
- Modify: `internal/db/codex_repository.go:98-177`
- Test: `internal/db/codex_repository_test.go:92-167,211-340`

**Interfaces:**

- Consumes: existing `Repository.InsertCodexAPIRequest(context.Context, *CodexAPIRequest) (int64, error)`.
- Produces: `CodexTokenUpdate{RequestRowID, CacheCreationTokens, TTFTMs}`.
- Produces: unchanged `UpdateCodexAPIRequestTokens(context.Context, *CodexTokenUpdate) (bool, error)`; true means exact/fallback UPDATE or idempotent exact no-op, false means fallback INSERT.

- [ ] **Step 1: Add failing repository tests for cache-write, exact row selection, and idempotency**

In `internal/db/codex_repository_test.go:92-142`, extend the existing newest-pending test so the update carries:

~~~go
updated, err := repo.UpdateCodexAPIRequestTokens(ctx, &CodexTokenUpdate{
    SessionID:           "sess-A",
    Model:               "gpt-5.1",
    Timestamp:           t0.Add(3 * time.Second),
    InputTokens:         1000,
    OutputTokens:        200,
    CacheReadTokens:     50,
    CacheCreationTokens: 25,
    ReasoningTokens:     20,
    TotalTokens:         1200,
    CostUSD:             0.125,
    DurationMs:          2300,
    TTFTMs:              250,
})
~~~

After the existing newest-row assertion, scan and assert the full row:

~~~go
var input, output, cacheRead, cacheCreate, reasoning, total, duration, ttft int64
var costUnits int64
err = repo.db.QueryRowContext(ctx, `
    SELECT input_tokens, output_tokens, cache_read_tokens,
           cache_creation_tokens, reasoning_tokens, total_tokens,
           cost_usd, duration_ms, ttft_ms
    FROM codex_api_requests WHERE id = ?`, tokenisedID,
).Scan(&input, &output, &cacheRead, &cacheCreate, &reasoning, &total, &costUnits, &duration, &ttft)
if err != nil {
    t.Fatalf("read tokenised row: %v", err)
}
if input != 1000 || output != 200 || cacheRead != 50 || cacheCreate != 25 ||
    reasoning != 20 || total != 1200 || duration != 2300 || ttft != 250 {
    t.Fatalf("unexpected tokenised row: in=%d out=%d read=%d create=%d reasoning=%d total=%d duration=%d ttft=%d",
        input, output, cacheRead, cacheCreate, reasoning, total, duration, ttft)
}
~~~

Replace `readCodexAgg` at current lines 211-222 with this exact signature and query:

~~~go
func readCodexAgg(t *testing.T, repo *Repository, date, model string) (
    input, output, cacheRead, cacheCreate, reasoning, totalTokens, count int64,
    ok bool,
) {
    t.Helper()
    row := repo.db.QueryRow(`
        SELECT input_tokens, output_tokens, cache_read_tokens,
               cache_creation_tokens, reasoning_tokens, total_tokens, request_count
        FROM codex_daily_model_agg WHERE date = ? AND model = ?`, date, model)
    if err := row.Scan(
        &input, &output, &cacheRead, &cacheCreate, &reasoning, &totalTokens, &count,
    ); err != nil {
        return 0, 0, 0, 0, 0, 0, 0, false
    }
    return input, output, cacheRead, cacheCreate, reasoning, totalTokens, count, true
}
~~~

Replace the four call-site assignments at current lines 242, 269, 294, and 330
with these respective forms:

~~~go
in, out, cache, cacheCreate, reason, totalTokens, count, ok := readCodexAgg(
    t, repo, dateKey, "gpt-5.1",
)
in, out, cache, cacheCreate, reason, totalTokens, count, _ = readCodexAgg(
    t, repo, dateKey, "gpt-5.1",
)
in, out, _, _, _, _, count, ok := readCodexAgg(
    t, repo, dateKey, "gpt-5.1",
)
in, out, _, _, _, _, count, _ := readCodexAgg(
    t, repo, dateKey, "gpt-5.1",
)
~~~

In `TestCodexAgg_InsertAndUpdate_PopulatesAggExactlyOnce`, use `CacheCreationTokens: 25` and assert `cacheCreate == 25` while `count == 1`.

Add this test after the aggregate test:

~~~go
func TestUpdateCodexAPIRequestTokens_ExactRowIsIdempotent(t *testing.T) {
    repo := newCodexTestRepo(t)
    ctx := context.Background()
    day := time.Date(2026, 7, 17, 10, 0, 0, 0, time.Local)

    olderID, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
        Timestamp: day, SessionID: "same-session", Model: "gpt-5.1",
    })
    if err != nil {
        t.Fatalf("insert older: %v", err)
    }
    newerID, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
        Timestamp: day.Add(time.Second), SessionID: "same-session", Model: "gpt-5.1",
    })
    if err != nil {
        t.Fatalf("insert newer: %v", err)
    }

    upd := &CodexTokenUpdate{
        RequestRowID: olderID, SessionID: "same-session", Model: "gpt-5.1",
        Timestamp: day.Add(2 * time.Second),
        InputTokens: 100, OutputTokens: 10, CacheReadTokens: 40,
        CacheCreationTokens: 20, ReasoningTokens: 5, TotalTokens: 110,
        CostUSD: 0.01, DurationMs: 2300, TTFTMs: 250,
    }
    for attempt := 0; attempt < 2; attempt++ {
        updated, updateErr := repo.UpdateCodexAPIRequestTokens(ctx, upd)
        if updateErr != nil {
            t.Fatalf("attempt %d: %v", attempt, updateErr)
        }
        if !updated {
            t.Fatalf("attempt %d used fallback insert", attempt)
        }
    }

    var olderInput, olderCreate, newerInput int64
    if err := repo.db.QueryRowContext(ctx,
        `SELECT input_tokens, cache_creation_tokens FROM codex_api_requests WHERE id = ?`,
        olderID,
    ).Scan(&olderInput, &olderCreate); err != nil {
        t.Fatalf("read older: %v", err)
    }
    if err := repo.db.QueryRowContext(ctx,
        `SELECT input_tokens FROM codex_api_requests WHERE id = ?`, newerID,
    ).Scan(&newerInput); err != nil {
        t.Fatalf("read newer: %v", err)
    }
    if olderInput != 100 || olderCreate != 20 || newerInput != 0 {
        t.Fatalf("wrong exact-row result: older=(%d,%d) newer=%d",
            olderInput, olderCreate, newerInput)
    }

    var rows int64
    if err := repo.db.QueryRowContext(ctx,
        `SELECT COUNT(*) FROM codex_api_requests WHERE session_id = 'same-session'`,
    ).Scan(&rows); err != nil {
        t.Fatalf("count rows: %v", err)
    }
    if rows != 2 {
        t.Fatalf("duplicate fallback row inserted: rows=%d", rows)
    }

    _, _, _, cacheCreate, _, _, count, ok := readCodexAgg(
        t, repo, day.Format("2006-01-02"), "gpt-5.1",
    )
    if !ok || cacheCreate != 20 || count != 2 {
        t.Fatalf("aggregate applied more than once: create=%d count=%d ok=%v",
            cacheCreate, count, ok)
    }
}
~~~

In `TestCodexAgg_FallbackInsert_CountsOnce` at current lines 276-300, replace
the update payload and assertions with:

~~~go
if _, err := repo.UpdateCodexAPIRequestTokens(ctx, &CodexTokenUpdate{
    SessionID:           "sess-fallback",
    Model:               "gpt-5.1",
    Timestamp:           day,
    InputTokens:         500,
    OutputTokens:        100,
    CacheReadTokens:     40,
    CacheCreationTokens: 20,
    TTFTMs:              250,
}); err != nil {
    t.Fatalf("update fallback: %v", err)
}

in, out, cacheRead, cacheCreate, _, _, count, ok := readCodexAgg(
    t, repo, dateKey, "gpt-5.1",
)
if !ok || in != 500 || out != 100 || cacheRead != 40 ||
    cacheCreate != 20 || count != 1 {
    t.Fatalf("fallback aggregate: in=%d out=%d read=%d create=%d count=%d ok=%v",
        in, out, cacheRead, cacheCreate, count, ok)
}
var rowCreate, rowTTFT int64
if err := repo.db.QueryRowContext(ctx, `
    SELECT cache_creation_tokens, ttft_ms
    FROM codex_api_requests WHERE session_id = 'sess-fallback'`,
).Scan(&rowCreate, &rowTTFT); err != nil {
    t.Fatalf("read fallback request: %v", err)
}
if rowCreate != 20 || rowTTFT != 250 {
    t.Fatalf("fallback request lost completion fields: create=%d ttft=%d",
        rowCreate, rowTTFT)
}
~~~

- [ ] **Step 2: Run the repository tests and confirm the red state**

Run:

~~~powershell
go test ./internal/db -run 'Test(UpdateCodexAPIRequestTokens|CodexAgg)' -count=1
~~~

Expected: build fails because `CodexTokenUpdate` has no `CacheCreationTokens`, `TTFTMs`, or `RequestRowID`; after those fields exist but before SQL changes, cache creation/idempotency assertions fail.

- [ ] **Step 3: Extend the payload and token-delta aggregate statement**

Replace `CodexTokenUpdate` at `internal/db/codex_types.go:43-54` with:

~~~go
type CodexTokenUpdate struct {
    RequestRowID        int64
    SessionID           string
    Model               string
    Timestamp           time.Time
    InputTokens         int64
    OutputTokens        int64
    CacheReadTokens     int64
    CacheCreationTokens int64
    ReasoningTokens     int64
    TotalTokens         int64
    CostUSD             float64
    DurationMs          int64
    TTFTMs              int64
}
~~~

Replace `stmtUpsCodexAggToks` at `internal/db/repository.go:246-256` with:

~~~go
r.stmtUpsCodexAggToks, _ = db.Prepare(`
    INSERT INTO codex_daily_model_agg (date, model, input_tokens, output_tokens,
        cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, cost_usd, request_count)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
    ON CONFLICT(date, model) DO UPDATE SET
        input_tokens          = input_tokens + excluded.input_tokens,
        output_tokens         = output_tokens + excluded.output_tokens,
        cache_read_tokens     = cache_read_tokens + excluded.cache_read_tokens,
        cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
        reasoning_tokens      = reasoning_tokens + excluded.reasoning_tokens,
        total_tokens          = total_tokens + excluded.total_tokens,
        cost_usd              = cost_usd + excluded.cost_usd`)
~~~

- [ ] **Step 4: Replace the completion repository method with exact-row-first atomic logic**

Replace `UpdateCodexAPIRequestTokens` at `internal/db/codex_repository.go:98-177` with:

~~~go
func (r *Repository) UpdateCodexAPIRequestTokens(ctx context.Context, u *CodexTokenUpdate) (bool, error) {
    cutoff := u.Timestamp.Unix() - 300

    tx, err := r.db.BeginTx(ctx, nil)
    if err != nil {
        return false, fmt.Errorf("begin codex update tx: %w", err)
    }
    defer tx.Rollback()

    var rowID, rowTs int64
    if u.RequestRowID > 0 {
        var input, output, cacheRead, cacheCreate, reasoning, total int64
        err = tx.QueryRowContext(ctx, `
            SELECT id, timestamp, input_tokens, output_tokens, cache_read_tokens,
                   cache_creation_tokens, reasoning_tokens, total_tokens
            FROM codex_api_requests
            WHERE id = ? AND session_id = ? AND model = ?`,
            u.RequestRowID, u.SessionID, u.Model,
        ).Scan(&rowID, &rowTs, &input, &output, &cacheRead, &cacheCreate, &reasoning, &total)
        if err == nil && (input != 0 || output != 0 || cacheRead != 0 ||
            cacheCreate != 0 || reasoning != 0 || total != 0) {
            if commitErr := tx.Commit(); commitErr != nil {
                return false, fmt.Errorf("commit idempotent codex update: %w", commitErr)
            }
            return true, nil
        }
        if err != nil && err != sql.ErrNoRows {
            return false, fmt.Errorf("lookup exact codex row: %w", err)
        }
        if err == sql.ErrNoRows {
            rowID = 0
            rowTs = 0
        }
    }

    if rowID == 0 {
        err = tx.QueryRowContext(ctx, `
            SELECT id, timestamp FROM codex_api_requests
            WHERE session_id = ? AND model = ?
              AND input_tokens = 0 AND output_tokens = 0
              AND timestamp >= ?
            ORDER BY id DESC LIMIT 1`,
            u.SessionID, u.Model, cutoff,
        ).Scan(&rowID, &rowTs)
    }

    if err == sql.ErrNoRows {
        if commitErr := tx.Commit(); commitErr != nil {
            return false, fmt.Errorf("commit empty codex update tx: %w", commitErr)
        }
        _, insertErr := r.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
            Timestamp:           u.Timestamp,
            SessionID:           u.SessionID,
            Model:               u.Model,
            InputTokens:         u.InputTokens,
            OutputTokens:        u.OutputTokens,
            CacheReadTokens:     u.CacheReadTokens,
            CacheCreationTokens: u.CacheCreationTokens,
            ReasoningTokens:     u.ReasoningTokens,
            TotalTokens:         u.TotalTokens,
            CostUSD:             u.CostUSD,
            DurationMs:          u.DurationMs,
            TTFTMs:              u.TTFTMs,
            EventName:           "codex.sse_event:response.completed",
        })
        if insertErr != nil {
            return false, insertErr
        }
        return false, nil
    }
    if err != nil {
        return false, fmt.Errorf("lookup codex pending row: %w", err)
    }

    if _, err := tx.ExecContext(ctx, `
        UPDATE codex_api_requests
        SET input_tokens = ?, output_tokens = ?, cache_read_tokens = ?,
            cache_creation_tokens = ?, reasoning_tokens = ?, total_tokens = ?,
            cost_usd = ?,
            duration_ms = CASE WHEN ? > 0 THEN ? ELSE duration_ms END,
            ttft_ms = CASE WHEN ? > 0 THEN ? ELSE ttft_ms END
        WHERE id = ?`,
        u.InputTokens, u.OutputTokens, u.CacheReadTokens,
        u.CacheCreationTokens, u.ReasoningTokens, u.TotalTokens,
        costToInt64(u.CostUSD),
        u.DurationMs, u.DurationMs,
        u.TTFTMs, u.TTFTMs,
        rowID,
    ); err != nil {
        return false, fmt.Errorf("update codex tokens: %w", err)
    }

    dateKey := time.Unix(rowTs, 0).Local().Format("2006-01-02")
    if _, err := tx.StmtContext(ctx, r.stmtUpsCodexAggToks).ExecContext(ctx,
        dateKey, u.Model,
        u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens,
        u.ReasoningTokens, u.TotalTokens, costToInt64(u.CostUSD),
    ); err != nil {
        return false, fmt.Errorf("upsert codex agg tokens: %w", err)
    }

    return true, tx.Commit()
}
~~~

- [ ] **Step 5: Run the focused and full database suites**

Run:

~~~powershell
gofmt -w internal/db/codex_types.go internal/db/codex_repository.go internal/db/codex_repository_test.go internal/db/repository.go
go test ./internal/db -run 'Test(UpdateCodexAPIRequestTokens|CodexAgg)' -count=1
go test ./internal/db -count=1
~~~

Expected: all commands exit 0; the idempotency test reports one pass and aggregate cache creation is 20, not 40.

- [ ] **Step 6: Commit Task 1**

~~~powershell
git add internal/db/codex_types.go internal/db/repository.go internal/db/codex_repository.go internal/db/codex_repository_test.go
git commit -m "feat(codex): persist cache writes atomically"
~~~

### Task 2: Exact-row-first WebSocket fallback timing helpers

**Files:**

- Modify: `internal/db/codex_repository.go:221-267`
- Test: `internal/db/codex_repository_test.go` after the exact-row tests from Task 1
- Modify: `internal/receiver/codex_parser.go:354,364` for a compile-only zero-row-ID adaptation; Task 6 supplies tracked row IDs

**Interfaces:**

- Produces: `UpdateCodexRequestDuration(ctx context.Context, requestRowID int64, sessionID, model string, ts time.Time, durationMs int64) (bool, error)`.
- Produces: `UpdateCodexRequestTTFT(ctx context.Context, requestRowID int64, sessionID, model string, ts time.Time, ttftMs int64) error`.
- The duration bool is true for an exact identity handled or a fallback UPDATE; false is reserved for a genuine no-match that permits a duration-only fallback INSERT.

- [ ] **Step 1: Add a failing timing-helper exact-row test**

Add:

~~~go
func TestCodexTimingHelpers_TargetExactRow(t *testing.T) {
    repo := newCodexTestRepo(t)
    ctx := context.Background()
    now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.Local)

    olderID, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
        Timestamp: now, SessionID: "timing-session", Model: "gpt-5.1",
    })
    if err != nil {
        t.Fatalf("insert older: %v", err)
    }
    newerID, err := repo.InsertCodexAPIRequest(ctx, &CodexAPIRequest{
        Timestamp: now.Add(time.Second), SessionID: "timing-session", Model: "gpt-5.1",
    })
    if err != nil {
        t.Fatalf("insert newer: %v", err)
    }

    updated, err := repo.UpdateCodexRequestDuration(
        ctx, olderID, "timing-session", "gpt-5.1", now.Add(2*time.Second), 2300,
    )
    if err != nil || !updated {
        t.Fatalf("duration update: updated=%v err=%v", updated, err)
    }
    if err := repo.UpdateCodexRequestTTFT(
        ctx, olderID, "timing-session", "gpt-5.1", now.Add(2*time.Second), 250,
    ); err != nil {
        t.Fatalf("ttft update: %v", err)
    }

    var olderDuration, olderTTFT, newerDuration, newerTTFT int64
    if err := repo.db.QueryRowContext(ctx,
        `SELECT duration_ms, ttft_ms FROM codex_api_requests WHERE id = ?`, olderID,
    ).Scan(&olderDuration, &olderTTFT); err != nil {
        t.Fatalf("read older: %v", err)
    }
    if err := repo.db.QueryRowContext(ctx,
        `SELECT duration_ms, ttft_ms FROM codex_api_requests WHERE id = ?`, newerID,
    ).Scan(&newerDuration, &newerTTFT); err != nil {
        t.Fatalf("read newer: %v", err)
    }
    if olderDuration != 2300 || olderTTFT != 250 ||
        newerDuration != 0 || newerTTFT != 0 {
        t.Fatalf("wrong timing target: older=(%d,%d) newer=(%d,%d)",
            olderDuration, olderTTFT, newerDuration, newerTTFT)
    }

    updated, err = repo.UpdateCodexRequestDuration(
        ctx, olderID, "timing-session", "gpt-5.1", now.Add(3*time.Second), 9999,
    )
    if err != nil || !updated {
        t.Fatalf("positive exact duration should be handled: updated=%v err=%v", updated, err)
    }
    if err := repo.UpdateCodexRequestTTFT(
        ctx, olderID, "timing-session", "gpt-5.1", now.Add(3*time.Second), 9999,
    ); err != nil {
        t.Fatalf("positive exact ttft should be handled: %v", err)
    }
    if err := repo.db.QueryRowContext(ctx,
        `SELECT duration_ms, ttft_ms FROM codex_api_requests WHERE id = ?`, olderID,
    ).Scan(&olderDuration, &olderTTFT); err != nil {
        t.Fatalf("reread older: %v", err)
    }
    if olderDuration != 2300 || olderTTFT != 250 {
        t.Fatalf("fallback overwrote authoritative values: duration=%d ttft=%d",
            olderDuration, olderTTFT)
    }
}
~~~

- [ ] **Step 2: Run the timing test and confirm it fails to compile**

Run:

~~~powershell
go test ./internal/db -run TestCodexTimingHelpers_TargetExactRow -count=1
~~~

Expected: FAIL because `UpdateCodexRequestDuration` does not exist and `UpdateCodexRequestTTFT` does not accept a row ID.

- [ ] **Step 3: Replace both timing helpers**

Replace current `UpdateCodexRequestDurationBySession` at lines 221-246 with:

~~~go
func (r *Repository) UpdateCodexRequestDuration(
    ctx context.Context,
    requestRowID int64,
    sessionID, model string,
    ts time.Time,
    durationMs int64,
) (bool, error) {
    if sessionID == "" || model == "" || durationMs <= 0 {
        return false, nil
    }
    if requestRowID > 0 {
        res, err := r.db.ExecContext(ctx, `
            UPDATE codex_api_requests
            SET duration_ms = ?
            WHERE id = ? AND session_id = ? AND model = ? AND duration_ms = 0`,
            durationMs, requestRowID, sessionID, model,
        )
        if err != nil {
            return false, err
        }
        if n, _ := res.RowsAffected(); n > 0 {
            return true, nil
        }
        var exists int
        if err := r.db.QueryRowContext(ctx, `
            SELECT EXISTS(
                SELECT 1 FROM codex_api_requests
                WHERE id = ? AND session_id = ? AND model = ?
            )`, requestRowID, sessionID, model).Scan(&exists); err != nil {
            return false, err
        }
        if exists == 1 {
            return true, nil
        }
    }

    cutoff := ts.Unix() - 600
    res, err := r.db.ExecContext(ctx, `
        UPDATE codex_api_requests
        SET duration_ms = ?
        WHERE id = (
            SELECT id FROM codex_api_requests
            WHERE session_id = ? AND model = ?
              AND duration_ms = 0
              AND timestamp >= ?
            ORDER BY id DESC LIMIT 1
        )`,
        durationMs, sessionID, model, cutoff,
    )
    if err != nil {
        return false, err
    }
    n, _ := res.RowsAffected()
    return n > 0, nil
}
~~~

Replace current `UpdateCodexRequestTTFT` at lines 248-267 with:

~~~go
func (r *Repository) UpdateCodexRequestTTFT(
    ctx context.Context,
    requestRowID int64,
    sessionID, model string,
    ts time.Time,
    ttftMs int64,
) error {
    if sessionID == "" || model == "" || ttftMs <= 0 {
        return nil
    }
    if requestRowID > 0 {
        res, err := r.db.ExecContext(ctx, `
            UPDATE codex_api_requests
            SET ttft_ms = ?
            WHERE id = ? AND session_id = ? AND model = ?
              AND (ttft_ms = 0 OR ttft_ms IS NULL)`,
            ttftMs, requestRowID, sessionID, model,
        )
        if err != nil {
            return err
        }
        if n, _ := res.RowsAffected(); n > 0 {
            return nil
        }
        var exists int
        if err := r.db.QueryRowContext(ctx, `
            SELECT EXISTS(
                SELECT 1 FROM codex_api_requests
                WHERE id = ? AND session_id = ? AND model = ?
            )`, requestRowID, sessionID, model).Scan(&exists); err != nil {
            return err
        }
        if exists == 1 {
            return nil
        }
    }

    cutoff := ts.Unix() - 600
    _, err := r.db.ExecContext(ctx, `
        UPDATE codex_api_requests
        SET ttft_ms = ?
        WHERE id = (
            SELECT id FROM codex_api_requests
            WHERE session_id = ? AND model = ?
              AND (ttft_ms = 0 OR ttft_ms IS NULL)
              AND timestamp >= ?
            ORDER BY id DESC LIMIT 1
        )`,
        ttftMs, sessionID, model, cutoff,
    )
    return err
}
~~~

- [ ] **Step 4: Adapt existing receiver callers to the typed signatures**

At current `internal/receiver/codex_parser.go:354`, change the call to:

~~~go
_ = repo.UpdateCodexRequestTTFT(
    ctx, 0, info.sessionID, info.model, ts, ttftMs,
)
~~~

At current lines 364-365, rename the duration helper and pass zero as the
temporary row ID:

~~~go
if updated, err := repo.UpdateCodexRequestDuration(
    ctx, 0, info.sessionID, info.model, ts, durationMs,
); err == nil {
~~~

Zero preserves the pre-existing session/model fallback until Task 6 uses
`info.rowID`.

- [ ] **Step 5: Run timing, database, and receiver tests**

~~~powershell
gofmt -w internal/db/codex_repository.go internal/db/codex_repository_test.go internal/receiver/codex_parser.go
go test ./internal/db -run TestCodexTimingHelpers_TargetExactRow -count=1
go test ./internal/db -count=1
go test ./internal/receiver -count=1
~~~

Expected: all commands exit 0.

- [ ] **Step 6: Commit Task 2**

~~~powershell
git add internal/db/codex_repository.go internal/db/codex_repository_test.go internal/receiver/codex_parser.go
git commit -m "fix(codex): target exact rows for timing fallbacks"
~~~

### Task 3: Non-destructive tracker and observed-time reconstruction

**Files:**

- Modify: `internal/receiver/codex_parser.go:3-13,53-154`
- Test: `internal/receiver/codex_parser_test.go` before the dispatch tests at current line 34

**Interfaces:**

- Produces: `codexLogObservedUnixNanos(*logspb.LogRecord) int64`.
- Produces: `codexRequestStartNanos(observedNanos, reportedDurationMs int64) int64`.
- Produces: `recordRequest(spanID string, info spanInfo) string`.
- Produces: `lookupRequest(spanID, sessionID, model string, observedNanos int64, maxAge time.Duration) (string, spanInfo, bool)`.
- Produces: `removeRequest(key string)`.
- `spanInfo` fields are exactly `rowID int64`, `sessionID string`, `model string`, and `startNanos int64`.

- [ ] **Step 1: Add failing pure tracker tests**

Add these tests before `TestDispatchCodexLog_APIRequest`:

~~~go
func TestCodexLogObservedUnixNanosPrefersObservedTime(t *testing.T) {
    lr := &logspb.LogRecord{
        TimeUnixNano:         100,
        ObservedTimeUnixNano: 200,
    }
    if got := codexLogObservedUnixNanos(lr); got != 200 {
        t.Fatalf("observed nanos = %d, want 200", got)
    }
    if got := codexLogUnixNanos(lr); got != 100 {
        t.Fatalf("persisted timestamp helper changed: got %d, want 100", got)
    }
}

func TestCodexRequestStartNanosClampsInvalidDuration(t *testing.T) {
    observed := int64(300 * time.Millisecond)
    if got := codexRequestStartNanos(observed, 300); got != 0 {
        t.Fatalf("start = %d, want 0", got)
    }
    if got := codexRequestStartNanos(observed, 301); got != observed {
        t.Fatalf("underflow was not clamped: got %d want %d", got, observed)
    }
    if got := codexRequestStartNanos(observed, -1); got != observed {
        t.Fatalf("negative duration changed start: got %d want %d", got, observed)
    }
}

func TestCodexSpanTrackerLookupRemoveFallbackMergeAndExpiry(t *testing.T) {
    tracker := newCodexSpanTracker()
    base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).UnixNano()

    fallbackKey := tracker.recordRequest("", spanInfo{
        rowID: 7, sessionID: "fallback-session", model: "gpt-5.1", startNanos: base,
    })
    if !strings.HasPrefix(fallbackKey, "fallback:") {
        t.Fatalf("fallback key = %q", fallbackKey)
    }
    key, info, ok := tracker.lookupRequest(
        "", "fallback-session", "gpt-5.1", base+int64(time.Second), 10*time.Minute,
    )
    if !ok || key != fallbackKey || info.rowID != 7 {
        t.Fatalf("fallback lookup: key=%q info=%+v ok=%v", key, info, ok)
    }
    if key2, _, ok2 := tracker.lookupRequest(
        "", "fallback-session", "gpt-5.1", base+int64(time.Second), 10*time.Minute,
    ); !ok2 || key2 != fallbackKey {
        t.Fatal("lookup destructively removed tracker state")
    }

    spanKey := "AQIDBAUGBwg="
    tracker.recordRequest(spanKey, spanInfo{
        rowID: 99, sessionID: "merge-session", model: "gpt-5.1",
        startNanos: base + int64(500*time.Millisecond),
    })
    tracker.recordRequest(spanKey, spanInfo{
        sessionID: "merge-session", model: "gpt-5.1",
        startNanos: base + int64(600*time.Millisecond),
    })
    _, merged, ok := tracker.lookupRequest(
        spanKey, "merge-session", "gpt-5.1", base+int64(time.Second), 10*time.Minute,
    )
    if !ok || merged.rowID != 99 ||
        merged.startNanos != base+int64(500*time.Millisecond) {
        t.Fatalf("span merge lost row/start: %+v ok=%v", merged, ok)
    }

    tracker.removeRequest(fallbackKey)
    if _, _, ok := tracker.lookupRequest(
        "", "fallback-session", "gpt-5.1", base+int64(time.Second), 10*time.Minute,
    ); ok {
        t.Fatal("explicit removal left fallback entry")
    }
    if _, _, ok := tracker.lookupRequest(
        spanKey, "merge-session", "gpt-5.1", base+int64(time.Second), 10*time.Minute,
    ); !ok {
        t.Fatal("removing one key removed another entry")
    }

    tracker.recordRequest("old-span", spanInfo{
        sessionID: "old", model: "gpt-5.1", startNanos: base,
    })
    tracker.recordRequest("new-span", spanInfo{
        sessionID: "new", model: "gpt-5.1",
        startNanos: base + int64(31*time.Minute),
    })
    if _, _, ok := tracker.lookupRequest(
        "old-span", "old", "gpt-5.1", base+int64(31*time.Minute), 60*time.Minute,
    ); ok {
        t.Fatal("30-minute eviction did not run below 100 entries")
    }
}
~~~

Add `"strings"` to the test imports.

- [ ] **Step 2: Run the tracker tests and confirm the red state**

~~~powershell
go test ./internal/receiver -run 'TestCodex(LogObserved|RequestStart|SpanTracker)' -count=1
~~~

Expected: build fails because the two timing helpers and new tracker method signatures do not exist.

- [ ] **Step 3: Add the observed-time and safe-start helpers**

Add `"strconv"` to `internal/receiver/codex_parser.go:3-13`. Immediately after `codexLogUnixNanos` at current lines 61-71, add:

~~~go
func codexLogObservedUnixNanos(lr *logspb.LogRecord) int64 {
    if lr == nil {
        return 0
    }
    if lr.ObservedTimeUnixNano > 0 {
        return int64(lr.ObservedTimeUnixNano)
    }
    if lr.TimeUnixNano > 0 {
        return int64(lr.TimeUnixNano)
    }
    return 0
}

func codexRequestStartNanos(observedNanos, reportedDurationMs int64) int64 {
    if observedNanos <= 0 {
        return 0
    }
    if reportedDurationMs <= 0 {
        return observedNanos
    }
    if reportedDurationMs > observedNanos/int64(time.Millisecond) {
        return observedNanos
    }
    return observedNanos - reportedDurationMs*int64(time.Millisecond)
}
~~~

- [ ] **Step 4: Replace the tracker types and methods**

Replace `codexSpanTracker`, `spanInfo`, and all methods at current lines 74-154 with:

~~~go
type codexSpanTracker struct {
    mu           sync.Mutex
    spans        map[string]spanInfo
    nextFallback uint64
}

type spanInfo struct {
    rowID      int64
    sessionID  string
    model      string
    startNanos int64
}

func newCodexSpanTracker() *codexSpanTracker {
    return &codexSpanTracker{spans: make(map[string]spanInfo)}
}

func (t *codexSpanTracker) recordRequest(spanID string, info spanInfo) string {
    t.mu.Lock()
    defer t.mu.Unlock()

    cutoff := info.startNanos - int64(30*time.Minute)
    if cutoff > 0 {
        for key, existing := range t.spans {
            if existing.startNanos > 0 && existing.startNanos < cutoff {
                delete(t.spans, key)
            }
        }
    }

    key := spanID
    if key == "" {
        t.nextFallback++
        key = "fallback:" + strconv.FormatUint(t.nextFallback, 10)
    }
    if existing, ok := t.spans[key]; ok {
        if info.rowID == 0 {
            info.rowID = existing.rowID
        }
        if info.sessionID == "" {
            info.sessionID = existing.sessionID
        }
        if info.model == "" {
            info.model = existing.model
        }
        if info.startNanos <= 0 ||
            (existing.startNanos > 0 && existing.startNanos < info.startNanos) {
            info.startNanos = existing.startNanos
        }
    }
    t.spans[key] = info
    return key
}

func (t *codexSpanTracker) lookupRequest(
    spanID, sessionID, model string,
    observedNanos int64,
    maxAge time.Duration,
) (string, spanInfo, bool) {
    if observedNanos <= 0 {
        return "", spanInfo{}, false
    }
    cutoff := observedNanos - int64(maxAge)
    eligible := func(info spanInfo) bool {
        if info.startNanos <= 0 || info.startNanos > observedNanos ||
            info.startNanos < cutoff {
            return false
        }
        if sessionID != "" && info.sessionID != sessionID {
            return false
        }
        if model != "" && info.model != model {
            return false
        }
        return true
    }

    t.mu.Lock()
    defer t.mu.Unlock()
    if spanID != "" {
        if info, ok := t.spans[spanID]; ok && eligible(info) {
            return spanID, info, true
        }
    }

    var bestKey string
    var best spanInfo
    for key, info := range t.spans {
        if !eligible(info) {
            continue
        }
        if bestKey == "" || info.startNanos > best.startNanos {
            bestKey = key
            best = info
        }
    }
    if bestKey == "" {
        return "", spanInfo{}, false
    }
    return bestKey, best, true
}

func (t *codexSpanTracker) removeRequest(key string) {
    if key == "" {
        return
    }
    t.mu.Lock()
    delete(t.spans, key)
    t.mu.Unlock()
}

// Temporary adapters keep untouched callers compiling until Tasks 5 and 6
// migrate them. Task 6 deletes all three.
func (t *codexSpanTracker) peekRequest(spanID string) (spanInfo, bool) {
    t.mu.Lock()
    info, ok := t.spans[spanID]
    t.mu.Unlock()
    return info, ok
}

func (t *codexSpanTracker) popRequest(spanID string) (spanInfo, bool) {
    t.mu.Lock()
    info, ok := t.spans[spanID]
    if ok {
        delete(t.spans, spanID)
    }
    t.mu.Unlock()
    return info, ok
}

func (t *codexSpanTracker) popLatestRequest(
    sessionID, model string,
    observedNanos int64,
    maxAge time.Duration,
) (spanInfo, bool) {
    key, info, ok := t.lookupRequest(
        "", sessionID, model, observedNanos, maxAge,
    )
    if ok {
        t.removeRequest(key)
    }
    return info, ok
}
~~~

At the current `codex.websocket_request` caller around line 337, make this
mechanical compile-only adaptation; Task 6 replaces the whole branch:

~~~go
tracker.recordRequest(spanID, spanInfo{
    sessionID:  attrs["conversation.id"],
    model:      attrs["model"],
    startNanos: obsNano,
})
~~~

The three temporary adapters preserve current behavior only long enough for
the package to compile. Task 5 removes the completion pop callers; Task 6
removes the WebSocket peek/pop callers and deletes all three adapters.

- [ ] **Step 5: Run only the pure tracker tests**

~~~powershell
gofmt -w internal/receiver/codex_parser.go internal/receiver/codex_parser_test.go
go test ./internal/receiver -run 'TestCodex(LogObserved|RequestStart|SpanTracker)' -count=1
~~~

Expected: the selected tests pass and the receiver package compiles because
the temporary adapters and mechanical WebSocket call update cover old callers.

- [ ] **Step 6: Commit Task 3**

~~~powershell
git add internal/receiver/codex_parser.go internal/receiver/codex_parser_test.go
git commit -m "refactor(codex): make request tracking non-destructive"
~~~

### Task 4: Successful API request anchors and failed-request duration

**Files:**

- Modify: `internal/receiver/codex_parser.go:188-213` at the Task 3-shifted `case "codex.api_request"`
- Test: `internal/receiver/codex_parser_test.go:34-61`

**Interfaces:**

- Consumes: Task 3 `codexLogObservedUnixNanos`, `codexRequestStartNanos`, `recordRequest`, and `spanInfo`.
- Consumes: existing `InsertCodexAPIRequest`, including its returned row ID.
- Produces: one pending tracker entry for every successfully inserted, potentially streaming API request, including events with empty span IDs.

- [ ] **Step 1: Replace the basic API test with successful-anchor and failed-request tests**

Replace current `TestDispatchCodexLog_APIRequest` at lines 34-61 with:

~~~go
func TestDispatchCodexLog_APIRequestAnchorsSuccessfulStream(t *testing.T) {
    repo := newCodexReceiverRepo(t)
    ctx := context.Background()
    tracker := newCodexSpanTracker()
    base := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
    observed := base.Add(300 * time.Millisecond)
    res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
        attr("service.name", "codex-cli"),
    }}

    ok := dispatchCodexLog(ctx, repo, &logspb.LogRecord{
        TimeUnixNano:         uint64(observed.UnixNano()),
        ObservedTimeUnixNano: uint64(observed.UnixNano()),
        Attributes: []*commontpb.KeyValue{
            attr("event.name", "codex.api_request"),
            attr("conversation.id", "anchor-session"),
            attr("model", "gpt-5.1"),
            attrInt("duration_ms", 300),
        },
    }, res, nil, tracker, nil)
    if !ok {
        t.Fatal("status-omitted successful request was not inserted")
    }

    var rowID, duration int64
    if err := repo.DB().QueryRowContext(ctx,
        `SELECT id, duration_ms FROM codex_api_requests WHERE session_id = 'anchor-session'`,
    ).Scan(&rowID, &duration); err != nil {
        t.Fatalf("read request: %v", err)
    }
    if duration != 0 {
        t.Fatalf("successful setup duration persisted as final duration: %d", duration)
    }
    key, info, found := tracker.lookupRequest(
        "", "anchor-session", "gpt-5.1",
        observed.Add(time.Second).UnixNano(), 10*time.Minute,
    )
    if !found || !strings.HasPrefix(key, "fallback:") ||
        info.rowID != rowID || info.startNanos != base.UnixNano() {
        t.Fatalf("wrong empty-span anchor: key=%q info=%+v found=%v", key, info, found)
    }
}

func TestDispatchCodexLog_APIRequestFailureKeepsReportedDuration(t *testing.T) {
    repo := newCodexReceiverRepo(t)
    ctx := context.Background()
    tracker := newCodexSpanTracker()
    when := time.Date(2026, 7, 17, 13, 5, 0, 0, time.UTC)

    ok := dispatchCodexLog(ctx, repo, &logspb.LogRecord{
        ObservedTimeUnixNano: uint64(when.UnixNano()),
        SpanId:               []byte{1, 2, 3, 4, 5, 6, 7, 8},
        Attributes: []*commontpb.KeyValue{
            attr("event.name", "codex.api_request"),
            attr("conversation.id", "failed-session"),
            attr("model", "gpt-5.1"),
            attrInt("duration_ms", 300),
            attrInt("http.response.status_code", 500),
            attr("error.message", "upstream failed"),
        },
    }, nil, nil, tracker, nil)
    if !ok {
        t.Fatal("failed request row was not inserted")
    }
    var duration int64
    if err := repo.DB().QueryRowContext(ctx,
        `SELECT duration_ms FROM codex_api_requests WHERE session_id = 'failed-session'`,
    ).Scan(&duration); err != nil {
        t.Fatalf("read failed request: %v", err)
    }
    if duration != 300 {
        t.Fatalf("failed request duration = %d, want 300", duration)
    }
    if _, _, found := tracker.lookupRequest(
        spanIDFromLog(&logspb.LogRecord{SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}}),
        "failed-session", "gpt-5.1", when.Add(time.Second).UnixNano(), 10*time.Minute,
    ); found {
        t.Fatal("failed request must not wait for completion")
    }
}
~~~

- [ ] **Step 2: Run the API tests and confirm the red state**

~~~powershell
go test ./internal/receiver -run 'TestDispatchCodexLog_APIRequest' -count=1
~~~

Expected: FAIL because the successful row still stores 300ms and no tracker entry is recorded.

- [ ] **Step 3: Replace the API request switch branch**

Replace the complete `case "codex.api_request"` branch through its `return false` with:

~~~go
case "codex.api_request":
    if attrs["model"] == "" {
        return false
    }
    reportedDurationMs := parseAttrInt(attrs, "duration_ms")
    status := parseAttrInt(attrs, "http.response.status_code")
    pendingStream := attrs["error.message"] == "" &&
        (status == 0 || (status >= 200 && status <= 299))
    storedDurationMs := reportedDurationMs
    if pendingStream {
        storedDurationMs = 0
    }
    req := &db.CodexAPIRequest{
        Timestamp:      ts,
        SessionID:      attrs["conversation.id"],
        ConversationID: attrs["conversation.id"],
        Model:          attrs["model"],
        DurationMs:     storedDurationMs,
        HTTPStatus:     status,
        Endpoint:       attrs["endpoint"],
        EventName:      "codex.api_request",
        TerminalType:   attrs["terminal.type"],
        ServiceName:    attrs["service.name"],
        ServiceVersion: attrs["service.version"],
        HostArch:       attrs["host.arch"],
        OSType:         attrs["os.type"],
        OSVersion:      attrs["os.version"],
        ErrorMessage:   attrs["error.message"],
    }
    rowID, err := repo.InsertCodexAPIRequest(ctx, req)
    if err != nil {
        return false
    }
    if pendingStream && tracker != nil {
        observedNanos := codexLogObservedUnixNanos(lr)
        if observedNanos == 0 {
            observedNanos = ts.UnixNano()
        }
        tracker.recordRequest(spanIDFromLog(lr), spanInfo{
            rowID:      rowID,
            sessionID:  req.SessionID,
            model:      req.Model,
            startNanos: codexRequestStartNanos(observedNanos, reportedDurationMs),
        })
    }
    notify()
    return true
~~~

- [ ] **Step 4: Run API and tracker tests**

~~~powershell
gofmt -w internal/receiver/codex_parser.go internal/receiver/codex_parser_test.go
go test ./internal/receiver -run 'Test(Codex|DispatchCodexLog_APIRequest)' -count=1
~~~

Expected: selected pure/API tests pass and the receiver package compiles; Task
2 supplied zero-row-ID timing calls and Task 3 supplied temporary tracker
adapters for the WebSocket behavior not migrated until Task 6.

- [ ] **Step 5: Commit Task 4**

~~~powershell
git add internal/receiver/codex_parser.go internal/receiver/codex_parser_test.go
git commit -m "feat(codex): anchor successful request streams"
~~~

### Task 5: Completion cache-write pricing, direct TTFT, full duration, and safe finalization

**Files:**

- Modify: `internal/receiver/codex_parser.go:215-267` at the Task 4-shifted `case "codex.sse_event"`
- Test: `internal/receiver/codex_parser_test.go:63-107` plus new failure test

**Interfaces:**

- Consumes: Task 1 `CodexTokenUpdate` and atomic repository method.
- Consumes: Task 3 non-destructive tracker lookup/removal.
- Produces: one completion transaction carrying tokens, cache write, cost, direct TTFT, and full duration.

- [ ] **Step 1: Add a recording pricer and replace the SSE completion test**

Add near the test helpers:

~~~go
type codexPricingCall struct {
    model       string
    input       int64
    output      int64
    cacheRead   int64
    cacheCreate int64
}

type recordingCodexPricer struct {
    call codexPricingCall
}

func (p *recordingCodexPricer) Calc(
    _ context.Context,
    model string,
    input, output, cacheRead, cacheCreate int64,
) float64 {
    p.call = codexPricingCall{
        model: model, input: input, output: output,
        cacheRead: cacheRead, cacheCreate: cacheCreate,
    }
    return 0.0123
}
~~~

Replace `TestDispatchCodexLog_SSECompletedUpdatesTokens` with:

~~~go
func TestDispatchCodexLog_SSECompletedPersistsAccountingAndFullDuration(t *testing.T) {
    repo := newCodexReceiverRepo(t)
    ctx := context.Background()
    tracker := newCodexSpanTracker()
    pricer := &recordingCodexPricer{}
    res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{
        attr("service.name", "codex-cli"),
    }}
    spanID := []byte{1, 3, 5, 7, 9, 11, 13, 15}
    base := time.Date(2026, 7, 17, 14, 0, 0, 0, time.Local)

    if ok := dispatchCodexLog(ctx, repo, &logspb.LogRecord{
        TimeUnixNano:         uint64(base.Add(300 * time.Millisecond).UnixNano()),
        ObservedTimeUnixNano: uint64(base.Add(300 * time.Millisecond).UnixNano()),
        SpanId:               spanID,
        Attributes: []*commontpb.KeyValue{
            attr("event.name", "codex.api_request"),
            attr("conversation.id", "completion-session"),
            attr("model", "gpt-5.1"),
            attrInt("duration_ms", 300),
            attrInt("http.response.status_code", 200),
        },
    }, res, nil, tracker, pricer); !ok {
        t.Fatal("request insert failed")
    }

    if ok := dispatchCodexLog(ctx, repo, &logspb.LogRecord{
        TimeUnixNano:         uint64(base.Add(2300 * time.Millisecond).UnixNano()),
        ObservedTimeUnixNano: uint64(base.Add(2300 * time.Millisecond).UnixNano()),
        SpanId:               spanID,
        Attributes: []*commontpb.KeyValue{
            attr("event.name", "codex.sse_event"),
            attr("event.kind", "response.completed"),
            attr("conversation.id", "completion-session"),
            attr("model", "gpt-5.1"),
            attrInt("input_token_count", 100),
            attrInt("cached_token_count", 40),
            attrInt("cache_write_token_count", 20),
            attrInt("output_token_count", 10),
            attrInt("reasoning_token_count", 5),
            attrInt("tool_token_count", 110),
            attrInt("ttft_ms", 250),
        },
    }, res, nil, tracker, pricer); !ok {
        t.Fatal("completion update failed")
    }

    var input, output, cacheRead, cacheCreate, reasoning, total, duration, ttft int64
    if err := repo.DB().QueryRowContext(ctx, `
        SELECT input_tokens, output_tokens, cache_read_tokens,
               cache_creation_tokens, reasoning_tokens, total_tokens,
               duration_ms, ttft_ms
        FROM codex_api_requests WHERE session_id = 'completion-session'`,
    ).Scan(&input, &output, &cacheRead, &cacheCreate, &reasoning, &total, &duration, &ttft); err != nil {
        t.Fatalf("read request: %v", err)
    }
    if input != 100 || output != 10 || cacheRead != 40 || cacheCreate != 20 ||
        reasoning != 5 || total != 110 || duration != 2300 || ttft != 250 {
        t.Fatalf("wrong completion row: in=%d out=%d read=%d create=%d reasoning=%d total=%d duration=%d ttft=%d",
            input, output, cacheRead, cacheCreate, reasoning, total, duration, ttft)
    }
    if pricer.call != (codexPricingCall{
        model: "gpt-5.1", input: 40, output: 10, cacheRead: 40, cacheCreate: 20,
    }) {
        t.Fatalf("wrong pricing arguments: %+v", pricer.call)
    }
    var aggregateCreate int64
    if err := repo.DB().QueryRowContext(ctx, `
        SELECT cache_creation_tokens FROM codex_daily_model_agg
        WHERE date = ? AND model = 'gpt-5.1'`,
        base.Format("2006-01-02"),
    ).Scan(&aggregateCreate); err != nil {
        t.Fatalf("read aggregate: %v", err)
    }
    if aggregateCreate != 20 {
        t.Fatalf("aggregate cache creation = %d, want 20", aggregateCreate)
    }
    if _, _, found := tracker.lookupRequest(
        spanIDFromLog(&logspb.LogRecord{SpanId: spanID}),
        "completion-session", "gpt-5.1",
        base.Add(2400*time.Millisecond).UnixNano(), 10*time.Minute,
    ); found {
        t.Fatal("successful completion did not remove tracker entry")
    }
}
~~~

Add a failure-retention test:

~~~go
func TestDispatchCodexLog_SSEDatabaseFailureRetainsTracker(t *testing.T) {
    repo := newCodexReceiverRepo(t)
    ctx := context.Background()
    tracker := newCodexSpanTracker()
    span := []byte{8, 6, 4, 2, 1, 3, 5, 7}
    base := time.Date(2026, 7, 17, 14, 10, 0, 0, time.UTC)

    dispatchCodexLog(ctx, repo, &logspb.LogRecord{
        ObservedTimeUnixNano: uint64(base.Add(100 * time.Millisecond).UnixNano()),
        SpanId:               span,
        Attributes: []*commontpb.KeyValue{
            attr("event.name", "codex.api_request"),
            attr("conversation.id", "db-failure-session"),
            attr("model", "gpt-5.1"),
            attrInt("duration_ms", 100),
            attrInt("http.response.status_code", 200),
        },
    }, nil, nil, tracker, nil)
    if err := repo.DB().Close(); err != nil {
        t.Fatalf("close db: %v", err)
    }

    ok := dispatchCodexLog(ctx, repo, &logspb.LogRecord{
        ObservedTimeUnixNano: uint64(base.Add(time.Second).UnixNano()),
        SpanId:               span,
        Attributes: []*commontpb.KeyValue{
            attr("event.name", "codex.sse_event"),
            attr("event.kind", "response.completed"),
            attr("conversation.id", "db-failure-session"),
            attr("model", "gpt-5.1"),
            attrInt("input_token_count", 10),
            attrInt("output_token_count", 1),
        },
    }, nil, nil, tracker, nil)
    if ok {
        t.Fatal("database failure reported success")
    }
    if _, _, found := tracker.lookupRequest(
        spanIDFromLog(&logspb.LogRecord{SpanId: span}),
        "db-failure-session", "gpt-5.1",
        base.Add(1100*time.Millisecond).UnixNano(), 10*time.Minute,
    ); !found {
        t.Fatal("database failure removed retryable tracker state")
    }
}
~~~

- [ ] **Step 2: Run the completion tests and confirm the red state**

~~~powershell
go test ./internal/receiver -run 'TestDispatchCodexLog_SSE' -count=1
~~~

Expected: FAIL because cache write and direct TTFT are not parsed, pricing sees cacheCreate 0, and old pop methods remove state before persistence.

- [ ] **Step 3: Replace the completion branch**

Replace the body of `if eventKindFromLog(lr, attrs) == "response.completed"` with:

~~~go
if eventKindFromLog(lr, attrs) == "response.completed" {
    upd := &db.CodexTokenUpdate{
        SessionID:           attrs["conversation.id"],
        Model:               attrs["model"],
        Timestamp:           ts,
        InputTokens:         parseAttrInt(attrs, "input_token_count"),
        OutputTokens:        parseAttrInt(attrs, "output_token_count"),
        CacheReadTokens:     parseAttrInt(attrs, "cached_token_count"),
        CacheCreationTokens: parseAttrInt(attrs, "cache_write_token_count"),
        ReasoningTokens:     parseAttrInt(attrs, "reasoning_token_count"),
        TotalTokens:         parseAttrInt(attrs, "tool_token_count"),
        TTFTMs:              parseAttrInt(attrs, "ttft_ms"),
    }
    if pricer != nil && !pricing.IsClaudeModel(upd.Model) {
        uncachedInput := upd.InputTokens - upd.CacheReadTokens - upd.CacheCreationTokens
        if uncachedInput < 0 {
            uncachedInput = 0
        }
        upd.CostUSD = pricer.Calc(
            ctx, upd.Model, uncachedInput, upd.OutputTokens,
            upd.CacheReadTokens, upd.CacheCreationTokens,
        )
    }

    trackerKey := ""
    if tracker != nil {
        observedNanos := codexLogObservedUnixNanos(lr)
        if observedNanos == 0 {
            observedNanos = ts.UnixNano()
        }
        key, info, ok := tracker.lookupRequest(
            spanIDFromLog(lr), upd.SessionID, upd.Model,
            observedNanos, 10*time.Minute,
        )
        if ok {
            trackerKey = key
            upd.RequestRowID = info.rowID
            durationMs := (observedNanos - info.startNanos) / int64(time.Millisecond)
            if durationMs > 0 {
                upd.DurationMs = durationMs
            }
        }
    }

    if _, err := repo.UpdateCodexAPIRequestTokens(ctx, upd); err != nil {
        return false
    }
    if tracker != nil {
        tracker.removeRequest(trackerKey)
    }
    notify()
    return true
}
return false
~~~

- [ ] **Step 4: Run completion, repository, and diagnostic-retention tests**

~~~powershell
gofmt -w internal/receiver/codex_parser.go internal/receiver/codex_parser_test.go
go test ./internal/receiver -run 'TestDispatchCodexLog_(SSE|Diagnostic)' -count=1
go test ./internal/db -run Codex -count=1
~~~

Expected: completion accounting and failure retention pass; `TestDispatchCodexLog_DiagnosticEventsAreNotPersisted` still reports zero `codex_events` rows.

- [ ] **Step 5: Commit Task 5**

~~~powershell
git add internal/receiver/codex_parser.go internal/receiver/codex_parser_test.go
git commit -m "feat(codex): finalize structured completion accounting"
~~~

### Task 6: WebSocket compatibility timing without premature tracker removal

**Files:**

- Modify: `internal/receiver/codex_parser.go:330-390` at the Task 5-shifted WebSocket branches
- Modify: `internal/receiver/codex_parser.go` tracker section to delete Task 3 adapters
- Test: `internal/receiver/codex_parser_test.go:109-244`

**Interfaces:**

- Consumes: Task 2 exact-row timing helpers.
- Consumes: Task 3 tracker lookup and request-start helpers.
- Produces: WebSocket fallback TTFT/duration that never consumes completion correlation state.

- [ ] **Step 1: Change existing WebSocket tests to assert retained state and authoritative SSE duration**

In `TestDispatchCodexLog_WebsocketDurationBeforeSSEUsesObservedTime`, immediately after dispatching the WebSocket `response.completed` event at current lines 127-136, add:

~~~go
trackerSpan := spanIDFromLog(&logspb.LogRecord{SpanId: spanID})
if _, _, found := tracker.lookupRequest(
    trackerSpan, "conv-duration", "gpt-5.5",
    start.Add(2450*time.Millisecond).UnixNano(), 10*time.Minute,
); !found {
    t.Fatal("websocket completion consumed state before structured SSE completion")
}
~~~

Keep the intermediate assertion that the duration-only candidate is 2408ms. After the later SSE completion at 2500ms, change the final expected duration from 2408 to 2500:

~~~go
if rows != 1 || input != 1234 || output != 567 || duration != 2500 {
    t.Fatalf("expected SSE to finalize the row at 2500ms; rows=%d input=%d output=%d duration=%d",
        rows, input, output, duration)
}
if _, _, found := tracker.lookupRequest(
    trackerSpan, "conv-duration", "gpt-5.5",
    start.Add(2600*time.Millisecond).UnixNano(), 10*time.Minute,
); found {
    t.Fatal("structured SSE completion did not remove state")
}
~~~

In `TestDispatchCodexLog_WebsocketEventNameCanComeFromBody`, retain the one-row/4200ms assertions and add:

~~~go
if _, _, found := tracker.lookupRequest(
    spanIDFromLog(&logspb.LogRecord{SpanId: spanID}),
    "conv-body-kind", "gpt-5.5",
    start.Add(4300*time.Millisecond).UnixNano(), 10*time.Minute,
); !found {
    t.Fatal("body-derived websocket completion consumed tracker state")
}
~~~

- [ ] **Step 2: Run WebSocket tests and confirm the red state**

~~~powershell
go test ./internal/receiver -run 'TestDispatchCodexLog_(Websocket|SSECompletedUsesTracked)' -count=1
~~~

Expected: FAIL because current `response.completed` uses `popRequest` and removes the tracker entry.

- [ ] **Step 3: Replace the WebSocket request branch**

Replace the full `case "codex.websocket_request"` branch with:

~~~go
case "codex.websocket_request":
    if tracker != nil {
        observedNanos := codexLogObservedUnixNanos(lr)
        if observedNanos == 0 {
            observedNanos = ts.UnixNano()
        }
        tracker.recordRequest(spanIDFromLog(lr), spanInfo{
            sessionID:  attrs["conversation.id"],
            model:      attrs["model"],
            startNanos: codexRequestStartNanos(
                observedNanos, parseAttrInt(attrs, "duration_ms"),
            ),
        })
    }
    return false
~~~

The Task 3 merge behavior preserves a non-zero `rowID` if API and WebSocket request logs share a span.

- [ ] **Step 4: Replace the WebSocket event branch**

Replace the full `case "codex.websocket_event"` branch with:

~~~go
case "codex.websocket_event":
    eventKind := eventKindFromLog(lr, attrs)
    observedNanos := codexLogObservedUnixNanos(lr)
    if observedNanos == 0 {
        observedNanos = ts.UnixNano()
    }

    if tracker != nil {
        _, info, found := tracker.lookupRequest(
            spanIDFromLog(lr), attrs["conversation.id"], attrs["model"],
            observedNanos, 10*time.Minute,
        )
        if found && eventKind == "response.created" {
            ttftMs := (observedNanos - info.startNanos) / int64(time.Millisecond)
            if ttftMs > 0 {
                _ = repo.UpdateCodexRequestTTFT(
                    ctx, info.rowID, info.sessionID, info.model, ts, ttftMs,
                )
            }
        }
        if found && eventKind == "response.completed" {
            durationMs := (observedNanos - info.startNanos) / int64(time.Millisecond)
            if durationMs < 0 {
                durationMs = 0
            }
            updated, err := repo.UpdateCodexRequestDuration(
                ctx, info.rowID, info.sessionID, info.model, ts, durationMs,
            )
            if err == nil {
                if updated {
                    notify()
                    return true
                }
                if durationMs > 0 && info.sessionID != "" && info.model != "" {
                    _, insertErr := repo.InsertCodexAPIRequest(ctx, &db.CodexAPIRequest{
                        Timestamp:      ts,
                        SessionID:      info.sessionID,
                        ConversationID: info.sessionID,
                        Model:          info.model,
                        DurationMs:     durationMs,
                        EventName:      "codex.websocket_event:response.completed",
                    })
                    if insertErr == nil {
                        notify()
                        return true
                    }
                }
            }
        }
    }
    return false
~~~

Delete the temporary `peekRequest`, `popRequest`, and `popLatestRequest` adapters introduced in Task 3. Confirm no references remain:

~~~powershell
$oldTrackerCalls = rg -n 'peekRequest|popRequest|popLatestRequest' internal/receiver
if ($LASTEXITCODE -eq 1) {
    Write-Output 'old_tracker_calls=clean'
} else {
    $oldTrackerCalls
    throw 'old destructive tracker calls remain'
}
~~~

Expected: output is `old_tracker_calls=clean`.

- [ ] **Step 5: Run the complete receiver and database suites**

~~~powershell
gofmt -w internal/receiver/codex_parser.go internal/receiver/codex_parser_test.go
go test ./internal/receiver -run Codex -count=1
go test ./internal/receiver ./internal/db -count=1
~~~

Expected: all tests pass. The final duration in the API+WebSocket+SSE sequence is the SSE boundary, and generic diagnostic retention remains zero.

- [ ] **Step 6: Commit Task 6**

~~~powershell
git add internal/receiver/codex_parser.go internal/receiver/codex_parser_test.go
git commit -m "fix(codex): retain correlation through websocket timing"
~~~

### Task 7: Codex token decomposition and persistent Cache Create columns

**Files:**

- Modify: `internal/web/static/js/token-math.js:3-30`
- Modify: `internal/web/static/app.js:120-135`
- Test: `internal/web/static/tests/token-math.test.mjs:1-34`

**Interfaces:**

- Produces: unchanged `tokenParts(row)` result shape with Codex `cacheCreate` and corrected `uncachedInput`.
- Preserves: `cacheHitParts(row)` Codex denominator `input_tokens`.
- Produces: source tabs that always leave existing `.col-cache-create` cells visible and Input group colspan at 3.

- [ ] **Step 1: Add the failing token decomposition test**

Change the import at `token-math.test.mjs:4` and add the test after current line 34:

~~~js
import { cacheHitParts, tokenParts } from '../js/token-math.js';

test('codex token parts subtract cache read and cache write once', () => {
    state.source = 'codex';
    const parts = tokenParts({
        input_tokens: 100,
        cache_read_tokens: 40,
        cache_creation_tokens: 20,
        output_tokens: 10,
        total_tokens: 110,
    });
    assert.deepEqual(parts, {
        inputSide: 100,
        uncachedInput: 40,
        cacheRead: 40,
        cacheCreate: 20,
        output: 10,
        total: 110,
    });
    state.source = 'claude';
});
~~~

- [ ] **Step 2: Run the token test and confirm the red state**

~~~powershell
node --test internal/web/static/tests/token-math.test.mjs
~~~

Expected: FAIL; current Codex result reports `uncachedInput: 60` and `cacheCreate: 0`.

- [ ] **Step 3: Correct the Codex branch**

Replace `internal/web/static/js/token-math.js:10-19` with:

~~~js
if (state.source === 'codex') {
    const uncachedInput = Math.max(input - cacheRead - cacheCreate, 0);
    return {
        inputSide: input,
        uncachedInput,
        cacheRead,
        cacheCreate,
        output,
        total: reportedTotal > 0 ? reportedTotal : input + output,
    };
}
~~~

Do not change `cacheHitParts` at lines 33-45.

- [ ] **Step 4: Stop source switching from hiding existing columns**

Replace `syncSourceTabsUI` at `internal/web/static/app.js:120-136` with:

~~~js
function syncSourceTabsUI() {
    document.querySelectorAll('.source-tab').forEach(btn => {
        btn.classList.toggle('is-active', btn.dataset.source === state.source);
    });
    document.querySelectorAll('.col-cache-create').forEach(el => {
        el.style.display = '';
    });
    document.querySelectorAll('.th-group').forEach(el => {
        if (el.textContent.trim() === 'Input') {
            el.setAttribute('colspan', '3');
        }
    });
}
~~~

Do not edit `internal/web/static/index.html:140-152,169-181,230-242`; all required headers already exist.

- [ ] **Step 5: Run the token test and inspect forbidden chart-pattern stability**

~~~powershell
node --test internal/web/static/tests/token-math.test.mjs
$forbidden = rg -n "stack: 'total'|trigger: 'axis'|emphasis: \{ focus: 'series' \}" internal/web/static/js/chart-main.js internal/web/static/js/panel-daily.js
if ($LASTEXITCODE -eq 1) {
    Write-Output 'changed_chart_patterns=clean'
} else {
    $forbidden
    throw 'forbidden chart pattern found in changed chart files'
}
~~~

Expected: Node exits 0 and PowerShell prints `changed_chart_patterns=clean`.

- [ ] **Step 6: Commit Task 7**

~~~powershell
git add internal/web/static/js/token-math.js internal/web/static/app.js internal/web/static/tests/token-math.test.mjs
git commit -m "feat(codex): expose cache-write token math"
~~~

### Task 8: Cache Create in chart, daily, intraday, and request rendering

**Files:**

- Modify: `internal/web/static/js/chart-main.js:11-55`
- Modify: `internal/web/static/js/panel-daily.js:74-160,403-434`
- Modify: `internal/web/static/js/panel-requests.js:103-139`
- Modify: `internal/web/static/tests/panel-requests.test.mjs:58-121`
- Create: `internal/web/static/tests/chart-main.test.mjs`
- Create: `internal/web/static/tests/panel-daily.test.mjs`

**Interfaces:**

- Produces: existing exported `buildBarTooltip(params, colors)` with an unconditional Cache Create row.
- Produces: exported pure `intradayLineTooltip(params, colors)`, `renderIntradayRow(row)`, and `renderDailyRow(row)`.
- Preserves: panel loaders and current API response shapes.

- [ ] **Step 1: Add failing chart and daily pure-renderer tests**

Create `internal/web/static/tests/chart-main.test.mjs`:

~~~js
import test from 'node:test';
import assert from 'node:assert/strict';
import { state } from '../js/state.js';
import { buildBarTooltip } from '../js/chart-main.js';

test('Codex chart tooltip renders Cache Create', () => {
    state.source = 'codex';
    const html = buildBarTooltip({
        color: '#666666',
        seriesName: 'gpt-5.1',
        data: {
            raw: {
                date: '2026-07-17',
                model: 'gpt-5.1',
                input_tokens: 100,
                cache_read_tokens: 40,
                cache_creation_tokens: 20,
                output_tokens: 10,
                total_tokens: 110,
                request_count: 1,
                cost_usd: 0.0123,
            },
        },
    }, {
        mutedText: '#777777',
        tooltipText: '#222222',
        axisLine: '#dddddd',
    });
    assert.match(html, /Cache Create/);
    assert.match(html, />20<\/td>/);
    state.source = 'claude';
});
~~~

Create `internal/web/static/tests/panel-daily.test.mjs`:

~~~js
import test from 'node:test';
import assert from 'node:assert/strict';
import { state } from '../js/state.js';
import {
    intradayLineTooltip,
    renderDailyRow,
    renderIntradayRow,
} from '../js/panel-daily.js';

const codexRow = {
    date: '2026-07-17',
    bucket_label: '07-17 14:00',
    model: 'gpt-5.1',
    input_tokens: 100,
    cache_read_tokens: 40,
    cache_creation_tokens: 20,
    output_tokens: 10,
    total_tokens: 110,
    request_count: 1,
    cost_usd: 0.0123,
};

test('Codex daily row renders uncached and Cache Create separately', () => {
    state.source = 'codex';
    const html = renderDailyRow(codexRow);
    assert.match(html, />40<\/td>/);
    assert.match(html, />20<\/td>/);
    assert.doesNotMatch(html, />100<\/td>/);
    state.source = 'claude';
});

test('Codex intraday row and tooltip render Cache Create', () => {
    state.source = 'codex';
    const rowHTML = renderIntradayRow(codexRow);
    assert.match(rowHTML, />20<\/td>/);
    const tooltipHTML = intradayLineTooltip({
        color: '#666666',
        seriesName: 'gpt-5.1',
        data: { raw: codexRow },
    }, {
        mutedText: '#777777',
        tooltipText: '#222222',
    });
    assert.match(tooltipHTML, /Cache Create/);
    assert.match(tooltipHTML, />20<\/td>/);
    state.source = 'claude';
});
~~~

In `panel-requests.test.mjs:87-98`, change `cache_creation_tokens` from 0 to 321, then after current model assertion add:

~~~js
assert.match(dom.requestBody.innerHTML, />321<\/td>/);
~~~

- [ ] **Step 2: Run the renderer tests and confirm the red state**

~~~powershell
node --test internal/web/static/tests/chart-main.test.mjs internal/web/static/tests/panel-daily.test.mjs internal/web/static/tests/panel-requests.test.mjs
~~~

Expected: FAIL because panel-daily exports do not exist, chart tooltip suppresses Cache Create for Codex, and request rows omit the 321 cell.

- [ ] **Step 3: Make the chart tooltip Cache Create row unconditional**

At `chart-main.js:23-27`, replace the conditional with:

~~~js
const cacheCreateRow =
    '<tr><td style="color:' + c.mutedText + ';' + sub +
    '">Cache Create</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">' +
    fmtNum(parts.cacheCreate) + '</td></tr>';
~~~

Keep the row insertion at current line 51 in its existing order: Uncached, Cache Read, Cache Create, Output.

- [ ] **Step 4: Export daily/intraday pure renderers and use them in loaders**

Change current `function intradayLineTooltip` at `panel-daily.js:74` to `export function intradayLineTooltip`. Replace its cache-create conditional at current lines 87-89 with:

~~~js
const cacheCreateRow =
    '<tr><td style="color:' + c.mutedText + ';' + sub +
    '">Cache Create</td><td style="font-family:var(--font-mono);text-align:right;' + sub + '">' +
    fmtNum(parts.cacheCreate) + '</td></tr>';
~~~

Add these pure helpers immediately before `loadIntraday`:

~~~js
export function renderIntradayRow(r) {
    const parts = tokenParts(r);
    return [
        '<tr>',
        '<td class="mono">' + escapeHtml(r.bucket_label || '') + '</td>',
        '<td><span class="badge">' + escapeHtml(r.model || 'Unknown') + '</span></td>',
        '<td class="mono">' + fmtNum(parts.total) + '</td>',
        '<td class="mono">' + fmtNum(parts.inputSide) + '</td>',
        '<td class="mono">' + fmtNum(parts.uncachedInput) + '</td>',
        '<td class="mono">' + fmtNum(parts.cacheRead) + '</td>',
        '<td class="mono">' + fmtNum(parts.cacheCreate) + '</td>',
        '<td class="mono">' + fmtNum(parts.output) + '</td>',
        '<td class="cost-val">$' + Number(r.cost_usd || 0).toFixed(4) + '</td>',
        '<td class="mono">' + Number(r.request_count || 0) + '</td>',
        '</tr>',
    ].join('');
}

export function renderDailyRow(r) {
    const parts = tokenParts(r);
    return [
        '<tr>',
        '<td class="mono">' + escapeHtml(r.date || '') + '</td>',
        '<td><span class="badge">' + escapeHtml(r.model || 'Unknown') + '</span></td>',
        '<td class="mono">' + Number(r.request_count || 0) + '</td>',
        '<td class="mono">' + fmtNum(parts.uncachedInput) + '</td>',
        '<td class="mono">' + fmtNum(parts.cacheRead) + '</td>',
        '<td class="mono">' + fmtNum(parts.cacheCreate) + '</td>',
        '<td class="mono">' + fmtNum(parts.output) + '</td>',
        '<td class="cost-val">$' + Number(r.cost_usd || 0).toFixed(4) + '</td>',
        '</tr>',
    ].join('');
}
~~~

Replace the intraday map block at current lines 145-160 with:

~~~js
tbody.innerHTML = sortedRows.map(renderIntradayRow).join('');
~~~

Replace the daily map block at current lines 422-432 with:

~~~js
tbody.innerHTML = rows.map(renderDailyRow).join('');
~~~

Change each intraday empty/error colspan at current lines 120, 138, and 405 from 9 to 10. Keep the daily empty colspan 8.

- [ ] **Step 5: Always render request Cache Create**

At `panel-requests.js:121-130`, delete `const hideCC = state.source === 'codex';` and replace the conditional cell expression with the unconditional existing cell:

~~~js
<td class="mono">${fmtNum(r.cache_creation_tokens)}</td>
~~~

This line remains inside the existing request-row template literal between Cache Read and Cost.

- [ ] **Step 6: Run all frontend tests**

~~~powershell
node --test internal/web/static/tests/*.test.mjs
~~~

Expected: all tests pass, including the new chart/daily tests and the request fixture value 321.

- [ ] **Step 7: Commit Task 8**

~~~powershell
git add internal/web/static/js/chart-main.js internal/web/static/js/panel-daily.js internal/web/static/js/panel-requests.js internal/web/static/tests/chart-main.test.mjs internal/web/static/tests/panel-daily.test.mjs internal/web/static/tests/panel-requests.test.mjs
git commit -m "feat(codex): show cache writes across dashboard views"
~~~

### Task 9: Telemetry documentation, full gates, bin build, and 18899 visual verification

**Files:**

- Modify: `docs/otel-events.md:153-285`
- Temporary create/delete: `bin/tmp/seed_codex_ui/main.go`
- Verify: `bin/cc-otel.exe`
- Verify: `bin/cc-otel-dev.yaml`
- Capture: `bin/verification/codex-cache-write.png`

**Interfaces:**

- Consumes: all Tasks 1-8.
- Produces: documented telemetry contract and evidence that API, embedded UI, and development binary agree.

- [ ] **Step 1: Update the Codex event-flow documentation**

At `docs/otel-events.md:159-171`, replace the flow with:

~~~text
codex.api_request
  -> insert request metadata with duration_ms=0 for a successful stream
  -> retain row id + reconstructed request start in the in-memory tracker

codex.sse_event kind=response.completed
  -> exact-row-first transaction for tokens, cache write, cost, TTFT, duration
  -> remove tracker state only after commit

codex.websocket_event kind=response.created/response.completed
  -> best-effort exact-row-first TTFT/duration fallback
  -> never consumes tracker state before structured SSE completion
~~~

Replace the `duration_ms` row at current line 184 with:

~~~markdown
| `duration_ms` | int | Successful stream setup/header duration used only to reconstruct request start; failed requests retain it as their final available duration |
~~~

Add these completion attribute rows after `cached_token_count` at current line 213:

~~~markdown
| `cache_write_token_count` | int | Cache-write input token subset; zero when omitted by older clients |
| `ttft_ms` | int | Codex-measured time to first output item; authoritative when positive |
~~~

Replace the token formula rows at current lines 219-227 with:

~~~markdown
| UI / DB concept | Formula |
|-----------------|---------|
| Total input | `input_token_count` |
| Cache read | `cached_token_count` |
| Cache write | `cache_write_token_count` |
| Uncached input | `max(input_token_count - cached_token_count - cache_write_token_count, 0)` |
| Output | `output_token_count` |
| Reported total | `tool_token_count` from Codex, stored as `total_tokens` |
| Fallback total | `input_token_count + output_token_count` |
| Cache hit rate | `cached_token_count / input_token_count` |
~~~

Immediately below, add the pricing contract:

~~~text
cost = uncached_input * input_price
     + output * output_price
     + cache_read * cache_read_price
     + cache_write * cache_create_price
~~~

Replace the backfill table at current lines 233-240 with:

~~~markdown
| Source event | Table | Behavior |
|--------------|-------|----------|
| `codex.sse_event` + `response.completed` with tracked row ID | `codex_api_requests` | Updates that exact `session_id + model + id` row |
| completion without a usable row ID | `codex_api_requests` | Updates newest zero-token row within five minutes for the same session/model |
| same completion | `codex_daily_model_agg` | Adds input, output, cache read, cache write, reasoning, total, and cost deltas without incrementing request count |
| no pending request row | `codex_api_requests` | Inserts one token-only fallback row and counts it once; duration stays zero without correlation |
| non-completion SSE events | not persisted | Parsed only when relevant; streaming deltas are discarded |
~~~

Replace the WebSocket timing description at current lines 242-270 with these exact boundaries:

~~~markdown
- Request start is `request_event_observed_time - request_event.duration_ms`.
- Full duration is `response.completed_observed_time - request_start`.
- Direct completion `ttft_ms` overwrites a positive WebSocket fallback.
- WebSocket timing targets the tracked row ID first, then session/model within ten minutes.
- Tracker lookup is non-destructive; successful structured completion removes it, database failure retains it, and abandoned state expires after 30 minutes.
- Tool execution duration stays in `codex_tool_result_events` and is never added to model-call duration.
~~~

Keep current lines 272-285 stating structured tool tables remain and `codex_events` is compatibility-only.

- [ ] **Step 2: Run focused race-sensitive and full repository gates**

Run each command separately and stop on the first failure:

~~~powershell
go test ./internal/receiver -run Codex -count=1
go test ./internal/db -run Codex -count=1
node --test internal/web/static/tests/token-math.test.mjs internal/web/static/tests/panel-requests.test.mjs internal/web/static/tests/panel-daily.test.mjs internal/web/static/tests/chart-main.test.mjs
go test -race ./internal/receiver ./internal/db
go test ./...
go vet ./...
node --test internal/web/static/tests/*.test.mjs
make build
~~~

Expected: every command exits 0. `Get-Item bin/cc-otel.exe` shows a newly written binary.

- [ ] **Step 3: Verify the development config before starting anything**

Run:

~~~powershell
Get-Content -Raw -LiteralPath bin/cc-otel-dev.yaml
~~~

The file must contain exactly these effective values:

~~~yaml
otel_port: 14317
web_port: 18899
db_path: ./bin/cc-otel-dev.db
model_mapping: {}
~~~

If the file is missing, create it with `apply_patch` using that content. If it names `4317`, `8899`, or a database outside `bin\`, stop and correct it before running the binary.

- [ ] **Step 4: Seed one development-only visual fixture while the dev daemon is stopped**

Run:

~~~powershell
bin/cc-otel.exe stop -config bin/cc-otel-dev.yaml
~~~

Create `bin/tmp/seed_codex_ui/main.go` with `apply_patch`:

~~~go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/young1lin/cc-otel/internal/config"
    "github.com/young1lin/cc-otel/internal/db"
)

func main() {
    database, err := db.Init(&config.Config{DBPath: "./bin/cc-otel-dev.db"})
    if err != nil {
        log.Fatal(err)
    }
    defer database.Close()
    repo := db.NewRepository(database)
    defer repo.Close()

    now := time.Now()
    _, err = repo.InsertCodexAPIRequest(context.Background(), &db.CodexAPIRequest{
        Timestamp:           now,
        SessionID:           "codex-ui-verification-" + now.Format("20060102-150405"),
        ConversationID:      "codex-ui-verification",
        Model:               "gpt-5.1-ui-verification",
        InputTokens:         100,
        OutputTokens:        10,
        CacheReadTokens:     40,
        CacheCreationTokens: 20,
        ReasoningTokens:     5,
        TotalTokens:         110,
        CostUSD:             0.0123,
        DurationMs:          2300,
        TTFTMs:              250,
        HTTPStatus:          200,
        EventName:           "codex.sse_event:response.completed",
    })
    if err != nil {
        log.Fatal(err)
    }
    var genericEvents int64
    if err := database.QueryRow(
        "SELECT COUNT(*) FROM codex_events",
    ).Scan(&genericEvents); err != nil {
        log.Fatal(err)
    }
    fmt.Printf("seeded model=%s codex_events=%d\n",
        "gpt-5.1-ui-verification", genericEvents)
}
~~~

Run:

~~~powershell
go run ./bin/tmp/seed_codex_ui
~~~

Expected: output includes `seeded model=gpt-5.1-ui-verification codex_events=0`.

- [ ] **Step 5: Start the development binary and verify APIs**

~~~powershell
bin/cc-otel.exe start -config bin/cc-otel-dev.yaml
Invoke-RestMethod 'http://localhost:18899/api/status' | ConvertTo-Json -Depth 6
Invoke-RestMethod 'http://localhost:18899/api/codex/requests?from=2026-07-17&to=2026-07-17&page=1&page_size=100' | ConvertTo-Json -Depth 8
~~~

Expected:

- Status reports a healthy database/API and OTLP listener `14317`.
- The requests response contains model `gpt-5.1-ui-verification`.
- That row contains `input_tokens=100`, `cache_read_tokens=40`, `cache_creation_tokens=20`, `output_tokens=10`, `total_tokens=110`, `ttft_ms=250`, and `duration_ms=2300`.

If the current local date is not 2026-07-17 when this plan executes, use the date printed by `Get-Date -Format yyyy-MM-dd` for both API parameters.

- [ ] **Step 6: Perform browser verification on port 18899 and capture evidence**

Open:

~~~text
http://localhost:18899/?source=codex&v=20260717-cache-write
~~~

Using browser automation:

1. Select Today or a custom range containing the fixture date.
2. Confirm the Codex source tab is active.
3. Hover the fixture bar in Token mode and confirm: Total 110; Input 100; Uncached 40; Cache Read 40; Cache Create 20; Output 10.
4. Open Daily and confirm Uncached is 40, not 100, and Cache Create is 20.
5. Open Intraday and confirm Cache Create 20 appears in the table and tooltip.
6. Open Requests and confirm Cache Create 20, TTFT 250ms, and Duration 2300ms.
7. Capture `bin/verification/codex-cache-write.png` with source tab, date range, and visible Cache Create values.

Before saving the screenshot, create its ignored output directory:

~~~powershell
New-Item -ItemType Directory -Force -Path bin/verification
~~~

Expected: no missing/shifted table cells, Input equals 40+40+20, Total equals 100+10, and the screenshot exists.

- [ ] **Step 7: Remove the temporary fixture source and inspect final Git state**

Delete `bin/tmp/seed_codex_ui/main.go` with `apply_patch`. Do not delete the development database or screenshot.

Run:

~~~powershell
git status --short
git diff --check
git log --oneline -10
~~~

Expected: only `docs/otel-events.md` is uncommitted; `bin\` artifacts remain ignored and no production path changed.

- [ ] **Step 8: Commit documentation and final evidence-free source state**

~~~powershell
git add docs/otel-events.md
git commit -m "docs(codex): document structured accounting semantics"
git status --short --branch
~~~

Expected: clean worktree on `feat/online-database-import`. Do not stage `bin/cc-otel.exe`, the development DB, or the screenshot.
