import {
    uploadDatabase, loadDatabaseImportStatus,
    startDatabaseImport, deleteDatabaseImport,
} from './api.js';

export function formatImportBytes(value) {
    const n = Math.max(0, Number(value) || 0);
    if (n < 1024) return `${Math.round(n)} B`;
    const units = ['KB', 'MB', 'GB'];
    let scaled = n;
    let unit = -1;
    do {
        scaled /= 1024;
        unit += 1;
    } while (scaled >= 1024 && unit < units.length - 1);
    const digits = scaled >= 10 || Number.isInteger(scaled) ? 0 : 1;
    return `${scaled.toFixed(digits)} ${units[unit]}`;
}

export function importPollDelay(job) {
    return job && (job.state === 'inspecting' || job.state === 'importing') ? 750 : 0;
}

export function viewForImportJob(job) {
    if (!job) return { panel: 'empty', primaryAction: 'choose', primaryLabel: 'Choose database' };
    if (job.state === 'ready') {
        return { panel: 'ready', primaryAction: 'start', primaryLabel: 'Start merge' };
    }
    if (job.state === 'succeeded') {
        return { panel: 'succeeded', primaryAction: 'done', primaryLabel: 'Done' };
    }
    if (job.state === 'failed') {
        return job.retryable
            ? { panel: 'failed', primaryAction: 'retry', primaryLabel: 'Retry merge' }
            : { panel: 'failed', primaryAction: 'choose', primaryLabel: 'Choose another database' };
    }
    if (job.state === 'importing' && job.phase === 'verifying') {
        return { panel: 'verifying', primaryAction: '', primaryLabel: '' };
    }
    return { panel: job.state, primaryAction: '', primaryLabel: '' };
}

const local = {
    job: null,
    upload: null,
    uploadController: null,
    pollTimer: null,
    notifiedResult: '',
    modalOpen: false,
};

function setText(element, value) {
    if (element) element.textContent = value == null ? '' : String(value);
}

function showPanel(root, name) {
    root.querySelectorAll('[data-dbi-panel]').forEach((panel) => {
        panel.hidden = panel.dataset.dbiPanel !== name;
    });
}

function setProgress(elements, progress = {}) {
    const percent = Math.max(0, Math.min(100, Number(progress.percent) || 0));
    elements.fill.style.width = `${percent}%`;
    elements.fill.parentElement?.setAttribute('aria-valuenow', String(percent));
    setText(elements.percent, `${percent.toFixed(percent >= 10 ? 0 : 1)}%`);
    setText(elements.count, `${progress.processed_rows || 0} / ${progress.total_rows || 0}`);
}

function importElements(modal, button) {
    return {
        modal, button,
        drop: document.getElementById('dbi-drop'),
        fileName: document.getElementById('dbi-file-name'),
        fileSize: document.getElementById('dbi-file-size'),
        status: document.getElementById('dbi-status'),
        fill: document.getElementById('dbi-progress-fill'),
        count: document.getElementById('dbi-progress-count'),
        percent: document.getElementById('dbi-progress-percent'),
        newCount: document.getElementById('dbi-new-count'),
        duplicateCount: document.getElementById('dbi-duplicate-count'),
        totalCount: document.getElementById('dbi-total-count'),
        tableBody: document.getElementById('dbi-table-body'),
        warnings: document.getElementById('dbi-warnings'),
        resultIcon: document.getElementById('dbi-result-icon'),
        resultTitle: document.getElementById('dbi-result-title'),
        resultMessage: document.getElementById('dbi-result-message'),
        primary: document.getElementById('dbi-primary'),
        secondary: document.getElementById('dbi-secondary'),
    };
}

function setAction(button, action, label) {
    if (!button) return;
    button.dataset.action = action || '';
    button.hidden = !action;
    button.disabled = !action;
    setText(button, label);
}

function renderTable(elements, tables = []) {
    if (!elements.tableBody) return;
    elements.tableBody.replaceChildren();
    for (const table of tables) {
        const row = document.createElement('tr');
        for (const value of [table.name, table.new_rows || 0, table.duplicate_rows || 0]) {
            const cell = document.createElement('td');
            setText(cell, value);
            row.appendChild(cell);
        }
        elements.tableBody.appendChild(row);
    }
}

function renderUpload(elements) {
    const upload = local.upload;
    showPanel(elements.modal, 'working');
    setText(elements.fileName, upload.file.name);
    setText(elements.fileSize, formatImportBytes(upload.file.size));
    setText(elements.status, 'Uploading database…');
    const total = upload.total || upload.file.size || 0;
    const percent = total > 0 ? upload.loaded * 100 / total : 0;
    elements.fill.style.width = `${Math.max(0, Math.min(100, percent))}%`;
    elements.fill.parentElement?.setAttribute('aria-valuenow', String(percent));
    setText(elements.count, `${formatImportBytes(upload.loaded)} / ${formatImportBytes(total)}`);
    setText(elements.percent, `${percent.toFixed(percent >= 10 ? 0 : 1)}%`);
    setAction(elements.secondary, 'cancel-upload', 'Cancel');
    setAction(elements.primary, '', '');
}

function renderJob(elements, job) {
    const view = viewForImportJob(job);
    if (!job) {
        showPanel(elements.modal, 'empty');
        setAction(elements.secondary, '', '');
        setAction(elements.primary, view.primaryAction, view.primaryLabel);
        return;
    }

    const file = job.file || {};
    setText(elements.fileName, file.name || 'Database');
    setText(elements.fileSize, formatImportBytes(file.size_bytes || 0));
    if (job.state === 'inspecting' || job.state === 'importing') {
        showPanel(elements.modal, 'working');
        const labels = {
            uploading: 'Receiving database…',
            inspecting: 'Inspecting schema and duplicate identities…',
            importing: 'Merging missing data…',
            verifying: 'Verifying imported identities…',
        };
        setText(elements.status, labels[job.phase] || 'Working…');
        setProgress(elements, job.progress || {});
        const cancellable = job.state === 'inspecting';
        setAction(elements.secondary, cancellable ? 'cancel-server' : '', cancellable ? 'Cancel' : '');
        setAction(elements.primary, '', '');
        return;
    }

    if (job.state === 'ready') {
        showPanel(elements.modal, 'ready');
        const preview = job.preview || {};
        setText(elements.newCount, preview.new_rows || 0);
        setText(elements.duplicateCount, preview.duplicate_rows || 0);
        setText(elements.totalCount, preview.source_rows || 0);
        renderTable(elements, preview.tables || []);
        setText(elements.warnings, (preview.warnings || []).join('\n'));
        setAction(elements.secondary, 'cancel-ready', 'Cancel');
        setAction(elements.primary, view.primaryAction, view.primaryLabel);
        return;
    }

    showPanel(elements.modal, 'result');
    if (job.state === 'succeeded') {
        const result = job.result || {};
        setText(elements.resultIcon, '✓');
        setText(elements.resultTitle, 'Import complete');
        setText(
            elements.resultMessage,
            `${result.inserted_rows || 0} rows added · ${result.duplicate_rows || 0} duplicates kept from the main database · ${result.verified_identities || 0} identities verified`,
        );
    } else {
        setText(elements.resultIcon, '!');
        setText(elements.resultTitle, 'Import failed');
        setText(elements.resultMessage, job.error?.message || 'The database could not be imported.');
    }
    setAction(elements.secondary, '', '');
    setAction(elements.primary, view.primaryAction, view.primaryLabel);
}

function render(elements) {
    const active = !!local.upload || local.job?.state === 'inspecting' || local.job?.state === 'importing';
    elements.button.classList.toggle('is-active', active);
    if (local.upload) renderUpload(elements);
    else renderJob(elements, local.job);
}

function resultNotificationKey(job) {
    if (!job?.job_id) return '';
    const committedFailure = job.state === 'failed' && Number(job.result?.inserted_rows) > 0;
    if (job.state !== 'succeeded' && !committedFailure) return '';
    return `${job.job_id}:${job.state}:${job.updated_at || 0}`;
}

export function initDatabaseImport({ openPopover, closePopover, onImported = () => {} }) {
    const button = document.getElementById('database-import-btn');
    const modal = document.getElementById('database-import-modal');
    const input = document.getElementById('database-import-file');
    if (!button || !modal || !input) return;
    const elements = importElements(modal, button);

    const notifyImported = (job) => {
        const key = resultNotificationKey(job);
        if (!key || local.notifiedResult === key) return;
        local.notifiedResult = key;
        onImported();
    };

    const schedulePoll = () => {
        clearTimeout(local.pollTimer);
        local.pollTimer = null;
        const delay = importPollDelay(local.job);
        if (!delay) return;
        local.pollTimer = setTimeout(async () => {
            try {
                const response = await loadDatabaseImportStatus(local.job?.job_id || '');
                local.job = response.job || null;
                notifyImported(local.job);
                render(elements);
            } catch (error) {
                console.error('database import status:', error);
            }
            schedulePoll();
        }, delay);
    };

    const restore = async () => {
        if (local.upload) return;
        try {
            const response = await loadDatabaseImportStatus(local.job?.job_id || '');
            local.job = response.job || null;
            notifyImported(local.job);
        } catch (error) {
            if (error.code === 'job_not_found') local.job = null;
            else console.error('database import restore:', error);
        }
        render(elements);
        schedulePoll();
    };

    const open = async () => {
        local.modalOpen = true;
        openPopover(modal, button);
        render(elements);
        await restore();
    };
    const close = () => {
        local.modalOpen = false;
        closePopover(modal);
    };

    const showClientError = (file, message) => {
        local.job = {
            state: 'failed', retryable: false,
            file: { name: file?.name || '', size_bytes: file?.size || 0 },
            error: { message },
        };
        render(elements);
    };

    const beginUpload = async (files) => {
        const selected = Array.from(files || []);
        if (selected.length !== 1) {
            showClientError(null, 'Choose exactly one database file.');
            return;
        }
        const file = selected[0];
        if (!file.name.toLowerCase().endsWith('.db')) {
            showClientError(file, 'Choose a .db database file.');
            return;
        }
        if (file.size > 2 * 1024 ** 3) {
            showClientError(file, 'The database file exceeds the 2 GB upload limit.');
            return;
        }
        const controller = new AbortController();
        local.uploadController = controller;
        local.upload = { file, loaded: 0, total: file.size };
        local.job = null;
        render(elements);
        try {
            const accepted = await uploadDatabase(file, {
                signal: controller.signal,
                onProgress: (loaded, total) => {
                    if (local.uploadController !== controller || !local.upload) return;
                    local.upload.loaded = loaded;
                    local.upload.total = total || file.size;
                    render(elements);
                },
            });
            local.upload = null;
            local.uploadController = null;
            const response = await loadDatabaseImportStatus(accepted.job_id);
            local.job = response.job || { job_id: accepted.job_id, state: accepted.state };
            render(elements);
            schedulePoll();
        } catch (error) {
            local.upload = null;
            local.uploadController = null;
            if (error.code === 'upload_cancelled') {
                local.job = null;
                render(elements);
                return;
            }
            showClientError(file, error.message || 'Upload failed.');
        }
    };

    const resetAndChoose = async () => {
        if (local.job?.job_id && local.job.state === 'failed') {
            try { await deleteDatabaseImport(local.job.job_id); } catch (error) {
                if (error.code !== 'job_not_found') console.error('database import delete:', error);
            }
        }
        local.job = null;
        input.value = '';
        render(elements);
        input.click();
    };

    const runAction = async (action) => {
        try {
            if (action === 'choose') {
                await resetAndChoose();
            } else if (action === 'start' || action === 'retry') {
                const response = await startDatabaseImport(local.job?.job_id || '');
                local.job = response.job || local.job;
                render(elements);
                schedulePoll();
            } else if (action === 'done') {
                if (local.job?.job_id) await deleteDatabaseImport(local.job.job_id);
                local.job = null;
                render(elements);
                close();
            } else if (action === 'cancel-upload') {
                local.uploadController?.abort();
            } else if (action === 'cancel-server' || action === 'cancel-ready') {
                if (local.job?.job_id) await deleteDatabaseImport(local.job.job_id);
                local.job = null;
                render(elements);
                schedulePoll();
            }
        } catch (error) {
            console.error('database import action:', error);
            if (local.job) {
                local.job = { ...local.job, state: 'failed', retryable: false, error: { message: error.message } };
                render(elements);
            }
        }
    };

    button.addEventListener('click', open);
    document.getElementById('database-import-close')?.addEventListener('click', close);
    modal.addEventListener('click', (event) => {
        if (event.target === modal) close();
    });
    document.addEventListener('keydown', (event) => {
        if (event.key === 'Escape' && local.modalOpen) close();
    });
    input.addEventListener('change', () => beginUpload(input.files));
    elements.drop?.addEventListener('click', () => input.click());
    for (const eventName of ['dragenter', 'dragover']) {
        elements.drop?.addEventListener(eventName, (event) => {
            event.preventDefault();
            elements.drop.classList.add('is-dragging');
        });
    }
    for (const eventName of ['dragleave', 'drop']) {
        elements.drop?.addEventListener(eventName, (event) => {
            event.preventDefault();
            elements.drop.classList.remove('is-dragging');
        });
    }
    elements.drop?.addEventListener('drop', (event) => beginUpload(event.dataTransfer?.files));
    elements.primary?.addEventListener('click', () => runAction(elements.primary.dataset.action));
    elements.secondary?.addEventListener('click', () => runAction(elements.secondary.dataset.action));
    restore();
}
