# Intraday Bar Chart Design

## Status

Approved in conversation on 2026-07-17.

## Objective

Replace the Intraday line chart with a bar chart over time buckets, add a
5/10/15/30/60-minute bucket selector matching the Token Rate panel, extend the
supported span from a single day to at most 7 days, and delete the Intraday
detail table.

Rate and throughput belong to the Token Rate panel. The Intraday view answers a
different question — how much was consumed in each time bucket — and a bar
chart states that directly.

## Background

This reverts a prior decision. `CHANGELOG.md` records that the 30-minute
per-model line chart replaced an earlier bar chart. The line form implies rate
and duplicates what Token Rate already shows better.

Measured on the 443 MB development database (2026-07-11..2026-07-17):

| Bucket | Points returned | Slots if gap-filled | Query time |
|---|---|---|---|
| 5 min | 1229 | 2016 | 42 ms |
| 10 min | 748 | 1008 | 41 ms |
| 15 min | 569 | 672 | 40 ms |
| 30 min | 371 | 336 | 39 ms |
| 60 min | 245 | 168 | 25 ms |

Eleven models carry data across that window.

Two conclusions drive this design:

1. Query cost is not a constraint. Even 7 days at 5-minute buckets answers in
   42 ms, so the span may be widened without a performance concern.
2. Rendering is the constraint. A bar chart draws one bar per (bucket, model).
   At 7 days and 5-minute buckets that is 2016 slots x 11 models = 22,176 bars,
   or about 0.07 px per bar at 1600 px. Lines tolerate sparse density; bars do
   not. Span-adaptive bucket defaults and dataZoom are therefore mandatory
   parts of this design, not enhancements.

## Scope

- Replace the `#hourly-chart` line chart with a bar chart.
- Add a bucket selector offering 5, 10, 15, 30, and 60 minutes.
- Allow spans up to 7 days; today the frontend allows a single day only.
- Delete the Intraday detail table and its row renderer.
- Extend the intraday backend bucket whitelist from 15/30/60 to 5/10/15/30/60,
  on both the Claude and the Codex path.

## Backend

Four call sites hard-code `15 || 30 || 60`, and all four must change. A request
for `bucket=5` is silently coerced to 30 rather than rejected, which would make
the new control appear broken:

| Location | Path |
|---|---|
| `internal/db/repository.go` `GetIntradayStatsByModel` | Claude |
| `internal/api/handler.go` `Handler.Intraday` | Claude |
| `internal/db/codex_repository.go` `GetCodexIntradayStatsByModel` | Codex |
| `internal/api/codex_handler.go` `Handler.CodexIntraday` | Codex |

The Codex pair is not optional. `intraday` is a member of `CODEX_ROUTES` in
`internal/web/static/js/api.js`, so the same Intraday view issues
`/api/codex/intraday` whenever `state.source === 'codex'`. Extending only the
Claude path would make the new 5- and 10-minute options silently fall back to 30
on the Codex tab — precisely the failure this section exists to prevent.

All four must use the existing `db.ValidRateBucketMinutes`, which already
returns true for 5, 10, 15, 30, and 60.

Codex token semantics are unchanged by this work. `GetCodexIntradayStatsByModel`
documents that Codex `input_tokens` already includes cached input and that
`cache_read_tokens` is a subset of it. Only the bucket whitelist changes.

This is safe rather than new capability. Both paths already floor timestamps
with the identical expression:

```sql
(timestamp - (timestamp % ?)) AS bucket_start
```

`GetRateOverTime` has served 5- and 10-minute buckets through that same
expression in production. No SQL, index, or schema change is required.

Update the doc comments on `IntradayModelSummary`, `GetIntradayStatsByModel`,
and `Handler.Intraday`, each of which currently states 15/30/60 and describes
the consumer as a line chart.

The existing 7-day API cap and its 400 response are already correct and stay
unchanged.

## Bucket Control

`makeRateDropdown` in `internal/web/static/js/panel-rate.js` is a custom
`.select-wrap` popover — a trigger button plus a `.rate-menu` list — used
because native `<select>` popups cannot be rounded on Windows Chromium. Its
bucket set is already exactly `[5, 10, 15, 30, 60]`.

Extract it into `internal/web/static/js/bucket-dropdown.js`, exporting:

- `BUCKET_CHOICES` — the frozen `[5, 10, 15, 30, 60]` set.
- `normalizeBucket(n)` — returns `n` when valid, otherwise 30.
- `recommendedBucketForSpan(spanDays)` — 5 when `spanDays <= 1`, otherwise 30.
- `makeBucketDropdown(wrap, onPick)` — the existing factory, renamed.

`panel-rate.js` imports these instead of defining them. Duplicating the control
into `panel-daily.js` is rejected: `FRONTEND_TASTE.md` requires preferring one
coherent pattern reused well over several slightly different patterns, and
requires reusing existing patterns before adding new ones.

The Intraday markup gains its own `.select-wrap` instance in the
`.chart-section-header`, reusing the Token Rate dropdown styles unchanged.

## State

`panel-daily.js` currently hard-codes `const INTRADAY_BUCKET_MIN = 30`. Replace
it with `state.intradayBucket`, initialized to 5 in `state.js` alongside
`rateBucket: 5`.

Follow the `rateBucket` precedent exactly: the bucket lives in `state` and is
not synchronized to the URL. `state.dailyDetailView` retains its existing
behavior.

Also mirror `panel-rate.js` load-time bucket handling, which tracks the previous
span in `state.rateSpan` and resets the bucket whenever the span changes:

```js
if (state.intradaySpan !== span) {
    state.intradaySpan = span;
    state.intradayBucket = recommendedBucketForSpan(span);
}
const useBucket = normalizeBucket(state.intradayBucket);
syncBucketSelect(useBucket);
state.intradayBucket = useBucket;
```

An explicit bucket choice therefore persists while the span is unchanged and is
reset to the recommendation when the span changes. This is deliberate: 5-minute
buckets chosen for a single day would otherwise survive a switch to 7 Days and
render 22,176 bars. Add `intradaySpan` to `state.js` next to `rateSpan`.

## Span Rule

`isIntradayRangeSelected()` currently requires a single day. Replace it with a
span check of at most 7 days, computed from the same `from`/`to` the loader
already derives.

| Range | Intraday available |
|---|---|
| Today | yes (1 day) |
| Yesterday | yes (1 day) |
| 7 Days | yes (7 days) |
| 30 Days | no |
| All Time | no |
| Custom | yes when the span is 7 days or fewer |

The existing fallback stays: when Intraday is unavailable and
`state.dailyDetailView === 'hour'`, reset the view to `'day'`. The disabled
sub-tab title text must state the 7-day limit rather than "single day".

## Chart

The bar chart mirrors `chart-main.js`, which is the reference pattern for this
project, and obeys every chart rule pinned in `.claude/CLAUDE.md`:

- One bar per (bucket, model). One ECharts series per model, so ECharts groups
  bars side by side within each category.
- `stack: 'total'` is forbidden.
- `trigger: 'item'` is required; `trigger: 'axis'` is forbidden.
- `emphasis: { focus: 'series' }` is forbidden.

Bar height comes from `metricValueFromRow(r)`, which honors the top-bar
Tokens / Cost / Requests switch the Intraday view already supported. In Tokens
mode that is `inputSide + output_tokens` via `tokenParts(raw).total`, matching
the main chart. Hard-coding the token total here would silently delete the
metric switch.

The gradient is the pinned two-segment form: a single series per bar, bottom
segment the whole input side in the model's base color, top segment Output in a
lightened mix, with `minVis = 0.06` so a small Output stays visible. It is
**shared, not copied** — the block is extracted from `chart-main.js` into
`makeTokenBarColor(model)` and imported by both charts, so the two can never
drift apart.

Tooltips reuse `buildBarTooltip` from `chart-main.js`. No new tooltip formatter
is written. `buildBarTooltip` already renders the pinned hierarchy — Total,
Input as a bold parent, Uncached / Cache Read / Cache Create as indented
children, then Output, Requests, Cost — and already falls back to the ECharts
category name for its header, which is what the bucket label provides.

`intradayLineTooltip` is deleted.

The x-axis is a category axis of `bucket_label` values, which the server already
renders as `"MM-DD HH:MM"` in local time and which therefore reads correctly
across day boundaries.

## Legend

The line chart being replaced carried a legend. An earlier revision of this spec
omitted it, the plan therefore never carried it, and the rewrite dropped it: the
delivered chart named no models and let none be filtered. Recorded here because
the omission is the defect — the rewrite described what to build without taking
inventory of what already existed.

The legend is not decoration. It is what makes a dense window readable: 192
slots x 11 models leaves each bar under a pixel, while a single isolated model
over the same window is comfortably wide. Without it the default window would
have to be narrowed to compensate, which is what forced the wrong default
described under dataZoom.

Behavior matches the Token Rate panel exactly, because one pattern reused beats
two that nearly agree:

- Clicking a legend entry isolates that model; clicking it again restores all.
- An `All models` button appears only while a model is isolated.
- A focus naming a model absent from the newly loaded range is dropped.
- Entries use a `roundRect` swatch in the model's own color from `getModelColor`.
- Past 7 models the legend switches to a single scrolling row, and the grid top
  moves from 44 to 56 so the plot never renders beneath it.

This is shared, not duplicated: the behavior is extracted from `panel-rate.js`
into `legend-focus.js` and consumed by both panels. Both call sites already used
identical values, so extraction changed no behavior. The module takes its focus
accessors and its button lookup by injection, keeping it free of both state
layout and the DOM, and therefore testable under `node --test`.

## Gap Filling

The SQL omits empty buckets. On a category axis this makes 01:55 and 09:00
adjacent when nothing ran between them, so the axis would misstate time.

Fill every empty bucket with zero so the axis is a true timeline. Build the slot
list from `bucket_start_unix`, which the API already returns as inclusive UTC
seconds, stepping by `bucket_minutes * 60` from the earliest to the latest
returned bucket. Index returned rows by `bucket_start_unix` and emit zero where
a (slot, model) pair is absent.

The fill spans the returned data's extent, not the requested date range. Padding
to the full range would append empty future slots — viewing Today at 14:00 with
5-minute buckets would render ten hours of blank axis. Internal gaps are the
problem being solved; leading and trailing emptiness is not.

Do not derive slots by parsing `bucket_label`. Reuse the server's `bucket_label`
for slots that carry data, and synthesize `"MM-DD HH:MM"` in local time only for
the filled gaps, so real rows keep the server's exact rendering.

The line chart's calendar-day break rule does not carry over. It existed to stop
an overnight diagonal from bridging two days; bars have no connecting segment,
so a continuous filled timeline is both correct and simpler.

## dataZoom

dataZoom is required. Without it, even a single day at 5-minute buckets renders
288 slots x 11 models at roughly 0.5 px per bar.

Follow `chart-main.js`, which enables zoom when categories exceed a constant and
computes an initial window percentage. Zoom is enabled only when the slot count
exceeds the window.

**The window is a duration, not a slot count.** An earlier revision specified a
fixed `visibleBuckets = 48`, reasoning only about pixels per bar. That is wrong:
48 slots is 4h at 5-minute buckets but a full day at 30-minute ones, so the
opening view's wall-clock width swung with the bucket. Since a 1-day span
recommends 5-minute buckets, Today opened on its last 4 hours and hid the other
20 behind a pan — the opposite of what a day view should do.

`DEFAULT_WINDOW_MINUTES = 16 * 60`, converted to slots by
`defaultVisibleSlots(bucketMinutes, totalSlots)`, which clamps to the data's own
extent so a short range shows whole rather than padding. The window holds 16h at
every bucket size.

16h rather than 24h because the window is right-anchored (see below) and real
work clusters in the day: for a range whose data runs to midnight this lands on
**08:00-24:00**, leaving the dead early hours one pan away instead of spending
the opening view on them. Verified against the dev database: 2026-07-16 and
2026-07-15 both open at 08:00, where the 48-slot rule opened at 20:00.

Anchoring to the data rather than to a literal 08:00 clock time is deliberate.
A hard 08:00 start would strand empty axis on days that end early, and would
hide a 03:00 session behind a pan with nothing on screen to suggest it exists.

Axis direction differs from the main chart, deliberately. `chart-main.js` sorts
dates descending so the newest day sits on the left, which suits a 7d/30d
comparison of discrete days. A time-of-day axis must read chronologically, so
the Intraday axis is ascending: earliest bucket on the left, latest on the
right. This also matches the current loader, which already sorts ascending by x.

The initial dataZoom window therefore anchors to the right edge — the most
recent buckets — rather than the left.

## Removals

- The `<table>` in `#daily-byhour` including its `<thead>` and `#hourly-tbody`.
- `renderIntradayRow` and its tests.
- `intradayLineTooltip` and its tests.
- The `INTRADAY_BUCKET_MIN` constant.

The empty-state message currently rendered into `#hourly-tbody` moves to the
chart area, since the table that hosted it no longer exists.

## Markup and Accessibility

- The bucket dropdown reuses the Token Rate trigger and menu markup, including
  its `aria-expanded` handling and `type="button"` triggers.
- The dropdown needs an accessible name identifying it as the Intraday bucket
  selector, so that two bucket dropdowns on one page remain distinguishable.
- The section hint changes from "One line per model · 30-min buckets · single
  day only" to wording describing per-bucket bars and the 7-day limit.
- Keyboard focus, hover, and pressed states follow the existing dropdown, which
  already satisfies `FRONTEND_TASTE.md`.

## Testing

Static and unit tests, run with `node --test`:

- `bucket-dropdown.test.mjs`: `normalizeBucket` accepts 5/10/15/30/60 and falls
  back to 30 for 0, 7, 45, negatives, `NaN`, and non-numbers;
  `recommendedBucketForSpan` returns 5 at spans 0 and 1 and 30 at 2 and 7.
- Gap filling: a pure helper mapping API rows plus a window to dense slots is
  extracted and tested directly — a sparse day yields zeros in the gaps, slot
  count matches `window / bucket`, and no slot is dropped or duplicated.
- Markup: `#daily-byhour` contains no `<table>` and no `#hourly-tbody`.
- Cache-busting: `style.css` and `app.js` version query strings are bumped.

Go tests:

- `GetIntradayStatsByModel` and `GetCodexIntradayStatsByModel` both accept 5 and
  10 and still reject 7 and 45.
- `Handler.Intraday` and `Handler.CodexIntraday` both echo `bucket_minutes: 5`
  for `bucket=5` rather than coercing to 30. This is the regression that would
  otherwise ship silently, and it must be asserted on the Codex path too — that
  path is reached by the same UI control whenever the Codex tab is active.
- The 7-day cap still returns 400 on both paths.
- The full Go suite runs because static assets are embedded.

Span-reset behavior is covered by the `recommendedBucketForSpan` unit test plus
a check that switching span resets a user-chosen bucket, since that is the guard
against a 5-minute choice leaking into a 7-day render.

## Verification

Per `FRONTEND_TASTE.md`, code that compiles is not a finished frontend change:

1. Run the focused and full static suites, and the full Go suite.
2. Rebuild and restart the development instance on ports 14317 and 18899. Never
   use production ports 4317/8899.
3. Load the page with a cache-busting query string.
4. Confirm every bucket from 5 to 60 on both a single-day and a 7-day range,
   dark and light themes, hover, keyboard focus, dropdown open and close, the
   30 Days and All Time disabled state, and responsive wrapping.
5. Repeat the bucket sweep with `?source=codex`, confirming the response
   `bucket_minutes` echoes 5 and 10 rather than 30.
5. Hover a bar and confirm Total equals Input plus Output, and that the Input
   parent equals the sum of Uncached, Cache Read, and Cache Create.
6. Capture at least one screenshot including the chart and its surrounding
   context.

## Non-goals

- No change to the Token Rate panel's behavior or appearance. It only stops
  owning the dropdown factory.
- No change to the By-day table.
- No change to bucketing SQL, indexes, or schema.
- No relaxation of the 7-day API cap.
- No URL synchronization for the bucket, matching `rateBucket`.
- No change to Codex token semantics. The Codex intraday whitelist widens to
  match Claude, and nothing else on that path changes.
