# Intraday Bar Chart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Intraday line chart with a per-(bucket, model) bar chart, add a 5/10/15/30/60-minute bucket dropdown, extend the span from one day to at most seven, and delete the Intraday detail table.

**Architecture:** Widen the intraday bucket whitelist on all four backend call sites to reuse `db.ValidRateBucketMinutes`. Extract the Token Rate dropdown factory into a shared ES module consumed by both panels. Add a pure gap-filling helper so the category axis is a true timeline. Rebuild the chart as bars mirroring `chart-main.js`, reusing its `buildBarTooltip`.

**Tech Stack:** Go 1.x + SQLite (modernc driver), vanilla ES modules (no build tool), ECharts via `window.echarts`, Node built-in test runner.

Spec: `docs/superpowers/specs/2026-07-17-intraday-bar-chart-design.md`

## Global Constraints

- Chart rules pinned in `.claude/CLAUDE.md` are absolute: `stack: 'total'` is forbidden; `trigger: 'axis'` is forbidden (must be `trigger: 'item'`); `emphasis: { focus: 'series' }` is forbidden. One bar per (time, model).
- Bar height is `inputSide + output_tokens`. Input means `input_tokens + cache_read_tokens + cache_creation_tokens`. Never redefine Input as `input_tokens` alone.
- Never `import` ECharts or Flatpickr — read them from `window.echarts` / `window.flatpickr`.
- All code comments in English.
- Dev instance only: ports **14317 / 18899**. Never touch production 4317 / 8899.
- Static assets are embedded via `go:embed`; any frontend edit needs `make build` + restart to appear.
- Bump `style.css?v=` and `app.js?v=` in `index.html` whenever either changes.
- **Each task commits its own work**; Task 7 then squashes the whole branch into a single commit carrying spec, plan, and implementation. This satisfies the user's "spec and implementation are one commit" rule at the level that matters — the final history — while keeping a per-task commit as a recovery anchor if the session is interrupted. Per-task commit messages are throwaway (`wip(task-N): ...`); only the squashed message ships.
- `docs/superpowers/` is listed in `.gitignore:39`, yet ten files under it are tracked — once tracked, the ignore rule stops applying. The new spec and plan are **not** tracked, so a plain `git add docs/superpowers/...` silently adds nothing. Use `git add -f` for those two paths, as the existing specs evidently were.
- The Intraday view honors `state.chartMetric` (Tokens / Cost / Requests) today. Preserve that. The top-bar metric buttons must keep working on Intraday.
- Work happens on branch `feat/intraday-bar-chart`, based on `da9158a`.

---

## File Structure

**Create:**
- `internal/web/static/js/bucket-dropdown.js` — shared bucket constants + dropdown factory.
- `internal/web/static/js/intraday-slots.js` — pure gap-filling helper.
- `internal/web/static/tests/bucket-dropdown.test.mjs`
- `internal/web/static/tests/intraday-slots.test.mjs`
- `internal/api/intraday_bucket_test.go`
- `internal/db/intraday_bucket_test.go`

**Modify:**
- `internal/db/repository.go` — `GetIntradayStatsByModel` validation + doc comment.
- `internal/api/handler.go` — `Handler.Intraday` validation + doc comment.
- `internal/db/codex_repository.go` — `GetCodexIntradayStatsByModel` validation.
- `internal/api/codex_handler.go` — `Handler.CodexIntraday` validation.
- `internal/web/static/js/chart-main.js` — export `makeTokenBarColor`; the main chart consumes it.
- `internal/web/static/js/panel-rate.js` — import shared dropdown instead of defining it.
- `internal/web/static/js/panel-daily.js` — bar chart; delete line tooltip, row renderer, constants.
- `internal/web/static/js/state.js` — add `intradayBucket`, `intradaySpan`.
- `internal/web/static/index.html` — add dropdown, remove table, bump versions.
- `internal/web/static/tests/panel-daily.test.mjs` — drop tests for deleted exports.

---

### Task 1: Widen the intraday bucket whitelist (backend, all four sites)

The frontend dropdown is useless until this lands: `bucket=5` is currently coerced to 30 with no error, so the new control would look broken. `/api/codex/intraday` must change too — `intraday` is in `CODEX_ROUTES` (`internal/web/static/js/api.js`), so the same UI control hits the Codex path whenever the Codex tab is active.

**Files:**
- Modify: `internal/db/repository.go` (`GetIntradayStatsByModel`, ~line 946-952)
- Modify: `internal/api/handler.go` (`Handler.Intraday`, ~line 913-955)
- Modify: `internal/db/codex_repository.go` (`GetCodexIntradayStatsByModel`, ~line 548-552)
- Modify: `internal/api/codex_handler.go` (`Handler.CodexIntraday`, ~line 97-131)
- Test: `internal/db/intraday_bucket_test.go` (create)
- Test: `internal/api/intraday_bucket_test.go` (create)

**Interfaces:**
- Consumes: `db.ValidRateBucketMinutes(n int) bool` — already exists at `internal/db/repository.go:1033`, returns true for 5, 10, 15, 30, 60.
- Produces: `/api/intraday` and `/api/codex/intraday` accept `bucket=5|10|15|30|60` and echo the value in `bucket_minutes`.

- [ ] **Step 1: Write the failing db test**

Create `internal/db/intraday_bucket_test.go`:

```go
package db

import (
	"context"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
)

func newIntradayBucketRepo(t *testing.T) *Repository {
	t.Helper()
	d, err := Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return NewRepository(d)
}

func TestGetIntradayStatsByModel_BucketWhitelist(t *testing.T) {
	repo := newIntradayBucketRepo(t)
	ctx := context.Background()

	for _, n := range []int{5, 10, 15, 30, 60} {
		if _, err := repo.GetIntradayStatsByModel(ctx, "2026-01-01", "2026-01-01", n, ""); err != nil {
			t.Errorf("bucket %d should be accepted, got error: %v", n, err)
		}
	}
	for _, n := range []int{0, 7, 45, -5} {
		if _, err := repo.GetIntradayStatsByModel(ctx, "2026-01-01", "2026-01-01", n, ""); err == nil {
			t.Errorf("bucket %d should be rejected, got nil error", n)
		}
	}
}

func TestGetCodexIntradayStatsByModel_BucketWhitelist(t *testing.T) {
	repo := newIntradayBucketRepo(t)
	ctx := context.Background()

	for _, n := range []int{5, 10, 15, 30, 60} {
		if _, err := repo.GetCodexIntradayStatsByModel(ctx, "2026-01-01", "2026-01-01", n, ""); err != nil {
			t.Errorf("codex bucket %d should be accepted, got error: %v", n, err)
		}
	}
	for _, n := range []int{0, 7, 45, -5} {
		if _, err := repo.GetCodexIntradayStatsByModel(ctx, "2026-01-01", "2026-01-01", n, ""); err == nil {
			t.Errorf("codex bucket %d should be rejected, got nil error", n)
		}
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/db/ -run IntradayStatsByModel_BucketWhitelist -v`
Expected: FAIL — buckets 5 and 10 are rejected with `bucket_minutes must be 15, 30, or 60`.

- [ ] **Step 3: Widen both repository whitelists**

In `internal/db/repository.go`, `GetIntradayStatsByModel`, replace:

```go
	if bucketMinutes != 15 && bucketMinutes != 30 && bucketMinutes != 60 {
		return nil, fmt.Errorf("bucket_minutes must be 15, 30, or 60 (got %d)", bucketMinutes)
	}
```

with:

```go
	if !ValidRateBucketMinutes(bucketMinutes) {
		return nil, fmt.Errorf("bucket_minutes must be 5, 10, 15, 30, or 60 (got %d)", bucketMinutes)
	}
```

Apply the identical replacement in `internal/db/codex_repository.go`, `GetCodexIntradayStatsByModel`.

Update the doc comment above `GetIntradayStatsByModel` — it says "using a fixed bucket size in minutes (15, 30, or 60)". Change to "(5, 10, 15, 30, or 60)".

Update the doc comment on `IntradayModelSummary` (`internal/db/repository.go:173-177`) — it says "for the new intraday line-chart view, supporting 15/30/60-minute buckets". Change to "for the intraday bar-chart view, supporting 5/10/15/30/60-minute buckets".

- [ ] **Step 4: Run the db test to confirm it passes**

Run: `go test ./internal/db/ -run IntradayStatsByModel_BucketWhitelist -v`
Expected: PASS (both tests).

- [ ] **Step 5: Write the failing api test**

Create `internal/api/intraday_bucket_test.go`:

```go
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
```

- [ ] **Step 6: Run it to confirm it fails**

Run: `go test ./internal/api/ -run Intraday -v`
Expected: `TestIntradayBucketEchoedNotCoerced` FAILs with `bucket_minutes = 30, want 5`.

- [ ] **Step 7: Widen both handler whitelists**

In `internal/api/handler.go`, `Handler.Intraday`, replace:

```go
		if err == nil && (n == 15 || n == 30 || n == 60) {
			bucket = n
		}
```

with:

```go
		if err == nil && db.ValidRateBucketMinutes(n) {
			bucket = n
		}
```

Apply the identical replacement in `internal/api/codex_handler.go`, `Handler.CodexIntraday`.

Update the `Handler.Intraday` doc comment: "bucketed at 15/30/60 minutes" becomes "bucketed at 5/10/15/30/60 minutes", "Designed for the new line-chart" becomes "Designed for the Intraday bar-chart", and the `bucket=15|30|60` param line becomes `bucket=5|10|15|30|60`.

- [ ] **Step 8: Run the full backend suites**

Run: `go test ./internal/api/ ./internal/db/ -v -run Intraday`
Expected: PASS.

Run: `go test ./...`
Expected: all packages ok.

---

### Task 2: Extract the shared bucket dropdown

**Files:**
- Create: `internal/web/static/js/bucket-dropdown.js`
- Create: `internal/web/static/tests/bucket-dropdown.test.mjs`
- Modify: `internal/web/static/js/panel-rate.js:25-37,46-82` (delete the moved code, import instead)

**Interfaces:**
- Produces:
  - `BUCKET_CHOICES: Set<number>` — `{5, 10, 15, 30, 60}`
  - `normalizeBucket(n: number) => number` — `n` when in `BUCKET_CHOICES`, else `30`
  - `recommendedBucketForSpan(spanDays: number) => number` — `5` when `spanDays <= 1`, else `30`
  - `makeBucketDropdown(wrap: Element|null, onPick?: (value: string) => void) => { setValue(value): void, setOpen(open: boolean): void } | null`
- Consumed by: Task 4 (`panel-daily.js`) and the rewired `panel-rate.js`.

- [ ] **Step 1: Write the failing test**

Create `internal/web/static/tests/bucket-dropdown.test.mjs`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import {
    BUCKET_CHOICES,
    normalizeBucket,
    recommendedBucketForSpan,
} from '../js/bucket-dropdown.js';

test('BUCKET_CHOICES is exactly 5/10/15/30/60', () => {
    assert.deepEqual([...BUCKET_CHOICES].sort((a, b) => a - b), [5, 10, 15, 30, 60]);
});

test('normalizeBucket passes valid buckets through', () => {
    for (const n of [5, 10, 15, 30, 60]) assert.equal(normalizeBucket(n), n);
});

test('normalizeBucket falls back to 30 for anything else', () => {
    for (const n of [0, 7, 45, -5, 3600, NaN, null, undefined, '5']) {
        assert.equal(normalizeBucket(n), 30);
    }
});

test('recommendedBucketForSpan: 5 for a single day, 30 beyond', () => {
    assert.equal(recommendedBucketForSpan(0), 5);
    assert.equal(recommendedBucketForSpan(1), 5);
    assert.equal(recommendedBucketForSpan(2), 30);
    assert.equal(recommendedBucketForSpan(7), 30);
});
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `node --test internal/web/static/tests/bucket-dropdown.test.mjs`
Expected: FAIL — `Cannot find module .../js/bucket-dropdown.js`.

- [ ] **Step 3: Create the shared module**

Create `internal/web/static/js/bucket-dropdown.js`. The factory body moves verbatim from `panel-rate.js:46-82` — do not redesign it:

```js
// Shared time-bucket control, used by the Token Rate panel and the Intraday
// chart. Both offer the same 5/10/15/30/60-minute choices, so the constants and
// the dropdown factory live here rather than being duplicated per panel.

export const BUCKET_CHOICES = new Set([5, 10, 15, 30, 60]);

export function normalizeBucket(n) {
    return BUCKET_CHOICES.has(n) ? n : 30;
}

// Fine buckets are only readable over a short span: a 5-minute bucket across
// 7 days is ~2000 slots per model.
export function recommendedBucketForSpan(spanDays) {
    return spanDays <= 1 ? 5 : 30;
}

// makeBucketDropdown wires a custom .select-wrap dropdown — a trigger button
// plus a .rate-menu popover — and returns { setValue, setOpen }. Native
// <select> option popups can't be rounded on Windows Chromium, so the open
// menu is a styled list. setValue updates the trigger label and the selected
// item WITHOUT firing onPick (used to sync the control to state, e.g. when a
// loader normalizes the bucket).
export function makeBucketDropdown(wrap, onPick) {
    if (!wrap) return null;
    const trigger = wrap.querySelector('[data-trigger]');
    const label = wrap.querySelector('[data-label]');
    const items = wrap.querySelectorAll('.rate-item');
    if (!trigger || !label) return null;

    const setOpen = (open) => {
        wrap.classList.toggle('open', open);
        trigger.setAttribute('aria-expanded', open ? 'true' : 'false');
    };

    trigger.addEventListener('click', () => {
        setOpen(!wrap.classList.contains('open'));
    });
    items.forEach((item) => {
        item.addEventListener('click', () => {
            setValue(item.dataset.value);
            setOpen(false);
            if (onPick) onPick(item.dataset.value);
        });
    });
    // Close when clicking outside this dropdown, or on Escape.
    document.addEventListener('click', (e) => { if (!wrap.contains(e.target)) setOpen(false); });
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape') setOpen(false); });

    function setValue(value) {
        let matched = null;
        items.forEach((item) => {
            const sel = item.dataset.value === String(value);
            item.setAttribute('aria-selected', sel ? 'true' : 'false');
            if (sel) matched = item;
        });
        if (matched) label.textContent = matched.textContent;
    }
    return { setValue, setOpen };
}
```

- [ ] **Step 4: Run the test to confirm it passes**

Run: `node --test internal/web/static/tests/bucket-dropdown.test.mjs`
Expected: PASS (4 tests).

- [ ] **Step 5: Rewire panel-rate.js**

In `internal/web/static/js/panel-rate.js`:

1. Add to the imports at the top:

```js
import { makeBucketDropdown, normalizeBucket, recommendedBucketForSpan } from './bucket-dropdown.js';
```

2. Delete `const BUCKET_CHOICES = new Set([5, 10, 15, 30, 60]);` and the `normalizeBucket` and `recommendedBucketForSpan` function bodies (lines 25-33).
3. Delete the whole `makeRateDropdown` function (lines 46-82) including its comment block, which moved into the shared module.
4. Replace the single `makeRateDropdown(` call site in `initPanelRate` with `makeBucketDropdown(`.

Leave `spanDaysInclusive` where it is. It is duplicated in `panel-daily.js`, but the two differ deliberately — `panel-rate.js` returns 1 for an open range while `panel-daily.js` returns 0 — and unifying them is out of scope for this change.

- [ ] **Step 6: Verify the Rate panel is untouched behaviourally**

Run: `node --test internal/web/static/tests/*.test.mjs`
Expected: PASS, no regressions.

Run: `grep -n "makeRateDropdown\|BUCKET_CHOICES" internal/web/static/js/panel-rate.js`
Expected: no output — every reference now comes from the shared module.

---

### Task 3: Gap-filling helper

**Files:**
- Create: `internal/web/static/js/intraday-slots.js`
- Create: `internal/web/static/tests/intraday-slots.test.mjs`

**Interfaces:**
- Produces:
  - `slotLabel(unixSec: number) => string` — `"MM-DD HH:MM"` in local time.
  - `fillIntradaySlots(rows: Array<object>, bucketMinutes: number) => { slots: Array<{unix: number, label: string}>, rowAt: Map<string, object> }` — `rowAt` is keyed `` `${unix}|${model}` ``.
- Consumed by: Task 5 (`panel-daily.js` chart build).

- [ ] **Step 1: Write the failing test**

Create `internal/web/static/tests/intraday-slots.test.mjs`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { fillIntradaySlots, slotLabel } from '../js/intraday-slots.js';

// 2026-07-17 09:00 local, expressed as a unix second so the test is TZ-agnostic.
const base = Math.floor(new Date(2026, 6, 17, 9, 0, 0).getTime() / 1000);
const STEP5 = 5 * 60;

function row(unix, model, extra = {}) {
    return {
        bucket_start_unix: unix,
        bucket_label: slotLabel(unix),
        model,
        input_tokens: 10,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        output_tokens: 1,
        request_count: 1,
        cost_usd: 0.01,
        ...extra,
    };
}

test('empty input yields no slots', () => {
    const { slots, rowAt } = fillIntradaySlots([], 5);
    assert.deepEqual(slots, []);
    assert.equal(rowAt.size, 0);
});

test('invalid bucket yields no slots', () => {
    for (const b of [0, -5, NaN, undefined]) {
        assert.deepEqual(fillIntradaySlots([row(base, 'a')], b).slots, []);
    }
});

test('a contiguous run is unchanged in length', () => {
    const rows = [row(base, 'a'), row(base + STEP5, 'a'), row(base + 2 * STEP5, 'a')];
    const { slots } = fillIntradaySlots(rows, 5);
    assert.equal(slots.length, 3);
    assert.deepEqual(slots.map((s) => s.unix), [base, base + STEP5, base + 2 * STEP5]);
});

test('internal gaps are filled so the axis is a true timeline', () => {
    // 09:00 then 09:20 — three empty 5-minute slots in between.
    const rows = [row(base, 'a'), row(base + 4 * STEP5, 'a')];
    const { slots, rowAt } = fillIntradaySlots(rows, 5);

    assert.equal(slots.length, 5);
    assert.deepEqual(slots.map((s) => s.unix), [
        base, base + STEP5, base + 2 * STEP5, base + 3 * STEP5, base + 4 * STEP5,
    ]);
    assert.ok(rowAt.has(base + '|a'));
    assert.ok(!rowAt.has(base + STEP5 + '|a'), 'gap slots carry no row');
});

test('fill spans data extent only — no leading or trailing padding', () => {
    const rows = [row(base, 'a')];
    const { slots } = fillIntradaySlots(rows, 5);
    assert.equal(slots.length, 1, 'a single bucket must not pad out to the whole day');
    assert.equal(slots[0].unix, base);
});

test('multiple models share one slot list and are keyed independently', () => {
    const rows = [row(base, 'a'), row(base, 'b'), row(base + 2 * STEP5, 'b')];
    const { slots, rowAt } = fillIntradaySlots(rows, 5);

    assert.equal(slots.length, 3, 'slots are unique instants, not per-model rows');
    assert.ok(rowAt.has(base + '|a'));
    assert.ok(rowAt.has(base + '|b'));
    assert.ok(!rowAt.has(base + 2 * STEP5 + '|a'), 'model a has no row in the last slot');
    assert.ok(rowAt.has(base + 2 * STEP5 + '|b'));
});

test('real rows keep the server label; gaps get a synthesized one', () => {
    const rows = [
        { ...row(base, 'a'), bucket_label: 'SERVER-LABEL' },
        row(base + 2 * STEP5, 'a'),
    ];
    const { slots } = fillIntradaySlots(rows, 5);
    assert.equal(slots[0].label, 'SERVER-LABEL');
    assert.equal(slots[1].label, slotLabel(base + STEP5));
});

test('rows out of order still produce an ascending slot list', () => {
    const rows = [row(base + 2 * STEP5, 'a'), row(base, 'a')];
    const { slots } = fillIntradaySlots(rows, 5);
    assert.deepEqual(slots.map((s) => s.unix), [base, base + STEP5, base + 2 * STEP5]);
});

test('slotLabel renders MM-DD HH:MM zero-padded in local time', () => {
    const t = Math.floor(new Date(2026, 0, 5, 7, 5, 0).getTime() / 1000);
    assert.equal(slotLabel(t), '01-05 07:05');
});
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `node --test internal/web/static/tests/intraday-slots.test.mjs`
Expected: FAIL — `Cannot find module .../js/intraday-slots.js`.

- [ ] **Step 3: Implement the helper**

Create `internal/web/static/js/intraday-slots.js`:

```js
// Dense-slot builder for the Intraday bar chart.
//
// The intraday SQL omits empty buckets. On a category axis that would place
// 01:55 directly beside 09:00 when nothing ran in between, so the axis would
// misstate time. fillIntradaySlots reinstates the missing instants.
//
// The fill spans the returned data's extent, not the requested date range:
// padding to the full range would append empty future slots, so viewing Today
// at 14:00 with 5-minute buckets would render ten hours of blank axis.

function pad2(n) {
    return String(n).padStart(2, '0');
}

// Mirrors the server's BucketLabel format ("MM-DD HH:MM", local time). Used
// only for synthesized gap slots; slots carrying data keep the server's label.
export function slotLabel(unixSec) {
    const d = new Date(unixSec * 1000);
    return `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

export function fillIntradaySlots(rows, bucketMinutes) {
    const empty = { slots: [], rowAt: new Map() };
    const step = Number(bucketMinutes) * 60;
    if (!Array.isArray(rows) || rows.length === 0) return empty;
    if (!Number.isFinite(step) || step <= 0) return empty;

    const labelAt = new Map();
    const rowAt = new Map();
    let first = Infinity;
    let last = -Infinity;

    for (const r of rows) {
        const u = Number(r?.bucket_start_unix);
        if (!Number.isFinite(u)) continue;
        if (u < first) first = u;
        if (u > last) last = u;
        if (!labelAt.has(u)) {
            const lbl = r?.bucket_label;
            labelAt.set(u, lbl != null && String(lbl) !== '' ? String(lbl) : slotLabel(u));
        }
        rowAt.set(u + '|' + r.model, r);
    }
    if (!Number.isFinite(first) || !Number.isFinite(last)) return empty;

    const slots = [];
    for (let u = first; u <= last; u += step) {
        slots.push({ unix: u, label: labelAt.get(u) ?? slotLabel(u) });
    }
    return { slots, rowAt };
}
```

- [ ] **Step 4: Run the test to confirm it passes**

Run: `node --test internal/web/static/tests/intraday-slots.test.mjs`
Expected: PASS (9 tests).

---

### Task 4: Markup, state, and span rule

**Files:**
- Modify: `internal/web/static/index.html:160-187` (dropdown in, table out) and the two version query strings
- Modify: `internal/web/static/js/state.js:18-19` (add the two intraday keys)
- Modify: `internal/web/static/js/panel-daily.js:8-34` (constants, span rule, sub-tab title)
- Modify: `internal/web/static/tests/panel-daily.test.mjs` (drop tests for deleted exports)

**Interfaces:**
- Consumes: `recommendedBucketForSpan`, `normalizeBucket` from Task 2.
- Produces: `state.intradayBucket: number`, `state.intradaySpan: number|null`; `#intraday-bucket-dd` in the DOM.

- [ ] **Step 1: Write the failing markup test**

Create `internal/web/static/tests/intraday-markup.test.mjs`. It is a static-HTML test, matching the pattern already used by `granularity-control.test.mjs`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

const html = fs.readFileSync(new URL('../index.html', import.meta.url), 'utf8');

// #daily-byhour spans from its opening div to the close of #panel-daily.
function byHourSection() {
    const start = html.indexOf('id="daily-byhour"');
    assert.ok(start > 0, 'missing #daily-byhour');
    const end = html.indexOf('id="panel-sessions"', start);
    assert.ok(end > start, 'missing #panel-sessions terminator');
    return html.slice(start, end);
}

test('Intraday detail table is gone', () => {
    const section = byHourSection();
    assert.ok(!section.includes('hourly-tbody'), 'hourly-tbody must be removed');
    assert.ok(!section.includes('<table'), 'the Intraday table must be removed');
});

test('Intraday bucket dropdown exists with all five choices', () => {
    const section = byHourSection();
    assert.match(section, /id="intraday-bucket-dd"/);
    for (const v of [5, 10, 15, 30, 60]) {
        assert.match(section, new RegExp(`data-value="${v}"`), `missing bucket ${v}`);
    }
});

test('Intraday bucket dropdown is accessibly named and distinct from the Rate one', () => {
    const section = byHourSection();
    assert.match(section, /aria-label="Intraday time bucket"/);
    assert.match(section, /type="button"[^>]*data-trigger/);
    assert.match(section, /aria-haspopup="listbox"/);
});

test('cache-busting versions bumped', () => {
    assert.match(html, /style\.css\?v=25/);
    assert.match(html, /app\.js\?v=67/);
});
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `node --test internal/web/static/tests/intraday-markup.test.mjs`
Expected: FAIL — `hourly-tbody must be removed`.

- [ ] **Step 3: Replace the Intraday markup**

In `internal/web/static/index.html`, replace the whole `#daily-byhour` block (currently lines 160-187) with:

```html
        <div id="daily-byhour" style="display:none">
            <div class="chart-section hourly-section">
                <div class="chart-section-header">
                    <span class="chart-section-title">Intraday by Model</span>
                    <span class="hourly-hint">One bar per bucket × model · max 7 days</span>
                    <div class="select-wrap" id="intraday-bucket-dd">
                        <button type="button" class="rate-select" data-trigger title="Time bucket size" aria-haspopup="listbox" aria-expanded="false" aria-label="Intraday time bucket">
                            <span data-label>5 min</span>
                            <span class="rate-caret" aria-hidden="true"></span>
                        </button>
                        <ul class="rate-menu" role="listbox">
                            <li class="rate-item" role="option" data-value="5" aria-selected="true">5 min</li>
                            <li class="rate-item" role="option" data-value="10">10 min</li>
                            <li class="rate-item" role="option" data-value="15">15 min</li>
                            <li class="rate-item" role="option" data-value="30">30 min</li>
                            <li class="rate-item" role="option" data-value="60">60 min</li>
                        </ul>
                    </div>
                </div>
                <div class="chart-wrap" id="hourly-chart"></div>
            </div>
        </div>
```

Then bump both version strings: `style.css?v=24` becomes `style.css?v=25`, and `app.js?v=66` becomes `app.js?v=67`.

- [ ] **Step 4: Check the header layout holds the new control**

Run: `grep -n "chart-section-header" -A 8 internal/web/static/style.css | head -20`

The Rate header already hosts a `.select-wrap` beside its title, so the pattern is proven. If `.chart-section-header` is not already `display:flex` with the title taking remaining space, add a scoped rule — `#intraday-bucket-dd { margin-left: auto; }` — so the dropdown sits right-aligned without touching the shared class. Do not restyle `.chart-section-header` globally: `FRONTEND_TASTE.md` requires scoping CSS to the component being changed.

- [ ] **Step 5: Add the state keys**

In `internal/web/static/js/state.js`, after `rateLegendFocus: null,` add:

```js
    intradayBucket: 5,
    intradaySpan: null,
```

- [ ] **Step 6: Widen the span rule and fix the sub-tab title**

In `internal/web/static/js/panel-daily.js`, replace lines 8-9:

```js
const INTRADAY_BUCKET_MIN = 30;
const INTRADAY_MAX_DAYS = 1;
```

with:

```js
const INTRADAY_MAX_DAYS = 7;
```

Then in `updateDailyViewControls`, replace the title assignment:

```js
    hourBtn.title = canHour
        ? `Intraday view (${INTRADAY_BUCKET_MIN}-min buckets)`
        : `Intraday view requires a single day (Today / Yesterday / a single custom date)`;
```

with:

```js
    hourBtn.title = canHour
        ? 'Intraday view (per-bucket bars)'
        : `Intraday view supports up to ${INTRADAY_MAX_DAYS} days (Today / Yesterday / 7 Days / a custom span ≤ ${INTRADAY_MAX_DAYS} days)`;
```

`isIntradayRangeSelected` needs no edit — it already reads `span <= INTRADAY_MAX_DAYS`.

- [ ] **Step 7: Drop the tests for exports this change deletes**

`internal/web/static/tests/panel-daily.test.mjs` imports `intradayLineTooltip` and `renderIntradayRow`, both removed in Task 5. Delete those two imports and every test that exercises them. Keep the `renderDailyRow` tests — the By-day table is out of scope and must keep passing.

- [ ] **Step 8: Run the static suite**

Run: `node --test internal/web/static/tests/*.test.mjs`
Expected: PASS. `intraday-markup.test.mjs` now passes; `panel-daily.test.mjs` still passes with fewer tests.

---

### Task 5: Extract the shared token bar color

`chart-main.js` builds its two-segment gradient inline inside `loadChart`, closing over `isCost` / `isReqs`. Task 6 needs the identical gradient. Copying it would duplicate a pinned visual rule across two files, so the next edit to that rule would land in only one of them. Extract it instead, next to `buildBarTooltip`, which is already shared the same way.

**Files:**
- Modify: `internal/web/static/js/chart-main.js` — add `makeTokenBarColor`, consume it in `loadChart`
- Test: `internal/web/static/tests/chart-main.test.mjs` (exists — extend it)

**Interfaces:**
- Produces: `makeTokenBarColor(model: string) => (params: object) => string | object` — an ECharts `itemStyle.color` callback. Returns a flat `getModelColor(model)` when `state.chartMetric` is `'cost'` or `'requests'`, otherwise the two-segment gradient. Reads `state.chartMetric` and `state.isDark` itself, so callers pass only the model.
- Consumed by: Task 6 (`panel-daily.js`).

- [ ] **Step 1: Write the failing test**

Append to `internal/web/static/tests/chart-main.test.mjs`:

```js
import { makeTokenBarColor } from '../js/chart-main.js';
import { state } from '../js/state.js';

// echarts is a global in the browser; the gradient branch constructs
// echarts.graphic.LinearGradient. Stub it so the pure logic is testable.
function withEchartsStub(fn) {
    const prev = globalThis.echarts;
    globalThis.echarts = {
        graphic: {
            LinearGradient: class {
                constructor(x0, y0, x1, y1, stops) {
                    Object.assign(this, { x0, y0, x1, y1, stops, __gradient: true });
                }
            },
        },
    };
    try { return fn(); } finally { globalThis.echarts = prev; }
}

const tokenRow = {
    raw: {
        model: 'm',
        input_tokens: 100,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        output_tokens: 100,
        request_count: 1,
        cost_usd: 1,
    },
};

test('makeTokenBarColor returns a flat color for cost and requests', () => {
    const prevMetric = state.chartMetric;
    try {
        for (const m of ['cost', 'requests']) {
            state.chartMetric = m;
            const color = makeTokenBarColor('m')({ data: tokenRow });
            assert.equal(typeof color, 'string', `${m} must not use a gradient`);
        }
    } finally { state.chartMetric = prevMetric; }
});

test('makeTokenBarColor returns a two-stop gradient for tokens', () => {
    const prevMetric = state.chartMetric;
    state.chartMetric = 'tokens';
    try {
        withEchartsStub(() => {
            const color = makeTokenBarColor('m')({ data: tokenRow });
            assert.ok(color.__gradient, 'tokens must use the gradient');
            // 50% output → the split sits at 0.5, well above minVis.
            assert.equal(color.stops[1].offset, 0.5);
            assert.equal(color.stops[2].offset, 0.5);
        });
    } finally { state.chartMetric = prevMetric; }
});

test('makeTokenBarColor floors a tiny output segment at minVis so it stays visible', () => {
    const prevMetric = state.chartMetric;
    state.chartMetric = 'tokens';
    try {
        withEchartsStub(() => {
            const tiny = { raw: { ...tokenRow.raw, input_tokens: 100000, output_tokens: 1 } };
            const color = makeTokenBarColor('m')({ data: tiny });
            assert.equal(color.stops[1].offset, 0.06);
        });
    } finally { state.chartMetric = prevMetric; }
});

test('makeTokenBarColor falls back to a flat color when the datum carries no raw', () => {
    const prevMetric = state.chartMetric;
    state.chartMetric = 'tokens';
    try {
        assert.equal(typeof makeTokenBarColor('m')({ data: 0 }), 'string');
        assert.equal(typeof makeTokenBarColor('m')({}), 'string');
    } finally { state.chartMetric = prevMetric; }
});
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `node --test internal/web/static/tests/chart-main.test.mjs`
Expected: FAIL — `makeTokenBarColor` is not exported.

- [ ] **Step 3: Extract the function**

In `internal/web/static/js/chart-main.js`, add after `buildBarTooltip`:

```js
/**
 * Shared itemStyle.color callback for token bars, used by the daily and
 * intraday charts. Cost and Requests are single quantities, so they render
 * flat; only Tokens carries the input/output split worth encoding.
 *
 * Reads state.chartMetric and state.isDark directly so both call sites stay
 * one argument wide, and so the pinned gradient rule lives in exactly one
 * place.
 */
export function makeTokenBarColor(model) {
    return function tokenBarColor(params) {
        const base = getModelColor(model);
        if (state.chartMetric === 'cost' || state.chartMetric === 'requests') return base;
        const raw = params?.data?.raw;
        if (!raw) return base;
        const parts = tokenParts(raw);
        if (!(parts.total > 0)) return base;
        // One bar: bottom = all input-side tokens; top = output (light).
        if (!(parts.output > 0)) return base;
        const light = mixHex(base, '#ffffff', state.isDark ? 0.28 : 0.35);
        if (!(parts.inputSide > 0)) return light;
        const exactRatio = parts.output / parts.total;
        const minVis = 0.06;
        const outputRatio = exactRatio < minVis ? minVis : exactRatio;
        return new echarts.graphic.LinearGradient(0, 0, 0, 1, [
            { offset: 0, color: light },
            { offset: outputRatio, color: light },
            { offset: outputRatio, color: base },
            { offset: 1, color: base },
        ]);
    };
}
```

- [ ] **Step 4: Consume it in loadChart**

In `loadChart`, replace the whole inline `itemStyle: { color(params) { ... } }` block (~lines 100-122) with:

```js
            itemStyle: { color: makeTokenBarColor(model) },
```

The behavior must be identical: the old code returned `getModelColor(model)` when `isCost || isReqs`, and `isCost`/`isReqs` are derived from `state.chartMetric` — exactly what the extracted function now reads itself.

- [ ] **Step 5: Run the tests**

Run: `node --test internal/web/static/tests/chart-main.test.mjs`
Expected: PASS.

Run: `node --test internal/web/static/tests/*.test.mjs`
Expected: PASS — no regression in the main chart's existing tests.

- [ ] **Step 6: Confirm the main chart lost nothing**

Run: `grep -n "LinearGradient\|minVis" internal/web/static/js/chart-main.js`
Expected: exactly one occurrence of each, inside `makeTokenBarColor`. A second copy means the inline block was not removed.

---

### Task 6: Replace the line chart with bars

**Files:**
- Modify: `internal/web/static/js/panel-daily.js` — delete `intradayLineTooltip` (~74-112) and `renderIntradayRow` (~113-146); rewrite `loadIntraday` (~147 onward)

**Interfaces:**
- Consumes: `fillIntradaySlots` (Task 3); `makeBucketDropdown`, `normalizeBucket`, `recommendedBucketForSpan` (Task 2); `buildBarTooltip` and `makeTokenBarColor` from `./chart-main.js` (Task 5); `chartColors` from `./theme.js`.
- Produces: `initIntradayBucketDropdown()` called once from `app.js` boot alongside the other panel initializers.

- [ ] **Step 1: Update the imports**

At the top of `internal/web/static/js/panel-daily.js`:

```js
import { state, paging } from './state.js';
import { fmtNum, escapeHtml, rangeToFromTo } from './utils.js';
import { chartColors, getModelColor } from './theme.js';
import { loadDailyData, loadIntradayData } from './api.js';
import { renderPagination } from './pagination.js';
import { tokenParts } from './token-math.js';
import { buildBarTooltip, makeTokenBarColor } from './chart-main.js';
import { fillIntradaySlots } from './intraday-slots.js';
import { makeBucketDropdown, normalizeBucket, recommendedBucketForSpan } from './bucket-dropdown.js';
```

`mixHex` is gone — the gradient moved into `makeTokenBarColor` (Task 5). `fmtNum` stays; the new yAxis formatter uses it. `tokenParts` stays; `metricValueFromRow` uses it.

After Step 2, run `grep -n "escapeHtml\|getModelColor" internal/web/static/js/panel-daily.js` and drop either import if the file no longer references it.

**Keep `metricLabel` and `metricValueFromRow` (lines ~62-72).** The Intraday view honors the top-bar Tokens / Cost / Requests switch today, and this change must not quietly remove that. The bar `value` comes from `metricValueFromRow(r)`, never from `tokenParts(r).total` directly.

- [ ] **Step 2: Delete the dead code**

Delete `intradayLineTooltip` and `renderIntradayRow` entirely, including their doc comments. Both are exported; confirm no other module imports them:

Run: `grep -rn "intradayLineTooltip\|renderIntradayRow" internal/web/static/`
Expected: no hits outside the deleted definitions (Task 4 Step 7 already cleaned the test file).

- [ ] **Step 3: Add the dropdown initializer**

Add near the top of the module, after the constants:

```js
// Custom bucket dropdown instance (set in initIntradayBucketDropdown).
// syncBucketSelect keeps the control's label in step with state when
// loadIntraday normalizes the bucket for the selected span.
let bucketDropdown = null;

function syncBucketSelect(bucket) {
    bucketDropdown?.setValue(String(bucket));
}

export function initIntradayBucketDropdown() {
    bucketDropdown = makeBucketDropdown(
        document.getElementById('intraday-bucket-dd'),
        (value) => {
            state.intradayBucket = normalizeBucket(parseInt(value, 10));
            loadIntraday();
        },
    );
    syncBucketSelect(state.intradayBucket);
}
```

- [ ] **Step 4: Rewrite loadIntraday**

Replace the whole existing `loadIntraday` body with:

```js
export async function loadIntraday() {
    const chartEl = document.getElementById('hourly-chart');
    if (!chartEl) return;

    const { from, to } = rangeToFromTo(state.currentRange);
    const span = spanDaysInclusive(from, to);
    if (span < 1 || span > INTRADAY_MAX_DAYS) {
        showIntradayMessage(chartEl, `Intraday view supports up to ${INTRADAY_MAX_DAYS} days. Pick a shorter range (Today / 7 Days / a custom span ≤ ${INTRADAY_MAX_DAYS} days).`);
        return;
    }

    // Mirrors panel-rate.js: a bucket chosen for one span must not leak into
    // another — 5-minute buckets over 7 days would render ~2000 slots x model.
    if (state.intradaySpan !== span) {
        state.intradaySpan = span;
        state.intradayBucket = recommendedBucketForSpan(span);
    }
    const useBucket = normalizeBucket(state.intradayBucket);
    state.intradayBucket = useBucket;
    syncBucketSelect(useBucket);

    const sourceAtStart = state.source;
    let json;
    try {
        json = await loadIntradayData({ from, to, bucket: useBucket });
    } catch (e) {
        showIntradayMessage(chartEl, 'Failed to load intraday data.');
        return;
    }
    if (state.source !== sourceAtStart) return; // user switched tabs mid-flight

    const rows = json.data || [];
    if (rows.length === 0) {
        showIntradayMessage(chartEl, 'No data in this range.');
        return;
    }

    const bucketMinutes = json.bucket_minutes || useBucket;
    const { slots, rowAt } = fillIntradaySlots(rows, bucketMinutes);
    const models = [...new Set(rows.map((r) => r.model))].sort();
    const c = chartColors();

    // Ascending time: a time-of-day axis must read left to right. This is the
    // opposite of chart-main.js, which sorts dates newest-first on purpose.
    const categories = slots.map((s) => s.label);

    const isCost = state.chartMetric === 'cost';
    const fmtVal = (v) => (isCost ? '$' + Number(v).toFixed(4) : fmtNum(v));

    const series = models.map((model) => ({
        name: model,
        type: 'bar',
        barMaxWidth: 44,
        // Shared with the main chart — see makeTokenBarColor in chart-main.js.
        itemStyle: { color: makeTokenBarColor(model) },
        data: slots.map((s) => {
            const r = rowAt.get(s.unix + '|' + model);
            // metricValueFromRow, not tokenParts: Intraday honors the top-bar
            // Tokens / Cost / Requests switch.
            return r ? { value: metricValueFromRow(r), raw: r } : 0;
        }),
    }));

    // Bars need width. Beyond this many slots the newest window is shown and
    // the rest is reachable by zooming.
    const visibleBuckets = 48;
    const hasZoom = slots.length > visibleBuckets;
    // Ascending time: the newest buckets are on the right, so anchor there.
    const winPct = hasZoom ? Math.round((visibleBuckets / slots.length) * 100) : 100;

    if (!state.hourlyChart) {
        state.hourlyChart = echarts.init(chartEl, null, { renderer: 'canvas' });
        window.addEventListener('resize', () => state.hourlyChart && state.hourlyChart.resize());
    }
    state.hourlyChart.setOption({
        backgroundColor: 'transparent',
        grid: { left: 56, right: 20, top: 24, bottom: hasZoom ? 56 : 32 },
        tooltip: {
            trigger: 'item',
            backgroundColor: c.tooltipBg,
            borderColor: c.tooltipBorder,
            textStyle: { color: c.tooltipText },
            formatter: (params) => buildBarTooltip(params, c),
        },
        xAxis: {
            type: 'category',
            data: categories,
            axisLine: { lineStyle: { color: c.axisLine } },
            axisLabel: { color: c.axisLabel, hideOverlap: true },
        },
        yAxis: {
            type: 'value',
            // metricLabel() keeps the axis honest when the metric switch flips.
            name: metricLabel(),
            nameTextStyle: { color: c.axisLabel },
            axisLabel: { color: c.axisLabel, formatter: (v) => fmtVal(v) },
            splitLine: { lineStyle: { color: c.splitLine } },
        },
        // Styling mirrors chart-main.js so both charts' zoom sliders match.
        // The eight dz* colors are theme-aware; an unstyled slider would render
        // as ECharts' default and look foreign next to the main chart.
        dataZoom: hasZoom
            ? [
                {
                    type: 'inside',
                    xAxisIndex: 0,
                    start: 100 - winPct,
                    end: 100,
                    zoomLock: true,
                },
                {
                    type: 'slider',
                    xAxisIndex: 0,
                    start: 100 - winPct,
                    end: 100,
                    height: 14,
                    bottom: 4,
                    borderColor: c.dzBorder,
                    backgroundColor: c.dzBg,
                    fillerColor: c.dzFill,
                    handleStyle: { color: c.dzHandle, borderColor: c.dzHandle },
                    moveHandleStyle: { color: c.dzHandle },
                    textStyle: { color: c.legendText, fontSize: 10 },
                    dataBackground: {
                        lineStyle: { color: c.dzBgLine },
                        areaStyle: { color: c.dzBgArea },
                    },
                    selectedDataBackground: {
                        lineStyle: { color: c.dzSelLine },
                        areaStyle: { color: c.dzSelArea },
                    },
                },
            ]
            : [],
        series,
    }, true);
    state.hourlyChart.resize();
}

function showIntradayMessage(chartEl, msg) {
    if (state.hourlyChart) {
        state.hourlyChart.dispose();
        state.hourlyChart = null;
    }
    chartEl.innerHTML = `<div style="text-align:center;color:var(--text-muted);padding:48px 24px">${msg}</div>`;
}
```

Delete any leftover line-chart machinery in the old `loadIntraday` — the `getZr()` hover binding, `bindHoverIsolate`-style helpers, and the `#hourly-tbody` writes all go. `showIntradayMessage` replaces the old empty-state that was rendered into the table body.

- [ ] **Step 5: Wire the initializer at boot**

In `internal/web/static/app.js`, find where the other panels initialize and add `initIntradayBucketDropdown()` to the same block, importing it from `./js/panel-daily.js`.

Run: `grep -n "initPanelRate\|initFilters" internal/web/static/app.js`
to locate the boot block, then add the call beside them.

- [ ] **Step 6: Run every test**

Run: `node --test internal/web/static/tests/*.test.mjs`
Expected: PASS.

Run: `go test ./...`
Expected: all packages ok.

- [ ] **Step 7: Confirm the pinned chart rules are not violated**

Run: `grep -n "stack:\|trigger:\|emphasis" internal/web/static/js/panel-daily.js`
Expected: exactly one `trigger: 'item'`; **no** `stack: 'total'`; **no** `emphasis`. Any other result is a violation of `.claude/CLAUDE.md` and must be fixed before continuing.

---

### Task 7: Verify and commit

**Files:** none modified — this task proves the change and lands it.

- [ ] **Step 1: Build and restart the dev instance**

```bash
make build
./bin/cc-otel.exe restart -config bin/cc-otel-dev.yaml
```

Never use production ports 4317 / 8899. The dev config pins 14317 / 18899.

- [ ] **Step 2: Prove the backend accepts every bucket on both paths**

```bash
for src in "" "codex/"; do
  for b in 5 10 15 30 60; do
    echo -n "/api/${src}intraday bucket=$b -> "
    curl -s "http://localhost:18899/api/${src}intraday?from=2026-07-16&to=2026-07-16&bucket=$b" \
      | python -c "import sys,json; print(json.load(sys.stdin).get('bucket_minutes'))"
  done
done
```

Expected: each line echoes its own bucket. A `30` where `5` was requested means Task 1 regressed on that path.

- [ ] **Step 3: Prove the 7-day span works**

```bash
curl -s -o /dev/null -w "7d/5min: %{http_code} %{time_total}s\n" \
  "http://localhost:18899/api/intraday?from=2026-07-11&to=2026-07-17&bucket=5"
curl -s -o /dev/null -w "30d cap: %{http_code}\n" \
  "http://localhost:18899/api/intraday?from=2026-06-17&to=2026-07-17&bucket=30"
```

Expected: `200` for the 7-day span; `400` for the 30-day span.

- [ ] **Step 4: Inspect the real page**

Open `http://localhost:18899/?v=67`, go to Daily Detail → Intraday, and check:

- every bucket from 5 to 60 on Today and on 7 Days;
- bars read left-to-right in time order, oldest on the left;
- a quiet period shows a gap, not two adjacent bars;
- dataZoom appears past 48 slots and opens anchored to the newest buckets;
- the Intraday sub-tab is disabled on 30 Days and All Time, with a title naming the 7-day limit;
- dark and light themes, hover, pressed, keyboard focus, dropdown open/close;
- `?source=codex` repeats the bucket sweep;
- **the top-bar Tokens / Cost / Requests switch still drives the Intraday chart** — Cost and Requests render flat bars with a matching y-axis label, Tokens renders the gradient. This is existing behavior the change must not drop;
- **the main chart is unharmed by the Task 5 extraction** — its bars still show the gradient on Tokens and flat color on Cost / Requests;
- the Token Rate panel's own dropdown still works — it now shares the factory.

- [ ] **Step 5: Verify the pinned tooltip semantics by hand**

Hover a bar in Token mode and confirm **Total = Input + Output**, and that the **Input** parent row equals the sum of **Uncached + Cache Read + Cache Create**. This check is mandatory in `.claude/CLAUDE.md` for any change touching these charts.

- [ ] **Step 6: Screenshot**

Capture at least one screenshot showing the chart, the bucket dropdown, and the surrounding page. `FRONTEND_TASTE.md` is explicit: no screenshot means verification is not complete. If no browser automation is available in the session, say so plainly and hand this step to the user rather than claiming it passed.

- [ ] **Step 7: Commit**

One commit carrying the spec, the plan, and the implementation — the user's standing rule is that spec and implementation land together.

`docs/superpowers/` is gitignored (`.gitignore:39`) even though ten files under it are tracked. The spec and plan are new, so they need `-f` or they will be silently skipped:

```bash
git add -f docs/superpowers/specs/2026-07-17-intraday-bar-chart-design.md \
           docs/superpowers/plans/2026-07-17-intraday-bar-chart.md
git add internal/db/repository.go internal/db/codex_repository.go \
        internal/api/handler.go internal/api/codex_handler.go \
        internal/db/intraday_bucket_test.go internal/api/intraday_bucket_test.go \
        internal/web/static/js/bucket-dropdown.js \
        internal/web/static/js/intraday-slots.js \
        internal/web/static/js/chart-main.js \
        internal/web/static/js/panel-daily.js \
        internal/web/static/js/panel-rate.js \
        internal/web/static/js/state.js \
        internal/web/static/index.html \
        internal/web/static/app.js \
        internal/web/static/tests/bucket-dropdown.test.mjs \
        internal/web/static/tests/intraday-slots.test.mjs \
        internal/web/static/tests/intraday-markup.test.mjs \
        internal/web/static/tests/chart-main.test.mjs \
        internal/web/static/tests/panel-daily.test.mjs
```

Verify the spec and plan actually staged before committing — this is the step the ignore rule silently breaks:

```bash
git diff --cached --name-only | grep docs/superpowers
```

Expected: both paths listed. If empty, the `-f` did not take and the commit would ship code without its spec.

```bash
git commit -m "feat(ui): Intraday bar chart with selectable time buckets"
```

The commit body must record what the verification actually covered and what it did not — in particular, whether the browser visual pass ran.
