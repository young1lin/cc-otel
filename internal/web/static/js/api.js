// Thin wrappers around every /api/* endpoint. The only place fetch() is called.
// When state.source === 'codex', routed endpoints hit /api/codex/* instead.

import { state } from './state.js';

async function fetchJSON(url) {
    const res = await fetch(url);
    if (!res.ok) {
        const body = await res.text().catch(() => '');
        throw new Error(body || `HTTP ${res.status}`);
    }
    return res.json();
}

// Body-capable helper for POST/DELETE-with-body endpoints. When `body` is null
// (e.g. DELETE) no Content-Type / body is sent. 204 is treated as "no content".
async function sendJSON(url, method, body) {
    const res = await fetch(url, {
        method,
        headers: body == null ? undefined : { 'Content-Type': 'application/json' },
        body: body == null ? undefined : JSON.stringify(body),
    });
    if (!res.ok) {
        const text = await res.text().catch(() => '');
        throw new Error(text || `HTTP ${res.status}`);
    }
    return res.status === 204 ? null : res.json();
}

function qs(params) {
    const sp = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
        if (v !== undefined && v !== null && v !== '') sp.set(k, String(v));
    }
    const s = sp.toString();
    return s ? '?' + s : '';
}

// Routes that have a Codex mirror at /api/codex/<name>.
const CODEX_ROUTES = new Set([
    'dashboard', 'calendar', 'daily', 'requests', 'sessions', 'durations', 'models', 'intraday',
]);

function apiPath(name) {
    if (state.source === 'codex' && CODEX_ROUTES.has(name)) {
        return '/api/codex/' + name;
    }
    return '/api/' + name;
}

export { fetchJSON, sendJSON };

export const loadStatusData = () => fetchJSON('/api/status');

export const loadDashboardData = (from, to) =>
    fetchJSON(apiPath('dashboard') + qs({ from, to }));

export const loadDailyData = ({ from, to, page = 1, pageSize = 20, granularity = 'day' }) =>
    fetchJSON(apiPath('daily') + qs({ from, to, page, page_size: pageSize, granularity }));

export const loadCalendarData = ({ from, to }) =>
    fetchJSON(apiPath('calendar') + qs({ from, to }));

export const loadHourlyData = (date) =>
    fetchJSON('/api/hourly' + qs({ date }));

export const loadIntradayData = ({ from, to, bucket = 30, model = '' } = {}) =>
    fetchJSON(apiPath('intraday') + qs({ from, to, bucket, model }));

// Rate-over-time is Claude-only (no Codex mirror), so it always hits /api/rate.
export const loadRateData = ({ from, to, bucket = 30, model = '' } = {}) =>
    fetchJSON('/api/rate' + qs({ from, to, bucket, model }));

// Latest 1-minute throughput for a session's most recent active window (Claude only).
export const loadSessionRateData = (sessionId) =>
    fetchJSON('/api/session/rate' + qs({ session_id: sessionId }));

export const loadSessionsData = ({ from, to, page = 1, pageSize = 20 }) =>
    fetchJSON(apiPath('sessions') + qs({ from, to, page, page_size: pageSize }));

export const loadRequestsData = ({ from, to, page = 1, pageSize = 20, model = '' }) =>
    fetchJSON(apiPath('requests') + qs({ from, to, page, page_size: pageSize, model }));

export const loadDurationsData = ({ from, to, model = '' }) =>
    fetchJSON(apiPath('durations') + qs({ from, to, model }));

export const loadModelsData = () => fetchJSON(apiPath('models'));

// Pricing Table admin (manual entry CRUD + OpenRouter suggest + recompute).
// The wire unit is USD/Mtok; the backend converts to/from USD/token.
export const loadPricingPage = ({ q = '', source = '', page = 1, pageSize = 50 } = {}) =>
    fetchJSON('/api/pricing' + qs({ q, source, page, page_size: pageSize }));
export const upsertPricing = (entry) => sendJSON('/api/pricing', 'POST', entry);
export const deletePricing = (model) =>
    sendJSON('/api/pricing?model=' + encodeURIComponent(model), 'DELETE', null);
export const suggestPricing = (model) =>
    fetchJSON('/api/pricing/suggest' + qs({ model }));
export const startRecompute = () => sendJSON('/api/pricing/recompute', 'POST', {});
export const recomputeStatus = () => fetchJSON('/api/pricing/recompute');
