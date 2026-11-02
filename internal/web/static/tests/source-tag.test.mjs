import test from 'node:test';
import assert from 'node:assert/strict';
import { formatSourceTag } from '../js/utils.js';

test('curated aliases render identically everywhere (user-approved)', () => {
    // The user asked to KEEP friendly aliases for the DSH clients — they
    // live in the one shared map, so Request Log and Clients show the same
    // label for the same service_name.
    assert.ok(formatSourceTag('deepseek-harness-plugin').includes('>DSH Plugin<'));
    assert.ok(formatSourceTag('deepseek-harness').includes('>DeepSeek Harness<'));
});

test('unknown service names render VERBATIM — no invented aliases', () => {
    const html = formatSourceTag('some-future-agent');
    assert.ok(html.includes('>some-future-agent<'));
    assert.ok(html.includes('source-some-future-agent'));
});

test('known clients keep their curated labels', () => {
    assert.ok(formatSourceTag('claude-code').includes('>Claude Code<'));
    assert.ok(formatSourceTag('opencode').includes('>OpenCode<'));
    assert.ok(formatSourceTag('pi').includes('>Pi<'));
});

test('every codex variant collapses onto the Codex label and class', () => {
    for (const name of ['codex', 'Codex_Exec', 'codex-cli']) {
        const html = formatSourceTag(name);
        assert.ok(html.includes('>Codex<'), name);
        assert.ok(html.includes('source-codex'), name);
    }
});

test('empty service name renders the em-dash unknown tag', () => {
    const html = formatSourceTag('');
    assert.ok(html.includes('source-unknown'));
});

test('special characters in a service name are escaped, not injected', () => {
    const html = formatSourceTag('weird<script>');
    assert.ok(!html.includes('<script>'));
    assert.ok(html.includes('&lt;script&gt;'));
});
