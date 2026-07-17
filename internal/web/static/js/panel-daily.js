import { state, paging } from './state.js';
import { fmtNum, escapeHtml, rangeToFromTo } from './utils.js';
import { chartColors } from './theme.js';
import { loadDailyData, loadIntradayData } from './api.js';
import { renderPagination } from './pagination.js';
import { tokenParts } from './token-math.js';
import { buildBarTooltip, makeTokenBarColor } from './chart-main.js';
import { fillIntradaySlots, defaultVisibleSlots } from './intraday-slots.js';
import { makeBucketDropdown, normalizeBucket, recommendedBucketForSpan } from './bucket-dropdown.js';
import { legendOption, legendGridTop, makeLegendFocus } from './legend-focus.js';

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
    if (!dayBtn || !hourBtn) return;

    const canHour = isIntradayRangeSelected();
    hourBtn.disabled = !canHour;
    hourBtn.title = canHour
        ? 'Intraday view (per-bucket bars)'
        : `Intraday view supports up to ${INTRADAY_MAX_DAYS} days (Today / Yesterday / 7 Days / a custom span ≤ ${INTRADAY_MAX_DAYS} days)`;

    if (!canHour && state.dailyDetailView === 'hour') state.dailyDetailView = 'day';

    dayBtn.classList.toggle('active', state.dailyDetailView === 'day');
    hourBtn.classList.toggle('active', state.dailyDetailView === 'hour');

    const byDay = document.getElementById('daily-byday');
    const byHour = document.getElementById('daily-byhour');
    if (byDay) byDay.style.display = state.dailyDetailView === 'day' ? '' : 'none';
    if (byHour) byHour.style.display = state.dailyDetailView === 'hour' ? '' : 'none';
}

export function setDailyDetailView(view) {
    state.dailyDetailView = view === 'hour' ? 'hour' : 'day';
    updateDailyViewControls();
    refreshDailyPanel();
}

export function refreshDailyPanel() {
    updateDailyViewControls();
    if (state.dailyDetailView === 'hour' && isIntradayRangeSelected()) {
        loadIntraday();
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

function showIntradayMessage(chartEl, msg) {
    if (state.hourlyChart) {
        state.hourlyChart.dispose();
        state.hourlyChart = null;
    }
    chartEl.innerHTML = `<div style="text-align:center;color:var(--text-muted);padding:48px 24px">${msg}</div>`;
}

export async function loadDailyTable() {
    const { from, to } = rangeToFromTo(state.currentRange);
    const { page, pageSize } = paging.daily;
    try {
        const json = await loadDailyData({ from, to, page, pageSize, granularity: state.chartGranularity });
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
    } catch (e) { console.error('daily table:', e); }
}

export function initPanelDaily() {
    document.getElementById('daily-view-day')?.addEventListener('click', () => setDailyDetailView('day'));
    document.getElementById('daily-view-hour')?.addEventListener('click', () => setDailyDetailView('hour'));
}
