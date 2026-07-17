import test from 'node:test';
import assert from 'node:assert/strict';
import { state } from '../js/state.js';
import { renderDailyRow } from '../js/panel-daily.js';

const codexRow = {
    date: '2026-07-17',
    bucket_label: '07-17 14:00',
    model: 'gpt-5.1',
    input_tokens: 100,
    cache_read_tokens: 40,
    cache_creation_tokens: 20,
    output_tokens: 10,
    total_tokens: 110,
    request_count: 1,
    cost_usd: 0.0123,
};

test('Codex daily row renders uncached and Cache Create separately', () => {
    state.source = 'codex';
    const html = renderDailyRow(codexRow);
    assert.match(html, />40<\/td>/);
    assert.match(html, />20<\/td>/);
    assert.doesNotMatch(html, />100<\/td>/);
    state.source = 'claude';
});
