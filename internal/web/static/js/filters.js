import { state } from './state.js';
import { toYMD, getTodayYMD, isValidYMD } from './utils.js';

const dayDropdownBtn = document.getElementById('day-dropdown-btn');
const dayDropdown    = document.getElementById('day-dropdown');

let onChange = () => {};
let onMetricChange = () => {};
let onResetPages = () => {};

export function buildDayDropdown() {
    const now = new Date();
    const fmt = d => toYMD(d);
    const weekdays = ['Sun','Mon','Tue','Wed','Thu','Fri','Sat'];
    dayDropdown.innerHTML = '';
    for (let i = 0; i < 7; i++) {
        const d = new Date(now);
        d.setDate(d.getDate() - i);
        const dateStr = fmt(d);
        const label = i === 0 ? 'Today' : i === 1 ? 'Yesterday' : weekdays[d.getDay()];
        const btn = document.createElement('button');
        btn.className = 'day-dropdown-item' + (i === 0 && !state.selectedDayDate ? ' active' : '') + (state.selectedDayDate === dateStr ? ' active' : '');
        btn.innerHTML = `<span class="day-label">${label}</span><span class="day-date">${dateStr}</span>`;
        btn.addEventListener('click', () => {
            state.selectedDayDate = i === 0 ? '' : dateStr;
            dayDropdown.classList.remove('open');
            dayDropdownBtn.innerHTML = (i === 0 ? 'Today' : label + ' ' + dateStr) + ' <span class="dropdown-arrow">&#9662;</span>';
            document.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
            dayDropdownBtn.classList.add('active');
            state.currentRange = 'single-day';
            state.customFrom = dateStr;
            state.customTo = dateStr;
            if (state.customRangeFlatpickr) {
                state.customRangeFlatpickr.setDate([dateStr, dateStr], false);
            }
            const crw = document.getElementById('custom-range-wrap');
            if (crw) crw.classList.remove('is-active');
            document.getElementById('granularity-switch').style.display = 'none';
            onResetPages(); onChange();
            buildDayDropdown();
            syncURLFromState();
        });
        dayDropdown.appendChild(btn);
    }
}

export function resetToToday() {
    state.currentRange = 'today';
    state.customFrom = '';
    state.customTo = '';
    state.selectedDayDate = '';
    if (state.customRangeFlatpickr) state.customRangeFlatpickr.clear();
    const crw = document.getElementById('custom-range-wrap');
    if (crw) crw.classList.remove('is-active');
    document.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
    dayDropdownBtn.classList.add('active');
    dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
    dayDropdown.classList.remove('open');
    document.getElementById('granularity-switch').style.display = 'none';
    onResetPages();
    onChange();
    syncURLFromState();
}

function formatDayDropdownBtnHTML(dateStr) {
    const arrow = '<span class="dropdown-arrow">&#9662;</span>';
    const t = getTodayYMD();
    if (dateStr === t) return `Today ${arrow}`;
    const yest = new Date();
    yest.setDate(yest.getDate() - 1);
    if (dateStr === toYMD(yest)) return `Yesterday ${dateStr} ${arrow}`;
    const mm = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateStr);
    if (!mm) return `Today ${arrow}`;
    const d = new Date(Number(mm[1]), Number(mm[2]) - 1, Number(mm[3]), 12, 0, 0, 0);
    const wd = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
    return `${wd[d.getDay()]} ${dateStr} ${arrow}`;
}

export function initCustomRangePicker() {
    const el = document.getElementById('custom-range-picker');
    if (!el || typeof flatpickr === 'undefined') return;
    const baseLocale = (flatpickr.l10ns && (flatpickr.l10ns.default || flatpickr.l10ns.en)) ? (flatpickr.l10ns.default || flatpickr.l10ns.en) : {};
    state.customRangeFlatpickr = flatpickr(el, {
        mode: 'range',
        dateFormat: 'Y-m-d',
        // Force local date parsing for YYYY-MM-DD to avoid environment-dependent
        // Date.parse / timezone quirks that can shift day-of-week in the calendar grid.
        parseDate(dateStr) {
            const s = String(dateStr || '').trim();
            if (!s) return null;
            if (s === 'today') return new Date();
            const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
            if (m) {
                const y = Number(m[1]);
                const mo = Number(m[2]) - 1;
                const d = Number(m[3]);
                return new Date(y, mo, d, 12, 0, 0, 0); // noon local avoids DST midnight edge cases
            }
            const fallback = new Date(s);
            return isNaN(fallback.getTime()) ? null : fallback;
        },
        allowInput: false,
        disableMobile: true,
        maxDate: 'today',
        showMonths: typeof window.matchMedia === 'function' && window.matchMedia('(min-width: 700px)').matches ? 2 : 1,
        altInput: true,
        altInputClass: 'range-date-pick',
        altFormat: 'M j, Y',
        locale: {
            ...baseLocale,
            firstDayOfWeek: 0,
            weekdays: baseLocale.weekdays || {
                shorthand: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'],
                longhand: ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'],
            },
            months: baseLocale.months || {
                shorthand: ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'],
                longhand: ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'],
            },
            rangeSeparator: ' — ',
        },
        onChange(selectedDates) {
            if (selectedDates.length !== 2) return;
            let f = toYMD(selectedDates[0]);
            let t = toYMD(selectedDates[1]);
            if (f > t) { const x = f; f = t; t = x; }
            document.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
            const crw = document.getElementById('custom-range-wrap');
            if (crw) crw.classList.add('is-active');
            state.currentRange = 'custom';
            state.customFrom = f;
            state.customTo = t;
            state.selectedDayDate = '';
            dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
            document.getElementById('granularity-switch').style.display = 'none';
            onResetPages();
            onChange();
            buildDayDropdown();
            syncURLFromState();
        },
    });
}

export function syncRangeNavUIFromState() {
    document.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
    const crw = document.getElementById('custom-range-wrap');
    if (crw) {
        if (state.currentRange === 'custom') crw.classList.add('is-active');
        else crw.classList.remove('is-active');
    }
    const granSwitch = document.getElementById('granularity-switch');
    document.querySelectorAll('.gran-btn').forEach(b => {
        b.classList.toggle('active', b.dataset.gran === state.chartGranularity);
    });
    granSwitch.style.display = state.currentRange === 'all' ? '' : 'none';

    if (state.currentRange === 'custom') {
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
        return;
    }
    if (state.currentRange === 'today') {
        dayDropdownBtn.classList.add('active');
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
        return;
    }
    if (state.currentRange === 'single-day') {
        dayDropdownBtn.classList.add('active');
        dayDropdownBtn.innerHTML = formatDayDropdownBtnHTML(state.customFrom);
        return;
    }
    if (state.currentRange === 'week') {
        document.querySelector('.range-btn[data-range="week"]')?.classList.add('active');
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
        return;
    }
    if (state.currentRange === 'month') {
        document.querySelector('.range-btn[data-range="month"]')?.classList.add('active');
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
        return;
    }
    if (state.currentRange === 'all') {
        document.querySelector('.range-btn[data-range="all"]')?.classList.add('active');
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
    }
}

export function syncURLFromState() {
    const p = new URLSearchParams();
    if (state.source && state.source !== 'claude') {
        p.set('source', state.source);
    }
    if (state.currentRange === 'custom' && state.customFrom && state.customTo) {
        p.set('from', state.customFrom);
        p.set('to', state.customTo);
    } else if (state.currentRange === 'single-day' && state.customFrom) {
        p.set('range', 'day');
        p.set('date', state.customFrom);
    } else if (state.currentRange === 'today') {
        p.set('range', 'today');
    } else if (state.currentRange === 'week') {
        p.set('range', 'week');
    } else if (state.currentRange === 'month') {
        p.set('range', 'month');
    } else if (state.currentRange === 'all') {
        p.set('range', 'all');
        if (state.chartGranularity === 'month') {
            p.set('granularity', 'month');
        }
    }
    const qs = p.toString();
    const next = `${window.location.pathname}${qs ? `?${qs}` : ''}${window.location.hash}`;
    const cur = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    if (next !== cur) {
        history.replaceState(null, '', next);
    }
}

export function applyStateFromURL() {
    const params = new URLSearchParams(location.search);
    const from = params.get('from') || '';
    const to = params.get('to') || '';
    const rangeParam = (params.get('range') || '').trim().toLowerCase();
    const date = (params.get('date') || '').trim();
    const gran = (params.get('granularity') || 'day').trim().toLowerCase();

    const finish = () => {
        syncRangeNavUIFromState();
        buildDayDropdown();
        syncURLFromState();
    };

    if (isValidYMD(from) && isValidYMD(to) && from <= to) {
        state.currentRange = 'custom';
        state.customFrom = from;
        state.customTo = to;
        state.selectedDayDate = '';
        if (state.customRangeFlatpickr) {
            try { state.customRangeFlatpickr.setDate([from, to], false); } catch (_) {}
        }
        finish();
        return;
    }

    if (rangeParam === 'day' && isValidYMD(date)) {
        state.currentRange = 'single-day';
        state.customFrom = date;
        state.customTo = date;
        state.selectedDayDate = date === getTodayYMD() ? '' : date;
        state.chartGranularity = 'day';
        if (state.customRangeFlatpickr) {
            try { state.customRangeFlatpickr.setDate([date, date], false); } catch (_) {}
        }
        finish();
        return;
    }

    const allowed = new Set(['today', 'week', 'month', 'all']);
    let r = allowed.has(rangeParam) ? rangeParam : null;
    if (!r) {
        if (!location.search) {
            state.currentRange = 'today';
            state.customFrom = '';
            state.customTo = '';
            state.selectedDayDate = '';
            state.chartGranularity = 'day';
            if (state.customRangeFlatpickr) {
                try { state.customRangeFlatpickr.clear(); } catch (_) {}
            }
            const crwE = document.getElementById('custom-range-wrap');
            if (crwE) crwE.classList.remove('is-active');
            finish();
            return;
        }
        r = 'today';
    }

    state.currentRange = r;
    state.customFrom = '';
    state.customTo = '';
    state.selectedDayDate = '';
    if (state.customRangeFlatpickr) {
        try { state.customRangeFlatpickr.clear(); } catch (_) {}
    }
    const crwClear = document.getElementById('custom-range-wrap');
    if (crwClear) crwClear.classList.remove('is-active');

    if (r === 'all' && (gran === 'month' || gran === 'day')) {
        state.chartGranularity = gran;
    } else {
        state.chartGranularity = 'day';
    }
    finish();
}

export function initFilters(opts = {}) {
    onChange       = opts.onChange       || (() => {});
    onMetricChange = opts.onMetricChange || (() => {});
    onResetPages   = opts.onResetPages   || (() => {});

    dayDropdownBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        const isOpen = dayDropdown.classList.contains('open');
        dayDropdown.classList.toggle('open', !isOpen);
        if (!isOpen) buildDayDropdown();
    });

    document.addEventListener('click', (e) => {
        if (!e.target.closest('.day-dropdown-wrap')) {
            dayDropdown.classList.remove('open');
        }
    });

    document.getElementById('nav-logo').addEventListener('click', () => resetToToday());

    document.querySelectorAll('.range-btn[data-range]').forEach(btn => {
        btn.addEventListener('click', () => {
            if (btn.id === 'day-dropdown-btn') return;
            document.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            state.currentRange = btn.dataset.range;
            state.customFrom = ''; state.customTo = '';
            state.selectedDayDate = '';
            if (state.currentRange !== 'all') {
                state.chartGranularity = 'day';
                document.querySelectorAll('.gran-btn').forEach(b => {
                    b.classList.toggle('active', b.dataset.gran === 'day');
                });
            }
            if (state.customRangeFlatpickr) state.customRangeFlatpickr.clear();
            const crw = document.getElementById('custom-range-wrap');
            if (crw) crw.classList.remove('is-active');
            dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
            document.getElementById('granularity-switch').style.display = state.currentRange === 'all' ? '' : 'none';
            onResetPages(); onChange();
            syncURLFromState();
        });
    });

    document.querySelectorAll('.metric-sw-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('.metric-sw-btn').forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            state.chartMetric = btn.dataset.metric;
            onMetricChange();
        });
    });

    document.querySelectorAll('.gran-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('.gran-btn').forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            state.chartGranularity = btn.dataset.gran;
            onResetPages(); onChange();
            syncURLFromState();
        });
    });

    document.querySelectorAll('.panel-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('.panel-btn').forEach(b => b.classList.remove('active'));
            document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
            btn.classList.add('active');
            document.getElementById('panel-' + btn.dataset.panel).classList.add('active');
            const filterEl = document.getElementById('request-filter');
            if (filterEl) filterEl.classList.toggle('visible', btn.dataset.panel === 'requests');
        });
    });
}
