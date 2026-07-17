import { state, paging } from './state.js';
import { fmtNum, escapeHtml, rangeToFromTo } from './utils.js';
import { chartColors, getModelColor } from './theme.js';
import { loadDailyData, loadIntradayData } from './api.js';
import { renderPagination } from './pagination.js';
import { tokenParts } from './token-math.js';

const INTRADAY_BUCKET_MIN = 30;
const INTRADAY_MAX_DAYS = 1;

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
        ? `Intraday view (${INTRADAY_BUCKET_MIN}-min buckets)`
        : `Intraday view requires a single day (Today / Yesterday / a single custom date)`;

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

export function intradayLineTooltip(params, c) {
    const list = Array.isArray(params) ? params : [params];
    const p = list.find((x) => x && x.data && x.data.raw)
        || list.find((x) => x && x.data)
        || list[0];
    if (!p || !p.data) return '';
    const raw = p.data.raw;
    if (!raw) return '';

    const parts = tokenParts(raw);
    const cost = Number(raw.cost_usd || 0);
    const reqs = Number(raw.request_count || 0);
    const sub = 'padding:2px 0 2px 16px;font-size:11px';
    const cacheCreateRow =
        '<tr><td style="color:' + c.mutedText + ';' + sub +
        '">Cache Create</td><td style="font-family:var(--font-mono);text-align:right;' + sub + '">' +
        fmtNum(parts.cacheCreate) + '</td></tr>';
    const header = escapeHtml(String(raw.bucket_label || ''));
    const modelColor = typeof p.color === 'string' ? p.color : getModelColor(raw.model || 'Unknown');
    const modelName = raw.model || p.seriesName || 'Unknown';
    const swatch = `<span style="display:inline-block;width:8px;height:8px;border-radius:2px;background:${modelColor};margin-right:6px;vertical-align:middle"></span>`;

    return `<div style="margin-bottom:6px;font-weight:600;color:${c.tooltipText}">${header}</div>` +
        `<div style="color:${c.tooltipText};font-weight:600;margin-bottom:8px">${swatch}<span style="color:${modelColor}">${escapeHtml(modelName)}</span></div>` +
        `<table style="width:100%;font-size:12px;border-collapse:collapse">` +
        `<tr><td style="color:${c.mutedText};padding:2px 0">Total</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0;font-weight:600">${fmtNum(parts.total)}</td></tr>` +
        `<tr><td colspan="2" style="height:4px"></td></tr>` +
        `<tr><td style="color:${c.tooltipText};padding:2px 0;font-weight:600">Input</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0;font-weight:600">${fmtNum(parts.inputSide)}</td></tr>` +
        `<tr><td style="color:${c.mutedText};${sub}">Uncached</td><td style="font-family:var(--font-mono);text-align:right;${sub}">${fmtNum(parts.uncachedInput)}</td></tr>` +
        `<tr><td style="color:${c.mutedText};${sub}">Cache Read</td><td style="font-family:var(--font-mono);text-align:right;${sub}">${fmtNum(parts.cacheRead)}</td></tr>` +
        cacheCreateRow +
        `<tr><td colspan="2" style="height:4px"></td></tr>` +
        `<tr><td style="color:${c.mutedText};padding:2px 0">Output</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${fmtNum(parts.output)}</td></tr>` +
        `<tr><td colspan="2" style="height:4px"></td></tr>` +
        `<tr><td style="color:${c.mutedText};padding:2px 0">Requests</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${fmtNum(reqs)}</td></tr>` +
        `<tr><td style="color:${c.mutedText};padding:2px 0">Cost</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">$${cost.toFixed(4)}</td></tr>` +
        `</table>`;
}

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

export async function loadIntraday() {
    const { from, to } = rangeToFromTo(state.currentRange);
    const tbody = document.getElementById('hourly-tbody');
    const chartEl = document.getElementById('hourly-chart');
    if (!tbody || !chartEl) return;

    const span = spanDaysInclusive(from, to);
    if (span < 1 || span > INTRADAY_MAX_DAYS) {
        tbody.innerHTML = `<tr><td colspan="10" style="text-align:center;color:var(--text-muted);padding:24px">Intraday view is only available for a single day (Today / Yesterday / a single custom date).</td></tr>`;
        return;
    }

    try {
        const json = await loadIntradayData({ from, to, bucket: INTRADAY_BUCKET_MIN });
        const rows = json.data || [];

        // Group by model -> Map<bucketStartUnix, row>
        const byModel = new Map();
        for (const r of rows) {
            const model = r.model || 'Unknown';
            if (!byModel.has(model)) byModel.set(model, new Map());
            byModel.get(model).set(Number(r.bucket_start_unix) * 1000, r);
        }
        const models = [...byModel.keys()].sort((a, b) => a.localeCompare(b));

        if (rows.length === 0) {
            tbody.innerHTML = '<tr><td colspan="10" style="text-align:center;color:var(--text-muted);padding:24px">No data in this range</td></tr>';
        } else {
            const sortedRows = [...rows].sort((a, b) => {
                const ts = Number(a.bucket_start_unix) - Number(b.bucket_start_unix);
                if (ts !== 0) return ts;
                return metricValueFromRow(b) - metricValueFromRow(a);
            });
            tbody.innerHTML = sortedRows.map(renderIntradayRow).join('');
        }

        const isCost = state.chartMetric === 'cost';
        const c = chartColors();

        const series = models.map(model => {
            const map = byModel.get(model);
            const color = getModelColor(model);
            // One line per model: keep only buckets where this model actually
            // has data, sorted by time. ECharts then draws straight segments
            // between consecutive points — no `connectNulls` games, no
            // sparse-grid stair-step artifacts. The trade-off is that a long
            // idle gap is bridged by a single diagonal segment; for token
            // usage that reads as "model wasn't used in between", which is
            // correct.
            const points = [...map.entries()]
                .sort((a, b) => a[0] - b[0])
                .map(([t, r]) => ({
                    value: [t, metricValueFromRow(r)],
                    raw: r,
                }));
            return {
                name: model,
                type: 'line',
                smooth: false,
                showSymbol: true,
                symbol: 'circle',
                symbolSize: 5,
                sampling: 'lttb',
                // Make the entire line hit-testable instead of just the point
                // markers — without this, mousing over the segment between
                // two markers does nothing under trigger:'item'.
                triggerLineEvent: true,
                lineStyle: { color, width: 2.4 },
                itemStyle: { color },
                emphasis: {
                    focus: 'none',
                    scale: 1.8,
                    lineStyle: { width: 3.2 },
                },
                data: points,
            };
        });

        const useScrollLegend = models.length > 7;
        const gridTop = useScrollLegend ? 56 : 44;

        const option = {
            backgroundColor: c.bg,
            grid: {
                left: 60,
                right: 24,
                top: gridTop,
                bottom: 56,
                containLabel: true,
            },
            tooltip: {
                trigger: 'item',
                confine: false,
                appendToBody: true,
                backgroundColor: c.tooltipBg,
                borderColor: c.tooltipBorder,
                borderWidth: 1,
                borderRadius: 10,
                padding: [12, 14],
                textStyle: { color: c.tooltipText, fontSize: 12 },
                extraCssText: `box-shadow: ${c.shadow};`,
                formatter(params) {
                    return intradayLineTooltip(params, c);
                },
            },
            legend: {
                type: useScrollLegend ? 'scroll' : 'plain',
                top: 4,
                left: 'center',
                width: '92%',
                itemGap: 14,
                itemHeight: 10,
                textStyle: { color: c.legendText, fontSize: 11 },
                data: models.map(m => ({ name: m, icon: 'roundRect', itemStyle: { color: getModelColor(m) } })),
            },
            xAxis: {
                type: 'time',
                axisLabel: {
                    color: c.axisLabel,
                    fontSize: 11,
                    hideOverlap: true,
                    formatter: span > 1
                        ? { day: '{MM}-{dd}', hour: '{HH}:{mm}' }
                        : { hour: '{HH}:{mm}', minute: '{HH}:{mm}' },
                },
                axisLine: { lineStyle: { color: c.axisLine } },
                axisTick: { lineStyle: { color: c.axisLine } },
                splitLine: { show: false },
            },
            yAxis: {
                type: 'value',
                name: metricLabel(),
                nameTextStyle: { color: c.axisLabel, fontSize: 11 },
                axisLabel: {
                    color: c.axisLabel,
                    fontSize: 11,
                    formatter: v => isCost ? ('$' + Number(v || 0).toFixed(2)) : fmtNum(v),
                },
                splitLine: { lineStyle: { color: c.splitLine } },
            },
            dataZoom: [
                {
                    type: 'inside',
                    xAxisIndex: 0,
                    start: 0,
                    end: 100,
                    zoomOnMouseWheel: 'shift',
                    moveOnMouseWheel: false,
                    moveOnMouseMove: true,
                },
                {
                    type: 'slider',
                    xAxisIndex: 0,
                    start: 0,
                    end: 100,
                    height: 14,
                    bottom: 4,
                    borderColor: c.dzBorder,
                    backgroundColor: c.dzBg,
                    fillerColor: c.dzFill,
                    handleStyle: { color: c.dzHandle, borderColor: c.dzHandle },
                    moveHandleStyle: { color: c.dzHandle },
                    selectedDataBackground: { lineStyle: { color: c.dzSelLine }, areaStyle: { color: c.dzSelArea } },
                    dataBackground: { lineStyle: { color: c.dzBgLine }, areaStyle: { color: c.dzBgArea } },
                    textStyle: { color: c.legendText, fontSize: 10 },
                },
            ],
            series,
        };

        if (!state.hourlyChart) {
            state.hourlyChart = echarts.init(chartEl, null, { renderer: 'canvas' });
            window.addEventListener('resize', () => state.hourlyChart && state.hourlyChart.resize());

            // ECharts' trigger:'item' only fires when the cursor is exactly
            // on a vertex marker — line segments are dead. We bridge that
            // gap ourselves: on every mousemove inside the canvas, pick the
            // line whose interpolated y at the cursor's x is closest to the
            // cursor's y, then dispatch showTip on the nearest data point of
            // that line. Result: the tooltip tracks whichever line you
            // graze, segment or marker, without using trigger:'axis' (which
            // CLAUDE.md forbids because it merges all series into one
            // popup).
            // Generous vertical hit zone: with 11 lines + sparse data the
            // segment between two distant points can sit 80-100 px from the
            // cursor, and a tighter cap would leave the user with no
            // feedback in exactly the empty stretches they're trying to
            // probe. 120 px ≈ ~1/3 of the visible plot height — beyond that
            // the cursor is clearly off-line and we suppress the popup.
            const PIXEL_HIT_VERTICAL_PX = 120;
            const findNearestSeriesAtCursor = (zEv) => {
                const ec = state.hourlyChart;
                if (!ec) return null;
                const opt = ec.getOption();
                const seriesArr = opt.series || [];
                const cursorXData = ec.convertFromPixel({ xAxisIndex: 0 }, zEv.offsetX);
                if (cursorXData == null || !Number.isFinite(cursorXData)) return null;
                let best = null;
                for (let si = 0; si < seriesArr.length; si++) {
                    const s = seriesArr[si];
                    if (!s || s.type !== 'line') continue;
                    const data = s.data || [];
                    if (data.length === 0) continue;
                    // Find the two adjacent data points bracketing cursorXData.
                    // Data is sorted ascending by x in loadIntraday().
                    let hi = data.length - 1, lo = 0;
                    while (lo < hi) {
                        const mid = (lo + hi) >> 1;
                        const xm = data[mid].value && data[mid].value[0];
                        if (xm < cursorXData) lo = mid + 1; else hi = mid;
                    }
                    const ihi = lo;
                    const ilo = Math.max(0, ihi - 1);
                    const a = data[ilo] && data[ilo].value;
                    const b = data[ihi] && data[ihi].value;
                    if (!a || !b || a[1] == null || b[1] == null) continue;
                    let yData;
                    if (cursorXData <= a[0]) yData = a[1];
                    else if (cursorXData >= b[0]) yData = b[1];
                    else if (a[0] === b[0]) yData = a[1];
                    else {
                        const t = (cursorXData - a[0]) / (b[0] - a[0]);
                        yData = a[1] + t * (b[1] - a[1]);
                    }
                    const yPx = ec.convertToPixel({ yAxisIndex: 0 }, yData);
                    if (yPx == null || !Number.isFinite(yPx)) continue;
                    const dy = Math.abs(yPx - zEv.offsetY);
                    if (best == null || dy < best.dy) {
                        const aPx = ec.convertToPixel({ xAxisIndex: 0 }, a[0]);
                        const bPx = ec.convertToPixel({ xAxisIndex: 0 }, b[0]);
                        const da = Math.abs((aPx == null ? Infinity : aPx) - zEv.offsetX);
                        const db = Math.abs((bPx == null ? Infinity : bPx) - zEv.offsetX);
                        const di = da <= db ? ilo : ihi;
                        best = { si, di, dy };
                    }
                }
                if (best == null || best.dy > PIXEL_HIT_VERTICAL_PX) return null;
                return best;
            };
            const zr = state.hourlyChart.getZr();
            zr.on('mousemove', (e) => {
                const hit = findNearestSeriesAtCursor(e);
                // ECharts' own zr listeners run in the same event tick and
                // call hideTip for non-item hovers, undoing our showTip
                // before paint. queueMicrotask defers our action to the
                // tail of the same task so it lands AFTER the built-in
                // handlers and the tooltip actually sticks.
                queueMicrotask(() => {
                    if (!state.hourlyChart) return;
                    if (hit) {
                        state.hourlyChart.dispatchAction({
                            type: 'showTip',
                            seriesIndex: hit.si,
                            dataIndex: hit.di,
                        });
                    } else {
                        state.hourlyChart.dispatchAction({ type: 'hideTip' });
                    }
                });
            });
            zr.on('globalout', () => {
                state.hourlyChart.dispatchAction({ type: 'hideTip' });
            });
        }
        state.hourlyChart.setOption(option, true);
        const syncSize = () => {
            try {
                state.hourlyChart.resize();
            } catch (e) {
                /* empty */
            }
        };
        requestAnimationFrame(() => {
            syncSize();
            requestAnimationFrame(syncSize);
        });
    } catch (e) {
        console.error('intraday:', e);
        tbody.innerHTML = '<tr><td colspan="10" style="text-align:center;color:var(--text-muted);padding:24px">Failed to load intraday data</td></tr>';
    }
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
