import { state, SSE_BACKOFF_MAX } from './state.js';
import { fmtTime } from './utils.js';
import { loadStatusData } from './api.js';

let onUpdate    = () => {};
let openPopover = () => {};
let closePopover = () => {};

function scheduleRefresh() {
    if (state.sseRefreshTimer) return;
    state.sseRefreshTimer = setTimeout(() => {
        state.sseRefreshTimer = null;
        onUpdate();
    }, 500);
}

function connectSSE() {
    const es = new EventSource('/api/events');

    es.onopen = () => {
        setStatus(true);
        state.sseBackoff = 1000;
    };

    es.onmessage = e => {
        const incomingSource = e.data || 'claude';
        if (incomingSource !== state.source) return;
        scheduleRefresh();
    };

    es.onerror = () => {
        setStatus(false);
        es.close();
        clearTimeout(state.sseReconnectTimer);
        state.sseReconnectTimer = setTimeout(connectSSE, state.sseBackoff);
        state.sseBackoff = Math.min(state.sseBackoff * 2, SSE_BACKOFF_MAX);
    };
}

export function setStatus(ok) {
    state.sseConnected = !!ok;
    const dot   = document.getElementById('status-dot');
    const label = document.getElementById('status-label');
    if (dot) dot.className = 'status-dot ' + (ok ? 'ok' : 'err');
    if (label) label.textContent = ok ? 'live' : 'offline';
}

// renderPricing fills the pricing row in the Server Status popup.
//
// Color rule (matches the docs in CLAUDE.md):
//   green  = last refresh < 48h and no error
//   yellow = 48h–7d (and no error)
//   red    = > 7d, error present, or refresh never ran
//
// Hide the row entirely if the daemon doesn't expose a pricing block (older
// build or pricer-less mode), so old screenshots / e2e tests stay valid.
function renderPricing(p) {
    const row = document.getElementById('st-pricing-row');
    const valEl = document.getElementById('st-pricing');
    if (!row || !valEl) return;
    if (!p) { row.style.display = 'none'; return; }
    row.style.display = '';

    const now = Math.floor(Date.now() / 1000);
    const ageSec = p.last_refresh_at > 0 ? (now - p.last_refresh_at) : Number.POSITIVE_INFINITY;
    let dotClass = 'err';
    if (!p.last_refresh_error) {
        if (ageSec < 48 * 3600) dotClass = 'ok';
        else if (ageSec < 7 * 86400) dotClass = 'warn';
    }
    const lastTxt = p.last_refresh_at > 0 ? fmtTime(p.last_refresh_at) : 'never';
    const summary = `${p.table_size ?? 0} models · last ${lastTxt}` +
                    (p.last_refresh_changed ? ` (Δ${p.last_refresh_changed})` : '') +
                    (p.miss_count_24h ? ` · ${p.miss_count_24h} misses` : '') +
                    (p.last_refresh_error ? ` · err: ${p.last_refresh_error}` : '');
    valEl.innerHTML =
        `<span class="status-dot ${dotClass}" style="display:inline-block;width:8px;height:8px;border-radius:50%;vertical-align:middle;margin-right:6px"></span>` +
        summary.replace(/</g, '&lt;');
}

async function loadStatus() {
    document.getElementById('st-sse').textContent = state.sseConnected ? 'connected' : 'disconnected';
    try {
        const s = await loadStatusData();

        document.getElementById('st-db').textContent = s.db_ok ? 'ok' : 'error';
        document.getElementById('st-otel').textContent = s.otel_receiver_listening ? `listening :${s.otel_port}` : `not responding :${s.otel_port}`;
        document.getElementById('st-last').textContent = s.last_update_unix ? fmtTime(s.last_update_unix) : '—';

        document.getElementById('st-otel-endpoint').textContent = `http://localhost:${s.otel_port}`;
        document.getElementById('st-web-endpoint').textContent  = `http://localhost:${s.web_port}`;
        const sseLine = state.sseConnected ? `connected · ${s.sse_clients ?? 0} clients` : 'disconnected';
        document.getElementById('st-sse').textContent = sseLine;

        renderPricing(s.pricing);
    } catch (e) {
        console.error('status:', e);
        document.getElementById('st-db').textContent = '—';
        document.getElementById('st-otel').textContent = '—';
        document.getElementById('st-last').textContent = '—';
        renderPricing(null);
    }
}

function openStatusModal() {
    const statusBtn   = document.getElementById('status-btn');
    const statusModal = document.getElementById('status-modal');
    openPopover(statusModal, statusBtn);
    loadStatus();
    clearInterval(state.statusTimer);
    state.statusTimer = setInterval(loadStatus, 4000);
}

function closeStatusModal() {
    const statusModal = document.getElementById('status-modal');
    closePopover(statusModal);
    clearInterval(state.statusTimer);
    state.statusTimer = null;
}

function bindStatusModal() {
    const statusBtn   = document.getElementById('status-btn');
    const statusModal = document.getElementById('status-modal');
    const statusClose = document.getElementById('status-close');

    statusBtn?.addEventListener('click', openStatusModal);
    statusClose?.addEventListener('click', closeStatusModal);
    statusModal?.addEventListener('click', (e) => {
        if (e.target === statusModal) closeStatusModal();
    });
    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && statusModal?.style.display !== 'none') closeStatusModal();
    });
    window.addEventListener('resize', () => {
        if (statusModal?.style.display !== 'none') openPopover(statusModal, statusBtn);
    });
}

export function initSSE(opts = {}) {
    onUpdate     = opts.onUpdate || (() => {});
    openPopover  = opts.openPopover || (() => {});
    closePopover = opts.closePopover || (() => {});

    bindStatusModal();
    connectSSE();
}
