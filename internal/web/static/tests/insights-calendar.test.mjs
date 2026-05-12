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

test('calendarLayoutMode separates strip, short, medium, and all-time ranges', () => {
    assert.equal(calendarLayoutMode(2, 7), 'strip');
    assert.equal(calendarLayoutMode(2, 10), 'strip');
    assert.equal(calendarLayoutMode(5, 30), 'short');
    assert.equal(calendarLayoutMode(8, 60), 'short');
    assert.equal(calendarLayoutMode(9, 70), 'medium');
    assert.equal(calendarLayoutMode(18, 120), 'medium');
    assert.equal(calendarLayoutMode(19, 365), 'long');
    assert.equal(calendarLayoutMode(5, 30, 'all'), 'short');
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

test('formatCalendarHeadline labels the calendar value as an average', () => {
    assert.equal(
        formatCalendarHeadline('tokens', 16055432, 8, '2026-04-12', '2026-05-11'),
        'Usage Calendar · avg/day 16.1M tokens · active days 8 · 2026-04-12 → 2026-05-11',
    );
});
