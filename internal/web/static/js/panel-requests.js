import { state, paging } from './state.js';
import { fmtNum, escapeHtml, formatUserCell, rangeToFromTo } from './utils.js';
import { loadRequestsData, loadDurationsData, loadModelsData } from './api.js';
import { renderPagination } from './pagination.js';

function setTTFTColumnVisible(visible) {
    const th = document.querySelector('#duration-stats-wrap th.ttft-col');
    if (!th) return;
    th.style.display = visible ? '' : 'none';
}

function getDurationSortNumber(row, key) {
    const v = row?.[key];
    if (v == null) return NaN;
    const n = Number(v);
    return Number.isFinite(n) ? n : NaN;
}

function sortedDurationRows() {
    const { key, dir } = state.durationSort || {};
    const sign = dir === 'asc' ? 1 : -1;
    const rows = Array.isArray(state.durationStatsRows) ? state.durationStatsRows.slice() : [];
    rows.sort((a, b) => {
        const av = getDurationSortNumber(a, key);
        const bv = getDurationSortNumber(b, key);
        const aBad = !Number.isFinite(av);
        const bBad = !Number.isFinite(bv);
        if (aBad && bBad) return String(a?.model || '').localeCompare(String(b?.model || ''));
        if (aBad) return 1;
        if (bBad) return -1;
        if (av === bv) return String(a?.model || '').localeCompare(String(b?.model || ''));
        return (av < bv ? -1 : 1) * sign;
    });
    return rows;
}

function updateDurationSortIndicators() {
    const ths = document.querySelectorAll('#duration-stats-wrap th[data-sort-key]');
    ths.forEach(th => {
        // If TTFT is hidden, skip updating its label to avoid showing it accidentally.
        if (th.classList.contains('ttft-col') && th.style.display === 'none') return;
        const key = th.getAttribute('data-sort-key');
        const label = th.getAttribute('data-sort-label') || th.textContent || '';
        const active = state.durationSort && key === state.durationSort.key;
        const arrow = !active ? '' : (state.durationSort.dir === 'asc' ? ' ▲' : ' ▼');
        th.textContent = label + arrow;
        th.classList.toggle('is-sorted', !!active);
    });
}

function renderDurationStatsTable() {
    const tbody = document.getElementById('duration-stats-tbody');
    if (!tbody) return;
    const rows = sortedDurationRows();

    const hasTTFT = rows.some(r => Number(r?.avg_ttft_ms || 0) > 0);
    setTTFTColumnVisible(hasTTFT);

    if (!hasTTFT && state.durationSort.key === 'avg_ttft_ms') {
        state.durationSort = { key: 'avg_duration_ms', dir: 'desc' };
    }

    updateDurationSortIndicators();
    if (!rows.length) {
        tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;color:var(--text-muted);padding:16px">No duration data</td></tr>';
        return;
    }
    tbody.innerHTML = rows.map(r => `<tr>
        <td><span class="badge">${escapeHtml(r.model)}</span></td>
        <td class="mono">${r.request_count}</td>
        <td class="mono">${r.avg_duration_ms ? Math.round(r.avg_duration_ms) + 'ms' : '—'}</td>
        <td class="mono">${r.avg_out_tokens_per_s ? Math.round(r.avg_out_tokens_per_s) : '—'}</td>
        ${hasTTFT ? `<td class="mono">${r.avg_ttft_ms ? Math.round(r.avg_ttft_ms) + 'ms' : '—'}</td>` : ``}
        <td class="mono">${r.min_duration_ms ? r.min_duration_ms + 'ms' : '—'}</td>
        <td class="mono">${r.max_duration_ms ? r.max_duration_ms + 'ms' : '—'}</td>
    </tr>`).join('');
}

export async function loadDurationStats(from, to, model) {
    const tbody = document.getElementById('duration-stats-tbody');
    if (!tbody) return;
    const sourceAtStart = state.source;
    try {
        const rows = await loadDurationsData({ from, to, model });
        if (sourceAtStart !== state.source) return;
        state.durationStatsRows = Array.isArray(rows) ? rows : [];
        if (state.durationStatsRows.length === 0) {
            setTTFTColumnVisible(false);
            tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;color:var(--text-muted);padding:16px">No duration data</td></tr>';
            return;
        }
        renderDurationStatsTable();
    } catch (e) {
        if (sourceAtStart !== state.source) return;
        console.error('durations:', e);
        state.durationStatsRows = [];
        setTTFTColumnVisible(false);
        tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;color:var(--text-muted);padding:16px">Failed to load duration stats</td></tr>';
    }
}

export async function loadRequests() {
    const { from, to } = rangeToFromTo(state.currentRange);
    const { page, pageSize } = paging.requests;
    const sourceAtStart = state.source;
    try {
        const model = document.getElementById('model-filter').value;
        loadDurationStats(from, to, model);
        const json = await loadRequestsData({ from, to, page, pageSize, model });
        if (sourceAtStart !== state.source) return;
        paging.requests.total = json.total || 0;
        const data = json.data || [];

        const tbody = document.getElementById('request-tbody');
        if (data.length === 0) {
            tbody.innerHTML = '<tr><td colspan="10" style="text-align:center;color:var(--text-muted);padding:32px">No data</td></tr>';
            renderPagination('requests-pagination', paging.requests, loadRequests);
            return;
        }
        tbody.innerHTML = data.map(r => `<tr>
            <td class="mono">${new Date(r.timestamp).toLocaleString()}</td>
            <td><span class="badge">${escapeHtml(r.model)}</span></td>
            <td class="mono">${formatUserCell(r.user_id)}</td>
            <td class="mono">${fmtNum(r.input_tokens)}</td>
            <td class="mono">${fmtNum(r.output_tokens)}</td>
            <td class="mono">${fmtNum(r.cache_read_tokens)}</td>
            <td class="mono">${fmtNum(r.cache_creation_tokens)}</td>
            <td class="cost-val">$${r.cost_usd.toFixed(4)}</td>
            <td class="mono" title="${escapeHtml(
                r.ttft_ms
                    ? `TTFT (Time To First Token): ${r.ttft_ms}ms\\nHow long it took to receive the first token after the request started.\\n\\nmodel=${r.model || '—'}\\nsession=${r.session_id || '—'}\\nprompt=${r.prompt_id || '—'}\\ntime=${r.timestamp || '—'}\\nduration_ms=${r.duration_ms || '—'}`
                    : `TTFT (Time To First Token): —\\nNo TTFT recorded yet for this request. It may be backfilled shortly from trace spans.\\n\\nmodel=${r.model || '—'}\\nsession=${r.session_id || '—'}\\nprompt=${r.prompt_id || '—'}\\ntime=${r.timestamp || '—'}\\nduration_ms=${r.duration_ms || '—'}`
            )}">${r.ttft_ms ? r.ttft_ms + 'ms' : '—'}</td>
            <td class="mono">${r.duration_ms ? r.duration_ms + 'ms' : '—'}</td>
        </tr>`).join('');
        renderPagination('requests-pagination', paging.requests, loadRequests);
    } catch (e) { console.error('requests:', e); }
}

export async function loadModelFilter({ preserveCurrent = true } = {}) {
    const select = document.getElementById('model-filter');
    if (!select) return;
    const current = preserveCurrent ? select.value : '';
    select.innerHTML = '<option value="">All Models</option>';
    select.value = '';

    try {
        const models = await loadModelsData();
        models.forEach(m => {
            const opt = document.createElement('option');
            opt.value = m; opt.textContent = m;
            select.appendChild(opt);
        });
        if (preserveCurrent && models.includes(current)) select.value = current;
    } catch(e) { console.error('models:', e); }
}

export function initPanelRequests() {
    document.getElementById('model-filter')?.addEventListener('change', () => loadRequests());
    document.getElementById('requests-refresh-btn')?.addEventListener('click', () => loadRequests());

    document.querySelectorAll('#duration-stats-wrap th[data-sort-key]').forEach(th => {
        th.style.cursor = 'pointer';
        th.addEventListener('click', () => {
            if (th.classList.contains('ttft-col') && th.style.display === 'none') return;
            const key = th.getAttribute('data-sort-key');
            if (!key) return;
            if (state.durationSort.key === key) {
                state.durationSort.dir = state.durationSort.dir === 'asc' ? 'desc' : 'asc';
            } else {
                state.durationSort.key = key;
                state.durationSort.dir = 'desc';
            }
            renderDurationStatsTable();
        });
    });
}
