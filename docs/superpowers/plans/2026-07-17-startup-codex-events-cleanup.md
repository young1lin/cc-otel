# Startup Legacy Codex Events Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Delete all compatibility-only `codex_events` rows in bounded background batches after every daemon start.

**Architecture:** Add one repository method that executes a single limited delete and returns `RowsAffected`. Keep the repeat-until-empty loop and English logging in `cmd/cc-otel/main.go`, and schedule it only after both OTLP and Web listeners have been launched so cleanup cannot delay service startup.

**Tech Stack:** Go 1.24, `database/sql`, SQLite, Go `testing`, Go AST parser.

## Global Constraints

- Run cleanup in one background goroutine on every daemon start.
- Delete at most 10,000 `codex_events` rows per SQLite statement.
- Stop on the first database error and retry only on the next application start.
- Do not run `VACUUM`, add configuration, use a retention cutoff, or touch other Codex tables.
- Write code, comments, tests, logs, documentation, and the final commit message in English.
- Rebuild and restart only `bin/cc-otel.exe` with `bin/cc-otel-dev.yaml` on ports 14317 and 18899; never touch production.

---

### Task 1: Add and deploy startup cleanup

**Files:**
- Create: `internal/db/codex_cleanup.go`
- Modify: `internal/db/codex_repository_test.go:12-20` (append focused cleanup tests after the test helper)
- Modify: `cmd/cc-otel/main.go:32,484-490,515` (add batch constant, schedule after listeners, add cleanup helpers)
- Modify: `cmd/cc-otel/main_test.go:3-30` (replace the obsolete prohibition with positive startup/loop tests)
- Verify: `bin/cc-otel.exe`, `bin/cc-otel-dev.yaml`, `bin/cc-otel-dev.db`

**Interfaces:**
- Consumes: `*db.Repository`, `context.Context`, and the existing `codex_events(id INTEGER PRIMARY KEY)` compatibility table.
- Produces: `func (r *Repository) CleanupLegacyCodexEventsBatch(ctx context.Context, limit int) (int64, error)`.
- Produces: `func cleanupLegacyCodexEvents(ctx context.Context, cleaner legacyCodexEventsCleaner) (int64, error)` and `func startLegacyCodexEventsCleanup(cleaner legacyCodexEventsCleaner)`.

- [ ] **Step 1: Write the failing repository tests**

Append tests that insert five rows, request a batch size of two, expect affected counts `2, 2, 1, 0`, and confirm the final row count is zero. Add a second assertion that a non-positive limit returns zero without deleting anything:

```go
func TestCleanupLegacyCodexEventsBatchDeletesBoundedBatches(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()
	if _, err := repo.db.ExecContext(ctx, `
		INSERT INTO codex_events (timestamp) VALUES (1), (2), (3), (4), (5)`); err != nil {
		t.Fatalf("insert legacy events: %v", err)
	}

	for i, want := range []int64{2, 2, 1, 0} {
		got, err := repo.CleanupLegacyCodexEventsBatch(ctx, 2)
		if err != nil {
			t.Fatalf("cleanup batch %d: %v", i+1, err)
		}
		if got != want {
			t.Fatalf("cleanup batch %d deleted %d rows, want %d", i+1, got, want)
		}
	}

	var remaining int64
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_events`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining events: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining legacy events = %d, want 0", remaining)
	}
}

func TestCleanupLegacyCodexEventsBatchIgnoresNonPositiveLimit(t *testing.T) {
	repo := newCodexTestRepo(t)
	ctx := context.Background()
	if _, err := repo.db.ExecContext(ctx, `INSERT INTO codex_events (timestamp) VALUES (1)`); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}

	deleted, err := repo.CleanupLegacyCodexEventsBatch(ctx, 0)
	if err != nil {
		t.Fatalf("cleanup with zero limit: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted %d rows with zero limit, want 0", deleted)
	}
}
```

- [ ] **Step 2: Replace the obsolete daemon prohibition with failing positive tests**

Replace the old AST assertion with the following positive startup and loop tests:

```go
type legacyCodexEventsCleanerStub struct {
	deleted []int64
	errs    []error
	calls   int
}

func (s *legacyCodexEventsCleanerStub) CleanupLegacyCodexEventsBatch(context.Context, int) (int64, error) {
	i := s.calls
	s.calls++
	if i < len(s.errs) && s.errs[i] != nil {
		return 0, s.errs[i]
	}
	return s.deleted[i], nil
}

func TestCmdServeSchedulesLegacyCodexEventsCleanup(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "cmdServe" {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "startLegacyCodexEventsCleanup" {
				found = true
			}
			return true
		})
		if !found {
			t.Fatal("cmdServe must schedule legacy Codex events cleanup")
		}
		return
	}
	t.Fatal("cmdServe function not found")
}

func TestCleanupLegacyCodexEventsRunsUntilEmpty(t *testing.T) {
	cleaner := &legacyCodexEventsCleanerStub{deleted: []int64{10_000, 3, 0}}
	total, err := cleanupLegacyCodexEvents(context.Background(), cleaner)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if total != 10_003 || cleaner.calls != 3 {
		t.Fatalf("cleanup returned total=%d calls=%d, want total=10003 calls=3", total, cleaner.calls)
	}
}

func TestCleanupLegacyCodexEventsStopsOnError(t *testing.T) {
	wantErr := errors.New("database busy")
	cleaner := &legacyCodexEventsCleanerStub{
		deleted: []int64{7, 0},
		errs:    []error{nil, wantErr},
	}
	total, err := cleanupLegacyCodexEvents(context.Background(), cleaner)
	if !errors.Is(err, wantErr) {
		t.Fatalf("cleanup error = %v, want %v", err, wantErr)
	}
	if total != 7 || cleaner.calls != 2 {
		t.Fatalf("cleanup returned total=%d calls=%d, want total=7 calls=2", total, cleaner.calls)
	}
}
```

- [ ] **Step 3: Run focused tests and verify RED**

Run:

```powershell
go test ./internal/db -run LegacyCodexEvents -count=1
go test ./cmd/cc-otel -run LegacyCodexEvents -count=1
```

Expected: both commands fail to compile because `CleanupLegacyCodexEventsBatch`, `cleanupLegacyCodexEvents`, and `startLegacyCodexEventsCleanup` do not exist yet.

- [ ] **Step 4: Implement one bounded repository delete**

Create `internal/db/codex_cleanup.go`:

```go
package db

import "context"

// CleanupLegacyCodexEventsBatch deletes at most limit compatibility events.
func (r *Repository) CleanupLegacyCodexEventsBatch(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM codex_events
		WHERE id IN (
			SELECT id FROM codex_events ORDER BY id LIMIT ?
		)`, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
```

- [ ] **Step 5: Implement and schedule the daemon-owned loop**

Add this implementation to `cmd/cc-otel/main.go`, and call `startLegacyCodexEventsCleanup(repo)` immediately after launching the Web listener goroutine at `cmd/cc-otel/main.go:484-490`:

```go
const legacyCodexEventsCleanupBatchSize = 10_000

type legacyCodexEventsCleaner interface {
	CleanupLegacyCodexEventsBatch(context.Context, int) (int64, error)
}

func cleanupLegacyCodexEvents(ctx context.Context, cleaner legacyCodexEventsCleaner) (int64, error) {
	var total int64
	for {
		deleted, err := cleaner.CleanupLegacyCodexEventsBatch(ctx, legacyCodexEventsCleanupBatchSize)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted == 0 {
			return total, nil
		}
	}
}

func startLegacyCodexEventsCleanup(cleaner legacyCodexEventsCleaner) {
	go func() {
		total, err := cleanupLegacyCodexEvents(context.Background(), cleaner)
		if err != nil {
			log.Printf("legacy Codex events cleanup failed after deleting %d rows: %v", total, err)
			return
		}
		if total > 0 {
			log.Printf("legacy Codex events cleanup complete: deleted %d rows", total)
		}
	}()
}
```

- [ ] **Step 6: Run focused tests and verify GREEN**

Run:

```powershell
gofmt -w internal/db/codex_cleanup.go internal/db/codex_repository_test.go cmd/cc-otel/main.go cmd/cc-otel/main_test.go
go test ./internal/db -run LegacyCodexEvents -count=1
go test ./cmd/cc-otel -run LegacyCodexEvents -count=1
```

Expected: both packages report `ok` and zero failures.

- [ ] **Step 7: Run the full verification matrix**

Run:

```powershell
go test ./... -count=1
go vet ./...
go test -race ./internal/db ./cmd/cc-otel -count=1
node --test internal/web/static/tests/*.test.mjs
```

Expected: every command exits 0 with no test failures or vet diagnostics.

- [ ] **Step 8: Rebuild and restart the development daemon**

Run:

```powershell
& .\bin\cc-otel.exe stop -config .\bin\cc-otel-dev.yaml
$version=(git describe --tags --always --dirty).Trim()
go build -ldflags "-s -w -X main.version=$version" -o .\bin\cc-otel.exe .\cmd\cc-otel\
& .\bin\cc-otel.exe start -config .\bin\cc-otel-dev.yaml
& .\bin\cc-otel.exe status -config .\bin\cc-otel-dev.yaml
```

Expected: build exits 0; status reports the development daemon running with OTLP port 14317 and Web port 18899. Confirm `GET http://localhost:18899/api/status` returns HTTP 200 and the development log contains an English cleanup result if rows existed.

- [ ] **Step 9: Squash into one small commit**

Soft-reset only the temporary design commit, stage the design, plan, code, and tests, review `git diff --cached`, then commit:

```powershell
git reset --soft 3bcd91e
git add -f docs/superpowers/specs/2026-07-17-startup-codex-events-cleanup-design.md docs/superpowers/plans/2026-07-17-startup-codex-events-cleanup.md
git add internal/db/codex_cleanup.go internal/db/codex_repository_test.go cmd/cc-otel/main.go cmd/cc-otel/main_test.go
git commit -m "chore(db): clean legacy Codex events on startup"
```

Expected: the worktree is clean, no push occurs, and `git log origin/main..HEAD` shows exactly one additional cleanup commit on top of the four pre-existing local commits.
