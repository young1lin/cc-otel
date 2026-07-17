import test from 'node:test';
import assert from 'node:assert/strict';
import { state } from '../js/state.js';
import { buildBarTooltip, makeTokenBarColor } from '../js/chart-main.js';

test('Codex chart tooltip renders Cache Create', () => {
    state.source = 'codex';
    const html = buildBarTooltip({
        color: '#666666',
        seriesName: 'gpt-5.1',
        data: {
            raw: {
                date: '2026-07-17',
                model: 'gpt-5.1',
                input_tokens: 100,
                cache_read_tokens: 40,
                cache_creation_tokens: 20,
                output_tokens: 10,
                total_tokens: 110,
                request_count: 1,
                cost_usd: 0.0123,
            },
        },
    }, {
        mutedText: '#777777',
        tooltipText: '#222222',
        axisLine: '#dddddd',
    });
    assert.match(html, /Cache Create/);
    assert.match(html, />20<\/td>/);
    state.source = 'claude';
});

// echarts is a global in the browser; the gradient branch constructs
// echarts.graphic.LinearGradient. Stub it so the pure logic is testable.
function withEchartsStub(fn) {
    const prev = globalThis.echarts;
    globalThis.echarts = {
        graphic: {
            LinearGradient: class {
                constructor(x0, y0, x1, y1, stops) {
                    Object.assign(this, { x0, y0, x1, y1, stops, __gradient: true });
                }
            },
        },
    };
    try { return fn(); } finally { globalThis.echarts = prev; }
}

const tokenRow = {
    raw: {
        model: 'm',
        input_tokens: 100,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        output_tokens: 100,
        request_count: 1,
        cost_usd: 1,
    },
};

test('makeTokenBarColor returns a flat color for cost and requests', () => {
    const prevMetric = state.chartMetric;
    try {
        for (const m of ['cost', 'requests']) {
            state.chartMetric = m;
            const color = makeTokenBarColor('m')({ data: tokenRow });
            assert.equal(typeof color, 'string', `${m} must not use a gradient`);
        }
    } finally { state.chartMetric = prevMetric; }
});

test('makeTokenBarColor returns a two-stop gradient for tokens', () => {
    const prevMetric = state.chartMetric;
    state.chartMetric = 'tokens';
    try {
        withEchartsStub(() => {
            const color = makeTokenBarColor('m')({ data: tokenRow });
            assert.ok(color.__gradient, 'tokens must use the gradient');
            // 50% output → the split sits at 0.5, well above minVis.
            assert.equal(color.stops[1].offset, 0.5);
            assert.equal(color.stops[2].offset, 0.5);
        });
    } finally { state.chartMetric = prevMetric; }
});

test('makeTokenBarColor floors a tiny output segment at minVis so it stays visible', () => {
    const prevMetric = state.chartMetric;
    state.chartMetric = 'tokens';
    try {
        withEchartsStub(() => {
            const tiny = { raw: { ...tokenRow.raw, input_tokens: 100000, output_tokens: 1 } };
            const color = makeTokenBarColor('m')({ data: tiny });
            assert.equal(color.stops[1].offset, 0.06);
        });
    } finally { state.chartMetric = prevMetric; }
});

test('makeTokenBarColor falls back to a flat color when the datum carries no raw', () => {
    const prevMetric = state.chartMetric;
    state.chartMetric = 'tokens';
    try {
        assert.equal(typeof makeTokenBarColor('m')({ data: 0 }), 'string');
        assert.equal(typeof makeTokenBarColor('m')({}), 'string');
    } finally { state.chartMetric = prevMetric; }
});
