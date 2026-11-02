import test from 'node:test';
import assert from 'node:assert/strict';
import { state } from '../js/state.js';
import { renderHourdayChart, loadHourOfDay } from '../js/panel-daily.js';

const CELL = (hour, svc, model, tokens) => ({
    hour, service_name: svc, model, input_tokens: tokens, output_tokens: 0,
    cache_read_tokens: 0, cache_creation_tokens: 0, total_tokens: tokens,
    cost_usd: 0.01, request_count: 1,
});

function installDom() {
    // Container that models innerHTML='' destroying child DOM — exactly what
    // happens in a real browser, and exactly what the wipe bug got wrong.
    const chartEl = {
        _children: [],
        _html: '',
        get innerHTML() { return this._html; },
        set innerHTML(v) { this._children = []; this._html = String(v); },
        appendChild(n) { this._children.push(n); },
    };
    const selects = {
        'hourday-client': { value: '', _html: '', options: [], set innerHTML(v) { this._html = v; this.options = []; }, get innerHTML() { return this._html; }, appendChild(o) { this.options.push(o); } },
        'hourday-model': { value: '', _html: '', options: [], set innerHTML(v) { this._html = v; this.options = []; }, get innerHTML() { return this._html; }, appendChild(o) { this.options.push(o); } },
    };
    const byId = new Map([['hourday-chart', chartEl], ...Object.entries(selects)]);
    global.document = {
        getElementById: (id) => byId.get(id) || null,
        createElement: (tag) => ({ tagName: tag, value: '', textContent: '' }),
    };

    const calls = { init: 0, setOption: 0, lastOption: null, disposed: 0 };
    global.echarts = {
        init(el) {
            calls.init++;
            el.appendChild('_echarts_root_'); // echarts mounts its DOM here
            return {
                setOption(opt) { calls.setOption++; calls.lastOption = opt; },
                resize() {},
                dispose() { calls.disposed++; },
            };
        },
    };
    global.window = { addEventListener() {} };
    return { chartEl, selects, calls };
}

test('render mounts the chart WITHOUT wiping echarts DOM after init', async () => {
    const old = { document: global.document, echarts: global.echarts, window: global.window };
    try {
        const dom = installDom();
        state.hourdayChart = null;
        state.hourdayClient = '';
        state.hourdayModel = '';
        state.chartMetric = 'tokens';

        // Seed the module's row cache via the real load path.
        const rows = [CELL(13, 'claude-code', 'claude-opus-5.5', 100), CELL(14, 'opencode', 'qwen3-coder', 40)];
        global.fetch = async () => ({ ok: true, json: async () => ({ data: rows }) });
        await loadHourOfDay();

        assert.ok(dom.calls.init === 1, 'chart initialized');
        assert.ok(dom.calls.setOption >= 1, 'option applied');
        // THE regression: the wipe cleared this after init and the chart went
        // permanently blank in real browsers.
        assert.ok(dom.chartEl._children.includes('_echarts_root_'),
            'echarts DOM must survive rendering — innerHTML was wiped after init');
        assert.equal(dom.calls.lastOption.series.length, 2);
        // Pinned chart rules from .claude/CLAUDE.md: never stacked bars,
        // never trigger:'axis'. This chart must keep both prohibitions.
        for (const s of dom.calls.lastOption.series) {
            assert.equal(s.stack, undefined, 'stacked bars are forbidden');
        }
        assert.equal(dom.calls.lastOption.tooltip.trigger, 'item', 'axis tooltips are forbidden');
        assert.equal(dom.calls.lastOption.series[0].data[13].value, 100);
        // Dropdowns populated.
        assert.ok(dom.selects['hourday-client'].options.length >= 2);
        assert.ok(dom.selects['hourday-model'].options.length >= 2);
    } finally {
        global.document = old.document; global.echarts = old.echarts; global.window = old.window;
    }
});

test('after a message, the placeholder is cleared BEFORE re-init', async () => {
    const old = { document: global.document, echarts: global.echarts, window: global.window };
    try {
        const dom = installDom();
        state.hourdayChart = null;
        state.hourdayClient = '';
        state.hourdayModel = '';
        state.chartMetric = 'tokens';

        // First load: empty range -> placeholder message, chart disposed.
        global.fetch = async () => ({ ok: true, json: async () => ({ data: [] }) });
        await loadHourOfDay();
        assert.ok(dom.chartEl.innerHTML.includes('No data'), 'placeholder shown');

        // Second load: real data -> must clear placeholder, then init, then paint.
        global.fetch = async () => ({ ok: true, json: async () => ({ data: [CELL(13, 'claude-code', 'glm-5.2', 9)] }) });
        await loadHourOfDay();
        assert.ok(dom.calls.init === 1);
        assert.ok(dom.chartEl._children.includes('_echarts_root_'));
        assert.ok(!dom.chartEl.innerHTML.includes('No data'), 'placeholder removed');
    } finally {
        global.document = old.document; global.echarts = old.echarts; global.window = old.window;
    }
});
