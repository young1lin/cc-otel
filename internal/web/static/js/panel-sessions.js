import { state, paging } from './state.js';
import { fmtNum, escapeHtml, truncate, formatUserCell, rangeToFromTo } from './utils.js';
import { loadSessionsData } from './api.js';
import { renderPagination } from './pagination.js';

export async function loadSessions() {
    const { from, to } = rangeToFromTo(state.currentRange);
    const { page, pageSize } = paging.sessions;
    try {
        const json = await loadSessionsData({ from, to, page, pageSize });
        paging.sessions.total = json.total || 0;
        const rows = json.data || [];
        const tbody = document.getElementById('sessions-tbody');
        if (rows.length === 0) {
            tbody.innerHTML = '<tr><td colspan="7" style="text-align:center;color:var(--text-muted);padding:32px">No data</td></tr>';
            renderPagination('sessions-pagination', paging.sessions, loadSessions);
            return;
        }
        tbody.innerHTML = rows.map(s => `<tr>
            <td><span class="badge">${escapeHtml(truncate(s.session_id, 16))}</span></td>
            <td class="mono">${formatUserCell(s.user_id)}</td>
            <td class="mono">${new Date(s.start_time).toLocaleString()}</td>
            <td class="mono">${s.request_count}</td>
            <td class="mono">${fmtNum(s.input_tokens)}</td>
            <td class="mono">${fmtNum(s.output_tokens)}</td>
            <td class="cost-val">$${s.cost_usd.toFixed(4)}</td>
        </tr>`).join('');
        renderPagination('sessions-pagination', paging.sessions, loadSessions);
    } catch (e) { console.error('sessions:', e); }
}
