import { state, paging } from './state.js';
import { fmtNum, escapeHtml, rangeToFromTo } from './utils.js';
import { chartColors } from './theme.js';
import { loadDailyData, loadIntradayData, loadHoursData } from './api.js';
import { renderPagination } from './pagination.js';
import { tokenParts } from './token-math.js';
import { buildBarTooltip, makeTokenBarColor } from './chart-main.js';
import { fillIntradaySlots, defaultVisibleSlots } from './intraday-slots.js';
import { makeBucketDropdown, normalizeBucket, recommendedBucketForSpan } from './bucket-dropdown.js';
import { legendOption, legendGridTop, makeLegendFocus } from './legend-focus.js';
import { beginLatestRequest } from './latest-request.js';

const INTRADAY_MAX_DAYS = 7;

// Shared with the Rate chart — see legend-focus.js. Isolating one model is what
// makes a dense window readable: 192 slots x 11 models leaves each bar under a
// pixel, while one model over the same window is comfortably wide.
const intradayLegendFocus = makeLegendFocus({
    getFocus: () => state.intradayLegendFocus,
    setFocus: (v) => { state.intradayLegendFocus = v; },
    getAllBtn: () => document.getElementById('intraday-legend-all-btn'),
});

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

    document.getElementById('intraday-legend-all-btn')?.addEventListener('click', () => {
        if (state.hourlyChart) {
            const names = (state.hourlyChart.getOption().series || []).map((s) => s.name).filter(Boolean);
            intradayLegendFocus.clear(state.hourlyChart, names);
        }
    });
}

function spanDaysInclusive(from, to) {
    if (!from || !to) return 0;
    const f = Date.parse(from + 'T00:00:00');
    const t = Date.parse(to + 'T00:00:00');
    if (!Number.isFinite(f) || !Number.isFinite(t) || t < f) return 0;
    return Math.round((t - f) / 86400000) + 1;
}

function isIntradayRangeSelected() {
    const { from, to } = rangeToFromTo(state.currentRange);
    const span = spanDaysInclusive(from, to);
    return span >= 1 && span <= INTRADAY_MAX_DAYS;
}

export function updateDailyViewControls() {
    const dayBtn = document.getElementById('daily-view-day');
    const hourBtn = document.getElementById('daily-view-hour');
    const hourdayBtn = document.getElementById('daily-view-hourday');
    if (!dayBtn || !hourBtn) return;

    const canHour = isIntradayRangeSelected();
    hourBtn.disabled = !canHour;
    hourBtn.title = canHour
        ? 'Intraday view (per-bucket bars)'
        : `Intraday view supports up to ${INTRADAY_MAX_DAYS} days (Today / Yesterday / 7 Days / a custom span ≤ ${INTRADAY_MAX_DAYS} days)`;

    if (!canHour && state.dailyDetailView === 'hour') state.dailyDetailView = 'day';

    // /api/hours reads the merged non-Codex pool and has no codex mirror
    // yet, so the subtab disappears under the Codex source.
    const canHourday = state.source !== 'codex';
    if (hourdayBtn) hourdayBtn.style.display = canHourday ? '' : 'none';
    if (!canHourday && state.dailyDetailView === 'hourday') state.dailyDetailView = 'day';

    dayBtn.classList.toggle('active', state.dailyDetailView === 'day');
    hourBtn.classList.toggle('active', state.dailyDetailView === 'hour');
    // Hour-of-day works for ANY span (it folds the whole range into 24 bars),
    // so it stays enabled where Intraday gives up.
    hourdayBtn?.classList.toggle('active', state.dailyDetailView === 'hourday');

    const byDay = document.getElementById('daily-byday');
    const byHour = document.getElementById('daily-byhour');
    const byHourDay = document.getElementById('daily-byhourday');
    if (byDay) byDay.style.display = state.dailyDetailView === 'day' ? '' : 'none';
    if (byHour) byHour.style.display = state.dailyDetailView === 'hour' ? '' : 'none';
    if (byHourDay) byHourDay.style.display = state.dailyDetailView === 'hourday' ? '' : 'none';
}

export function setDailyDetailView(view) {
    state.dailyDetailView = (view === 'hour' || view === 'hourday') ? view : 'day';
    updateDailyViewControls();
    refreshDailyPanel();
}

export function refreshDailyPanel() {
    updateDailyViewControls();
    if (state.dailyDetailView === 'hour' && isIntradayRangeSelected()) {
        loadIntraday();
    } else if (state.dailyDetailView === 'hourday') {
        loadHourOfDay();
    } else {
        loadDailyTable();
    }
}

function metricLabel() {
    if (state.chartMetric === 'cost') return 'USD';
    if (state.chartMetric === 'requests') return 'Reqs';
    return 'Tokens';
}

function metricValueFromRow(r) {
    if (state.chartMetric === 'cost') return Number(r.cost_usd || 0);
    if (state.chartMetric === 'requests') return Number(r.request_count || 0);
    return tokenParts(r).total;
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

export async function loadIntraday() {
    const isCurrent = beginLatestRequest('daily-panel');
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
        if (isCurrent()) showIntradayMessage(chartEl, 'Failed to load intraday data.');
        return;
    }
    if (!isCurrent() || state.source !== sourceAtStart) return;

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

    // Open on a working day's worth of buckets; the rest is a pan away. See
    // DEFAULT_WINDOW_MINUTES in intraday-slots.js for why this is a duration
    // rather than a bar count.
    const visibleBuckets = defaultVisibleSlots(bucketMinutes, slots.length);
    const hasZoom = slots.length > visibleBuckets;
    // Ascending time: the newest buckets are on the right, so anchor there.
    const winPct = hasZoom ? Math.round((visibleBuckets / slots.length) * 100) : 100;

    if (!state.hourlyChart) {
        state.hourlyChart = echarts.init(chartEl, null, { renderer: 'canvas' });
        window.addEventListener('resize', () => state.hourlyChart && state.hourlyChart.resize());
    }
    state.hourlyChart.setOption({
        backgroundColor: 'transparent',
        // The legend sits outside the grid, so the plot must start below it.
        grid: { left: 56, right: 20, top: legendGridTop(models), bottom: hasZoom ? 56 : 32 },
        legend: legendOption(models, c, state.intradayLegendFocus),
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
    // After setOption: the notMerge above replaces the legend, so re-bind and
    // re-assert the focus against the models this range actually returned.
    intradayLegendFocus.bind(state.hourlyChart, models);
    intradayLegendFocus.apply(state.hourlyChart, models);
    state.hourlyChart.resize();
}

// ── Hour of Day: hour x client x model pivot ────────────────────────────
// The backend sends one row per (local hour, client, model) cell; this view
// pivots them into 24 stacked bars. Two dropdowns pick the lens:
//   - nothing pinned  -> bars split by MODEL (top N + Other)
//   - client pinned   -> that client's models, hour by hour
//   - model pinned    -> bars split by CLIENT ("who ran Claude Opus at 13:00")
//   - both pinned     -> single series ("Claude Code's Claude Opus at 13:00")
// That is the dimension Intraday cannot show: it is chronological per-bucket
// detail for a short span, while this folds the whole range by hour-of-day
// AND cross-cuts it by client/model.

const HOURDAY_TOP_N = 8;

function hourdayMetricOf(r, metric) {
    if (metric === 'cost') return Number(r.cost_usd) || 0;
    if (metric === 'requests') return Number(r.request_count) || 0;
    return Number(r.total_tokens) || 0;
}

function hourdayMergeCell(dst, r) {
    dst.input_tokens += Number(r.input_tokens) || 0;
    dst.output_tokens += Number(r.output_tokens) || 0;
    dst.cache_read_tokens += Number(r.cache_read_tokens) || 0;
    dst.cache_creation_tokens += Number(r.cache_creation_tokens) || 0;
    dst.total_tokens += Number(r.total_tokens) || 0;
    dst.cost_usd += Number(r.cost_usd) || 0;
    dst.request_count += Number(r.request_count) || 0;
    dst.hour = r.hour;
    return dst;
}

const hourdayEmptyCell = (hour) => ({
    hour, service_name: '', model: '', input_tokens: 0, output_tokens: 0,
    cache_read_tokens: 0, cache_creation_tokens: 0, total_tokens: 0,
    cost_usd: 0, request_count: 0,
});

// Pure pivot from (hour, service, model) rows to stacked series data.
// Exported for hourday.test.mjs — no DOM, no echarts.
export function buildHourdayView(rows, { client = '', model = '', metric = 'tokens' } = {}) {
    const list = Array.isArray(rows) ? rows : [];

    // Dropdown options come from the UNFILTERED rows so both selects stay
    // usable no matter which one is pinned.
    const clientTotals = new Map();
    const modelTotals = new Map();
    for (const r of list) {
        clientTotals.set(r.service_name, (clientTotals.get(r.service_name) || 0) + hourdayMetricOf(r, metric));
        modelTotals.set(r.model, (modelTotals.get(r.model) || 0) + hourdayMetricOf(r, metric));
    }
    const ranked = (m) => [...m.entries()].sort((a, b) => b[1] - a[1]).map(([name]) => name);

    const sel = list.filter((r) =>
        (!client || r.service_name === client) && (!model || r.model === model));

    // Pinning a model switches the breakdown axis to clients (the question
    // becomes "who used it"); anything else breaks down by model.
    const byModel = !model;
    const dimOf = (r) => (byModel ? r.model : r.service_name);

    const dimTotals = new Map();
    for (const r of sel) dimTotals.set(dimOf(r), (dimTotals.get(dimOf(r)) || 0) + hourdayMetricOf(r, metric));
    const dims = ranked(dimTotals);
    const top = dims.slice(0, HOURDAY_TOP_N);
    const rest = dims.slice(HOURDAY_TOP_N);
    const otherLabel = 'Other' + (rest.length ? ' (' + rest.length + ')' : '');
    const dimKey = (d) => (rest.includes(d) ? '__other__' : d);

    // cellByHour: hour -> dimKey -> merged cell + the OTHER dimension's
    // contributions (clients inside a model bar / models inside a client
    // bar), so tooltips can answer "which client's which model" inline.
    const cellByHour = new Map();
    for (const r of sel) {
        if (!cellByHour.has(r.hour)) cellByHour.set(r.hour, new Map());
        const m = cellByHour.get(r.hour);
        const key = dimKey(dimOf(r));
        let entry = m.get(key);
        if (!entry) { entry = { cell: hourdayEmptyCell(r.hour), parts: new Map() }; m.set(key, entry); }
        hourdayMergeCell(entry.cell, r);
        const sub = byModel ? r.service_name : r.model;
        entry.parts.set(sub, (entry.parts.get(sub) || 0) + hourdayMetricOf(r, metric));
    }

    const names = rest.length ? [...top, otherLabel] : [...top];
    const keyOf = (name) => (name === otherLabel ? '__other__' : name);
    const categories = Array.from({ length: 24 }, (_, h) => String(h).padStart(2, '0') + ':00');
    const series = names.map((name) => ({
        name,
        data: categories.map((_, h) => {
            const entry = cellByHour.get(h)?.get(keyOf(name));
            if (!entry) return { value: 0, raw: hourdayEmptyCell(h), parts: [] };
            const parts = [...entry.parts.entries()]
                .sort((a, b) => b[1] - a[1])
                .map(([nm, v]) => ({ name: nm, value: v }));
            return { value: hourdayMetricOf(entry.cell, metric), raw: entry.cell, parts };
        }),
    }));

    const sum = (fn) => sel.reduce((s, r) => s + fn(r), 0);
    return {
        categories,
        series,
        meta: { client, model, byModel },
        clients: ranked(clientTotals),
        models: ranked(modelTotals),
        totals: {
            tokens: sum((r) => Number(r.total_tokens) || 0),
            cost: sum((r) => Number(r.cost_usd) || 0),
            requests: sum((r) => Number(r.request_count) || 0),
        },
    };
}

// Rows of the currently loaded range, kept so the dropdowns can re-pivot
// without a refetch.
let hourdayRowsCache = [];

function populateHourdaySelects(view) {
    const fill = (el, values, selected, allLabel) => {
        if (!el) return;
        const keep = values.includes(selected) ? selected : '';
        el.innerHTML = '';
        const opt = document.createElement('option');
        opt.value = '';
        opt.textContent = allLabel;
        el.appendChild(opt);
        for (const v of values) {
            const o = document.createElement('option');
            o.value = v;
            o.textContent = v;
            el.appendChild(o);
        }
        el.value = keep;
    };
    fill(document.getElementById('hourday-client'), view.clients, state.hourdayClient, 'All Clients');
    fill(document.getElementById('hourday-model'), view.models, state.hourdayModel, 'All Models');
    state.hourdayClient = document.getElementById('hourday-client')?.value || '';
    state.hourdayModel = document.getElementById('hourday-model')?.value || '';
}

export async function loadHourOfDay() {
    const isCurrent = beginLatestRequest('daily-panel');
    const chartEl = document.getElementById('hourday-chart');
    if (!chartEl) return;

    const { from, to } = rangeToFromTo(state.currentRange);
    const sourceAtStart = state.source;
    let json;
    try {
        json = await loadHoursData({ from, to });
    } catch (e) {
        if (isCurrent()) showHourdayMessage(chartEl, 'Failed to load hour-of-day data.');
        return;
    }
    if (!isCurrent() || state.source !== sourceAtStart) return;

    const rows = Array.isArray(json.data) ? json.data : [];
    hourdayRowsCache = rows;
    const hasData = rows.some((r) => (Number(r.total_tokens) || 0) > 0 || (Number(r.request_count) || 0) > 0);
    if (!hasData) {
        showHourdayMessage(chartEl, 'No data in this range.');
        return;
    }
    renderHourdayChart(chartEl);
}

export function renderHourdayChart(chartEl) {
    const el = chartEl || document.getElementById('hourday-chart');
    if (!el) return;
    const rows = hourdayRowsCache;
    if (!rows.length) {
        showHourdayMessage(el, 'No data in this range.');
        return;
    }

    const view = buildHourdayView(rows, {
        client: state.hourdayClient,
        model: state.hourdayModel,
        metric: state.chartMetric === 'cost' ? 'cost' : state.chartMetric === 'requests' ? 'requests' : 'tokens',
    });
    populateHourdaySelects(view);

    const c = chartColors();
    const isCost = state.chartMetric === 'cost';
    const fmtVal = (v) => (isCost ? '$' + Number(v).toFixed(4) : fmtNum(v));
    // With a model pinned the whole chart IS that model, so every client bar
    // keeps the model's canonical color (same as the main chart) and the
    // rank position varies the shade — the model's color identity must not
    // change just because the breakdown axis flipped to clients.
    const shadeOf = (base, i) => {
        const m = /^#([0-9a-f]{6})$/i.exec(base);
        if (!m) return base;
        const a = Math.max(0.3, 1 - 0.12 * i);
        const r = parseInt(m[1].slice(0, 2), 16), g = parseInt(m[1].slice(2, 4), 16), b = parseInt(m[1].slice(4, 6), 16);
        return 'rgba(' + r + ',' + g + ',' + b + ',' + a.toFixed(2) + ')';
    };
    const pinnedModel = view.meta && view.meta.model && !view.meta.byModel ? makeTokenBarColor(view.meta.model) : null;
    const colorOf = (name, i) => pinnedModel
        ? shadeOf(pinnedModel, i)
        : (name.indexOf('Other') === 0 ? '#8e8e93' : makeTokenBarColor(name));

    // Grouped bars, never stacked — a pinned chart rule from .claude/CLAUDE.md:
    // stack distorts per-series magnitudes. Up to 9 series x 24 hours stay
    // readable with slim bars.
    const series = view.series.map((s, i) => ({
        name: s.name,
        type: 'bar',
        barMaxWidth: 16,
        itemStyle: { color: colorOf(s.name, i) },
        data: s.data,
    }));

    if (!state.hourdayChart) {
        // Clear any placeholder message BEFORE init: echarts.init() mounts its
        // own canvas DOM inside the container, and wiping innerHTML afterwards
        // would delete it and leave a permanently blank chart (the bug behind
        // the 'nothing renders' report — pinned by hourday-render.test.mjs).
        el.innerHTML = '';
        state.hourdayChart = echarts.init(el, null, { renderer: 'canvas' });
        window.addEventListener('resize', () => state.hourdayChart && state.hourdayChart.resize());
    }
    state.hourdayChart.setOption({
        backgroundColor: 'transparent',
        grid: { left: 56, right: 20, top: 44, bottom: 32 },
        legend: { top: 4, type: 'scroll', textStyle: { color: c.legendText } },
        tooltip: {
            // trigger: 'item' is a pinned chart rule (.claude/CLAUDE.md):
            // hovering a bar shows only its own info, never a merged
            // all-series tooltip.
            trigger: 'item',
            backgroundColor: c.tooltipBg,
            borderColor: c.tooltipBorder,
            textStyle: { color: c.tooltipText },
            formatter: (params) => buildHourdayTooltip(params, view, c),
        },
        xAxis: {
            type: 'category',
            data: view.categories,
            axisLine: { lineStyle: { color: c.axisLine } },
            axisLabel: { color: c.axisLabel },
        },
        yAxis: {
            type: 'value',
            name: metricLabel(),
            nameTextStyle: { color: c.axisLabel },
            axisLabel: { color: c.axisLabel, formatter: (v) => fmtVal(v) },
            splitLine: { lineStyle: { color: c.splitLine } },
        },
        series,
    }, true);
    state.hourdayChart.resize();
}

function buildHourdayTooltip(params, view, c) {
    // trigger:'item' hands the formatter a single param object; accept both
    // shapes but only ever render the hovered bar's own data.
    const p = Array.isArray(params) ? params[0] : params;
    if (!p) return '';
    const hour = Number(p.data?.raw?.hour ?? p.dataIndex ?? 0);
    const scope = view.meta ? [view.meta.client, view.meta.model].filter(Boolean).map(escapeHtml).join(' · ') : '';
    const head = '<div style="font-weight:600;margin-bottom:4px">'
        + String(hour).padStart(2, '0') + ':00 – ' + String((hour + 1) % 24).padStart(2, '0') + ':00'
        + (scope ? ' · ' + scope : '') + '</div>';
    const row = (k, v, color) => '<div style="display:flex;justify-content:space-between;gap:16px">'
        + '<span style="color:' + (color || c.legendText) + '">' + k + '</span><span>' + v + '</span></div>';

    const isCost = state.chartMetric === 'cost';
    const isReqs = state.chartMetric === 'requests';
    const fmtVal = (v) => (isCost ? '$' + Number(v).toFixed(4) : fmtNum(v));

    // Own-bar-only content (pinned rule: item tooltips show the hovered
    // bar's info, never the merged hour). The bar itself is one model (or one
    // client); its cross-dimension parts answer "which client's which model".
    const r = p.data?.raw || hourdayEmptyCell(hour);
    if (!(p.value > 0)) return head + row('No data', '—');

    let html = head + row(p.marker + ' ' + escapeHtml(p.seriesName), '<b>' + fmtVal(p.value) + '</b>');

    const subs = p.data?.parts || [];
    if (subs.length && view.series.length > 1) {
        const total = subs.reduce((s, x) => s + x.value, 0);
        const txt = subs.slice(0, 3)
            .map((x) => escapeHtml(x.name) + ' ' + (total > 0 ? Math.round((x.value / total) * 100) : 0) + '%')
            .join(' · ');
        html += '<div style="margin:-2px 0 2px 16px;font-size:11px;color:' + c.axisLabel + '">' + txt + '</div>';
    }

    // Both filters pinned -> the single series IS "this client's this model",
    // so show the full token composition (same column names as Daily Detail).
    if (view.series.length === 1 && !isCost && !isReqs) {
        html += row('Uncached Input', fmtNum(r.input_tokens))
            + row('Cache Read', fmtNum(r.cache_read_tokens))
            + row('Cache Create', fmtNum(r.cache_creation_tokens))
            + row('Output', fmtNum(r.output_tokens));
    }

    return html
        + '<div style="border-top:1px solid ' + c.tooltipBorder + ';margin:4px 0"></div>'
        + row('Requests', fmtNum(r.request_count))
        + row('Cost', '$' + (Number(r.cost_usd) || 0).toFixed(4));
}

function showHourdayMessage(chartEl, msg) {
    if (state.hourdayChart) {
        state.hourdayChart.dispose();
        state.hourdayChart = null;
    }
    hourdayRowsCache = [];
    chartEl.innerHTML = '<div style="text-align:center;color:var(--text-muted);padding:48px 24px">' + msg + '</div>';
}
function showIntradayMessage(chartEl, msg) {
    if (state.hourlyChart) {
        state.hourlyChart.dispose();
        state.hourlyChart = null;
    }
    chartEl.innerHTML = `<div style="text-align:center;color:var(--text-muted);padding:48px 24px">${msg}</div>`;
}

export async function loadDailyTable() {
    const isCurrent = beginLatestRequest('daily-panel');
    const sourceAtStart = state.source;
    const { from, to } = rangeToFromTo(state.currentRange);
    const { page, pageSize } = paging.daily;
    try {
        const json = await loadDailyData({ from, to, page, pageSize, granularity: state.chartGranularity });
        if (!isCurrent() || state.source !== sourceAtStart) return;
        paging.daily.total = json.total || 0;
        const rows = json.data || [];
        const tbody = document.getElementById('daily-tbody');
        if (rows.length === 0) {
            tbody.innerHTML = '<tr><td colspan="8" style="text-align:center;color:var(--text-muted);padding:32px">No data</td></tr>';
            renderPagination('daily-pagination', paging.daily, loadDailyTable);
            return;
        }
        tbody.innerHTML = rows.map(renderDailyRow).join('');
        renderPagination('daily-pagination', paging.daily, loadDailyTable);
    } catch (e) { if (isCurrent()) console.error('daily table:', e); }
}

export function initPanelDaily() {
    document.getElementById('daily-view-day')?.addEventListener('click', () => setDailyDetailView('day'));
    document.getElementById('daily-view-hour')?.addEventListener('click', () => setDailyDetailView('hour'));
    document.getElementById('daily-view-hourday')?.addEventListener('click', () => setDailyDetailView('hourday'));
    // Re-pivot on dropdown change from the cached rows — no refetch needed.
    document.getElementById('hourday-client')?.addEventListener('change', (e) => {
        state.hourdayClient = e.target.value || '';
        renderHourdayChart();
    });
    document.getElementById('hourday-model')?.addEventListener('change', (e) => {
        state.hourdayModel = e.target.value || '';
        renderHourdayChart();
    });
}
