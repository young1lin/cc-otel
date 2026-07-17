import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

const html = fs.readFileSync(new URL('../index.html', import.meta.url), 'utf8');

// #daily-byhour spans from its opening div to the close of #panel-daily.
function byHourSection() {
    const start = html.indexOf('id="daily-byhour"');
    assert.ok(start > 0, 'missing #daily-byhour');
    const end = html.indexOf('id="panel-sessions"', start);
    assert.ok(end > start, 'missing #panel-sessions terminator');
    return html.slice(start, end);
}

test('Intraday detail table is gone', () => {
    const section = byHourSection();
    assert.ok(!section.includes('hourly-tbody'), 'hourly-tbody must be removed');
    assert.ok(!section.includes('<table'), 'the Intraday table must be removed');
});

test('Intraday bucket dropdown exists with all five choices', () => {
    const section = byHourSection();
    assert.match(section, /id="intraday-bucket-dd"/);
    for (const v of [5, 10, 15, 30, 60]) {
        assert.match(section, new RegExp(`data-value="${v}"`), `missing bucket ${v}`);
    }
});

test('Intraday bucket dropdown is accessibly named and distinct from the Rate one', () => {
    const section = byHourSection();
    assert.match(section, /aria-label="Intraday time bucket"/);
    assert.match(section, /type="button"[^>]*data-trigger/);
    assert.match(section, /aria-haspopup="listbox"/);
});

test('Intraday has an All-models escape hatch, hidden until a model is isolated', () => {
    const section = byHourSection();
    assert.match(section, /<button type="button" id="intraday-legend-all-btn"/);
    assert.match(section, /id="intraday-legend-all-btn"[^>]*style="display:none"/);
});

// Pinned on purpose: embedded assets are cached by these query strings, so any
// asset change must bump them here too. Update both when you edit CSS or JS.
test('cache-busting versions bumped', () => {
    assert.match(html, /style\.css\?v=26/);
    assert.match(html, /app\.js\?v=68/);
});
