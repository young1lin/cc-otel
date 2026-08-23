import test from 'node:test';
import assert from 'node:assert/strict';
import { state } from '../js/state.js';
import { loadRate } from '../js/panel-rate.js';

test('Codex Rate is rejected without fetching non-Codex data', async () => {
    const oldDocument = global.document;
    const oldFetch = global.fetch;
    try {
        const chart = { innerHTML: '' };
        global.document = {
            getElementById(id) { return id === 'rate-chart' ? chart : null; },
        };
        let fetches = 0;
        global.fetch = async () => {
            fetches += 1;
            throw new Error('Rate must not fetch while Codex is selected');
        };
        state.source = 'codex';

        await loadRate();

        assert.equal(fetches, 0);
        assert.match(chart.innerHTML, /unavailable for Codex/);
    } finally {
        global.document = oldDocument;
        global.fetch = oldFetch;
        state.source = 'claude';
    }
});
