// Clients panel: per-client (service_name) token & cost accounting for the
// selected range, backed by /api/clients (claude_pool_requests GROUP BY
// service_name — the view mirrors the FULL unified pool, Codex included,
// all-time, unaffected by retention pruning of the agg). This is the
// "who consumed what" view the KPI row aggregates
// away. Badge rendering mirrors panel-requests.js formatSource so a client
// looks identical in both tables.
//
// Drill-down: clicking a row lazily fetches /api/clients/models for the SAME
// range selector and expands an inline per-model table (total-token DESC,
// share bars relative to the client). Clicking again collapses. Expansion
// state lives in `expandedClients` and survives the wholesale tbody re-render
// that every SSE push / range switch / manual refresh triggers: loadClients
// re-fetches and re-expands whatever was open, so numbers stay live.
import { state } from './state.js';
import { fmtNum, fmtUSD, escapeHtml, rangeToFromTo, formatSourceTag } from './utils.js';
import { loadClientsData, loadClientModelsData } from './api.js';
import { beginLatestRequest } from './latest-request.js';

// Client badge rendering is the SHARED formatSourceTag from utils.js: same
// mapping as the Request Log, so 'deepseek-harness-plugin' shows verbatim in
// both places — no per-panel aliases.
const formatClient = formatSourceTag;

const expandedClients = new Set();

const CHEVRON_SVG = '<svg width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden="true" focusable="false">'
    + '<path d="M3 1.5 L6.5 5 L3 8.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>';

function shareBar(share) {
    const pct = (share * 100).toFixed(1);
    return pct + '% '
        + '<span style="display:inline-block;width:52px;height:6px;border-radius:3px;background:var(--border);vertical-align:middle;overflow:hidden">'
        + '<span style="display:block;height:100%;width:' + Math.min(100, share * 100).toFixed(1) + '%;background:var(--accent)"></span></span>';
}

function rowHtml(r, totalTokens) {
    const total = Number(r.total_tokens) || 0;
    const share = totalTokens > 0 ? total / totalTokens : 0;
    const days = (r.first_date && r.last_date)
        ? (r.first_date === r.last_date ? r.first_date : r.first_date + ' → ' + r.last_date)
        : '—';
    const name = escapeHtml(r.service_name);
    return [
        '<tr class="client-row" data-service-name="' + name + '">',
        '<td><button type="button" class="client-toggle" aria-expanded="false" aria-label="Toggle model drill-down for ' + name + '">' + CHEVRON_SVG + '</button>' + formatClient(r.service_name) + '</td>',
        '<td class="mono">' + fmtNum(Number(r.request_count) || 0) + '</td>',
        '<td class="mono">' + fmtNum(Number(r.input_tokens) || 0) + '</td>',
        '<td class="mono">' + fmtNum(Number(r.cache_read_tokens) || 0) + '</td>',
        '<td class="mono">' + fmtNum(Number(r.cache_creation_tokens) || 0) + '</td>',
        '<td class="mono">' + fmtNum(Number(r.output_tokens) || 0) + '</td>',
        '<td class="mono"><b>' + fmtNum(total) + '</b></td>',
        '<td class="mono">' + shareBar(share) + '</td>',
        '<td class="mono">' + ((Number(r.cache_hit_rate) || 0) * 100).toFixed(1) + '%</td>',
        '<td class="cost-val">' + fmtUSD(Number(r.cost_usd) || 0) + '</td>',
        '<td class="mono">' + (Number(r.model_count) || 0) + '</td>',
        '<td class="mono">' + escapeHtml(days) + '</td>',
        '</tr>',
    ].join('');
}

// One model row inside the drill-down. Column set mirrors the parent table
// (minus Client/Models/Active) so the pinned token definitions read the same
// in both tables. Share is relative to the SUM OF FETCHED MODEL TOTALS, not
// the (possibly half-stale) parent row, so the bars always add up to 100%.
function modelRowHtml(m, clientTotal) {
    const total = Number(m.total_tokens) || 0;
    const share = clientTotal > 0 ? total / clientTotal : 0;
    return [
        '<tr>',
        '<td class="mono">' + escapeHtml(m.model) + '</td>',
        '<td class="mono">' + fmtNum(Number(m.request_count) || 0) + '</td>',
        '<td class="mono">' + fmtNum(Number(m.input_tokens) || 0) + '</td>',
        '<td class="mono">' + fmtNum(Number(m.cache_read_tokens) || 0) + '</td>',
        '<td class="mono">' + fmtNum(Number(m.cache_creation_tokens) || 0) + '</td>',
        '<td class="mono">' + fmtNum(Number(m.output_tokens) || 0) + '</td>',
        '<td class="mono"><b>' + fmtNum(total) + '</b></td>',
        '<td class="mono">' + shareBar(share) + '</td>',
        '<td class="mono">' + ((Number(m.cache_hit_rate) || 0) * 100).toFixed(1) + '%</td>',
        '<td class="cost-val">' + fmtUSD(Number(m.cost_usd) || 0) + '</td>',
        '</tr>',
    ].join('');
}

export function detailTableHtml(models) {
    const clientTotal = models.reduce((s, m) => s + (Number(m.total_tokens) || 0), 0);
    return '<div class="table-wrap client-models-wrap"><table class="client-models-table"><thead><tr>'
        + '<th>Model</th><th>Requests</th><th>Input (Uncached)</th><th>Cache Read</th><th>Cache Create</th>'
        + '<th>Output</th><th>Total Tokens</th><th>Share</th><th>Cache Hit</th><th>Cost</th>'
        + '</tr></thead><tbody>'
        + models.map((m) => modelRowHtml(m, clientTotal)).join('')
        + '</tbody></table></div>';
}

function clientRowByName(tbody, serviceName) {
    for (const tr of tbody.querySelectorAll('tr.client-row')) {
        if (tr.dataset.serviceName === serviceName) return tr;
    }
    return null;
}

function removeDetailRows(tbody, serviceName) {
    for (const tr of [...tbody.querySelectorAll('tr.client-detail-row')]) {
        if (tr.dataset.detailFor === serviceName) tr.remove();
    }
}

function setExpandedUi(tbody, serviceName, expanded) {
    const tr = clientRowByName(tbody, serviceName);
    if (!tr) return;
    tr.classList.toggle('client-expanded', expanded);
    const btn = tr.querySelector('button.client-toggle');
    if (btn) btn.setAttribute('aria-expanded', String(expanded));
}

function collapseClient(serviceName) {
    expandedClients.delete(serviceName);
    const tbody = document.getElementById('clients-tbody');
    if (!tbody) return;
    removeDetailRows(tbody, serviceName);
    setExpandedUi(tbody, serviceName, false);
}

// Always a FRESH fetch (user-pinned behavior): no caching, so a re-expand or
// an SSE re-render reloads the latest numbers for the current range. The
// per-client latest-request token also makes a stale response lose against a
// newer expand of the same client.
function expandClient(serviceName, rowEl) {
    const tbody = document.getElementById('clients-tbody');
    if (!tbody || !rowEl) return;
    expandedClients.add(serviceName);
    removeDetailRows(tbody, serviceName);
    setExpandedUi(tbody, serviceName, true);

    const detail = document.createElement('tr');
    detail.className = 'client-detail-row';
    detail.dataset.detailFor = serviceName;
    detail.innerHTML = '<td colspan="12"><div class="client-detail-cell client-detail-loading">Loading models…</div></td>';
    rowEl.after(detail);

    const isCurrent = beginLatestRequest('clients-models:' + serviceName);
    (async () => {
        const { from, to } = rangeToFromTo(state.currentRange);
        const json = await loadClientModelsData({ serviceName, from, to });
        if (!isCurrent()) return;
        const models = Array.isArray(json.data) ? json.data : [];
        detail.innerHTML = '<td colspan="12"><div class="client-detail-cell">'
            + (models.length
                ? detailTableHtml(models)
                : '<span class="client-detail-empty">No model data for this client in the current range</span>')
            + '</div></td>';
    })().catch((e) => {
        if (!isCurrent()) return;
        detail.innerHTML = '<td colspan="12"><div class="client-detail-cell client-detail-error">Failed to load models — collapse and expand again to retry</div></td>';
        console.error('client models:', e);
    });
}

function toggleClient(serviceName, rowEl) {
    if (expandedClients.has(serviceName)) collapseClient(serviceName);
    else expandClient(serviceName, rowEl);
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
        // Re-render wiped the detail DOM rows: re-expand whatever was open so
        // an SSE push mid-read never collapses the row under the user.
        for (const name of [...expandedClients]) {
            const tr = clientRowByName(tbody, name);
            if (tr) expandClient(name, tr);
            else expandedClients.delete(name); // client vanished from this range
        }
    })().catch((e) => { if (isCurrent()) console.error('clients:', e); });
}

export function initPanelClients() {
    document.getElementById('clients-refresh-btn')?.addEventListener('click', () => loadClients());
    const tbody = document.getElementById('clients-tbody');
    // One delegated listener survives the wholesale tbody re-renders. The
    // chevron button sits INSIDE the row, so its Enter/Space click bubbles to
    // the same toggle — keyboard and mouse share one path.
    tbody?.addEventListener('click', (ev) => {
        const tr = ev.target.closest('tr.client-row');
        if (!tr) return;
        const name = tr.dataset.serviceName;
        if (!name) return;
        toggleClient(name, tr);
    });
}
