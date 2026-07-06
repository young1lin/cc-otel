import { state } from './state.js';
import { fmtNum, escapeHtml, rangeToFromTo } from './utils.js';
import { chartColors, getModelColor } from './theme.js';
import { loadRateData } from './api.js';

const MAX_DAYS = 7;

// Model whose line the cursor is currently nearest to (set by bindHoverIsolate).
// The axis tooltip receives every series at the hovered x; we render only this one.
let hoverModel = null;

function spanDaysInclusive(from, to) {
    if (!from || !to) return 1; // "today"/open range → treat as single day
    const f = Date.parse(from + 'T00:00:00');
    const t = Date.parse(to + 'T00:00:00');
    if (!Number.isFinite(f) || !Number.isFinite(t) || t < f) return 0;
    return Math.round((t - f) / 86400000) + 1;
}

const BUCKET_CHOICES = new Set([5, 15, 30, 60]);

function normalizeBucket(n) {
    return BUCKET_CHOICES.has(n) ? n : 30;
}

function recommendedBucketForSpan(spanDays) {
    return spanDays <= 1 ? 5 : 30;
}

function syncBucketSelect(bucket) {
    const sel = document.getElementById('rate-bucket');
    if (sel && sel.value !== String(bucket)) sel.value = String(bucket);
}

// Which RateBucket field to plot, from the two toggles.
function rateField() {
    const m = state.rateMethod === 'avg' ? 'avg' : 'weighted';
    const tok = state.rateTokens === 'total' ? 'total' : 'out';
    return `${m}_${tok}_per_s`;
}

function yAxisLabel() {
    const m = state.rateMethod === 'avg' ? 'avg' : 'weighted';
    const tok = state.rateTokens === 'total' ? 'Total' : 'Out';
    return `${tok} tok/s (${m})`;
}

function showMessage(chartEl, msg) {
    if (state.rateChart) { state.rateChart.dispose(); state.rateChart = null; }
    chartEl.innerHTML = `<div style="display:flex;align-items:center;justify-content:center;height:100%;color:var(--text-muted);padding:24px;text-align:center">${escapeHtml(msg)}</div>`;
}

function rateTooltip(params, c) {
    const list = Array.isArray(params) ? params : [params];
    let p = hoverModel
        ? list.find(x => x && x.data && x.data.raw && x.data.raw.model === hoverModel)
        : null;
    if (hoverModel && !p) return '';
    if (!p) p = list.find(x => x && x.data && x.data.raw) || list[0];
    if (!p || !p.data || !p.data.raw) return '';
    const raw = p.data.raw;
    const color = typeof p.color === 'string' ? p.color : getModelColor(raw.model || 'Unknown');
    const swatch = `<span style="display:inline-block;width:8px;height:8px;border-radius:2px;background:${color};margin-right:6px;vertical-align:middle"></span>`;
    const val = Number(p.data.value[1] || 0);
    const secs = Number(raw.duration_ms_sum || 0) / 1000;
    const row = (k, v) => `<tr><td style="color:${c.mutedText};padding:2px 0">${k}</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${v}</td></tr>`;
    return `<div style="margin-bottom:6px;font-weight:600;color:${c.tooltipText}">${escapeHtml(String(raw.bucket_label || ''))}</div>` +
        `<div style="margin-bottom:8px;font-weight:600">${swatch}<span style="color:${color}">${escapeHtml(raw.model || 'Unknown')}</span></div>` +
        `<table style="width:100%;font-size:12px;border-collapse:collapse">` +
        `<tr><td style="color:${c.tooltipText};padding:2px 0;font-weight:600">${escapeHtml(yAxisLabel())}</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0;font-weight:600">${val.toFixed(1)}</td></tr>` +
        row('Weighted out', Number(raw.weighted_out_per_s || 0).toFixed(1)) +
        row('Avg out', Number(raw.avg_out_per_s || 0).toFixed(1)) +
        row('Output tok', fmtNum(raw.out_tokens)) +
        row('Total tok', fmtNum(raw.total_tokens)) +
        row('API time', secs.toFixed(1) + 's') +
        row('Requests', fmtNum(raw.request_count)) +
        `</table>`;
}

// Snap cursor time (ms) to the same bucket floor used by the backend:
// bucket_start = timestamp - (timestamp % bucketSec).
function snapBucketMs(tMs, bucketMinutes) {
    const bucketSec = bucketMinutes * 60;
    const tSec = Math.floor(tMs / 1000);
    return (tSec - (tSec % bucketSec)) * 1000;
}

function localDateKey(tsMs) {
    const d = new Date(tsMs);
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${d.getFullYear()}-${m}-${day}`;
}

// Intraday (1 day): connect all points — continuous within the day.
// Multi-day: connect within each calendar day only; no overnight diagonal bridges.
function rateSeriesData(sortedPoints, spanDays) {
    if (spanDays <= 1 || sortedPoints.length <= 1) return sortedPoints;
    const out = [sortedPoints[0]];
    for (let i = 1; i < sortedPoints.length; i++) {
        const prev = sortedPoints[i - 1].value[0];
        const cur = sortedPoints[i].value[0];
        if (localDateKey(prev) !== localDateKey(cur)) out.push('-');
        out.push(sortedPoints[i]);
    }
    return out;
}

function legendSelectedMap(models, focus) {
    const sel = {};
    for (const m of models) sel[m] = !focus || m === focus;
    return sel;
}

function syncRateLegendAllBtn() {
    const btn = document.getElementById('rate-legend-all-btn');
    if (btn) btn.style.display = state.rateLegendFocus ? '' : 'none';
}

function applyLegendFocus(chart, models) {
    if (state.rateLegendFocus && !models.includes(state.rateLegendFocus)) {
        state.rateLegendFocus = null;
    }
    chart.__rateLegendApplying = true;
    chart.setOption({ legend: { selected: legendSelectedMap(models, state.rateLegendFocus) } });
    chart.__rateLegendApplying = false;
    syncRateLegendAllBtn();
    refreshRateHover(chart);
}

function bindLegendIsolate(chart, models) {
    chart.off('legendselectchanged');
    chart.on('legendselectchanged', (params) => {
        if (chart.__rateLegendApplying) return;
        const name = params.name;
        state.rateLegendFocus = state.rateLegendFocus === name ? null : name;
        applyLegendFocus(chart, models);
    });
}

// Make the whole line hoverable: snap to the time bucket under the cursor, then
// pick the nearest line **among models that actually have data in that bucket**.
// This avoids matching a stale point from another hour when the model was idle.
function bindHoverIsolate(chart, seriesMeta, bucketMinutes) {
    const zr = chart.getZr();
    if (chart.__rateMove) zr.off('mousemove', chart.__rateMove);
    if (chart.__rateOut) zr.off('globalout', chart.__rateOut);

    // Dim every line except `active` (pass -1 to reset). Driven through setOption so
    // ECharts treats it as base style, not a transient emphasis state it may revert.
    const applyDim = (active) => {
        chart.setOption({
            series: seriesMeta.map(m => ({
                lineStyle: {
                    opacity: active === -1 || m.idx === active ? 1 : 0,
                    width: m.idx === active ? 3 : 2,
                },
            })),
        });
    };

    let curS = -1;
    const clear = () => {
        if (curS === -1 && hoverModel === null) return;
        hoverModel = null;
        if (curS !== -1) applyDim(-1);
        chart.dispatchAction({ type: 'hideTip' });
        curS = -1;
    };
    const onMove = (e) => {
        const x = e.offsetX, y = e.offsetY;
        if (!chart.containPixel({ gridIndex: 0 }, [x, y])) { clear(); return; }
        const t = chart.convertFromPixel({ xAxisIndex: 0 }, x);
        const bucketMs = snapBucketMs(t, bucketMinutes);

        const candidates = [];
        for (const m of seriesMeta) {
            const j = m.byBucket.get(bucketMs);
            if (j === undefined) continue;
            const px = chart.convertToPixel({ seriesIndex: m.idx }, m.pts[j]);
            if (!px) continue;
            const dy = px[1] - y;
            candidates.push({ d: dy * dy, s: m.idx, name: m.name });
        }
        if (candidates.length === 0) { clear(); return; }
        // One model in this bucket → show it. Several → pick by vertical distance.
        const best = candidates.length === 1
            ? candidates[0]
            : candidates.reduce((a, b) => (a.d <= b.d ? a : b));
        if (best.s !== curS) {
            applyDim(best.s);
            curS = best.s;
        }
        hoverModel = best.name;
        // Axis tooltip anchored to the cursor; the formatter renders only hoverModel.
        chart.dispatchAction({ type: 'showTip', x, y });
    };

    chart.__rateMove = onMove;
    chart.__rateOut = clear;
    zr.on('mousemove', onMove);
    zr.on('globalout', clear);
}

export async function loadRate() {
    const chartEl = document.getElementById('rate-chart');
    if (!chartEl) return;

    const { from, to } = rangeToFromTo(state.currentRange);
    const span = spanDaysInclusive(from, to);
    if (span < 1) { showMessage(chartEl, 'Invalid date range.'); return; }
    if (span > MAX_DAYS) {
        showMessage(chartEl, `Rate view supports up to ${MAX_DAYS} days. Pick a shorter range (Today / 7 Days / a custom span ≤ 7 days).`);
        return;
    }

    if (state.rateSpan !== span) {
        state.rateSpan = span;
        state.rateBucket = recommendedBucketForSpan(span);
        state.rateLegendFocus = null;
    }
    const useBucket = normalizeBucket(state.rateBucket);
    syncBucketSelect(useBucket);
    state.rateBucket = useBucket;

    const sourceAtStart = state.source;
    let json;
    try {
        json = await loadRateData({ from, to, bucket: useBucket });
    } catch (e) {
        console.error('rate:', e);
        showMessage(chartEl, 'Failed to load rate data.');
        return;
    }
    if (sourceAtStart !== state.source) return;

    const rows = (json && json.data) || [];
    if (rows.length === 0) { showMessage(chartEl, 'No data in this range.'); return; }

    const field = rateField();
    const byModel = new Map();
    for (const r of rows) {
        const model = r.model || 'Unknown';
        if (!byModel.has(model)) byModel.set(model, []);
        byModel.get(model).push({ value: [Number(r.bucket_start_unix) * 1000, Number(r[field] || 0)], raw: r });
    }
    const models = [...byModel.keys()].sort((a, b) => a.localeCompare(b));

    const c = chartColors();
    const series = models.map(model => {
        const color = getModelColor(model);
        const sorted = byModel.get(model).sort((a, b) => a.value[0] - b.value[0]);
        const points = rateSeriesData(sorted, span);
        const intraday = span <= 1;
        return {
            name: model,
            type: 'line',
            smooth: intraday ? 0.25 : false,
            smoothMonotone: intraday ? 'x' : undefined,
            showSymbol: intraday,
            symbol: 'circle',
            symbolSize: 5,
            sampling: intraday ? undefined : 'lttb',
            // Isolate-on-hover is done by dimming the other lines via setOption (see
            // bindHoverIsolate). ECharts' own hover emphasis is disabled so its state
            // machine can't fight / reset that dim as the cursor moves.
            emphasis: { disabled: true },
            lineStyle: { color, width: 2 },
            itemStyle: { color },
            data: points,
        };
    });

    const span1 = span > 1;
    const useScrollLegend = models.length > 7;
    const gridTop = useScrollLegend ? 56 : 44;
    const option = {
        backgroundColor: c.bg,
        grid: { left: 60, right: 24, top: gridTop, bottom: 56, containLabel: true },
        tooltip: {
            // Axis trigger + manual control (triggerOn:'none'): bindHoverIsolate drives
            // showTip from the cursor so the whole line is hoverable, not just its points.
            // triggerEmphasis:false stops the axis pointer from re-highlighting every
            // series, so our single-series focus (fade the rest) survives.
            trigger: 'axis', triggerOn: 'none', confine: false, appendToBody: true,
            axisPointer: { type: 'line', snap: true, triggerEmphasis: false, lineStyle: { color: c.axisLine, width: 1, type: 'dashed' }, label: { show: false } },
            backgroundColor: c.tooltipBg, borderColor: c.tooltipBorder, borderWidth: 1,
            borderRadius: 10, padding: [12, 14], textStyle: { color: c.tooltipText, fontSize: 12 },
            extraCssText: `box-shadow: ${c.shadow};`,
            formatter(params) { return rateTooltip(params, c); },
        },
        legend: {
            type: useScrollLegend ? 'scroll' : 'plain', top: 4, left: 'center', width: '92%',
            selectedMode: 'multiple',
            itemGap: 14, itemHeight: 10, textStyle: { color: c.legendText, fontSize: 11 },
            data: models.map(m => ({ name: m, icon: 'roundRect', itemStyle: { color: getModelColor(m) } })),
            selected: legendSelectedMap(models, state.rateLegendFocus),
        },
        xAxis: {
            type: 'time',
            axisLabel: {
                color: c.axisLabel, fontSize: 11, hideOverlap: true,
                formatter: span1 ? { day: '{MM}-{dd}', hour: '{HH}:{mm}' } : { hour: '{HH}:{mm}', minute: '{HH}:{mm}' },
            },
            axisLine: { lineStyle: { color: c.axisLine } },
            axisTick: { lineStyle: { color: c.axisLine } },
            splitLine: { show: false },
        },
        yAxis: {
            type: 'value', name: yAxisLabel(), nameTextStyle: { color: c.axisLabel, fontSize: 11 },
            axisLabel: { color: c.axisLabel, fontSize: 11, formatter: v => fmtNum(v) },
            splitLine: { lineStyle: { color: c.splitLine } },
        },
        dataZoom: [
            { type: 'inside', xAxisIndex: 0, start: 0, end: 100, zoomOnMouseWheel: 'shift', moveOnMouseWheel: false, moveOnMouseMove: true },
            {
                type: 'slider', xAxisIndex: 0, start: 0, end: 100, height: 14, bottom: 4,
                borderColor: c.dzBorder, backgroundColor: c.dzBg, fillerColor: c.dzFill,
                handleStyle: { color: c.dzHandle, borderColor: c.dzHandle }, moveHandleStyle: { color: c.dzHandle },
                selectedDataBackground: { lineStyle: { color: c.dzSelLine }, areaStyle: { color: c.dzSelArea } },
                dataBackground: { lineStyle: { color: c.dzBgLine }, areaStyle: { color: c.dzBgArea } },
                textStyle: { color: c.legendText, fontSize: 10 },
            },
        ],
        series,
    };

    if (chartEl.querySelector('div') && !state.rateChart) chartEl.innerHTML = '';
    if (!state.rateChart) {
        state.rateChart = echarts.init(chartEl, null, { renderer: 'canvas' });
        window.addEventListener('resize', () => state.rateChart && state.rateChart.resize());
    }
    state.rateChart.setOption(option, true);
    state.rateChart.resize();

    // Whole-line hover: highlight the nearest line + show its rate anywhere,
    // not just when the cursor lands exactly on a data point.
    const seriesMeta = models.map((model, idx) => {
        const sorted = byModel.get(model).sort((a, b) => a.value[0] - b.value[0]);
        const pts = sorted.map(p => p.value);
        const byBucket = new Map(sorted.map((p, i) => [p.value[0], i]));
        return { idx, name: model, pts, byBucket };
    });
    const hoverMeta = state.rateLegendFocus
        ? seriesMeta.filter(m => m.name === state.rateLegendFocus)
        : seriesMeta;
    bindLegendIsolate(state.rateChart, models);
    applyLegendFocus(state.rateChart, models);
    bindHoverIsolate(state.rateChart, hoverMeta, useBucket);
    state.rateChart.__rateSeriesMeta = seriesMeta;
    state.rateChart.__rateBucketMin = useBucket;
}

function refreshRateHover(chart) {
    if (!chart || !chart.__rateSeriesMeta) return;
    const hoverMeta = state.rateLegendFocus
        ? chart.__rateSeriesMeta.filter(m => m.name === state.rateLegendFocus)
        : chart.__rateSeriesMeta;
    bindHoverIsolate(chart, hoverMeta, chart.__rateBucketMin || 30);
}

export function initPanelRate() {
    const methodSeg = document.querySelector('#panel-rate .rate-seg');
    methodSeg?.querySelectorAll('button[data-method]').forEach(btn => {
        btn.addEventListener('click', () => {
            methodSeg.querySelectorAll('button[data-method]').forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            state.rateMethod = btn.dataset.method === 'avg' ? 'avg' : 'weighted';
            loadRate();
        });
    });
    document.getElementById('rate-tokens')?.addEventListener('change', (e) => {
        state.rateTokens = e.target.value === 'total' ? 'total' : 'out';
        loadRate();
    });
    document.getElementById('rate-bucket')?.addEventListener('change', (e) => {
        state.rateBucket = normalizeBucket(parseInt(e.target.value, 10));
        loadRate();
    });
    document.getElementById('rate-legend-all-btn')?.addEventListener('click', () => {
        state.rateLegendFocus = null;
        if (state.rateChart) {
            const names = (state.rateChart.getOption().series || []).map(s => s.name).filter(Boolean);
            applyLegendFocus(state.rateChart, names);
        }
    });
    document.getElementById('rate-refresh-btn')?.addEventListener('click', () => loadRate());

    // Lazy-load: only fetch/render when the Rate tab is opened (chart needs a
    // visible container to size correctly), then resize on re-activation.
    document.querySelector('.panel-btn[data-panel="rate"]')?.addEventListener('click', () => {
        loadRate();
    });
}
