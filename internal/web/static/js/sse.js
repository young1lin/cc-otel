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
    } catch (e) {
        console.error('status:', e);
        document.getElementById('st-db').textContent = '—';
        document.getElementById('st-otel').textContent = '—';
        document.getElementById('st-last').textContent = '—';
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
