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
    'dashboard', 'daily', 'requests', 'sessions', 'durations', 'models', 'intraday',
]);

function apiPath(name) {
    if (state.source === 'codex' && CODEX_ROUTES.has(name)) {
        return '/api/codex/' + name;
    }
    return '/api/' + name;
}

export { fetchJSON };

export const loadStatusData = () => fetchJSON('/api/status');

export const loadDashboardData = (from, to) =>
    fetchJSON(apiPath('dashboard') + qs({ from, to }));

export const loadDailyData = ({ from, to, page = 1, pageSize = 20, granularity = 'day' }) =>
    fetchJSON(apiPath('daily') + qs({ from, to, page, page_size: pageSize, granularity }));

export const loadHourlyData = (date) =>
    fetchJSON('/api/hourly' + qs({ date }));

export const loadIntradayData = ({ from, to, bucket = 30, model = '' } = {}) =>
    fetchJSON(apiPath('intraday') + qs({ from, to, bucket, model }));

export const loadSessionsData = ({ from, to, page = 1, pageSize = 20 }) =>
    fetchJSON(apiPath('sessions') + qs({ from, to, page, page_size: pageSize }));

export const loadRequestsData = ({ from, to, page = 1, pageSize = 20, model = '' }) =>
    fetchJSON(apiPath('requests') + qs({ from, to, page, page_size: pageSize, model }));

export const loadDurationsData = ({ from, to, model = '' }) =>
    fetchJSON(apiPath('durations') + qs({ from, to, model }));

export const loadModelsData = () => fetchJSON(apiPath('models'));
