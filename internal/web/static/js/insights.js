import { state } from './state.js';
import { fmtNum, fmtUSD, escapeHtml } from './utils.js';
import { loadCalendarData, loadDailyData } from './api.js';
import { tokenParts } from './token-math.js';

let openPopoverFn = () => {};
let closePopoverFn = () => {};
let selectDateFn = () => {};

export function ensureInsightsBar() {
    if (document.getElementById('insights-bar')) return;
    const anchor = document.querySelector('.kpi-row');
    if (!anchor) return;
    const bar = document.createElement('div');
    bar.id = 'insights-bar';
    bar.className = 'insights-bar';
    bar.style.display = 'none';
    bar.innerHTML = `
        <div class="insights-main">
            <div class="usage-calendar-visual">
                <div class="usage-calendar-visual-head">
                    <div class="usage-calendar-headline mono" id="usage-calendar-headline">Usage Calendar</div>
                    <div class="usage-calendar-legend" aria-hidden="true">
                        <span>Less</span>
                        <span class="usage-calendar-swatch level-0"></span>
                        <span class="usage-calendar-swatch level-1"></span>
                        <span class="usage-calendar-swatch level-2"></span>
                        <span class="usage-calendar-swatch level-3"></span>
                        <span class="usage-calendar-swatch level-4"></span>
                        <span class="usage-calendar-swatch level-5"></span>
                        <span>More</span>
                    </div>
                </div>
                <div class="usage-calendar-body">
                    <div class="usage-calendar-weekdays" aria-hidden="true">
                        <span>Sun</span><span>Mon</span><span>Tue</span><span>Wed</span><span>Thu</span><span>Fri</span><span>Sat</span>
                    </div>
                    <div class="usage-calendar-scroll">
                        <div class="usage-calendar-months" id="usage-calendar-months" aria-hidden="true"></div>
                        <div class="usage-calendar-grid" id="usage-calendar-grid" aria-label="Daily usage calendar"></div>
                        <div class="usage-calendar-dates mono" id="usage-calendar-dates" aria-hidden="true"></div>
                    </div>
                </div>
            </div>
        </div>
        <button type="button" class="insights-details-btn" id="insights-details-btn" title="Show details">Details</button>
    `;
    anchor.insertAdjacentElement('afterend', bar);

    ensureInsightsModal();
    document.getElementById('insights-details-btn')?.addEventListener('click', () => openInsightsModal());
}

export function setInsightsVisible(show) {
    ensureInsightsBar();
    const bar = document.getElementById('insights-bar');
    if (!bar) return;
    bar.style.display = show ? '' : 'none';
    if (!show) closeInsightsModal();
}

function ensureInsightsModal() {
    if (state.insightsModal) return;
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.id = 'insights-modal';
    backdrop.style.display = 'none';
    backdrop.setAttribute('aria-hidden', 'true');
    backdrop.innerHTML = `
        <div class="modal insights-modal" role="dialog" aria-modal="true" aria-labelledby="insights-title">
            <div class="modal-header">
                <div class="modal-title" id="insights-title">Insights</div>
                <div class="insights-controls" role="group" aria-label="Insights controls">
                    <select class="insights-select" id="insights-metric">
                        <option value="tokens">Tokens</option>
                        <option value="cost">Cost</option>
                        <option value="reqs">Reqs</option>
                    </select>
                    <select class="insights-select" id="insights-model">
                        <option value="">All models</option>
                    </select>
                </div>
                <button type="button" class="modal-close" id="insights-close" title="Close">×</button>
            </div>
            <div class="modal-body">
                <div class="insights-grid">
                    <div class="insights-card">
                        <div class="insights-card-k">Top model (range total, current metric)</div>
                        <div class="insights-card-v mono" id="ins-top-summary">—</div>
                        <div class="insights-card-sub mono" id="ins-top-alt">—</div>
                    </div>
                    <div class="insights-card">
                        <div class="insights-card-k">Active days</div>
                        <div class="insights-card-v mono" id="ins-active-days-summary">—</div>
                        <details class="insights-days-details">
                            <summary class="insights-days-summary">Show dates</summary>
                            <div class="mono insights-days-list" id="ins-active-days-list">—</div>
                        </details>
                        <div class="insights-card-sub">Only days with data are counted in Avg/day.</div>
                    </div>
                </div>
                <div class="insights-table-wrap">
                    <div class="insights-table-title">Daily ranking</div>
                    <table class="insights-table">
                        <thead>
                            <tr>
                                <th>Date</th>
                                <th>Top1</th>
                                <th>Selected model rank</th>
                                <th>Selected model value</th>
                                <th>Selected model share</th>
                            </tr>
                        </thead>
                        <tbody id="ins-daily-rank-tbody"></tbody>
                    </table>
                </div>
                <div class="insights-table-wrap">
                    <div class="insights-table-title">Model share (Top 10)</div>
                    <table class="insights-table">
                        <thead>
                            <tr>
                                <th>#</th>
                                <th>Model</th>
                                <th>Value</th>
                                <th>Share</th>
                            </tr>
                        </thead>
                        <tbody id="ins-model-share-tbody"></tbody>
                    </table>
                </div>
            </div>
        </div>
    `;
    document.body.appendChild(backdrop);
    state.insightsModal = backdrop;

    document.getElementById('insights-close')?.addEventListener('click', closeInsightsModal);
    backdrop.addEventListener('click', (e) => {
        if (e.target === backdrop) closeInsightsModal();
    });
    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && state.insightsModal?.style.display !== 'none') closeInsightsModal();
    });
}

function openInsightsModal() {
    ensureInsightsModal();
    openPopoverFn(state.insightsModal, null);
    if (state.insightsData) renderInsightsDetails();
}

function closeInsightsModal() {
    if (!state.insightsModal) return;
    closePopoverFn(state.insightsModal);
}

/** Coerce per-model aggregate field to a finite number (avoids NaN in UI when data is missing). */
export function insightMetricScalar(v, key) {
    if (v == null || typeof v !== 'object') return 0;
    const n = Number(v[key]);
    return Number.isFinite(n) ? n : 0;
}

export function fmtMetricValue(metric, v) {
    const n = Number(v);
    const safe = Number.isFinite(n) ? n : 0;
    const key = metricKey(metric);
    if (key === 'cost') return fmtUSD(safe);
    if (key === 'reqs') return String(Math.round(safe));
    return fmtNum(Math.round(safe));
}

export function formatCalendarHeadline(metric, value, activeDays, from, to) {
    const key = metricKey(metric);
    const suffix = key === 'reqs' ? 'requests' : key;
    const days = Math.max(0, Math.round(Number(activeDays) || 0));
    return `Usage Calendar · avg/day ${fmtMetricValue(key, value)} ${suffix} · active days ${days} · ${from} → ${to}`;
}

export function metricKey(metric) {
    if (metric === 'cost') return 'cost';
    if (metric === 'reqs' || metric === 'requests') return 'reqs';
    return 'tokens';
}

function parseYMDLocal(date) {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(date || ''));
    if (!m) return null;
    return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 12, 0, 0, 0);
}

function ymdFromLocalDate(d) {
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${y}-${m}-${day}`;
}

export function calendarMetricValue(row, metric) {
    if (row == null || typeof row !== 'object') return 0;
    const key = metricKey(metric);
    if (key === 'cost') {
        const n = Number(row.cost_usd ?? row.cost ?? 0);
        return Number.isFinite(n) ? n : 0;
    }
    if (key === 'reqs') {
        const n = Number(row.request_count ?? row.reqs ?? 0);
        return Number.isFinite(n) ? n : 0;
    }
    const reported = Number(row.total_tokens || 0);
    if (Number.isFinite(reported) && reported > 0) return reported;
    const total = Number(row.input_tokens || 0)
        + Number(row.output_tokens || 0)
        + Number(row.cache_read_tokens || 0)
        + Number(row.cache_creation_tokens || 0);
    return Number.isFinite(total) ? total : 0;
}

export function calendarIntensityLevel(value, values) {
    const v = Number(value);
    if (!Number.isFinite(v) || v <= 0) return 0;
    const positives = (Array.isArray(values) ? values : [])
        .map(Number)
        .filter(n => Number.isFinite(n) && n > 0)
        .sort((a, b) => a - b);
    if (!positives.length) return 0;
    const max = positives[positives.length - 1];
    if (v >= max) return 5;
    const level = Math.ceil((Math.log1p(v) / Math.log1p(max)) * 5);
    return Math.max(1, Math.min(5, level));
}

export function buildCalendarGrid(days, from, to) {
    const byDate = new Map((days || []).map(d => [d.date, d]));
    const start = parseYMDLocal(from);
    const end = parseYMDLocal(to);
    if (!start || !end || end < start) {
        return { cells: [], weekCount: 0 };
    }

    const gridStart = new Date(start);
    gridStart.setDate(start.getDate() - start.getDay());

    const cells = [];
    const cur = new Date(gridStart);
    let i = 0;
    while (cur <= end) {
        const date = ymdFromLocalDate(cur);
        cells.push({
            date,
            row: cur.getDay(),
            col: Math.floor(i / 7),
            data: byDate.get(date) || null,
            isPadding: cur < start,
        });
        cur.setDate(cur.getDate() + 1);
        i++;
    }
    const weekCount = cells.length ? Math.max(...cells.map(c => c.col)) + 1 : 0;
    return { cells, weekCount };
}

function calendarSpanDays(from, to) {
    const start = parseYMDLocal(from);
    const end = parseYMDLocal(to);
    if (!start || !end || end < start) return 0;
    return Math.floor((end - start) / 86400000) + 1;
}

function buildCalendarStrip(days, from, to) {
    const byDate = new Map((days || []).map(d => [d.date, d]));
    const start = parseYMDLocal(from);
    const end = parseYMDLocal(to);
    if (!start || !end || end < start) return [];

    const cells = [];
    const cur = new Date(start);
    let col = 0;
    while (cur <= end) {
        const date = ymdFromLocalDate(cur);
        cells.push({
            date,
            row: 0,
            col,
            data: byDate.get(date) || null,
            isPadding: false,
        });
        cur.setDate(cur.getDate() + 1);
        col++;
    }
    return cells;
}

function shortDate(date) {
    const d = parseYMDLocal(date);
    if (!d) return date;
    return `${String(d.getMonth() + 1).padStart(2, '0')}/${String(d.getDate()).padStart(2, '0')}`;
}

function weekdayShort(date) {
    const d = parseYMDLocal(date);
    return d ? d.toLocaleString('en-US', { weekday: 'short' }) : '';
}

export function calendarLayoutMode(weekCount, spanDays = Number.POSITIVE_INFINITY, range = '') {
    const span = Number(spanDays);
    if (Number.isFinite(span) && span > 0 && span <= 10) return 'strip';
    const n = Number(weekCount);
    if (!Number.isFinite(n) || n <= 8) return 'short';
    if (n <= 18) return 'medium';
    return 'long';
}

export function calendarMonthLabels(cells) {
    const labels = [];
    let lastMonth = '';
    for (const cell of cells || []) {
        if (!cell || cell.isPadding) continue;
        const d = parseYMDLocal(cell.date);
        if (!d) continue;
        const month = d.toLocaleString('en-US', { month: 'short' });
        if (!lastMonth || month !== lastMonth) {
            labels.push({ col: cell.col, label: month });
            lastMonth = month;
        }
    }
    return labels;
}

export function topEntry(map, key) {
    let bestM = '—';
    let bestV = -Infinity;
    for (const [m, v] of map.entries()) {
        const val = insightMetricScalar(v, key);
        if (val > bestV) { bestV = val; bestM = m; }
    }
    if (bestV === -Infinity || !Number.isFinite(bestV)) {
        return { model: '—', value: 0 };
    }
    return { model: bestM, value: bestV };
}

function renderUsageCalendar(days, from, to) {
    const gridEl = document.getElementById('usage-calendar-grid');
    if (!gridEl) return;

    const rows = Array.isArray(days) ? [...days].sort((a, b) => String(a.date).localeCompare(String(b.date))) : [];
    const metric = state.chartMetric || 'tokens';
    const values = rows.map(r => calendarMetricValue(r, metric));
    const activeRows = rows.filter(r => calendarMetricValue(r, metric) > 0);
    const activeDays = activeRows.length || rows.length;
    const total = values.reduce((sum, v) => sum + v, 0);
    const avg = activeDays > 0 ? total / activeDays : 0;

    const gridFrom = (state.currentRange === 'all' && rows[0]?.date) ? rows[0].date : from;
    const grid = buildCalendarGrid(rows, gridFrom, to);
    const spanDays = calendarSpanDays(gridFrom, to);
    const mode = calendarLayoutMode(grid.weekCount, spanDays, state.currentRange);
    const renderCells = mode === 'strip' ? buildCalendarStrip(rows, gridFrom, to) : grid.cells;
    const renderCols = mode === 'strip' ? renderCells.length : grid.weekCount;
    const bodyEl = document.querySelector('.usage-calendar-body');
    bodyEl?.classList.toggle('usage-calendar-body-strip', mode === 'strip');
    bodyEl?.classList.toggle('usage-calendar-body-short', mode === 'short');
    bodyEl?.classList.toggle('usage-calendar-body-medium', mode === 'medium');
    bodyEl?.classList.toggle('usage-calendar-body-long', mode === 'long');
    gridEl.classList.toggle('usage-calendar-grid-strip', mode === 'strip');
    gridEl.classList.toggle('usage-calendar-grid-short', mode === 'short');
    gridEl.classList.toggle('usage-calendar-grid-medium', mode === 'medium');
    gridEl.classList.toggle('usage-calendar-grid-long', mode === 'long');
    gridEl.style.gridTemplateColumns = `repeat(${Math.max(renderCols, 1)}, var(--usage-cell-size, 12px))`;

    const headlineEl = document.getElementById('usage-calendar-headline');
    if (headlineEl) headlineEl.textContent = formatCalendarHeadline(metric, avg, activeDays, gridFrom, to);

    const monthEl = document.getElementById('usage-calendar-months');
    if (monthEl) {
        monthEl.style.gridTemplateColumns = gridEl.style.gridTemplateColumns;
        if (mode === 'strip') {
            monthEl.innerHTML = renderCells
                .map(cell => `<span style="grid-column:${cell.col + 1}">${escapeHtml(weekdayShort(cell.date))}</span>`)
                .join('');
        } else {
            monthEl.innerHTML = calendarMonthLabels(grid.cells)
                .map(m => `<span style="grid-column:${m.col + 1}">${escapeHtml(m.label)}</span>`)
                .join('');
        }
    }

    const datesEl = document.getElementById('usage-calendar-dates');
    if (datesEl) {
        datesEl.classList.toggle('usage-calendar-dates-strip', mode === 'strip');
        if (mode === 'strip') {
            datesEl.style.gridTemplateColumns = gridEl.style.gridTemplateColumns;
            datesEl.innerHTML = renderCells
                .map(cell => `<span style="grid-column:${cell.col + 1}">${escapeHtml(shortDate(cell.date))}</span>`)
                .join('');
        } else {
            datesEl.style.gridTemplateColumns = '';
            datesEl.innerHTML = `<span>${escapeHtml(gridFrom)}</span><span>${escapeHtml(to)}</span>`;
        }
    }

    gridEl.innerHTML = renderCells.map(cell => {
        const row = cell.data || { date: cell.date };
        const value = calendarMetricValue(row, metric);
        const level = cell.isPadding ? 0 : calendarIntensityLevel(value, values);
        const isActive = Boolean(cell.data);
        const label = [
            cell.date,
            `Tokens ${fmtMetricValue('tokens', calendarMetricValue(row, 'tokens'))}`,
            `Cost ${fmtMetricValue('cost', calendarMetricValue(row, 'cost'))}`,
            `Requests ${fmtMetricValue('requests', calendarMetricValue(row, 'requests'))}`,
            `Top ${row.top_model || '—'}`,
        ].join('\n');
        return `<button type="button"
            class="usage-calendar-cell level-${level}${isActive ? ' has-data' : ''}${cell.isPadding ? ' is-padding' : ''}"
            style="grid-column:${cell.col + 1};grid-row:${cell.row + 1}"
            data-date="${escapeHtml(cell.date)}"
            title="${escapeHtml(label)}"
            aria-label="${escapeHtml(label)}"></button>`;
    }).join('');

    gridEl.querySelectorAll('.usage-calendar-cell:not(.is-padding)').forEach(btn => {
        btn.addEventListener('click', () => {
            const date = btn.getAttribute('data-date');
            if (date) selectDateFn(date);
        });
    });
}

function renderInsightsDetails() {
    const insightsData = state.insightsData;
    if (!insightsData) return;
    const metricSel = document.getElementById('insights-metric');
    const modelSel = document.getElementById('insights-model');
    const metric = metricSel?.value || 'tokens';
    const key = metricKey(metric);
    const selectedModel = modelSel?.value || '';

    const top = topEntry(insightsData.byModel, key);
    const total = Number(insightsData.totals[key]);
    const totalSafe = Number.isFinite(total) ? total : 0;
    const share = totalSafe > 0 ? (top.value / totalSafe) * 100 : NaN;
    const topSummary = document.getElementById('ins-top-summary');
    const topAlt = document.getElementById('ins-top-alt');
    if (topSummary) {
        topSummary.textContent = `${metric} ${top.model}`;
    }
    if (topAlt) {
        topAlt.textContent = `total=${fmtMetricValue(metric, top.value)} · share=${Number.isFinite(share) ? share.toFixed(1) + '%' : '—'}`;
    }

    const activeEl = document.getElementById('ins-active-days-list');
    if (activeEl) activeEl.textContent = insightsData.activeDates.join(', ') || '—';
    const activeSummaryEl = document.getElementById('ins-active-days-summary');
    if (activeSummaryEl) {
        const n = insightsData.activeDates.length;
        const first = insightsData.activeDates[0];
        const last = insightsData.activeDates[n - 1];
        activeSummaryEl.textContent = n ? `${n} days · ${first} → ${last}` : '—';
    }

    const dailyTbody = document.getElementById('ins-daily-rank-tbody');
    if (dailyTbody) {
        dailyTbody.innerHTML = insightsData.dates.map(date => {
            const dm = insightsData.byDayModel.get(date) || new Map();
            const entries = [...dm.entries()]
                .map(([m, v]) => [m, insightMetricScalar(v, key)])
                .sort((a, b) => b[1] - a[1]);
            const top1 = entries[0]?.[0] || '—';
            const dayTotal = insightMetricScalar(insightsData.dayTotals.get(date) || {}, key);

            let rankText = '—';
            let valueText = '—';
            let shareText = '—';
            if (selectedModel) {
                const idx = entries.findIndex(e => e[0] === selectedModel);
                if (idx >= 0) {
                    const v = entries[idx][1] || 0;
                    rankText = `#${idx + 1}/${entries.length}`;
                    valueText = fmtMetricValue(metric, v);
                    const sh = dayTotal > 0 ? (Number(v) / dayTotal) * 100 : NaN;
                    shareText = Number.isFinite(sh) ? sh.toFixed(1) + '%' : '—';
                } else {
                    rankText = `—/${entries.length}`;
                    valueText = fmtMetricValue(metric, 0);
                    shareText = '0.0%';
                }
            } else {
                const v = entries[0]?.[1] || 0;
                rankText = '#1';
                valueText = fmtMetricValue(metric, v);
                const sh = dayTotal > 0 ? (Number(v) / dayTotal) * 100 : NaN;
                shareText = Number.isFinite(sh) ? sh.toFixed(1) + '%' : '—';
            }

            return `<tr>
                <td class="mono">${escapeHtml(date)}</td>
                <td class="mono">${escapeHtml(top1)}</td>
                <td class="mono">${escapeHtml(rankText)}</td>
                <td class="mono">${escapeHtml(valueText)}</td>
                <td class="mono">${escapeHtml(shareText)}</td>
            </tr>`;
        }).join('');
    }

    const shareTbody = document.getElementById('ins-model-share-tbody');
    if (shareTbody) {
        const total2 = Number.isFinite(Number(insightsData.totals[key]))
            ? Number(insightsData.totals[key])
            : 0;
        const ranked = [...insightsData.byModel.entries()]
            .map(([m, v]) => ({ model: m, value: insightMetricScalar(v, key) }))
            .sort((a, b) => b.value - a.value)
            .slice(0, 10);
        shareTbody.innerHTML = ranked.map((it, i) => {
            const sh = total2 > 0 ? (it.value / total2) * 100 : NaN;
            return `<tr>
                <td class="mono">${i + 1}</td>
                <td class="mono">${escapeHtml(it.model)}</td>
                <td class="mono">${escapeHtml(fmtMetricValue(metric, it.value))}</td>
                <td class="mono">${Number.isFinite(sh) ? sh.toFixed(1) + '%' : '—'}</td>
            </tr>`;
        }).join('');
    }
}

export async function loadInsights(from, to) {
    ensureInsightsBar();
    const myId = ++state.insightsReqId;
    try {
        const [calendarJson, json] = await Promise.all([
            loadCalendarData({ from, to }),
            loadDailyData({ from, to, page: 1, pageSize: 2000, granularity: 'day' }),
        ]);
        const calendarRows = (calendarJson.data || calendarJson) || [];
        const rows = (json.data || json) || [];
        if (myId !== state.insightsReqId) return;

        if (!calendarRows.length && !rows.length) {
            setInsightsVisible(false);
            return;
        }

        renderUsageCalendar(calendarRows, from, to);

        const byDate = new Map();
        const byModel = new Map();
        const byDayModel = new Map();
        const dayTotals = new Map();

        for (const r of rows) {
            const date = r.date;
            const model = r.model || 'unknown';
            const cost = Number(r.cost_usd || 0);
            const reqs = Number(r.request_count || 0);
            const tokensTotal = tokenParts(r).total;

            byDate.set(date, true);

            const acc = byModel.get(model) || { tokens: 0, cost: 0, reqs: 0 };
            acc.tokens += tokensTotal;
            acc.cost += cost;
            acc.reqs += reqs;
            byModel.set(model, acc);

            let dm = byDayModel.get(date);
            if (!dm) { dm = new Map(); byDayModel.set(date, dm); }
            const dAcc = dm.get(model) || { tokens: 0, cost: 0, reqs: 0 };
            dAcc.tokens += tokensTotal;
            dAcc.cost += cost;
            dAcc.reqs += reqs;
            dm.set(model, dAcc);

            const dt = dayTotals.get(date) || { tokens: 0, cost: 0, reqs: 0 };
            dt.tokens += tokensTotal;
            dt.cost += cost;
            dt.reqs += reqs;
            dayTotals.set(date, dt);
        }

        const activeDays = byDate.size || 1;
        let totalTokens = 0, totalCost = 0, totalReqs = 0;
        for (const v of byModel.values()) {
            totalTokens += v.tokens;
            totalCost += v.cost;
            totalReqs += v.reqs;
        }

        const models = [...byModel.keys()].sort();
        const dates = [...byDayModel.keys()].sort().reverse();
        state.insightsData = {
            from, to,
            activeDays,
            activeDates: [...byDate.keys()].sort(),
            totals: { tokens: totalTokens, cost: totalCost, reqs: totalReqs },
            byModel,
            byDayModel,
            dayTotals,
            models,
            dates,
        };

        const metricSel = document.getElementById('insights-metric');
        const modelSel = document.getElementById('insights-model');
        if (metricSel) metricSel.value = (state.chartMetric === 'requests') ? 'reqs' : (state.chartMetric === 'cost' ? 'cost' : 'tokens');

        if (modelSel) {
            const current = modelSel.value;
            modelSel.innerHTML = '<option value="">All models</option>' +
                models.map(m => `<option value="${escapeHtml(m)}">${escapeHtml(m)}</option>`).join('');
            if (models.includes(current)) modelSel.value = current;
        }

        if (metricSel && !metricSel.dataset.bound) {
            metricSel.addEventListener('change', renderInsightsDetails);
            metricSel.dataset.bound = '1';
        }
        if (modelSel && !modelSel.dataset.bound) {
            modelSel.addEventListener('change', renderInsightsDetails);
            modelSel.dataset.bound = '1';
        }

        renderInsightsDetails();

        setInsightsVisible(true);
    } catch (e) {
        console.error('insights:', e);
        if (myId === state.insightsReqId) setInsightsVisible(false);
    }
}

export function initInsightsModal(opts = {}) {
    openPopoverFn  = opts.openPopover  || (() => {});
    closePopoverFn = opts.closePopover || (() => {});
    selectDateFn   = opts.onSelectDate || (() => {});
    ensureInsightsBar();
    ensureInsightsModal();
}
