import test from 'node:test';
import assert from 'node:assert/strict';
import {
    buildCalendarGrid,
    calendarLayoutMode,
    calendarIntensityLevel,
    calendarMetricValue,
    calendarMonthLabels,
    formatCalendarHeadline,
} from '../js/insights.js';

test('calendarMetricValue reads tokens, cost, and request fields safely', () => {
    const row = {
        total_tokens: 1234,
        input_tokens: 100,
        output_tokens: 20,
        cache_read_tokens: 30,
        cache_creation_tokens: 4,
        cost_usd: 1.25,
        request_count: 9,
    };
    assert.equal(calendarMetricValue(row, 'tokens'), 1234);
    assert.equal(calendarMetricValue(row, 'cost'), 1.25);
    assert.equal(calendarMetricValue(row, 'requests'), 9);
    assert.equal(calendarMetricValue({}, 'tokens'), 0);
});

test('calendarIntensityLevel is zero-safe, monotonic, and caps at 5', () => {
    const values = [0, 10, 100, 1000, 10000];
    assert.equal(calendarIntensityLevel(0, values), 0);
    assert.equal(calendarIntensityLevel(10000, values), 5);
    assert.ok(calendarIntensityLevel(10, values) >= 1);
    assert.ok(calendarIntensityLevel(100, values) >= calendarIntensityLevel(10, values));
    assert.ok(calendarIntensityLevel(1000, values) >= calendarIntensityLevel(100, values));
    assert.equal(calendarIntensityLevel(50000, values), 5);
});

test('buildCalendarGrid aligns dates by weekday and inserts leading padding', () => {
    const grid = buildCalendarGrid(
        [{ date: '2026-05-04', total_tokens: 10 }],
        '2026-05-04',
        '2026-05-10',
    );

    assert.equal(grid.weekCount, 2);
    assert.equal(grid.cells[0].date, '2026-05-03');
    assert.equal(grid.cells[0].isPadding, true);

    const monday = grid.cells.find(c => c.date === '2026-05-04');
    assert.equal(monday.row, 1);
    assert.equal(monday.col, 0);
    assert.equal(monday.isPadding, false);
    assert.equal(monday.data.total_tokens, 10);

    const sunday = grid.cells.find(c => c.date === '2026-05-10');
    assert.equal(sunday.row, 0);
    assert.equal(sunday.col, 1);
});

test('calendarLayoutMode separates strip, short, medium, and long fixed windows', () => {
    assert.equal(calendarLayoutMode(2, 7), 'strip');
    assert.equal(calendarLayoutMode(2, 10), 'strip');
    assert.equal(calendarLayoutMode(5, 30), 'short');
    assert.equal(calendarLayoutMode(8, 60), 'short');
    assert.equal(calendarLayoutMode(9, 70), 'medium');
    assert.equal(calendarLayoutMode(18, 120), 'medium');
    assert.equal(calendarLayoutMode(19, 365), 'long');
    // Fixed windows keep their ladder when the range key is passed.
    assert.equal(calendarLayoutMode(2, 8, 'week'), 'strip');
    assert.equal(calendarLayoutMode(9, 70, 'month'), 'medium');
});

test('All Time always uses the compact left-aligned layout regardless of history span', () => {
    // Production shape 2026-08-28 → 2026-11-04 is 69 days / 11 weeks and used
    // to fall into 'medium': big centered cells plus a horizontal scrollbar.
    assert.equal(calendarLayoutMode(11, 69, 'all'), 'long');
    assert.equal(calendarLayoutMode(2, 7, 'all'), 'long');
    assert.equal(calendarLayoutMode(5, 30, 'all'), 'long');
    assert.equal(calendarLayoutMode(9, 70, 'all'), 'long');
    assert.equal(calendarLayoutMode(18, 120, 'all'), 'long');
    assert.equal(calendarLayoutMode(20, 365, 'all'), 'long');
});

test('calendarMonthLabels marks the first visible month and month changes', () => {
    const grid = buildCalendarGrid([], '2026-04-12', '2026-05-11');
    const labels = calendarMonthLabels(grid.cells);

    assert.deepEqual(labels, [
        { col: 0, label: 'Apr' },
        { col: 2, label: 'May' },
    ]);
});

test('calendarMonthLabels drops labels that would overlap in compact grids', () => {
    // History starting 2026-08-28 (Fri): the grid pads back to Sun 08-23, so
    // 'Aug' sits at col 0 and 'Sep' begins at col 1 — one track apart. At 13px
    // (long) or 22px (medium) tracks the two 3-char labels overlap.
    const monthEnd = buildCalendarGrid([], '2026-08-28', '2026-09-30');
    assert.deepEqual(calendarMonthLabels(monthEnd.cells), [
        { col: 0, label: 'Aug' },
    ]);
    // Wide 'short' tracks (30px) fit adjacent labels: minColGap=1 keeps them.
    assert.deepEqual(calendarMonthLabels(monthEnd.cells, 1), [
        { col: 0, label: 'Aug' },
        { col: 1, label: 'Sep' },
    ]);

    // Production shape: later months stay ≥ 2 tracks apart and survive.
    const prodShape = buildCalendarGrid([], '2026-08-28', '2026-11-04');
    assert.deepEqual(calendarMonthLabels(prodShape.cells), [
        { col: 0, label: 'Aug' },
        { col: 5, label: 'Oct' },
        { col: 10, label: 'Nov' },
    ]);
});

test('formatCalendarHeadline labels the calendar value as an average', () => {
    assert.equal(
        formatCalendarHeadline('tokens', 16055432, 8, '2026-04-12', '2026-05-11'),
        'Usage Calendar · avg/day 16.1M tokens · active days 8 · 2026-04-12 → 2026-05-11',
    );
});
