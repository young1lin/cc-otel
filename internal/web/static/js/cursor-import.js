// Cursor usage-events CSV import card, embedded in the database-import modal.
//
// Mirrors codex-migrate.js: a click POSTs the chosen CSV files (multipart) to
// /api/cursor/import and polls GET for progress. The backend job is
// idempotent (content-hash request_id dedup on api_requests), so re-importing
// overlapping month exports or duplicate downloads only adds what is missing.

import { loadCursorImportStatus, startCursorImport, dryRunCursorImport } from './api.js';

let pollTimer = null;
let lastFinishedAt = 0;
// The most recent status payload, shared between render and the poll gate.
let lastJob = null;
let chosenFiles = [];

const fmt = (n) => Number(n || 0).toLocaleString();

// Dry-run preview line. preview: null | {pending:true} | {error:true} | result.
function previewText(preview) {
    if (!preview || preview.error) return '';
    if (preview.pending) return 'Previewing…';
    if (preview.none_importable) return 'No importable rows in these files';
    const models = preview.new_models || [];
    return fmt(preview.files && preview.files.length || 0) + ' file(s) · +'
        + fmt(preview.total_new) + ' new · ' + fmt(preview.total_duplicates) + ' duplicates'
        + (preview.first_date ? ' · ' + preview.first_date + ' → ' + preview.last_date : '')
        + (models.length ? ' · new models: ' + models.slice(0, 4).join(', ')
            + (models.length > 4 ? ' +' + (models.length - 4) : '') : '');
}

// Pure view-model for the card: derive every DOM state (status line, button
// label/disabled, preview line, clients link) from the latest job snapshot,
// whether CSV files are chosen, whether another background writer is busy,
// and the optional dry-run preview result. Exported for tests.
export function cursorImportView(job, hasFiles, otherWriterBusy, preview) {
    const running = !!(job && job.running);
    const failed = !running && !!job && !!job.error;
    const result = job && job.last_result;
    let status = '';
    if (running) {
        // Counts live in the progress bar's meta line now; the status line
        // stays short so the two never duplicate each other's text.
        status = 'Importing…';
    } else if (failed) {
        status = 'Import failed: ' + job.error;
    } else if (result && job.finished_at) {
        status = 'Done: ' + fmt(result.inserted_rows) + ' rows added · '
            + fmt(result.duplicate_rows) + ' duplicates skipped'
            + (result.skipped_zero_rows ? ' · ' + fmt(result.skipped_zero_rows) + ' zero-token skipped' : '')
            + (result.identity_violations ? ' · ' + fmt(result.identity_violations) + ' row anomalies' : '')
            + (result.invalid_rows ? ' · ' + fmt(result.invalid_rows) + ' unparseable' : '')
            // Same instant + model with different token counts = Cursor
            // revised a row between exports; both variants are stored by
            // design, so surface the count instead of silently doubling.
            + (job.superseded_hint ? ' · ⚠ ' + fmt(job.superseded_hint) + ' possibly revised rows (both kept)' : '');
    }
    // Progress bar — the same visual language as the DB import (.dbi-progress
    // + meta line): exact fraction drives the fill width, rounded % is the
    // text; the meta line carries processed/total plus running tallies.
    let progress = { visible: false, fraction: 0, ariaPercent: 0, countText: '', percentText: '' };
    if (running) {
        const total = Number(job.total_rows || 0);
        const processed = Number(job.processed_rows || 0);
        const pct = total ? Math.round((processed / total) * 100) : 0;
        progress = {
            visible: true,
            fraction: total ? Math.min(processed / total, 1) : 0,
            ariaPercent: pct,
            countText: fmt(processed) + ' / ' + fmt(total) + ' rows · inserted '
                + fmt(job.inserted_rows) + ' · duplicates ' + fmt(job.duplicate_rows),
            percentText: pct + '%',
        };
    }
    return {
        running,
        status,
        progress,
        // One-line dry-run preview under the file list: "+N new · M
        // duplicates · date range". Pending shows a quiet placeholder; errors
        // show nothing (the import itself remains the source of truth).
        previewText: previewText(preview),
        // Offer the jump only on a clean finished state — right after "Done".
        clientsLink: !running && !!job && !!job.finished_at && !job.error && !!job.last_result,
        // History lines: "file — N rows — date" for every past import,
        // newest first (from import_ledger via the status payload).
        history: (job && job.history || []).map((h) => ({
            file: h.file || '',
            rows: Number(h.rows || 0),
            importedAt: Number(h.imported_at || 0),
        })),
        startLabel: running ? 'Importing…' : (failed ? 'Retry import' : 'Import Cursor CSV'),
        // No files chosen yet, or another writer holds the DB → stay idle.
        startDisabled: running || !hasFiles || !!otherWriterBusy,
        chooseDisabled: running || !!otherWriterBusy,
    };
}

export function initCursorImport({ onImported = () => {} } = {}) {
    const modal = document.getElementById('database-import-modal');
    const statusEl = document.getElementById('cursor-import-status');
    const filesEl = document.getElementById('cursor-import-files');
    const previewEl = document.getElementById('cursor-import-preview');
    const clientsEl = document.getElementById('cursor-import-clients');
    const historyEl = document.getElementById('cursor-import-history');
    const progressEl = document.getElementById('cursor-import-progress');
    const progressFillEl = document.getElementById('cursor-import-progress-fill');
    const progressMetaEl = document.getElementById('cursor-import-progress-meta');
    const progressCountEl = document.getElementById('cursor-import-progress-count');
    const progressPercentEl = document.getElementById('cursor-import-progress-percent');
    const chooseBtn = document.getElementById('cursor-import-choose');
    const btn = document.getElementById('cursor-import-start');
    const keepZeroEl = document.getElementById('cursor-import-keepzero');
    const input = document.getElementById('cursor-import-file');
    if (!modal || !statusEl || !filesEl || !chooseBtn || !btn || !keepZeroEl || !input) return;

    // Dry-run preview state; lives outside the view-model so tests stay pure.
    let preview = null;
    let previewSeq = 0;

    const modalOpen = () => modal.style.display !== 'none' && modal.getAttribute('aria-hidden') !== 'true';

    // Other writers expose their busy state via data flags on their buttons
    // (set by their own render loops); read, never write, those flags here.
    const otherWriterBusy = () =>
        document.querySelector('#dbi-primary')?.dataset.dbiBusy === '1'
        || document.getElementById('codex-migrate-start')?.dataset.running === '1';

    const renderFiles = () => {
        if (!chosenFiles.length) {
            filesEl.hidden = true;
            filesEl.textContent = '';
            return;
        }
        const bytes = chosenFiles.reduce((sum, f) => sum + f.size, 0);
        filesEl.hidden = false;
        filesEl.textContent = chosenFiles.length + ' file(s) · '
            + (bytes / 1048576).toFixed(1) + ' MB · '
            + chosenFiles.map((f) => f.name).join(', ');
    };

    // Past imports, newest first, capped to keep the card compact. Data comes
    // from import_ledger via the status payload, so it survives reloads.
    const renderHistory = (history) => {
        if (!historyEl) return;
        if (!history || !history.length) {
            historyEl.hidden = true;
            historyEl.textContent = '';
            return;
        }
        historyEl.hidden = false;
        historyEl.replaceChildren(...history.slice(0, 5).map((h) => {
            const li = document.createElement('li');
            const when = h.importedAt ? new Date(h.importedAt * 1000).toLocaleString() : '';
            li.textContent = h.file + ' — ' + fmt(h.rows) + ' rows — ' + when;
            return li;
        }));
    };

    const render = (job) => {
        lastJob = job;
        const busy = otherWriterBusy();
        const view = cursorImportView(job, chosenFiles.length > 0, busy, preview);
        statusEl.textContent = view.status;
        if (progressEl && progressFillEl && progressMetaEl && progressCountEl && progressPercentEl) {
            progressEl.hidden = !view.progress.visible;
            progressFillEl.style.width = (view.progress.fraction * 100) + '%';
            progressEl.setAttribute('aria-valuenow', String(view.progress.ariaPercent));
            progressMetaEl.hidden = !view.progress.visible;
            progressCountEl.textContent = view.progress.countText;
            progressPercentEl.textContent = view.progress.percentText;
        }
        if (previewEl) {
            previewEl.hidden = !view.previewText;
            previewEl.textContent = view.previewText;
        }
        if (clientsEl) {
            clientsEl.hidden = !view.clientsLink;
        }
        renderHistory(view.history);
        btn.textContent = view.startLabel;
        btn.disabled = view.startDisabled;
        btn.dataset.cursorBusy = view.running ? '1' : '0';
        chooseBtn.disabled = view.chooseDisabled;
        renderFiles();

        // Cross-disable the modal's other two writers while we run (the
        // server 409s concurrent starts; keep the UI from dead-ending).
        // Only touch their buttons when THEY are idle — their own render
        // owns their busy states.
        document.querySelectorAll('#database-import-modal #dbi-primary, #database-import-modal #dbi-secondary')
            .forEach((el) => {
                if (el.dataset.dbiBusy !== '1') el.disabled = view.running;
            });
        const codexBtn = document.getElementById('codex-migrate-start');
        if (codexBtn && codexBtn.dataset.running !== '1') codexBtn.disabled = view.running;

        if (!view.running && job && job.finished_at && !job.error && job.last_result
            && job.finished_at !== lastFinishedAt) {
            lastFinishedAt = job.finished_at;
            onImported();
        }
    };

    const refresh = async () => {
        try {
            render(await loadCursorImportStatus());
        } catch (error) {
            console.error('cursor import status:', error);
            statusEl.textContent = 'Import status unavailable: ' + (error.message || error);
        }
    };

    // Poll ONLY while an import is running or while the modal is open — same
    // gate as codex-migrate.js, so an unattended dashboard stays quiet.
    const pollActive = () => {
        const job = lastJob;
        return (job && job.running) || modalOpen();
    };
    const schedule = () => {
        clearTimeout(pollTimer);
        pollTimer = setTimeout(async () => {
            await refresh();
            if (pollActive()) schedule();
        }, 1500);
    };
    document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'visible') {
            clearTimeout(pollTimer);
            if (pollActive()) schedule();
        }
    });
    // Opening the modal must resume polling too: the gate closed when the
    // modal last closed and nothing else re-evaluates it, so a finished or
    // failed past job would otherwise never reach the card (same incident).
    new MutationObserver(() => {
        if (modalOpen()) {
            clearTimeout(pollTimer);
            refresh();
            schedule();
        }
    }).observe(modal, { attributes: true, attributeFilter: ['style', 'aria-hidden'] });
    schedule();

    chooseBtn.addEventListener('click', () => input.click());

    // File selection funnel: both the picker and a drop end here, so preview
    // and render stay in lockstep no matter how files arrived.
    const setChosenFiles = (fileList) => {
        const csvs = Array.from(fileList || []).filter((f) => f.name.toLowerCase().endsWith('.csv'));
        chosenFiles = csvs;
        render(lastJob);
        runPreview();
    };

    // Read-only dry run against /api/cursor/import/dry-run. Stale responses
    // (user re-picks files mid-flight) are dropped via the sequence token.
    const runPreview = () => {
        const seq = ++previewSeq;
        if (!chosenFiles.length) {
            preview = null;
            render(lastJob);
            return;
        }
        preview = { pending: true };
        render(lastJob);
        const formData = new FormData();
        for (const file of chosenFiles) formData.append('files', file, file.name);
        if (keepZeroEl.checked) formData.append('keep_zero', '1');
        dryRunCursorImport(formData).then((result) => {
            if (seq !== previewSeq) return;
            preview = result;
            render(lastJob);
        }).catch((error) => {
            if (seq !== previewSeq) return;
            // 400 no_usable_rows means exactly what it says — surface it as
            // the preview line rather than swallowing it; other errors stay
            // quiet (import remains the authority).
            preview = error && error.code === 'no_usable_rows'
                ? { none_importable: true }
                : { error: true };
            render(lastJob);
        });
    };

    input.addEventListener('change', () => setChosenFiles(input.files));

    // Drop zone — the exact .dbi-drop pattern the Database tab uses: click
    // opens the picker, drag highlights the zone itself. One coherent pattern.
    const dropEl = document.getElementById('cursor-import-drop');
    dropEl?.addEventListener('click', () => input.click());
    for (const eventName of ['dragenter', 'dragover']) {
        dropEl?.addEventListener(eventName, (event) => {
            event.preventDefault();
            dropEl.classList.add('is-dragging');
        });
    }
    for (const eventName of ['dragleave', 'drop']) {
        dropEl?.addEventListener(eventName, () => dropEl.classList.remove('is-dragging'));
    }
    dropEl?.addEventListener('drop', (event) => {
        event.preventDefault();
        setChosenFiles(event.dataTransfer && event.dataTransfer.files);
    });

    // "View in Clients panel" after a clean Done: close this modal and switch
    // the bottom panel, so freshly imported data is one click away.
    clientsEl?.addEventListener('click', () => {
        document.getElementById('database-import-close')?.click();
        document.querySelector('.panel-btn[data-panel="clients"]')?.click();
    });

    btn.addEventListener('click', async () => {
        if (!chosenFiles.length || btn.disabled) return;
        btn.disabled = true;
        btn.textContent = 'Starting…';
        const formData = new FormData();
        for (const file of chosenFiles) formData.append('files', file, file.name);
        if (keepZeroEl.checked) formData.append('keep_zero', '1');
        try {
            render(await startCursorImport(formData));
            // The 202 body is a point-in-time snapshot (running, 0 rows).
            // Restart the poll chain HERE: the boot-time chain stopped long
            // ago (gate closed while the modal was shut), and without this
            // the card freezes on that snapshot forever (2026-11-07 live
            // incident: a 4s backend run displayed as "Importing 0 / 15,807
            // rows (0%)" until page reload).
            schedule();
        } catch (error) {
            console.error('cursor import start:', error);
            statusEl.textContent = 'Could not start import: ' + (error.message || error);
            btn.disabled = false;
            btn.textContent = 'Retry import';
        }
    });
}
