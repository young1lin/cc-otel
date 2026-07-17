import test from 'node:test';
import assert from 'node:assert/strict';
import { fillIntradaySlots, slotLabel, defaultVisibleSlots, DEFAULT_WINDOW_MINUTES } from '../js/intraday-slots.js';

// 2026-07-17 09:00 local, expressed as a unix second so the test is TZ-agnostic.
const base = Math.floor(new Date(2026, 6, 17, 9, 0, 0).getTime() / 1000);
const STEP5 = 5 * 60;

function row(unix, model, extra = {}) {
    return {
        bucket_start_unix: unix,
        bucket_label: slotLabel(unix),
        model,
        input_tokens: 10,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        output_tokens: 1,
        request_count: 1,
        cost_usd: 0.01,
        ...extra,
    };
}

test('empty input yields no slots', () => {
    const { slots, rowAt } = fillIntradaySlots([], 5);
    assert.deepEqual(slots, []);
    assert.equal(rowAt.size, 0);
});

test('invalid bucket yields no slots', () => {
    for (const b of [0, -5, NaN, undefined]) {
        assert.deepEqual(fillIntradaySlots([row(base, 'a')], b).slots, []);
    }
});

test('a contiguous run is unchanged in length', () => {
    const rows = [row(base, 'a'), row(base + STEP5, 'a'), row(base + 2 * STEP5, 'a')];
    const { slots } = fillIntradaySlots(rows, 5);
    assert.equal(slots.length, 3);
    assert.deepEqual(slots.map((s) => s.unix), [base, base + STEP5, base + 2 * STEP5]);
});

test('internal gaps are filled so the axis is a true timeline', () => {
    // 09:00 then 09:20 — three empty 5-minute slots in between.
    const rows = [row(base, 'a'), row(base + 4 * STEP5, 'a')];
    const { slots, rowAt } = fillIntradaySlots(rows, 5);

    assert.equal(slots.length, 5);
    assert.deepEqual(slots.map((s) => s.unix), [
        base, base + STEP5, base + 2 * STEP5, base + 3 * STEP5, base + 4 * STEP5,
    ]);
    assert.ok(rowAt.has(base + '|a'));
    assert.ok(!rowAt.has(base + STEP5 + '|a'), 'gap slots carry no row');
});

test('fill spans data extent only — no leading or trailing padding', () => {
    const rows = [row(base, 'a')];
    const { slots } = fillIntradaySlots(rows, 5);
    assert.equal(slots.length, 1, 'a single bucket must not pad out to the whole day');
    assert.equal(slots[0].unix, base);
});

test('multiple models share one slot list and are keyed independently', () => {
    const rows = [row(base, 'a'), row(base, 'b'), row(base + 2 * STEP5, 'b')];
    const { slots, rowAt } = fillIntradaySlots(rows, 5);

    assert.equal(slots.length, 3, 'slots are unique instants, not per-model rows');
    assert.ok(rowAt.has(base + '|a'));
    assert.ok(rowAt.has(base + '|b'));
    assert.ok(!rowAt.has(base + 2 * STEP5 + '|a'), 'model a has no row in the last slot');
    assert.ok(rowAt.has(base + 2 * STEP5 + '|b'));
});

test('real rows keep the server label; gaps get a synthesized one', () => {
    const rows = [
        { ...row(base, 'a'), bucket_label: 'SERVER-LABEL' },
        row(base + 2 * STEP5, 'a'),
    ];
    const { slots } = fillIntradaySlots(rows, 5);
    assert.equal(slots[0].label, 'SERVER-LABEL');
    assert.equal(slots[1].label, slotLabel(base + STEP5));
});

test('rows out of order still produce an ascending slot list', () => {
    const rows = [row(base + 2 * STEP5, 'a'), row(base, 'a')];
    const { slots } = fillIntradaySlots(rows, 5);
    assert.deepEqual(slots.map((s) => s.unix), [base, base + STEP5, base + 2 * STEP5]);
});

test('slotLabel renders MM-DD HH:MM zero-padded in local time', () => {
    const t = Math.floor(new Date(2026, 0, 5, 7, 5, 0).getTime() / 1000);
    assert.equal(slotLabel(t), '01-05 07:05');
});

test('the default window is a working day, not the full 24 hours', () => {
    assert.equal(DEFAULT_WINDOW_MINUTES, 16 * 60);
});

test('the default window holds the same wall-clock width at every bucket size', () => {
    // The window is a duration, not a bar count: 48 slots means 4h at 5min but
    // a full day at 30min, which is why a fixed count could not mean "a day".
    const dense = 100000;
    for (const [bucket, slots] of [[5, 192], [10, 96], [15, 64], [30, 32], [60, 16]]) {
        assert.equal(defaultVisibleSlots(bucket, dense), slots, `${bucket}min`);
        assert.equal(defaultVisibleSlots(bucket, dense) * bucket, DEFAULT_WINDOW_MINUTES, `${bucket}min spans 16h`);
    }
});

test('a range shorter than the window shows every slot rather than padding', () => {
    assert.equal(defaultVisibleSlots(5, 40), 40);
});

test('a full day of 5min buckets opens on the last 16h — 08:00 to 24:00', () => {
    const wholeDay = (24 * 60) / 5; // 288
    assert.equal(defaultVisibleSlots(5, wholeDay), 192);
    assert.equal((wholeDay - 192) * 5, 8 * 60, 'the hidden head is exactly 00:00-08:00');
});

test('invalid input yields no window rather than NaN', () => {
    assert.equal(defaultVisibleSlots(0, 100), 0);
    assert.equal(defaultVisibleSlots(5, 0), 0);
    assert.equal(defaultVisibleSlots(-5, 100), 0);
});
