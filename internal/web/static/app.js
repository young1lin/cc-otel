// ── State ──────────────────────────────────────────────────────────────────
import { state, paging, SSE_BACKOFF_MAX } from './js/state.js';
import {
    fmtNum, fmtUSD, fmtPct, fmtTokens, fmtTime,
    escapeHtml, truncate, formatUserCell,
    toYMD, getTodayYMD, isValidYMD, rangeToFromTo,
} from './js/utils.js';
import {
    initTheme, applyTheme, chartColors,
    getModelColor, getModelFamily, hashModelName, mixHex,
} from './js/theme.js';
import {
    loadStatusData, loadDashboardData, loadDailyData,
    loadSessionsData, loadRequestsData, loadDurationsData, loadModelsData,
} from './js/api.js';
import { initSSE } from './js/sse.js';
import {
    initFilters, applyStateFromURL, syncURLFromState,
    syncRangeNavUIFromState, buildDayDropdown, resetToToday,
    initCustomRangePicker,
} from './js/filters.js';
import { initBreakdownModal } from './js/breakdown.js';
import { loadInsights, setInsightsVisible, initInsightsModal } from './js/insights.js';
import { loadChart, buildBarTooltip } from './js/chart-main.js';
import {
    loadDailyTable, refreshDailyPanel,
    setDailyDetailView, updateDailyViewControls, initPanelDaily,
} from './js/panel-daily.js';
import { loadSessions } from './js/panel-sessions.js';
import {
    loadRequests, loadDurationStats, loadModelFilter, initPanelRequests,
} from './js/panel-requests.js';
import { loadRate, initPanelRate } from './js/panel-rate.js';
import { renderPagination } from './js/pagination.js';

// ── Popover positioner — shared by status / breakdown / insights modals ────
function openPopover(backdropEl, anchorEl) {
    if (!backdropEl) return;
    const modalEl = backdropEl.querySelector('.modal');
    if (!modalEl) return;

    // Show first so we can measure.
    backdropEl.style.display = '';
    backdropEl.setAttribute('aria-hidden', 'false');

    // Insights: centered by flex on .modal-backdrop; avoid fixed+transform so native resize works.
    if (backdropEl.id === 'insights-modal') {
        modalEl.style.left = '';
        modalEl.style.top = '';
        modalEl.style.transform = '';
        modalEl.style.maxHeight = '';
        return;
    }

    // Mobile: center (popover is too easy to overflow).
    if (window.matchMedia('(max-width: 640px)').matches || !anchorEl) {
        modalEl.style.left = '50%';
        modalEl.style.top = '50%';
        modalEl.style.transform = 'translate(-50%, -50%)';
        modalEl.style.maxHeight = 'calc(100vh - 32px)';
        return;
    }

    const pad = 12;
    const gap = 10;
    const r = anchorEl.getBoundingClientRect();
    const vw = window.innerWidth;
    const vh = window.innerHeight;

    // Reset transforms to get natural size.
    modalEl.style.transform = 'none';
    modalEl.style.left = '0px';
    modalEl.style.top = '0px';

    const mr = modalEl.getBoundingClientRect();
    const mw = mr.width || 420;
    const mh = mr.height || 260;

    // Prefer below; flip above if not enough space.
    const spaceBelow = vh - r.bottom - pad;
    const spaceAbove = r.top - pad;
    const placeBelow = spaceBelow >= Math.min(260, mh) || spaceBelow >= spaceAbove;
    let top = placeBelow ? (r.bottom + gap) : (r.top - gap - mh);

    // Clamp vertical.
    top = Math.max(pad, Math.min(top, vh - pad - mh));

    // Align start; if overflow, align end.
    let left = r.left;
    if (left + mw > vw - pad) left = r.right - mw;
    left = Math.max(pad, Math.min(left, vw - pad - mw));

    modalEl.style.left = `${Math.round(left)}px`;
    modalEl.style.top = `${Math.round(top)}px`;
    modalEl.style.maxHeight = `calc(100vh - ${Math.round(top + pad)}px)`;
}

function closePopover(backdropEl) {
    if (!backdropEl) return;
    backdropEl.style.display = 'none';
    backdropEl.setAttribute('aria-hidden', 'true');
}

// SSE + status modal are owned by js/sse.js (initSSE() in boot).

// ── Source tabs (Claude Code | Codex) ──────────────────────────────────────
function applySourceFromURL() {
    const sp = new URLSearchParams(location.search);
    const src = sp.get('source');
    if (src === 'codex' || src === 'claude') state.source = src;
    else state.source = 'claude';
}

function syncSourceTabsUI() {
    document.querySelectorAll('.source-tab').forEach(btn => {
        btn.classList.toggle('is-active', btn.dataset.source === state.source);
    });
    // Hide Cache Create header cells for sources that don't have it (Codex).
    // Data rows are handled at render time (they skip the <td> entirely).
    const hide = state.source === 'codex';
    document.querySelectorAll('.col-cache-create').forEach(el => {
        el.style.display = hide ? 'none' : '';
    });
    // Adjust Input colspan to match: 3 (Claude) or 2 (Codex — no Cache Create).
    document.querySelectorAll('.th-group').forEach(el => {
        if (el.textContent.trim() === 'Input') {
            el.setAttribute('colspan', hide ? '2' : '3');
        }
    });
}

function initSourceTabs() {
    document.querySelectorAll('.source-tab').forEach(btn => {
        btn.addEventListener('click', () => {
            const next = btn.dataset.source;
            if (next === state.source) return;
            state.source = next;
            const url = new URL(location.href);
            if (next === 'claude') url.searchParams.delete('source');
            else url.searchParams.set('source', next);
            history.pushState(null, '', url.toString());
            syncSourceTabsUI();
            loadModelFilter({ preserveCurrent: false });
            resetPages();
            loadAll();
        });
    });
}

document.querySelectorAll('.endpoint-copy').forEach(btn => {
    btn.addEventListener('click', async () => {
        const from = btn.getAttribute('data-copy-from');
        const el = from ? document.getElementById(from) : null;
        const text = el?.textContent?.trim() || '';
        if (!text) return;
        try {
            await navigator.clipboard.writeText(text);
            btn.textContent = 'Copied';
            setTimeout(() => (btn.textContent = 'Copy'), 900);
        } catch {
            // Fallback: selection
            const r = document.createRange();
            r.selectNodeContents(el);
            const sel = window.getSelection();
            sel.removeAllRanges();
            sel.addRange(r);
            document.execCommand('copy');
            sel.removeAllRanges();
        }
    });
});

// ── KPI breakdown modal owned by js/breakdown.js (initBreakdownModal in boot) ─

// ── Dashboard cards ─────────────────────────────────────────────────────────
async function loadDashboard() {
    try {
        const { from, to } = rangeToFromTo(state.currentRange);
        const d = await loadDashboardData(from, to);
        document.getElementById('h-cost').textContent   = '$' + (d.total_cost_usd ?? 0).toFixed(4);
        document.getElementById('h-input').textContent  = fmtNum(d.total_input_tokens);
        document.getElementById('h-output').textContent = fmtNum(d.total_output_tokens);
        document.getElementById('h-cache').textContent  = ((d.cache_hit_rate ?? 0) * 100).toFixed(1) + '%';
        document.getElementById('h-reqs').textContent   = d.request_count ?? 0;
        // For multi-day ranges, show quick insights (Avg/day + Top models).
        if (from && to && from !== to) {
            loadInsights(from, to);
        } else {
            setInsightsVisible(false);
        }
    } catch (e) { console.error('dashboard:', e); }
}



// ── Daily detail table ─────────────────────────────────────────────────────


function resetPages() {
    paging.daily.page = 1;
    paging.sessions.page = 1;
    paging.requests.page = 1;
}
function loadAll() {
    updateDailyViewControls();
    loadDashboard();
    loadChart();
    refreshDailyPanel();
    loadSessions();
    loadRequests();
    if (document.querySelector('.panel.active')?.id === 'panel-rate') loadRate();
}

function selectCalendarDate(date) {
    if (!isValidYMD(date)) return;
    state.currentRange = 'single-day';
    state.customFrom = date;
    state.customTo = date;
    state.selectedDayDate = date === getTodayYMD() ? '' : date;
    state.chartGranularity = 'day';
    if (state.customRangeFlatpickr) {
        try { state.customRangeFlatpickr.setDate([date, date], false); } catch (_) {}
    }
    const crw = document.getElementById('custom-range-wrap');
    if (crw) crw.classList.remove('is-active');
    syncRangeNavUIFromState();
    buildDayDropdown();
    syncURLFromState();
    resetPages();
    loadAll();
}

// ── Local day rollover auto-refresh ─────────────────────────────────────────
let lastSeenTodayYMD = null;
function startDayRolloverWatcher() {
    lastSeenTodayYMD = toYMD(new Date());
    setInterval(() => {
        const nowYMD = toYMD(new Date());
        if (nowYMD === lastSeenTodayYMD) return;
        lastSeenTodayYMD = nowYMD;

        // Update Flatpickr maxDate (otherwise the range picker may still cap at yesterday).
        try { state.customRangeFlatpickr?.set?.('maxDate', 'today'); } catch {}

        // Refresh dropdown labels/active highlights.
        try { buildDayDropdown(); } catch {}

        // Auto-refresh only when the current view is "today" (or single-day pointing at today).
        const isTodayView =
            state.currentRange === 'today' ||
            (state.currentRange === 'single-day' && !state.selectedDayDate);
        if (isTodayView) {
            // A single-day view pinned to "today" stores the old date in customFrom/customTo;
            // bump it forward so the URL (?range=day&date=...) and the data fetch follow the new day.
            if (state.currentRange === 'single-day') {
                state.customFrom = nowYMD;
                state.customTo = nowYMD;
            }
            // Replace, not push: rollover isn't a user navigation.
            syncURLFromState({ replace: true });
            resetPages();
            loadAll();
        }
    }, 45 * 1000);
}

// ── Boot ───────────────────────────────────────────────────────────────────
initTheme({
    onThemeChange: () => {
        if (state.mainChart) loadChart();
        if (state.rateChart && document.querySelector('.panel.active')?.id === 'panel-rate') loadRate();
        if (state.customRangeFlatpickr && typeof state.customRangeFlatpickr.redraw === 'function') {
            state.customRangeFlatpickr.redraw();
        }
    },
});
initFilters({
    onChange: () => loadAll(),
    onMetricChange: () => { loadDashboard(); loadChart(); refreshDailyPanel(); },
    onResetPages: () => resetPages(),
});
initBreakdownModal({ openPopover, closePopover });
initInsightsModal({ openPopover, closePopover, onSelectDate: selectCalendarDate });
initPanelDaily();
initPanelRequests();
initPanelRate();
initCustomRangePicker();
applySourceFromURL();
syncSourceTabsUI();
initSourceTabs();
applyStateFromURL();
loadModelFilter();
loadAll();
initSSE({
    onUpdate: () => {
        loadDashboard();
        loadChart();
        refreshDailyPanel();
        const activePanel = document.querySelector('.panel.active');
        if (activePanel?.id === 'panel-sessions') loadSessions();
        if (activePanel?.id === 'panel-requests') loadRequests();
        if (activePanel?.id === 'panel-rate') loadRate();
    },
    openPopover, closePopover,
});
startDayRolloverWatcher();
window.addEventListener('popstate', () => {
    const oldSource = state.source;
    applySourceFromURL();
    syncSourceTabsUI();
    applyStateFromURL();
    if (oldSource !== state.source) loadModelFilter({ preserveCurrent: false });
    loadAll();
});

// Panel-level event listeners are wired by initPanelDaily() / initPanelRequests().
