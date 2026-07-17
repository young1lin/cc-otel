import test from 'node:test';
import assert from 'node:assert/strict';
import { state } from '../js/state.js';
import { buildBarTooltip } from '../js/chart-main.js';

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
