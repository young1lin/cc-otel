/**
 * Regression tests for Insights metric formatting (see app.js insightMetricScalar / fmtMetricValue).
 * Run: node --test internal/web/static/tests/insights-metric.test.mjs
 *
 * Bug fixed: fmtMetricValue('reqs', undefined) used String(Math.round(undefined)) => "NaN"
 * while tokens used fmtNum which coerced NaN to "0".
 */

import test from 'node:test';
import assert from 'node:assert/strict';

// Keep in sync with internal/web/static/app.js (Insights section)
function insightMetricScalar(v, key) {
    if (v == null || typeof v !== 'object') return 0;
    const n = Number(v[key]);
    return Number.isFinite(n) ? n : 0;
}

function fmtMetricValue(metric, v) {
    const n = Number(v);
    const safe = Number.isFinite(n) ? n : 0;
    if (metric === 'cost') return '$' + safe.toFixed(4);
    if (metric === 'reqs') return String(Math.round(safe));
    return String(Math.round(safe));
}

function metricKey(metric) {
    if (metric === 'cost') return 'cost';
    if (metric === 'reqs' || metric === 'requests') return 'reqs';
    return 'tokens';
}

function topEntry(map, key) {
    let bestM = '—';
    let bestV = -Infinity;
    for (const [m, v] of map.entries()) {
        const val = insightMetricScalar(v, key);
        if (val > bestV) { bestV = val; bestM = m; }
    }
    if (bestV === -Infinity || !Number.isFinite(bestV)) {
        return { model: '—', value: 0 };
    }
    return { model: bestM, value: bestV };
}

test('fmtMetricValue(reqs) never prints NaN for undefined/null', () => {
    assert.equal(fmtMetricValue('reqs', undefined), '0');
    assert.equal(fmtMetricValue('reqs', null), '0');
    assert.equal(fmtMetricValue('reqs', NaN), '0');
});

test('fmtMetricValue(tokens) coerces bad input to 0', () => {
    assert.equal(fmtMetricValue('tokens', undefined), '0');
});

test('metricKey maps chart metric alias requests -> reqs', () => {
    assert.equal(metricKey('requests'), 'reqs');
});

test('insightMetricScalar handles missing keys and non-objects', () => {
    assert.equal(insightMetricScalar(null, 'reqs'), 0);
    assert.equal(insightMetricScalar({ reqs: '12' }, 'reqs'), 12);
    assert.equal(insightMetricScalar({ reqs: undefined }, 'reqs'), 0);
});

test('topEntry returns value 0 when map empty or all zeros', () => {
    const empty = new Map();
    assert.deepEqual(topEntry(empty, 'reqs'), { model: '—', value: 0 });

    const m = new Map([['a', { tokens: 0, cost: 0, reqs: 0 }]]);
    assert.deepEqual(topEntry(m, 'reqs'), { model: 'a', value: 0 });
});
