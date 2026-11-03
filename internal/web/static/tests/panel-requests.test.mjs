import test from 'node:test';
import assert from 'node:assert/strict';
import { state, paging } from '../js/state.js';
import { loadModelFilter, loadRequests } from '../js/panel-requests.js';
import { invalidateRequestContext } from '../js/latest-request.js';

function jsonResponse(data) {
    return {
        ok: true,
        json: async () => data,
        text: async () => JSON.stringify(data),
    };
}

function installRequestPanelDOM() {
    const durationWrap = { style: { display: '' } };
    const durationBody = { innerHTML: '' };
    const requestBody = { innerHTML: '' };
    const pagination = { innerHTML: '', append() {} };
    const modelFilter = {
        value: '',
        _innerHTML: '',
        options: [],
        get innerHTML() { return this._innerHTML; },
        set innerHTML(value) { this._innerHTML = value; this.options = []; },
        appendChild(opt) { this.options.push(opt); },
    };
    const ttftHeader = { style: { display: '' }, classList: { contains: () => true, toggle() {} } };

    const byId = new Map([
        ['duration-stats-wrap', durationWrap],
        ['duration-stats-tbody', durationBody],
        ['request-tbody', requestBody],
        ['requests-pagination', pagination],
        ['model-filter', modelFilter],
    ]);

    global.document = {
        getElementById(id) { return byId.get(id) || null; },
        querySelector(sel) {
            if (sel === '#duration-stats-wrap th.ttft-col') return ttftHeader;
            return null;
        },
        querySelectorAll() { return []; },
        createElement(tagName) {
            return {
                tagName,
                textContent: '',
                value: '',
                disabled: false,
                onclick: null,
                style: {},
                append() {},
            };
        },
    };

    return { durationWrap, durationBody, requestBody, modelFilter };
}

test('request log keeps duration summary when duration rows exist', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;

    try {
        const dom = installRequestPanelDOM();
        state.source = 'claude';
        state.currentRange = 'custom';
        state.customFrom = '2026-05-12';
        state.customTo = '2026-05-12';
        paging.requests.page = 1;
        paging.requests.pageSize = 20;
        paging.requests.total = 0;

        global.fetch = async (url) => {
            const u = String(url);
            // Unified view: generic endpoints serve every client.
            if (u.startsWith('/api/durations')) {
                return jsonResponse([{
                    model: 'gpt-5.5',
                    request_count: 1,
                    avg_duration_ms: 4200,
                    avg_ttft_ms: 0,
                    avg_out_tokens_per_s: 0,
                    max_duration_ms: 4200,
                    min_duration_ms: 4200,
                }]);
            }
            if (u.startsWith('/api/requests')) {
                return jsonResponse({
                    data: [{
                        timestamp: '2026-05-12T10:09:39+08:00',
                        model: 'gpt-5.5',
                        user_id: '',
                        input_tokens: 70844,
                        output_tokens: 70,
                        cache_read_tokens: 70528,
                        cache_creation_tokens: 321,
                        cost_usd: 0.03894,
                        ttft_ms: 0,
                        duration_ms: 0,
                    }],
                    total: 1,
                    page: 1,
                    page_size: 20,
                });
            }
            throw new Error(`unexpected fetch ${u}`);
        };

        await loadRequests();
        await new Promise(resolve => setTimeout(resolve, 0));

        assert.equal(dom.durationWrap.style.display, '');
        assert.match(dom.durationBody.innerHTML, /4200ms/);
        assert.match(dom.requestBody.innerHTML, /gpt-5\.5/);
        assert.match(dom.requestBody.innerHTML, />321<\/td>/);
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.source = 'claude';
        state.currentRange = 'today';
        state.customFrom = '';
        state.customTo = '';
    }
});

test('model filter can reset stale selection before loading models', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;

    try {
        const dom = installRequestPanelDOM();
        state.source = 'claude';
        dom.modelFilter.value = 'claude-opus-4-7';

        global.fetch = async (url) => {
            assert.equal(String(url), '/api/models');
            return jsonResponse(['gpt-5.5']);
        };

        await loadModelFilter({ preserveCurrent: false });

        assert.equal(dom.modelFilter.value, '');
        assert.deepEqual(dom.modelFilter.options.map(o => o.value), ['gpt-5.5']);
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.source = 'claude';
    }
});

test('resetting model options clears the old source filter before awaiting models', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;
    try {
        const dom = installRequestPanelDOM();
        dom.modelFilter.value = 'claude-opus-4-7';
        dom.modelFilter.options = [{ value: 'claude-opus-4-7' }];
        state.source = 'claude';
        let resolveModels;
        global.fetch = async () => new Promise(resolve => { resolveModels = resolve; });

        const loading = loadModelFilter({ preserveCurrent: false });
        assert.equal(dom.modelFilter.value, '');
        assert.deepEqual(dom.modelFilter.options, []);
        assert.match(dom.modelFilter.innerHTML, /All Models/);

        resolveModels(jsonResponse(['gpt-5.5']));
        await loading;
        assert.deepEqual(dom.modelFilter.options.map(o => o.value), ['gpt-5.5']);
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.source = 'claude';
    }
});

test('hidden Request Log response is invalidated by a global context change', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;
    try {
        const dom = installRequestPanelDOM();
        dom.requestBody.innerHTML = 'current-view';
        state.source = 'claude';
        state.currentRange = 'today';
        paging.requests.page = 1;
        let resolveRequests;
        global.fetch = async (url) => {
            if (String(url).startsWith('/api/durations')) return jsonResponse([]);
            return new Promise(resolve => { resolveRequests = resolve; });
        };

        const oldLoad = loadRequests();
        invalidateRequestContext();
        resolveRequests(jsonResponse({ data: [], total: 0 }));
        await oldLoad;

        assert.equal(dom.requestBody.innerHTML, 'current-view');
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.source = 'claude';
        state.currentRange = 'today';
    }
});

test('slower old Request Log page cannot overwrite a newer page', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;
    try {
        const dom = installRequestPanelDOM();
        let resolvePage1;
        let resolvePage2;
        const page1 = new Promise(resolve => { resolvePage1 = resolve; });
        const page2 = new Promise(resolve => { resolvePage2 = resolve; });
        state.source = 'claude';
        state.currentRange = 'today';
        paging.requests.pageSize = 20;

        const response = data => ({ ok: true, json: async () => data });
        global.fetch = async (url) => {
            const raw = String(url);
            if (raw.startsWith('/api/durations')) return response([]);
            const page = new URL('http://local' + raw).searchParams.get('page');
            return page === '2' ? page2 : page1;
        };

        paging.requests.page = 1;
        const oldLoad = loadRequests();
        paging.requests.page = 2;
        const newLoad = loadRequests();

        const row = model => ({
            timestamp: '2026-05-12T10:09:39+08:00',
            model, service_name: 'claude-code', user_id: '',
            input_tokens: 10, output_tokens: 2, cache_read_tokens: 0,
            cache_creation_tokens: 0, cost_usd: 0.01, ttft_ms: 0, duration_ms: 100,
        });
        resolvePage2(response({ data: [row('new-model')], total: 21 }));
        await newLoad;
        resolvePage1(response({ data: [row('old-model')], total: 21 }));
        await oldLoad;

        assert.match(dom.requestBody.innerHTML, /new-model/);
        assert.doesNotMatch(dom.requestBody.innerHTML, /old-model/);
        assert.equal(paging.requests.page, 2);
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.source = 'claude';
        state.currentRange = 'today';
        paging.requests.page = 1;
    }
});
