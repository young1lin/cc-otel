import test from 'node:test';
import assert from 'node:assert/strict';
import { state, paging } from '../js/state.js';
import { loadSessions } from '../js/panel-sessions.js';

function deferred() {
    let resolve;
    const promise = new Promise((done) => { resolve = done; });
    return { promise, resolve };
}

function response(data) {
    return { ok: true, json: async () => data };
}

function installDOM() {
    const tbody = { innerHTML: '' };
    const pagination = { innerHTML: '', append() {} };
    const byId = new Map([
        ['sessions-tbody', tbody],
        ['sessions-pagination', pagination],
    ]);
    global.document = {
        getElementById(id) { return byId.get(id) || null; },
        createElement() {
            return { textContent: '', disabled: false, onclick: null, append() {} };
        },
    };
    return tbody;
}

function session(model, id) {
    return {
        session_id: id,
        user_id: '',
        start_time: '2026-05-12T10:00:00Z',
        request_count: 1,
        input_tokens: 10,
        output_tokens: 2,
        cost_usd: 0.01,
        model,
    };
}

test('slower old Sessions page cannot overwrite a newer page', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;
    try {
        const tbody = installDOM();
        const page1 = deferred();
        const page2 = deferred();
        state.source = 'claude';
        state.currentRange = 'today';
        paging.sessions.pageSize = 20;

        global.fetch = async (url) => {
            const page = new URL('http://local' + String(url)).searchParams.get('page');
            return page === '2' ? page2.promise : page1.promise;
        };

        paging.sessions.page = 1;
        const oldLoad = loadSessions();
        paging.sessions.page = 2;
        const newLoad = loadSessions();

        page2.resolve(response({ data: [session('new-model', 'new-session')], total: 21 }));
        await newLoad;
        page1.resolve(response({ data: [session('old-model', 'old-session')], total: 21 }));
        await oldLoad;

        assert.match(tbody.innerHTML, /new-session/);
        assert.doesNotMatch(tbody.innerHTML, /old-session/);
        assert.equal(paging.sessions.page, 2);
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.source = 'claude';
        state.currentRange = 'today';
        paging.sessions.page = 1;
    }
});
