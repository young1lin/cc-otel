// AllClient migration card, embedded in the database-import modal.
//
// One click merges the legacy Codex archive tables (codex_api_requests and
// friends) inside THIS database into the unified All Clients pool with
// service_name "codex". The backend job is idempotent (identity dedup via
// import_ledger), so the button can be pressed repeatedly: after the first
// run the preview shows every row as "Already merged".

import { loadCodexMigrateStatus, startCodexMigrate } from './api.js';

let pollTimer = null;
let lastFinishedAt = 0;

export function initCodexMigrate({ onMigrated = () => {} } = {}) {
    const modal = document.getElementById('database-import-modal');
    const tbody = document.getElementById('codex-migrate-table-body');
    const statusEl = document.getElementById('codex-migrate-status');
    const btn = document.getElementById('codex-migrate-start');
    if (!modal || !tbody || !statusEl || !btn) return;

    const fmt = (n) => Number(n || 0).toLocaleString();
    const modalOpen = () => modal.style.display !== 'none' && modal.getAttribute('aria-hidden') !== 'true';

    const render = (job) => {
        const preview = (job && job.preview) || [];
        tbody.innerHTML = preview.length
            ? preview.map((t) => '<tr>'
                + '<td>' + (t.source_table || '') + '</td>'
                + '<td>' + fmt(t.source_rows) + '</td>'
                + '<td>' + fmt(t.new_rows) + '</td>'
                + '<td>' + fmt(t.existing_rows) + '</td>'
                + '</tr>').join('')
            : '<tr><td colspan="4">No Codex archive tables found in this database — nothing to migrate.</td></tr>';

        if (job && job.running) {
            const pct = job.total_rows ? Math.round((job.processed_rows / job.total_rows) * 100) : 0;
            statusEl.textContent = 'Migrating ' + fmt(job.processed_rows) + ' / ' + fmt(job.total_rows)
                + ' rows (' + pct + '%) · inserted ' + fmt(job.inserted_rows)
                + ' · duplicates ' + fmt(job.duplicate_rows);
            btn.disabled = true;
            btn.textContent = 'Migrating…';
            return;
        }
        if (job && job.error) {
            statusEl.textContent = 'Migration failed: ' + job.error;
            btn.disabled = false;
            btn.textContent = 'Retry migration';
            return;
        }
        const result = job && job.last_result;
        if (result && job.finished_at) {
            statusEl.textContent = 'Done: ' + fmt(result.inserted_rows) + ' rows added · '
                + fmt(result.duplicate_rows) + ' duplicates skipped · '
                + fmt(result.verified_identities) + ' identities verified.';
            if (lastFinishedAt !== job.finished_at) {
                lastFinishedAt = job.finished_at;
                onMigrated();
            }
        } else {
            statusEl.textContent = '';
        }
        btn.disabled = false;
        btn.textContent = 'Merge Codex history';
    };

    const refresh = async () => {
        try {
            render(await loadCodexMigrateStatus());
        } catch (error) {
            console.error('codex migrate status:', error);
            statusEl.textContent = 'Migration status unavailable: ' + (error.message || error);
        }
    };

    // Poll while a migration is running or while the modal is open, so the
    // preview stays fresh and progress advances live.
    const schedule = () => {
        clearTimeout(pollTimer);
        pollTimer = setTimeout(async () => {
            await refresh();
            schedule();
        }, 1500);
    };
    schedule();

    btn.addEventListener('click', async () => {
        btn.disabled = true;
        btn.textContent = 'Starting…';
        try {
            render(await startCodexMigrate());
        } catch (error) {
            console.error('codex migrate start:', error);
            statusEl.textContent = 'Could not start migration: ' + (error.message || error);
            btn.disabled = false;
            btn.textContent = 'Retry migration';
        }
    });
}
