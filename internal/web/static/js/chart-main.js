import { state } from './state.js';
import { fmtNum, escapeHtml, rangeToFromTo, fmtHourRange } from './utils.js';
import { chartColors, getModelColor, mixHex } from './theme.js';
import { loadDailyData } from './api.js';
import { tokenParts } from './token-math.js';

/**
 * Shared bar-chart tooltip formatter for daily and hourly charts.
 * Accepts the echarts params object and a color-theme object (from chartColors()).
 */
export function buildBarTooltip(params, c) {
    // item+axisPointer can pass a single param or an array of series at the tick; normalize.
    const list = Array.isArray(params) ? params : [params];
    const p = list.find((x) => x && x.data && typeof x.data === 'object' && x.data !== null && x.data.raw)
        || list.find((x) => x && x.data && typeof x.data === 'object' && x.data !== null)
        || list[0];
    if (!p) return '';

    const datum = p.data;
    const raw = datum && typeof datum === 'object' && 'raw' in datum ? datum.raw : null;
    if (!raw) return '';

    const parts = tokenParts(raw);
    const sub = 'padding:2px 0 2px 16px;font-size:11px';
    const cacheCreateRow = state.source === 'codex'
        ? ''
        : `<tr><td style="color:${c.mutedText};${sub}">Cache Create</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${fmtNum(parts.cacheCreate)}</td></tr>`;
    const dayLabel = raw.date != null
        ? escapeHtml(String(raw.date))
        : (p.name != null ? escapeHtml(String(p.name)) : '');
    const header = raw.hour != null
        ? fmtHourRange(Number(raw.hour))
        : dayLabel;
    const modelColor = typeof p.color === 'string' ? p.color : getModelColor(raw.model || p.seriesName || 'Unknown');
    const modelName = (raw.model != null && String(raw.model).trim() !== '')
        ? String(raw.model)
        : (p.seriesName != null && String(p.seriesName).trim() !== '')
            ? String(p.seriesName)
            : 'Unknown';
    const swatch = typeof modelColor === 'string'
        ? `<span style="display:inline-block;width:8px;height:8px;border-radius:2px;background:${modelColor};margin-right:6px;vertical-align:middle"></span>`
        : '';
    return `<div style="margin-bottom:6px;font-weight:600;color:${c.tooltipText}">${header}</div>` +
        `<div style="color:${c.tooltipText};font-weight:600;margin-bottom:8px">${swatch}<span style="color:${modelColor}">${escapeHtml(modelName)}</span></div>` +
        `<table style="width:100%;font-size:12px;border-collapse:collapse">` +
        `<tr><td style="color:${c.mutedText};padding:2px 0">Total</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0;font-weight:600">${fmtNum(parts.total)}</td></tr>` +
        `<tr><td colspan="2" style="height:4px"></td></tr>` +
        `<tr><td style="color:${c.tooltipText};padding:2px 0;font-weight:600">Input</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0;font-weight:600">${fmtNum(parts.inputSide)}</td></tr>` +
        `<tr><td style="color:${c.mutedText};${sub}">Uncached</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${fmtNum(parts.uncachedInput)}</td></tr>` +
        `<tr><td style="color:${c.mutedText};${sub}">Cache Read</td><td style="font-family:var(--font-mono);text-align:right;color:var(--green);padding:2px 0">${fmtNum(parts.cacheRead)}</td></tr>` +
        cacheCreateRow +
        `<tr><td colspan="2" style="height:4px"></td></tr>` +
        `<tr><td style="color:${c.mutedText};padding:2px 0">Output</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${fmtNum(parts.output)}</td></tr>` +
        `<tr><td style="color:${c.mutedText};padding:2px 0">Requests</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${raw.request_count}</td></tr>` +
        `<tr style="border-top:1px solid ${c.axisLine}"><td style="color:${c.mutedText};padding:6px 0 0;font-weight:500">Cost</td><td style="font-family:var(--font-mono);font-weight:600;color:var(--orange);text-align:right;padding:6px 0 0">$${Number(raw.cost_usd || 0).toFixed(4)}</td></tr>` +
        `</table>`;
}

export async function loadChart() {
    const { from, to } = rangeToFromTo(state.currentRange);
    try {
        const json = await loadDailyData({ from, to, page: 1, pageSize: 1000, granularity: state.chartGranularity });
        const rows = (json.data || json) || [];

        // Newest day on the left, then older (yesterday, …) — matches 7d/30d mental model; YYYY-MM sorts lexicographically
        const dates  = [...new Set(rows.map(r => r.date))].sort().reverse();
        const models = [...new Set(rows.map(r => r.model))].sort();

        // Build O(1) lookup map: "date|model" → row  (replaces O(n²) rows.find)
        const rowIndex = new Map();
        for (const r of rows) rowIndex.set(r.date + '|' + r.model, r);

        const c = chartColors();

        const isCost = state.chartMetric === 'cost';
        const isReqs = state.chartMetric === 'requests';
        function getVal(r) {
            if (!r) return 0;
            if (isCost) return r.cost_usd;
            if (isReqs) return r.request_count;
            return tokenParts(r).total;
        }
        function fmtVal(v) {
            if (isCost) return '$' + v.toFixed(4);
            return fmtNum(v);
        }

        const titleEl = document.getElementById('chart-title');
        if (titleEl) titleEl.textContent = isCost ? 'Cost' : isReqs ? 'Requests' : 'Token Usage';

        const dataCount = dates.length;
        const shortSpan = dataCount <= 2;
        const barMaxW = dataCount === 1 ? 72 : dataCount <= 3 ? 52 : 44;

        const series = models.map(model => ({
            name: model,
            type: 'bar',
            barMaxWidth: barMaxW,
            itemStyle: {
                color(params) {
                    if (isCost || isReqs) return getModelColor(model);
                    const raw = params?.data?.raw;
                    if (!raw) return getModelColor(model);
                    const parts = tokenParts(raw);
                    const base = getModelColor(model);
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
                },
            },
            data: dates.map(d => {
                const r = rowIndex.get(d + '|' + model);
                return r ? { value: getVal(r), raw: r } : 0;
            }),
        }));

        const visibleDates = 14;
        const hasZoom = dates.length > visibleDates;
        // Descending dates: left = recent → [0, winPct] is the “latest N days” window
        const winPct = hasZoom
            ? Math.round(visibleDates / dates.length * 100)
            : 100;
        const dZoomStart = 0;
        const dZoomEnd = hasZoom ? winPct : 100;

        const chartEl = document.getElementById('main-chart');
        // Few x categories (e.g. All Time + month) still have many series — need extra plot height
        // so grouped bars and axes are not squashed. Scroll legend keeps one row (no runaway wrap).
        // Tighter card height: extra bottom margin was for overflow fix, not a tall blank band.
        // Extra vertical room: legend is NOT inside grid — must keep grid.top > legend block height
        const h =
            (dataCount <= 3 ? 280 : dataCount <= 7 ? 320 : 360) + (shortSpan ? 32 : 0);
        chartEl.style.height = `${Math.min(560, h)}px`;

        // scroll legend: few items get jammed to the left in a tight strip; use plain + center when N is small
        const useScrollLegend = models.length > 7;
        const legendTwoRows = !useScrollLegend && models.length > 5;
        // px: single-row legend ~28–32px; scroll row ~34; two rows ~64 — keep plot fully below legend
        const gridTop = useScrollLegend ? 90 : (legendTwoRows ? 100 : 72);

        const legendData = models.map(m => ({
            name: m,
            icon: 'roundRect',
            itemStyle: { color: getModelColor(m) },
        }));
        const legendCommon = {
            data: legendData,
            textStyle: { color: c.legendText, fontSize: 11 },
        };
        const legendPad = [6, 8, 8, 8];
        const legend = useScrollLegend
            ? {
                ...legendCommon,
                type: 'scroll',
                orient: 'horizontal',
                top: 4,
                left: 'center',
                width: '92%',
                itemGap: 12,
                itemHeight: 10,
                pageButtonPosition: 'end',
                pageIconSize: 10,
                padding: legendPad,
            }
            : {
                ...legendCommon,
                type: 'plain',
                orient: 'horizontal',
                top: 4,
                left: 'center',
                itemGap: 20,
                itemHeight: 12,
                padding: legendPad,
            };

        const option = {
            backgroundColor: c.bg,
            tooltip: {
                trigger: 'item',
                confine: false,
                appendToBody: true,
                // Avoid axisPointer+shadow: some echarts versions batch params / break item formatter + model line
                backgroundColor: c.tooltipBg,
                borderColor: c.tooltipBorder,
                borderWidth: 1,
                borderRadius: 10,
                padding: [12, 14],
                textStyle: { color: c.tooltipText, fontSize: 12 },
                extraCssText: `box-shadow: ${c.shadow};`,
                formatter(params) {
                    return buildBarTooltip(params, c);
                },
            },
            legend,
            grid: {
                left: 60,
                right: 20,
                top: gridTop,
                // slider ~16 + bottom 6 + one line of date labels; was 68 and left a wide dead band
                bottom: hasZoom ? 50 : 36,
                containLabel: true,
            },
            dataZoom: hasZoom ? [
                {
                    type: 'inside',
                    xAxisIndex: 0,
                    start: dZoomStart,
                    end: dZoomEnd,
                    zoomLock: true,
                },
                {
                    type: 'slider',
                    xAxisIndex: 0,
                    start: dZoomStart,
                    end: dZoomEnd,
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
            ] : [],
            xAxis: {
                type: 'category',
                data: dates,
                // few categories: default boundaryGap leaves huge side margins → paper-thin bar groups
                boundaryGap: dataCount === 1 ? [0.2, 0.2] : dataCount <= 3 ? [0.08, 0.08] : true,
                axisLabel: {
                    color: c.axisLabel,
                    fontSize: 11,
                    align: 'center',
                },
                axisTick: { alignWithLabel: true },
                axisLine: { lineStyle: { color: c.axisLine } },
                splitLine: { show: false },
            },
            yAxis: {
                name: isCost ? 'USD' : isReqs ? 'Reqs' : 'Tokens',
                nameTextStyle: { color: c.axisLabel, fontSize: 11 },
                axisLabel: { color: c.axisLabel, fontSize: 11, formatter: v => fmtVal(v) },
                axisLine: { show: false },
                splitLine: { lineStyle: { color: c.splitLine } },
            },
            series,
        };

        if (!state.mainChart) {
            state.mainChart = echarts.init(chartEl, null, { renderer: 'canvas' });
            window.addEventListener('resize', () => state.mainChart.resize());
        }
        // Apply option first, then measure container — resize() before setOption used stale layout
        // (range/granularity switches were leaving bars/axes misaligned to the box).
        state.mainChart.setOption(option, true);
        const syncSize = () => {
            try {
                state.mainChart.resize();
            } catch (e) {
                /* empty */
            }
        };
        requestAnimationFrame(() => {
            syncSize();
            requestAnimationFrame(syncSize);
        });
    } catch (e) { console.error('chart:', e); }
}
