import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { syncGranularityButtons } from '../js/granularity.js';

const html = fs.readFileSync(new URL('../index.html', import.meta.url), 'utf8');
const css = fs.readFileSync(new URL('../style.css', import.meta.url), 'utf8');

test('toolbar granularity control exposes segmented-control semantics', () => {
    assert.match(html, /id="granularity-switch"[^>]*role="group"[^>]*aria-label="Chart granularity"/);
    assert.match(html, /type="button" class="gran-btn active" data-gran="day" aria-pressed="true"/);
    assert.match(html, /type="button" class="gran-btn" data-gran="month" aria-pressed="false"/);
    assert.match(html, /style\.css\?v=24/);
    assert.match(html, /app\.js\?v=66/);
});

test('toolbar granularity control uses scoped graphite styling', () => {
    assert.match(css, /#granularity-switch\s*\{[^}]*background:\s*var\(--surface2\)/s);
    assert.match(css, /#granularity-switch::before\s*\{[^}]*background:\s*var\(--border-hover\)/s);
    const activeRule = css.match(/#granularity-switch \.gran-btn\.active\s*\{([^}]*)\}/s);
    assert.ok(activeRule, 'missing active granularity rule');
    assert.match(activeRule[1], /background:\s*var\(--surface3\)/);
    assert.doesNotMatch(activeRule[1], /--accent|#0a84ff|#007aff|white/i);
});

function fakeButton(gran) {
    const classes = new Set();
    const attributes = new Map();
    return {
        dataset: { gran },
        classList: {
            toggle(name, enabled) {
                if (enabled) classes.add(name);
                else classes.delete(name);
            },
            contains(name) { return classes.has(name); },
        },
        setAttribute(name, value) { attributes.set(name, value); },
        getAttribute(name) { return attributes.get(name); },
    };
}

test('syncGranularityButtons keeps active class and aria-pressed aligned', () => {
    const day = fakeButton('day');
    const month = fakeButton('month');
    const buttons = [day, month];

    syncGranularityButtons(buttons, 'month');
    assert.equal(day.classList.contains('active'), false);
    assert.equal(day.getAttribute('aria-pressed'), 'false');
    assert.equal(month.classList.contains('active'), true);
    assert.equal(month.getAttribute('aria-pressed'), 'true');

    syncGranularityButtons(buttons, 'day');
    assert.equal(day.classList.contains('active'), true);
    assert.equal(day.getAttribute('aria-pressed'), 'true');
    assert.equal(month.classList.contains('active'), false);
    assert.equal(month.getAttribute('aria-pressed'), 'false');
});
