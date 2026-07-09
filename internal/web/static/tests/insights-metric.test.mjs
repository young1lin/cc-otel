/**
 * Regression tests for Insights metric formatting.
 * Run: node --test internal/web/static/tests/insights-metric.test.mjs
 *
 * Bug fixed: fmtMetricValue('reqs', undefined) used String(Math.round(undefined)) => "NaN"
 * while tokens used fmtNum which coerced NaN to "0".
 */

import test from 'node:test';
import assert from 'node:assert/strict';
import {
    insightMetricScalar, fmtMetricValue, metricKey, topEntry,
} from '../js/insights.js';

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
