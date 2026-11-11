import test from 'node:test';
import assert from 'node:assert/strict';
import { state } from '../js/state.js';
import { loadClients, initPanelClients, detailTableHtml } from '../js/panel-clients.js';

/* Minimal DOM stubs for the Clients drill-down interaction. The module's DOM
   touchpoints are narrow (tbody by id, row lookup by dataset, one delegated
   click listener), so a hand-rolled fake is enough to drive the real
   expand/collapse state machine end-to-end under the Node runner. */

function jsonResponse(data) {
    return { ok: true, json: async () => data };
}

function installClientsDOM() {
    const domRows = []; // ordered sibling list standing in for tbody children
    const listeners = {};

    function matches(el, sel) {
        if (sel === 'tr.client-row') return el.className === 'client-row';
        if (sel === 'tr.client-detail-row') return el.className === 'client-detail-row';
        return false;
    }

    const tbody = {
        _innerHTML: '',
        get innerHTML() { return this._innerHTML; },
        set innerHTML(v) { this._innerHTML = v; },
        addEventListener(type, fn) { listeners[type] = fn; },
        querySelectorAll(sel) { return domRows.filter((el) => matches(el, sel)); },
        _domRows: domRows, _listeners: listeners,
    };

    const makeRow = (name) => {
        const btn = { attrs: {}, setAttribute(k, v) { this.attrs[k] = v; } };
        const row = {
            tagName: 'tr', className: 'client-row',
            dataset: { serviceName: name },
            classList: {
                _s: new Set(['client-row']),
                toggle(c, on) { if (on) this._s.add(c); else this._s.delete(c); },
                contains(c) { return this._s.has(c); },
            },
            querySelector(sel) { return sel === 'button.client-toggle' ? btn : null; },
            after(el) { domRows.splice(domRows.indexOf(row) + 1, 0, el); },
        };
        row._btn = btn;
        return row;
    };

    const makeDetail = (name) => ({
        tagName: 'tr', className: 'client-detail-row',
        dataset: { detailFor: name },
        _html: '',
        get innerHTML() { return this._html; },
        set innerHTML(v) { this._html = v; },
        remove() { const i = domRows.indexOf(this); if (i >= 0) domRows.splice(i, 1); },
    });

    const countEl = { textContent: '' };
    global.document = {
        getElementById(id) { return id === 'clients-tbody' ? tbody : (id === 'clients-count' ? countEl : null); },
        createElement: () => null, // replaced per-test via makeDetail below
    };

    return { tbody, listeners, domRows, makeRow, makeDetail, countEl,
        patchCreateElement: (fn) => { global.document.createElement = fn; } };
}

const CLIENTS_PAYLOAD = {
    data: [
        { service_name: 'claude-code', request_count: 10, input_tokens: 200, cache_read_tokens: 200,
          cache_creation_tokens: 100, output_tokens: 100, total_tokens: 600, cost_usd: 0.05,
          cache_hit_rate: 0.4, model_count: 2, first_date: '2026-11-01', last_date: '2026-11-10' },
        { service_name: 'codex', request_count: 5, input_tokens: 1000, cache_read_tokens: 0,
          cache_creation_tokens: 0, output_tokens: 500, total_tokens: 1500, cost_usd: 0.1,
          cache_hit_rate: 0, model_count: 1, first_date: '2026-11-02', last_date: '2026-11-02' },
    ],
};

const MODELS_PAYLOAD = {
    data: [
        { model: 'claude-opus-4-6', request_count: 1, input_tokens: 1000, cache_read_tokens: 1000,
          cache_creation_tokens: 500, output_tokens: 500, total_tokens: 3000, cost_usd: 0.05, cache_hit_rate: 0.4 },
        { model: 'glm-5.3', request_count: 2, input_tokens: 200, cache_read_tokens: 200,
          cache_creation_tokens: 100, output_tokens: 100, total_tokens: 600, cost_usd: 0.01, cache_hit_rate: 0.4 },
    ],
};

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

test('clients rows render with drill-down affordances', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;
    try {
        const dom = installClientsDOM();
        state.currentRange = 'all';
        global.fetch = async (url) => {
            assert.match(String(url), /^\/api\/clients\?from=1970-01-01/);
            return jsonResponse(CLIENTS_PAYLOAD);
        };
        await loadClients();
        await flush();
        const html = dom.tbody.innerHTML;
        assert.match(html, /<tr class="client-row" data-service-name="claude-code">/);
        assert.match(html, /class="client-toggle" aria-expanded="false"/);
        assert.match(html, /data-service-name="codex"/);
        assert.equal(dom.countEl.textContent, '2');
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.currentRange = 'today';
    }
});

test('click expands per-model drill-down, click again collapses', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;
    try {
        const dom = installClientsDOM();
        state.currentRange = 'all';
        let modelsFetches = 0;
        global.fetch = async (url) => {
            const u = String(url);
            if (u.startsWith('/api/clients/models')) {
                modelsFetches++;
                assert.match(u, /service_name=claude-code/);
                assert.match(u, /from=1970-01-01/);
                return jsonResponse(MODELS_PAYLOAD);
            }
            return jsonResponse(CLIENTS_PAYLOAD);
        };
        const row = dom.makeRow('claude-code');
        dom.domRows.push(row);
        dom.patchCreateElement(() => dom.makeDetail('claude-code'));
        initPanelClients();

        // Expand via the delegated click path (same path a keyboard Enter on
        // the chevron button takes).
        dom.listeners.click({ target: { closest: (sel) => (sel === 'tr.client-row' ? row : null) } });
        await flush();

        assert.equal(modelsFetches, 1);
        assert.equal(row._btn.attrs['aria-expanded'], 'true');
        assert.ok(row.classList.contains('client-expanded'));
        const detail = dom.domRows.find((el) => el.className === 'client-detail-row');
        assert.ok(detail, 'detail row inserted after the client row');
        assert.match(detail.innerHTML, /claude-opus-4-6/);
        assert.match(detail.innerHTML, /glm-5\.3/);
        // Descending order: opus (3000) must render before glm (600).
        assert.ok(detail.innerHTML.indexOf('claude-opus-4-6') < detail.innerHTML.indexOf('glm-5.3'));
        // Share is relative to the fetched model sum (3000+600): opus 83.3%.
        assert.match(detail.innerHTML, /83\.3%/);
        assert.match(detail.innerHTML, /16\.7%/);

        // Collapse.
        dom.listeners.click({ target: { closest: (sel) => (sel === 'tr.client-row' ? row : null) } });
        await flush();
        assert.equal(row._btn.attrs['aria-expanded'], 'false');
        assert.ok(!row.classList.contains('client-expanded'));
        assert.equal(dom.domRows.filter((el) => el.className === 'client-detail-row').length, 0);

        // Re-expand fetches FRESH data (no caching) — user-pinned behavior.
        dom.listeners.click({ target: { closest: (sel) => (sel === 'tr.client-row' ? row : null) } });
        await flush();
        assert.equal(modelsFetches, 2);

        // Leave the module-level expansion set empty for the next test.
        dom.listeners.click({ target: { closest: (sel) => (sel === 'tr.client-row' ? row : null) } });
        await flush();
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.currentRange = 'today';
    }
});

test('loadClients re-render keeps expanded rows alive with fresh numbers', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;
    try {
        const dom = installClientsDOM();
        state.currentRange = 'all';
        let modelsFetches = 0;
        global.fetch = async (url) => {
            const u = String(url);
            if (u.startsWith('/api/clients/models')) { modelsFetches++; return jsonResponse(MODELS_PAYLOAD); }
            return jsonResponse(CLIENTS_PAYLOAD);
        };
        const row = dom.makeRow('claude-code');
        dom.domRows.push(row);
        dom.patchCreateElement(() => dom.makeDetail('claude-code'));
        initPanelClients();

        dom.listeners.click({ target: { closest: (sel) => (sel === 'tr.client-row' ? row : null) } });
        await flush();
        assert.equal(modelsFetches, 1);

        // An SSE push re-renders the whole tbody. The simulated DOM keeps the
        // (re-parsed) row; expansion must survive and re-fetch.
        await loadClients();
        await flush();
        assert.equal(modelsFetches, 2);
        assert.equal(row._btn.attrs['aria-expanded'], 'true');
        assert.ok(dom.domRows.some((el) => el.className === 'client-detail-row'));
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.currentRange = 'today';
    }
});

test('detailTableHtml: escaping, share denominators, empty state handled by caller', () => {
    const html = detailTableHtml([
        { model: '<script>x</script>', request_count: 1, input_tokens: 10, cache_read_tokens: 0,
          cache_creation_tokens: 0, output_tokens: 10, total_tokens: 20, cost_usd: 0.01, cache_hit_rate: 0 },
        { model: 'glm-5.3', request_count: 3, input_tokens: 30, cache_read_tokens: 0,
          cache_creation_tokens: 0, output_tokens: 30, total_tokens: 60, cost_usd: 0.02, cache_hit_rate: 0 },
    ]);
    assert.ok(!html.includes('<script>x</script>'), 'model names must be escaped');
    assert.match(html, /&lt;script&gt;/);
    assert.match(html, /25\.0%/); // 20 of (20+60)
    assert.match(html, /75\.0%/);
    const header = html.slice(0, html.indexOf('</thead>'));
    for (const label of ['Model', 'Requests', 'Input (Uncached)', 'Cache Read', 'Cache Create', 'Output', 'Total Tokens', 'Share', 'Cache Hit', 'Cost']) {
        assert.ok(header.includes(label), 'missing column ' + label);
    }
});
