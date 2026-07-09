import { state } from './state.js';
import { fmtNum, fmtUSD, escapeHtml } from './utils.js';
import { loadDailyData } from './api.js';
import { tokenParts } from './token-math.js';

let openPopoverFn = () => {};
let closePopoverFn = () => {};

export function ensureInsightsBar() {
    if (document.getElementById('insights-bar')) return;
    const anchor = document.querySelector('.kpi-row');
    if (!anchor) return;
    const bar = document.createElement('div');
    bar.id = 'insights-bar';
    bar.className = 'insights-bar';
    bar.style.display = 'none';
    bar.innerHTML = `
        <button type="button" class="insights-main" id="insights-toggle" title="Show details">
            <span class="insights-k">Avg/day</span>
            <span class="insights-v mono" id="ins-avg-summary">—</span>
            <span class="insights-tail mono" id="ins-days">—</span>
        </button>
        <button type="button" class="insights-details-btn" id="insights-details-btn" title="Show details">Details</button>
    `;
    anchor.insertAdjacentElement('afterend', bar);

    ensureInsightsModal();
    const open = () => openInsightsModal();
    document.getElementById('insights-toggle')?.addEventListener('click', open);
    document.getElementById('insights-details-btn')?.addEventListener('click', open);
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
    if (metric === 'cost') return fmtUSD(safe);
    if (metric === 'reqs') return String(Math.round(safe));
    return fmtNum(Math.round(safe));
}

export function metricKey(metric) {
    if (metric === 'cost') return 'cost';
    if (metric === 'reqs' || metric === 'requests') return 'reqs';
    return 'tokens';
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
        const json = await loadDailyData({ from, to, page: 1, pageSize: 2000, granularity: 'day' });
        const rows = (json.data || json) || [];
        if (myId !== state.insightsReqId) return;

        if (!rows.length) {
            setInsightsVisible(false);
            return;
        }

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

        const avgTokens = Math.round(totalTokens / activeDays);
        const avgCost = totalCost / activeDays;
        const avgReqs = Math.round(totalReqs / activeDays);

        const metric = state.chartMetric;
        let avgText = '';
        if (metric === 'cost') avgText = `cost ${fmtUSD(avgCost)}`;
        else if (metric === 'requests') avgText = `reqs ${avgReqs}`;
        else avgText = `tokens ${fmtNum(avgTokens)}`;

        const avgEl = document.getElementById('ins-avg-summary');
        if (avgEl) avgEl.textContent = avgText;

        const daysEl = document.getElementById('ins-days');
        if (daysEl) daysEl.textContent = `· active days ${activeDays}`;

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
    ensureInsightsBar();
    ensureInsightsModal();
}
