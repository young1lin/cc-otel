// Clients panel: per-client (service_name) token & cost accounting for the
// selected range, backed by /api/clients (daily_model_agg GROUP BY
// service_name). This is the "who consumed what" view the KPI row aggregates
// away. Badge rendering mirrors panel-requests.js formatSource so a client
// looks identical in both tables.
import { state } from './state.js';
import { fmtNum, fmtUSD, escapeHtml, rangeToFromTo, formatSourceTag } from './utils.js';
import { loadClientsData } from './api.js';
import { beginLatestRequest } from './latest-request.js';

// Client badge rendering is the SHARED formatSourceTag from utils.js: same
// mapping as the Request Log, so 'deepseek-harness-plugin' shows verbatim in
// both places — no per-panel aliases.
const formatClient = formatSourceTag;

function rowHtml(r, totalTokens) {
    const total = Number(r.total_tokens) || 0;
    const share = totalTokens > 0 ? total / totalTokens : 0;
    const pct = (share * 100).toFixed(1);
    const bar = `<span style="display:inline-block;width:52px;height:6px;border-radius:3px;background:var(--border);vertical-align:middle;overflow:hidden"><span style="display:block;height:100%;width:${Math.min(100, share * 100).toFixed(1)}%;background:var(--accent)"></span></span>`;
    const days = (r.first_date && r.last_date)
        ? (r.first_date === r.last_date ? r.first_date : `${r.first_date} → ${r.last_date}`)
        : '—';
    return [
        '<tr>',
        `<td>${formatClient(r.service_name)}</td>`,
        `<td class="mono">${fmtNum(Number(r.request_count) || 0)}</td>`,
        `<td class="mono">${fmtNum(Number(r.input_tokens) || 0)}</td>`,
        `<td class="mono">${fmtNum(Number(r.cache_read_tokens) || 0)}</td>`,
        `<td class="mono">${fmtNum(Number(r.cache_creation_tokens) || 0)}</td>`,
        `<td class="mono">${fmtNum(Number(r.output_tokens) || 0)}</td>`,
        `<td class="mono"><b>${fmtNum(total)}</b></td>`,
        `<td class="mono">${pct}% ${bar}</td>`,
        `<td class="mono">${((Number(r.cache_hit_rate) || 0) * 100).toFixed(1)}%</td>`,
        `<td class="cost-val">${fmtUSD(Number(r.cost_usd) || 0)}</td>`,
        `<td class="mono">${Number(r.model_count) || 0}</td>`,
        `<td class="mono">${escapeHtml(days)}</td>`,
        '</tr>',
    ].join('');
}

export function loadClients() {
    const tbody = document.getElementById('clients-tbody');
    if (!tbody) return;
    const isCurrent = beginLatestRequest('clients');
    (async () => {
        const { from, to } = rangeToFromTo(state.currentRange);
        const json = await loadClientsData({ from, to });
        if (!isCurrent()) return;
        const rows = Array.isArray(json.data) ? json.data : [];
        const totalTokens = rows.reduce((s, r) => s + (Number(r.total_tokens) || 0), 0);
        tbody.innerHTML = rows.length
            ? rows.map((r) => rowHtml(r, totalTokens)).join('')
            : '<tr><td colspan="12" style="text-align:center;color:var(--text-muted);padding:32px">No client data in this range</td></tr>';
        const countEl = document.getElementById('clients-count');
        if (countEl) countEl.textContent = String(rows.length);
    })().catch((e) => { if (isCurrent()) console.error('clients:', e); });
}

export function initPanelClients() {
    document.getElementById('clients-refresh-btn')?.addEventListener('click', () => loadClients());
}
