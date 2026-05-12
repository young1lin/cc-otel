import test from 'node:test';
import assert from 'node:assert/strict';
import { state, paging } from '../js/state.js';
import { loadModelFilter, loadRequests } from '../js/panel-requests.js';

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
        innerHTML: '',
        options: [],
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

test('Codex request log keeps duration summary when duration rows exist', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;

    try {
        const dom = installRequestPanelDOM();
        state.source = 'codex';
        state.currentRange = 'custom';
        state.customFrom = '2026-05-12';
        state.customTo = '2026-05-12';
        paging.requests.page = 1;
        paging.requests.pageSize = 20;
        paging.requests.total = 0;

        global.fetch = async (url) => {
            const u = String(url);
            if (u.startsWith('/api/codex/durations')) {
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
            if (u.startsWith('/api/codex/requests')) {
                return jsonResponse({
                    data: [{
                        timestamp: '2026-05-12T10:09:39+08:00',
                        model: 'gpt-5.5',
                        user_id: '',
                        input_tokens: 70844,
                        output_tokens: 70,
                        cache_read_tokens: 70528,
                        cache_creation_tokens: 0,
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
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.source = 'claude';
        state.currentRange = 'today';
        state.customFrom = '';
        state.customTo = '';
    }
});

test('model filter can reset stale source selection before loading Codex models', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;

    try {
        const dom = installRequestPanelDOM();
        state.source = 'codex';
        dom.modelFilter.value = 'claude-opus-4-7';

        global.fetch = async (url) => {
            assert.equal(String(url), '/api/codex/models');
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
