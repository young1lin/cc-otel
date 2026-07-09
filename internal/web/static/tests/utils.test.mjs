import test from 'node:test';
import assert from 'node:assert/strict';
import {
    fmtNum, fmtUSD, fmtPct, fmtTokens, fmtTime, fmtDateTime,
    escapeHtml, truncate, formatUserCell,
    toYMD, getTodayYMD, isValidYMD, rangeToFromTo, quantBadge,
} from '../js/utils.js';
import { state } from '../js/state.js';

test('fmtNum coerces NaN/null to "0"', () => {
    assert.equal(fmtNum(undefined), '0');
    assert.equal(fmtNum(null), '0');
    assert.equal(fmtNum(NaN), '0');
});

test('fmtNum abbreviates K / M / B thresholds', () => {
    assert.equal(fmtNum(999), '999');
    assert.equal(fmtNum(1500), '1.5K');
    assert.equal(fmtNum(1_500_000), '1.5M');
    assert.equal(fmtNum(2_340_000_000), '2.34B');
});

test('fmtTokens delegates to fmtNum', () => {
    assert.equal(fmtTokens(1500), '1.5K');
});

test('fmtUSD always 4 decimals, NaN-safe', () => {
    assert.equal(fmtUSD(0), '$0.0000');
    assert.equal(fmtUSD(1.2), '$1.2000');
    assert.equal(fmtUSD(undefined), '$0.0000');
});

test('fmtPct rounds to 1 decimal, infinite -> em-dash', () => {
    assert.equal(fmtPct(12.345), '12.3%');
    assert.equal(fmtPct(NaN), '—');
    assert.equal(fmtPct(Infinity), '—');
});

test('fmtTime: falsy -> em-dash', () => {
    assert.equal(fmtTime(0), '—');
    assert.equal(fmtTime(null), '—');
    assert.equal(fmtTime(undefined), '—');
});

test('fmtDateTime: 24-hour, zero-padded; midnight is 00 not "12 AM"', () => {
    // Local midnight; built from local components so the assertion is timezone-independent.
    const d = new Date(2026, 5, 11, 0, 6, 29);
    assert.equal(fmtDateTime(d), '2026-06-11 00:06:29');
});

test('fmtDateTime: afternoon stays 24-hour (13, not "1 PM")', () => {
    const d = new Date(2026, 5, 11, 13, 5, 8);
    assert.equal(fmtDateTime(d), '2026-06-11 13:05:08');
});

test('fmtDateTime: empty/invalid -> em-dash, bad string passthrough', () => {
    assert.equal(fmtDateTime(''), '—');
    assert.equal(fmtDateTime(null), '—');
    assert.equal(fmtDateTime('not-a-date'), 'not-a-date');
});

test('escapeHtml escapes the five reserved chars', () => {
    assert.equal(
        escapeHtml(`<a href="x">&y'z</a>`),
        '&lt;a href=&quot;x&quot;&gt;&amp;y&#39;z&lt;/a&gt;',
    );
    assert.equal(escapeHtml(null), '');
});

test('truncate respects max length', () => {
    assert.equal(truncate('hello world', 5), 'hello…');
    assert.equal(truncate('hi', 5), 'hi');
    assert.equal(truncate('', 5), '');
});

test('formatUserCell returns em-dash for empty, badge HTML otherwise', () => {
    assert.equal(formatUserCell(''), '—');
    const html = formatUserCell('user-12345678901234567890');
    assert.match(html, /^<span class="badge" title="user-12345678901234567890">/);
    assert.match(html, /user-12345…<\/span>$/);
});

test('toYMD formats Date as YYYY-MM-DD', () => {
    assert.equal(toYMD(new Date(2026, 0, 5)), '2026-01-05');
    assert.equal(toYMD(new Date(2026, 11, 31)), '2026-12-31');
});

test('isValidYMD strict format', () => {
    assert.equal(isValidYMD('2026-01-05'), true);
    assert.equal(isValidYMD('2026-1-5'), false);
    assert.equal(isValidYMD('2026-02-30'), false);
    assert.equal(isValidYMD(''), false);
    assert.equal(isValidYMD(null), false);
});

test('rangeToFromTo "today" returns today twice', () => {
    const r = rangeToFromTo('today');
    assert.equal(r.from, getTodayYMD());
    assert.equal(r.to, getTodayYMD());
});

test('rangeToFromTo "all" starts at 1970-01-01', () => {
    const r = rangeToFromTo('all');
    assert.equal(r.from, '1970-01-01');
});

test('rangeToFromTo "custom" reads from state.customFrom/To', () => {
    state.customFrom = '2026-04-01';
    state.customTo   = '2026-04-15';
    const r = rangeToFromTo('custom');
    assert.deepEqual(r, { from: '2026-04-01', to: '2026-04-15' });
    state.customFrom = '';
    state.customTo   = '';
});

test('quantBadge flags aggressive low-bit quants only', () => {
    assert.equal(quantBadge('fp4'), 'quantized');
    assert.equal(quantBadge('int4'), 'quantized');
    assert.equal(quantBadge('NF4'), 'quantized');
    assert.equal(quantBadge('fp8'), '');
    assert.equal(quantBadge('unknown'), '');
    assert.equal(quantBadge(''), '');
    assert.equal(quantBadge(null), '');
});
