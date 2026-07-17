import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {
    formatImportBytes, viewForImportJob, importPollDelay,
} from '../js/import-db.js';
import { shouldRefreshForSSE } from '../js/sse.js';

test('formatImportBytes uses compact binary units', () => {
    assert.equal(formatImportBytes(0), '0 B');
    assert.equal(formatImportBytes(1024), '1 KB');
    assert.equal(formatImportBytes(1536), '1.5 KB');
    assert.equal(formatImportBytes(2 * 1024 ** 3), '2 GB');
});

test('ready job maps to preview and start action', () => {
    const view = viewForImportJob({
        state: 'ready',
        phase: 'preview',
        preview: { source_rows: 10, new_rows: 7, duplicate_rows: 3 },
    });
    assert.equal(view.panel, 'ready');
    assert.equal(view.primaryAction, 'start');
    assert.equal(view.primaryLabel, 'Start merge');
});

test('retryable failure and non-retryable failure choose different actions', () => {
    assert.equal(viewForImportJob({ state: 'failed', retryable: true }).primaryAction, 'retry');
    assert.equal(viewForImportJob({ state: 'failed', retryable: false }).primaryAction, 'choose');
});

test('poll delay is active only for server work', () => {
    assert.equal(importPollDelay({ state: 'inspecting' }), 750);
    assert.equal(importPollDelay({ state: 'importing' }), 750);
    assert.equal(importPollDelay({ state: 'ready' }), 0);
    assert.equal(importPollDelay(null), 0);
});

test('index contains the import entry before pricing and the modal contract', () => {
    const html = fs.readFileSync(new URL('../index.html', import.meta.url), 'utf8');
    const importAt = html.indexOf('id="database-import-btn"');
    const pricingAt = html.indexOf('id="pricing-btn"');
    assert.ok(importAt > 0 && importAt < pricingAt);
    for (const id of [
        'database-import-modal', 'database-import-file',
        'dbi-drop', 'dbi-file-name', 'dbi-progress-fill',
        'dbi-table-body', 'dbi-primary', 'dbi-secondary',
    ]) {
        assert.ok(html.includes(`id="${id}"`), `missing ${id}`);
    }
    assert.match(html, /stroke="currentColor"/);
});

test('import css uses graphite fill and not accent for progress', () => {
    const css = fs.readFileSync(new URL('../style.css', import.meta.url), 'utf8');
    assert.match(css, /\.dbi-primary[^}]*background:\s*var\(--text\)/s);
    assert.match(css, /\.dbi-progress-fill[^}]*background:\s*var\(--text-muted\)/s);
});

test('all SSE tag refreshes either source', () => {
    assert.equal(shouldRefreshForSSE('all', 'claude'), true);
    assert.equal(shouldRefreshForSSE('all', 'codex'), true);
    assert.equal(shouldRefreshForSSE('codex', 'claude'), false);
});

test('verifying has a working panel with no cancel action', () => {
    const view = viewForImportJob({ state: 'importing', phase: 'verifying' });
    assert.equal(view.panel, 'verifying');
    assert.equal(view.primaryAction, '');
});
