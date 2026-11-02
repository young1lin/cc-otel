// Thin wrappers around every /api/* endpoint. The only place fetch() is called.
// Since the AllClient merge every client shares one dashboard and one pool
// (api_requests + service_name), so no per-source routing happens here.
// /api/codex/migrate remains for the one-click history migration UI.

async function responseError(res) {
    let body = null;
    try { body = await res.json(); } catch {}
    const err = new Error(body?.error?.message || `HTTP ${res.status}`);
    err.code = body?.error?.code || 'http_error';
    err.status = res.status;
    err.details = body?.error?.details || null;
    return err;
}

async function fetchJSON(url) {
    const res = await fetch(url);
    if (!res.ok) {
        throw await responseError(res);
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
        throw await responseError(res);
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

export { fetchJSON, sendJSON };

export const loadStatusData = () => fetchJSON('/api/status');

export const loadDashboardData = (from, to) =>
    fetchJSON('/api/dashboard' + qs({ from, to }));

// Per-client (service_name) token accounting and hour-of-day buckets — both
// read the merged non-Codex pool, so they never mirror to /api/codex/*.
export const loadClientsData = ({ from, to }) =>
    fetchJSON('/api/clients' + qs({ from, to }));

export const loadHoursData = ({ from, to }) =>
    fetchJSON('/api/hours' + qs({ from, to }));

export const loadDailyData = ({ from, to, page = 1, pageSize = 20, granularity = 'day' } = {}) =>
    fetchJSON('/api/daily' + qs({ from, to, page, page_size: pageSize, granularity }));

export const loadCalendarData = ({ from, to }) =>
    fetchJSON('/api/calendar' + qs({ from, to }));

export const loadHourlyData = (date) =>
    fetchJSON('/api/hourly' + qs({ date }));

export const loadIntradayData = ({ from, to, bucket = 30, model = '' } = {}) =>
    fetchJSON('/api/intraday' + qs({ from, to, bucket, model }));

// Rate-over-time is Claude-only (no Codex mirror), so it always hits /api/rate.
export const loadRateData = ({ from, to, bucket = 30, model = '' } = {}) =>
    fetchJSON('/api/rate' + qs({ from, to, bucket, model }));

// Latest 1-minute throughput for a session's most recent active window (Claude only).
export const loadSessionRateData = (sessionId) =>
    fetchJSON('/api/session/rate' + qs({ session_id: sessionId }));

export const loadSessionsData = ({ from, to, page = 1, pageSize = 20 }) =>
    fetchJSON('/api/sessions' + qs({ from, to, page, page_size: pageSize }));

export const loadRequestsData = ({ from, to, page = 1, pageSize = 20, model = '' }) =>
    fetchJSON('/api/requests' + qs({ from, to, page, page_size: pageSize, model }));

export const loadDurationsData = ({ from, to, model = '' }) =>
    fetchJSON('/api/durations' + qs({ from, to, model }));

export const loadModelsData = () => fetchJSON('/api/models');

// AllClient migration: GET polls the job snapshot (per-table preview and
// progress); POST starts the idempotent one-click Codex history migration.
export const loadCodexMigrateStatus = () => fetchJSON('/api/codex/migrate');
export const startCodexMigrate = () => sendJSON('/api/codex/migrate', 'POST', {});

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

export function uploadDatabase(file, { onProgress = () => {}, signal } = {}) {
    return new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        const form = new FormData();
        form.append('file', file, file.name);
        xhr.open('POST', '/api/import/inspect');
        xhr.responseType = 'json';
        xhr.upload.onprogress = (event) => {
            if (event.lengthComputable) onProgress(event.loaded, event.total);
        };
        xhr.onerror = () => reject(Object.assign(new Error('Upload failed'), { code: 'network_error' }));
        xhr.onabort = () => reject(Object.assign(new Error('Upload cancelled'), { code: 'upload_cancelled' }));
        xhr.onload = () => {
            const body = xhr.response || {};
            if (xhr.status >= 200 && xhr.status < 300) {
                resolve(body);
                return;
            }
            reject(Object.assign(
                new Error(body?.error?.message || `HTTP ${xhr.status}`),
                { code: body?.error?.code || 'http_error', status: xhr.status },
            ));
        };
        const abort = () => xhr.abort();
        signal?.addEventListener('abort', abort, { once: true });
        xhr.onloadend = () => signal?.removeEventListener('abort', abort);
        xhr.send(form);
    });
}

export const loadDatabaseImportStatus = (jobID = '') =>
    fetchJSON('/api/import/status' + qs({ job_id: jobID }));

export const startDatabaseImport = (jobID) =>
    sendJSON('/api/import/start', 'POST', { job_id: jobID });

export const deleteDatabaseImport = (jobID) =>
    sendJSON('/api/import' + qs({ job_id: jobID }), 'DELETE', null);
