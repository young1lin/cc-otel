import test from 'node:test';
import assert from 'node:assert/strict';
import { buildHourdayView } from '../js/panel-daily.js';

const cell = (hour, svc, model, tokens, reqs = 1, cost = 0.01) => ({
    hour, service_name: svc, model,
    input_tokens: tokens, output_tokens: 0, cache_read_tokens: 0,
    cache_creation_tokens: 0, total_tokens: tokens,
    cost_usd: cost, request_count: reqs,
});

const rows = [
    cell(13, 'claude-code', 'claude-opus-5.5', 100, 2, 0.02),
    cell(13, 'claude-code', 'glm-5.2', 50),
    cell(13, 'opencode', 'claude-opus-5.5', 30),
    cell(14, 'opencode', 'qwen3-coder', 40),
];

const valAt = (view, name, hour) => {
    const s = view.series.find((x) => x.name === name);
    if (!s) throw new Error('no series ' + name + ' in ' + view.series.map((x) => x.name));
    return s.data[hour].value;
};

test('no filter: stacked by model, clients merged into the model bar', () => {
    const v = buildHourdayView(rows);
    assert.deepEqual(v.series.map((s) => s.name), ['claude-opus-5.5', 'glm-5.2', 'qwen3-coder']);
    assert.equal(valAt(v, 'claude-opus-5.5', 13), 130); // claude-code 100 + opencode 30
    assert.equal(valAt(v, 'glm-5.2', 13), 50);
    assert.equal(valAt(v, 'qwen3-coder', 14), 40);
    assert.equal(valAt(v, 'claude-opus-5.5', 12), 0); // empty hour zeroed
    assert.equal(v.categories.length, 24);
});

test('client pinned: only that client, still broken down by model', () => {
    const v = buildHourdayView(rows, { client: 'claude-code' });
    assert.deepEqual(v.series.map((s) => s.name), ['claude-opus-5.5', 'glm-5.2']);
    assert.equal(valAt(v, 'claude-opus-5.5', 13), 100);
    assert.equal(valAt(v, 'glm-5.2', 13), 50);
    assert.equal(valAt(v, 'claude-opus-5.5', 14), 0);
});

test('model pinned: axis flips to clients — "who ran Claude Opus at 13:00"', () => {
    const v = buildHourdayView(rows, { model: 'claude-opus-5.5' });
    assert.deepEqual(v.series.map((s) => s.name), ['claude-code', 'opencode']);
    assert.equal(valAt(v, 'claude-code', 13), 100);
    assert.equal(valAt(v, 'opencode', 13), 30);
});

test('both pinned: a single series answering the exact question', () => {
    const v = buildHourdayView(rows, { client: 'claude-code', model: 'claude-opus-5.5' });
    assert.equal(v.series.length, 1);
    assert.equal(v.series[0].name, 'claude-code');
    assert.equal(v.series[0].data[13].value, 100);
});

test('top-N models collapse into an Other bucket that still stacks', () => {
    const many = [];
    for (let i = 0; i < 10; i++) many.push(cell(13, 'claude-code', 'm' + i, 10 + i));
    const v = buildHourdayView(many);
    const names = v.series.map((s) => s.name);
    assert.equal(names.length, 9); // 8 named + Other
    assert.ok(names[names.length - 1].startsWith('Other (2)'));
    // m0/m1 are the two smallest -> inside Other: 10 + 11 = 21.
    assert.equal(v.series[v.series.length - 1].data[13].value, 21);
    const total = v.series.reduce((s, x) => s + x.data[13].value, 0);
    assert.equal(total, many.reduce((s, r) => s + r.total_tokens, 0));
});

test('metric switch: cost and requests use their own columns', () => {
    const vCost = buildHourdayView(rows, { model: 'claude-opus-5.5', metric: 'cost' });
    assert.equal(vCost.series[0].data[13].value === 0.02, true); // only the opus 2-request row contributes
    const vReqs = buildHourdayView(rows, { model: 'claude-opus-5.5', metric: 'requests' });
    assert.equal(vReqs.series[0].name, 'claude-code');
    assert.equal(vReqs.series[0].data[13].value, 2); // claude-code's 2 opus requests
    assert.equal(vReqs.series[1].data[13].value, 1); // opencode's 1
});

test('dropdown options rank by size and come from unfiltered rows', () => {
    const v = buildHourdayView(rows, { client: 'claude-code' });
    assert.deepEqual(v.clients, ['claude-code', 'opencode']); // opencode still selectable
    assert.deepEqual(v.models, ['claude-opus-5.5', 'glm-5.2', 'qwen3-coder']);
    assert.equal(v.totals.tokens, 150);
    assert.equal(v.totals.requests, 3);
});

test('data points carry the cross-dimension breakdown for tooltips', () => {
    // Default view (by model): the opus bar at 13:00 must expose WHICH
    // clients contributed and how much — "which client's which model".
    const v = buildHourdayView(rows);
    const opus = v.series.find((s) => s.name === 'claude-opus-5.5');
    assert.deepEqual(opus.data[13].parts, [
        { name: 'claude-code', value: 100 },
        { name: 'opencode', value: 30 },
    ]);
    assert.deepEqual(v.meta, { client: '', model: '', byModel: true });

    // Model pinned (by client): each client bar's parts list the models.
    const vp = buildHourdayView(rows, { model: 'claude-opus-5.5' });
    const cc = vp.series.find((s) => s.name === 'claude-code');
    assert.deepEqual(cc.data[13].parts, [{ name: 'claude-opus-5.5', value: 100 }]);
    assert.deepEqual(vp.meta, { client: '', model: 'claude-opus-5.5', byModel: false });
});

test('Other bucket merges cross-dimension parts too', () => {
    const many = [];
    for (let i = 0; i < 10; i++) many.push(cell(13, 'c' + (i % 2), 'm' + i, 10 + i));
    const v = buildHourdayView(many);
    const other = v.series[v.series.length - 1];
    // m0 (c0, 10) and m1 (c1, 11) fall into Other at 13:00.
    assert.deepEqual(other.data[13].parts, [
        { name: 'c1', value: 11 },
        { name: 'c0', value: 10 },
    ]);
});

test('empty input yields an empty but well-shaped view', () => {
    const v = buildHourdayView([]);
    assert.equal(v.series.length, 0);
    assert.equal(v.categories.length, 24);
    assert.deepEqual(v.clients, []);
});
