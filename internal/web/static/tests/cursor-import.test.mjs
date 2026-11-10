import { test } from 'node:test';
import assert from 'node:assert/strict';
import { cursorImportView } from '../js/cursor-import.js';

test('cursorImportView: idle without files keeps start disabled', () => {
    const v = cursorImportView(null, false, false);
    assert.equal(v.running, false);
    assert.equal(v.status, '');
    assert.equal(v.progress.visible, false);
    assert.equal(v.startLabel, 'Import Cursor CSV');
    assert.equal(v.startDisabled, true);
    assert.equal(v.chooseDisabled, false);
});

test('cursorImportView: chosen files enable start; other writers block it', () => {
    assert.equal(cursorImportView(null, true, false).startDisabled, false);
    assert.equal(cursorImportView(null, true, true).startDisabled, true);
    assert.equal(cursorImportView(null, true, true).chooseDisabled, true);
});

test('cursorImportView: running shows progress and disables everything', () => {
    const v = cursorImportView({
        running: true, total_rows: 100, processed_rows: 25,
        inserted_rows: 20, duplicate_rows: 5,
    }, true, false);
    assert.equal(v.running, true);
    assert.equal(v.status, 'Importing…');
    // Progress bar: exact fraction for the fill, rounded % for text, counts
    // and running tallies in the meta line (same language as the DB import).
    assert.equal(v.progress.visible, true);
    assert.equal(v.progress.fraction, 0.25);
    assert.equal(v.progress.ariaPercent, 25);
    assert.equal(v.progress.countText, '25 / 100 rows · inserted 20 · duplicates 5');
    assert.equal(v.progress.percentText, '25%');
    assert.equal(v.startLabel, 'Importing…');
    assert.equal(v.startDisabled, true);
});

test('cursorImportView: done summarizes dedup counters', () => {
    const v = cursorImportView({
        running: false, error: '', finished_at: 1770000000,
        last_result: {
            inserted_rows: 18890, duplicate_rows: 0,
            skipped_zero_rows: 1793, identity_violations: 2, invalid_rows: 0,
        },
    }, true, false);
    assert.match(v.status, /Done: 18,890 rows added/);
    assert.match(v.status, /1,793 zero-token skipped/);
    assert.match(v.status, /2 row anomalies/);
    assert.equal(v.startLabel, 'Import Cursor CSV');
    assert.equal(v.startDisabled, false);
});

test('cursorImportView: failure offers retry and a public message', () => {
    const v = cursorImportView({ running: false, error: 'the import failed; committed rows are kept' }, true, false);
    assert.match(v.status, /Import failed: the import failed/);
    assert.equal(v.startLabel, 'Retry import');
    assert.equal(v.startDisabled, false);
});

test('cursorImportView: superseded hint appended only when non-zero', () => {
    const base = {
        running: false, error: '', finished_at: 1770000000,
        last_result: { inserted_rows: 10, duplicate_rows: 0 },
    };
    assert.equal(cursorImportView(base, true, false).status.includes('revised rows'), false);
    const hinted = cursorImportView({ ...base, superseded_hint: 3 }, true, false);
    assert.match(hinted.status, /⚠ 3 possibly revised rows \(both kept\)/);
});

test('cursorImportView: history normalizes entries and survives empty jobs', () => {
    assert.deepEqual(cursorImportView(null, false, false).history, []);
    const v = cursorImportView({
        running: false,
        history: [
            { file: 'usage-events-a.csv', rows: '18890', imported_at: '1794070225' },
            { file: 'usage-events-b.csv', rows: 2651, imported_at: 1794071970 },
        ],
    }, false, false);
    assert.deepEqual(v.history, [
        { file: 'usage-events-a.csv', rows: 18890, importedAt: 1794070225 },
        { file: 'usage-events-b.csv', rows: 2651, importedAt: 1794071970 },
    ]);
});

test('cursorImportView: dry-run preview line formats result and states', () => {
    assert.equal(cursorImportView(null, true, false, null).previewText, '');
    assert.equal(cursorImportView(null, true, false, { pending: true }).previewText, 'Previewing…');
    assert.equal(cursorImportView(null, true, false, { error: true }).previewText, '');
    assert.equal(cursorImportView(null, true, false, { none_importable: true }).previewText,
        'No importable rows in these files');
    const text = cursorImportView(null, true, false, {
        files: [{ file: 'a.csv' }, { file: 'b.csv' }],
        total_new: 412, total_duplicates: 1204,
        first_date: '2025-06-18', last_date: '2026-11-07',
        new_models: ['mimo-v2-flash', 'github_bugbot', 'x1', 'x2', 'x3', 'x4', 'x5'],
    }).previewText;
    assert.match(text, /2 file\(s\) · \+412 new · 1,204 duplicates/);
    assert.match(text, /2025-06-18 → 2026-11-07/);
    assert.match(text, /new models: mimo-v2-flash, github_bugbot, x1, x2 \+3/);
});

test('cursorImportView: clients link appears only on a clean finished state', () => {
    const done = { running: false, error: '', finished_at: 1770000000, last_result: { inserted_rows: 1 } };
    assert.equal(cursorImportView(done, true, false).clientsLink, true);
    assert.equal(cursorImportView({ ...done, error: 'x' }, true, false).clientsLink, false);
    assert.equal(cursorImportView({ ...done, running: true }, true, false).clientsLink, false);
    assert.equal(cursorImportView(null, true, false).clientsLink, false);
});
