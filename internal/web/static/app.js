// ── State ──────────────────────────────────────────────────────────────────
let currentRange = 'today';
let customFrom   = '';
let customTo     = '';
let mainChart    = null;
let isDark       = true;
let chartMetric  = 'tokens'; // 'tokens' | 'cost' | 'requests'
let chartGranularity = 'day';  // 'day' | 'month' — only relevant when All Time is selected
let customRangeFlatpickr = null; // Flatpickr range picker (replaces native <input type="date">)

// Stable model-family palettes: same family => same color system across all charts.
const FAMILY_BASE_DARK = {
    claude: '#ff9f0a',
    glm: '#5e5ce6',
    step: '#30d158',
    gemini: '#64d2ff',
    gpt: '#0a84ff',
    qwen: '#bf5af2',
    deepseek: '#ff375f',
    kimi: '#ffd60a',
    other: '#8e8e93',
};
const FAMILY_BASE_LIGHT = {
    claude: '#ff9500',
    glm: '#5856d6',
    step: '#34c759',
    gemini: '#32ade6',
    gpt: '#007aff',
    qwen: '#af52de',
    deepseek: '#ff2d55',
    kimi: '#ffcc00',
    other: '#8e8e93',
};

// KPI breakdown pie: ranked slice colors (1st = warm accent, then varied, last = muted).
const COLOR_TIERS_DARK = [
    '#ff9f0a', '#0a84ff', '#30d158', '#5e5ce6', '#64d2ff',
    '#bf5af2', '#ff375f', '#ffd60a', '#8e8e93', '#636366',
];
const COLOR_TIERS_LIGHT = [
    '#ff9500', '#007aff', '#34c759', '#5856d6', '#32ade6',
    '#af52de', '#ff2d55', '#ffcc00', '#8e8e93', '#aeaeb2',
];

// Pagination state per table
const paging = {
    daily:    { page: 1, pageSize: 20, total: 0 },
    sessions: { page: 1, pageSize: 20, total: 0 },
    requests: { page: 1, pageSize: 20, total: 0 },
};

// ── Theme toggle ────────────────────────────────────────────────────────────
function applyTheme(dark) {
    isDark = dark;
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
    document.getElementById('theme-icon-sun').style.display  = dark ? 'none' : '';
    document.getElementById('theme-icon-moon').style.display = dark ? '' : 'none';
    localStorage.setItem('cc-otel-theme', dark ? 'dark' : 'light');
    // Re-render chart with new theme colors
    if (mainChart) loadChart();
    if (customRangeFlatpickr && typeof customRangeFlatpickr.redraw === 'function') {
        customRangeFlatpickr.redraw();
    }
}

(function initTheme() {
    const saved = localStorage.getItem('cc-otel-theme');
    const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
    applyTheme(saved ? saved === 'dark' : prefersDark);
})();

document.getElementById('theme-toggle').addEventListener('click', () => {
    applyTheme(!isDark);
});

// ── Theme-aware chart colors ────────────────────────────────────────────────
function chartColors() {
    return isDark ? {
        bg:           'transparent',
        tooltipBg:    '#1c1c1e',
        tooltipBorder:'rgba(255,255,255,0.08)',
        tooltipText:  '#f5f5f7',
        legendText:   '#86868b',
        axisLabel:    '#86868b',
        axisLine:     'rgba(255,255,255,0.06)',
        splitLine:    'rgba(255,255,255,0.04)',
        dzBorder:     'rgba(255,255,255,0.06)',
        dzBg:         'rgba(255,255,255,0.03)',
        dzFill:       'rgba(10,132,255,0.10)',
        dzHandle:     '#0a84ff',
        dzSelLine:    'rgba(10,132,255,0.25)',
        dzSelArea:    'rgba(10,132,255,0.06)',
        dzBgLine:     'rgba(255,255,255,0.06)',
        dzBgArea:     'rgba(255,255,255,0.02)',
        mutedText:    '#86868b',
        shadow:       '0 4px 24px rgba(0,0,0,0.4)',
    } : {
        bg:           'transparent',
        tooltipBg:    '#ffffff',
        tooltipBorder:'rgba(0,0,0,0.08)',
        tooltipText:  '#1d1d1f',
        legendText:   '#86868b',
        axisLabel:    '#86868b',
        axisLine:     'rgba(0,0,0,0.06)',
        splitLine:    'rgba(0,0,0,0.04)',
        dzBorder:     'rgba(0,0,0,0.08)',
        dzBg:         'rgba(0,0,0,0.02)',
        dzFill:       'rgba(0,122,255,0.08)',
        dzHandle:     '#007aff',
        dzSelLine:    'rgba(0,122,255,0.25)',
        dzSelArea:    'rgba(0,122,255,0.06)',
        dzBgLine:     'rgba(0,0,0,0.08)',
        dzBgArea:     'rgba(0,0,0,0.02)',
        mutedText:    '#86868b',
        shadow:       '0 4px 24px rgba(0,0,0,0.12)',
    };
}

function hexToRgb(hex) {
    const h = String(hex || '').replace('#', '').trim();
    if (h.length === 3) {
        const r = parseInt(h[0] + h[0], 16);
        const g = parseInt(h[1] + h[1], 16);
        const b = parseInt(h[2] + h[2], 16);
        return { r, g, b };
    }
    if (h.length !== 6) return null;
    const r = parseInt(h.slice(0, 2), 16);
    const g = parseInt(h.slice(2, 4), 16);
    const b = parseInt(h.slice(4, 6), 16);
    return { r, g, b };
}
function rgbToHex({ r, g, b }) {
    const to = n => Math.max(0, Math.min(255, Math.round(n))).toString(16).padStart(2, '0');
    return `#${to(r)}${to(g)}${to(b)}`;
}
function mixHex(a, b, t) {
    const ca = hexToRgb(a);
    const cb = hexToRgb(b);
    if (!ca || !cb) return a;
    const tt = Math.max(0, Math.min(1, Number(t || 0)));
    return rgbToHex({
        r: ca.r + (cb.r - ca.r) * tt,
        g: ca.g + (cb.g - ca.g) * tt,
        b: ca.b + (cb.b - ca.b) * tt,
    });
}

function hashModelName(name) {
    const s = String(name || '').trim().toLowerCase();
    let h = 0;
    for (let i = 0; i < s.length; i++) {
        h = ((h * 31) + s.charCodeAt(i)) >>> 0;
    }
    return h >>> 0;
}

function getModelFamily(name) {
    const s = String(name || '').trim().toLowerCase();
    if (!s) return 'other';
    if (s.startsWith('claude')) return 'claude';
    if (s.startsWith('glm')) return 'glm';
    if (s.startsWith('step')) return 'step';
    if (s.startsWith('gemini')) return 'gemini';
    if (s.startsWith('gpt') || s.startsWith('o1') || s.startsWith('o3') || s.startsWith('o4')) return 'gpt';
    if (s.startsWith('qwen')) return 'qwen';
    if (s.startsWith('deepseek')) return 'deepseek';
    if (s.startsWith('kimi')) return 'kimi';
    return 'other';
}

function getModelColor(name) {
    const model = String(name || '').trim().toLowerCase();
    const family = getModelFamily(name);
    const base = (isDark ? FAMILY_BASE_DARK : FAMILY_BASE_LIGHT)[family] || (isDark ? FAMILY_BASE_DARK.other : FAMILY_BASE_LIGHT.other);

    // Make Opus the most eye-catching orange in the Claude family.
    if (family === 'claude' && model.includes('opus')) {
        return isDark ? '#ff9f0a' : '#ff8a00';
    }
    if (family === 'claude') {
        const h = hashModelName(name);
        const variant = h % 4;
        if (isDark) {
            const claudeShades = ['#ffb340', '#ffcc73', '#ff9a3c', '#e8902a'];
            return claudeShades[variant];
        }
        const claudeShades = ['#ffad33', '#ffc266', '#ff9a1f', '#e88600'];
        return claudeShades[variant];
    }
    if (family === 'glm') {
        // More separated shades so multiple GLM models stay readable on the same chart.
        const h = hashModelName(name);
        const variant = h % 5;
        if (isDark) {
            const glmShades = ['#4f7cff', '#22a6f2', '#6b5cff', '#00b8d9', '#7c4dff'];
            return glmShades[variant];
        }
        const glmShades = ['#3f6fff', '#168fe0', '#5b50e6', '#00a0c7', '#6d45e0'];
        return glmShades[variant];
    }
    if (family === 'step') {
        const h = hashModelName(name);
        const variant = h % 4;
        if (isDark) {
            const stepShades = ['#30d158', '#1fbf75', '#49e37f', '#00c27a'];
            return stepShades[variant];
        }
        const stepShades = ['#34c759', '#1fb36d', '#42d47a', '#00b36b'];
        return stepShades[variant];
    }

    // Same family, different model => slight shade variation within the family color system.
    const h = hashModelName(name);
    const variant = h % 5; // 5 nearby shades per family
    if (variant === 0) return base;

    // Mix toward white/black a bit to keep family identity while separating siblings.
    if (isDark) {
        const steps = [0.14, 0.26, 0.10, 0.34];
        return mixHex(base, '#ffffff', steps[variant - 1]);
    }
    const steps = [0.10, 0.18, 0.26, 0.08];
    return mixHex(base, '#000000', steps[variant - 1]);
}

// ── Day dropdown — recent 7 days picker ────────────────────────────────────
const dayDropdownBtn = document.getElementById('day-dropdown-btn');
const dayDropdown    = document.getElementById('day-dropdown');
let selectedDayDate  = ''; // '' means today

function buildDayDropdown() {
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
        btn.className = 'day-dropdown-item' + (i === 0 && !selectedDayDate ? ' active' : '') + (selectedDayDate === dateStr ? ' active' : '');
        btn.innerHTML = `<span class="day-label">${label}</span><span class="day-date">${dateStr}</span>`;
        btn.addEventListener('click', () => {
            selectedDayDate = i === 0 ? '' : dateStr;
            dayDropdown.classList.remove('open');
            // Update button text
            dayDropdownBtn.innerHTML = (i === 0 ? 'Today' : label + ' ' + dateStr) + ' <span class="dropdown-arrow">&#9662;</span>';
            // Set range to single-day
            document.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
            dayDropdownBtn.classList.add('active');
            currentRange = 'single-day';
            customFrom = dateStr;
            customTo = dateStr;
            if (customRangeFlatpickr) {
                customRangeFlatpickr.setDate([dateStr, dateStr], false);
            }
            const crw = document.getElementById('custom-range-wrap');
            if (crw) crw.classList.remove('is-active');
            document.getElementById('granularity-switch').style.display = 'none';
            resetPages(); loadAll();
            // Rebuild to update active state
            buildDayDropdown();
            syncURLFromState();
        });
        dayDropdown.appendChild(btn);
    }
}

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

// ── Logo: reset to Today ───────────────────────────────────────────────────
function resetToToday() {
    currentRange = 'today';
    customFrom = '';
    customTo = '';
    selectedDayDate = '';
    if (customRangeFlatpickr) customRangeFlatpickr.clear();
    const crw = document.getElementById('custom-range-wrap');
    if (crw) crw.classList.remove('is-active');
    document.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
    dayDropdownBtn.classList.add('active');
    dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
    dayDropdown.classList.remove('open');
    document.getElementById('granularity-switch').style.display = 'none';
    resetPages();
    loadAll();
    syncURLFromState();
}

document.getElementById('nav-logo').addEventListener('click', () => resetToToday());

// ── Custom date range — Flatpickr (styled range calendar, not OS native) ─────
function toYMD(d) {
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${y}-${m}-${day}`;
}

function getTodayYMD() {
    return toYMD(new Date());
}

function isValidYMD(s) {
    if (!s || typeof s !== 'string') return false;
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s.trim());
    if (!m) return false;
    const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 12, 0, 0, 0);
    if (isNaN(d.getTime())) return false;
    return toYMD(d) === s.trim();
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

function initCustomRangePicker() {
    const el = document.getElementById('custom-range-picker');
    if (!el || typeof flatpickr === 'undefined') return;
    const baseLocale = (flatpickr.l10ns && (flatpickr.l10ns.default || flatpickr.l10ns.en)) ? (flatpickr.l10ns.default || flatpickr.l10ns.en) : {};
    customRangeFlatpickr = flatpickr(el, {
        mode: 'range',
        dateFormat: 'Y-m-d',
        // Force local date parsing for YYYY-MM-DD to avoid any environment-dependent
        // Date.parse / timezone quirks that can shift day-of-week in the calendar grid.
        parseDate(dateStr, _format) {
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
        // Force Sunday-first calendar headers to match the grid.
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
            currentRange = 'custom';
            customFrom = f;
            customTo = t;
            selectedDayDate = '';
            dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
            document.getElementById('granularity-switch').style.display = 'none';
            resetPages();
            loadAll();
            buildDayDropdown();
            syncURLFromState();
        },
    });
}

// ── URL ↔ date filter (shareable / refresh-safe) ────────────────────────────
function syncRangeNavUIFromState() {
    document.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
    const crw = document.getElementById('custom-range-wrap');
    if (crw) {
        if (currentRange === 'custom') crw.classList.add('is-active');
        else crw.classList.remove('is-active');
    }
    const granSwitch = document.getElementById('granularity-switch');
    document.querySelectorAll('.gran-btn').forEach(b => {
        b.classList.toggle('active', b.dataset.gran === chartGranularity);
    });
    granSwitch.style.display = currentRange === 'all' ? '' : 'none';

    if (currentRange === 'custom') {
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
        return;
    }
    if (currentRange === 'today') {
        dayDropdownBtn.classList.add('active');
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
        return;
    }
    if (currentRange === 'single-day') {
        dayDropdownBtn.classList.add('active');
        dayDropdownBtn.innerHTML = formatDayDropdownBtnHTML(customFrom);
        return;
    }
    if (currentRange === 'week') {
        document.querySelector('.range-btn[data-range="week"]')?.classList.add('active');
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
        return;
    }
    if (currentRange === 'month') {
        document.querySelector('.range-btn[data-range="month"]')?.classList.add('active');
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
        return;
    }
    if (currentRange === 'all') {
        document.querySelector('.range-btn[data-range="all"]')?.classList.add('active');
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
    }
}

function syncURLFromState() {
    const p = new URLSearchParams();
    if (currentRange === 'custom' && customFrom && customTo) {
        p.set('from', customFrom);
        p.set('to', customTo);
    } else if (currentRange === 'single-day' && customFrom) {
        p.set('range', 'day');
        p.set('date', customFrom);
    } else if (currentRange === 'today') {
        p.set('range', 'today');
    } else if (currentRange === 'week') {
        p.set('range', 'week');
    } else if (currentRange === 'month') {
        p.set('range', 'month');
    } else if (currentRange === 'all') {
        p.set('range', 'all');
        if (chartGranularity === 'month') {
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

function applyStateFromURL() {
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
        currentRange = 'custom';
        customFrom = from;
        customTo = to;
        selectedDayDate = '';
        if (customRangeFlatpickr) {
            try { customRangeFlatpickr.setDate([from, to], false); } catch (_) {}
        }
        finish();
        return;
    }

    if (rangeParam === 'day' && isValidYMD(date)) {
        currentRange = 'single-day';
        customFrom = date;
        customTo = date;
        selectedDayDate = date === getTodayYMD() ? '' : date;
        chartGranularity = 'day';
        if (customRangeFlatpickr) {
            try { customRangeFlatpickr.setDate([date, date], false); } catch (_) {}
        }
        finish();
        return;
    }

    const allowed = new Set(['today', 'week', 'month', 'all']);
    let r = allowed.has(rangeParam) ? rangeParam : null;
    if (!r) {
        if (!location.search) {
            currentRange = 'today';
            customFrom = '';
            customTo = '';
            selectedDayDate = '';
            chartGranularity = 'day';
            if (customRangeFlatpickr) {
                try { customRangeFlatpickr.clear(); } catch (_) {}
            }
            const crwE = document.getElementById('custom-range-wrap');
            if (crwE) crwE.classList.remove('is-active');
            finish();
            return;
        }
        r = 'today';
    }

    currentRange = r;
    customFrom = '';
    customTo = '';
    selectedDayDate = '';
    if (customRangeFlatpickr) {
        try { customRangeFlatpickr.clear(); } catch (_) {}
    }
    const crwClear = document.getElementById('custom-range-wrap');
    if (crwClear) crwClear.classList.remove('is-active');

    if (r === 'all' && (gran === 'month' || gran === 'day')) {
        chartGranularity = gran;
    } else {
        chartGranularity = 'day';
    }
    finish();
}

// ── Range & Panel tabs ─────────────────────────────────────────────────────
document.querySelectorAll('.range-btn[data-range]').forEach(btn => {
    btn.addEventListener('click', () => {
        if (btn.id === 'day-dropdown-btn') return; // handled by dropdown
        document.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        currentRange = btn.dataset.range;
        customFrom = ''; customTo = '';
        selectedDayDate = '';
        if (currentRange !== 'all') {
            chartGranularity = 'day';
            document.querySelectorAll('.gran-btn').forEach(b => {
                b.classList.toggle('active', b.dataset.gran === 'day');
            });
        }
        if (customRangeFlatpickr) customRangeFlatpickr.clear();
        const crw = document.getElementById('custom-range-wrap');
        if (crw) crw.classList.remove('is-active');
        // Reset day dropdown button text
        dayDropdownBtn.innerHTML = 'Today <span class="dropdown-arrow">&#9662;</span>';
        // Show granularity switcher only for All Time
        document.getElementById('granularity-switch').style.display = currentRange === 'all' ? '' : 'none';
        resetPages(); loadAll();
        syncURLFromState();
    });
});

document.querySelectorAll('.metric-sw-btn').forEach(btn => {
    btn.addEventListener('click', () => {
        document.querySelectorAll('.metric-sw-btn').forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        chartMetric = btn.dataset.metric;
        loadChart();
        refreshDailyPanel();
    });
});

// Granularity switcher — Day / Month
document.querySelectorAll('.gran-btn').forEach(btn => {
    btn.addEventListener('click', () => {
        document.querySelectorAll('.gran-btn').forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        chartGranularity = btn.dataset.gran;
        resetPages(); loadAll();
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

// ── SSE — throttled refresh + exponential backoff ───────────────────────────
let sseRefreshTimer = null;
let sseReconnectTimer = null;
let sseBackoff = 1000; // initial backoff 1s, doubles up to max
const SSE_BACKOFF_MAX = 30000; // 30s cap

function scheduleRefresh() {
    if (sseRefreshTimer) return;
    sseRefreshTimer = setTimeout(() => {
        sseRefreshTimer = null;
        refreshVisiblePanels();
    }, 500);
}

function refreshVisiblePanels() {
    loadDashboard();
    loadChart();
    refreshDailyPanel();
    const activePanel = document.querySelector('.panel.active');
    if (activePanel?.id === 'panel-sessions') loadSessions();
    if (activePanel?.id === 'panel-requests') loadRequests();
}

function connectSSE() {
    const es = new EventSource('/api/events');

    es.onopen = () => {
        setStatus(true);
        sseBackoff = 1000; // reset on successful connection
    };

    es.onmessage = e => {
        if (e.data === 'update') scheduleRefresh();
    };

    es.onerror = () => {
        setStatus(false);
        es.close();
        clearTimeout(sseReconnectTimer);
        sseReconnectTimer = setTimeout(connectSSE, sseBackoff);
        sseBackoff = Math.min(sseBackoff * 2, SSE_BACKOFF_MAX);
    };
}

let sseConnected = false;
function setStatus(ok) {
    sseConnected = !!ok;
    const dot   = document.getElementById('status-dot');
    const label = document.getElementById('status-label');
    dot.className   = 'status-dot ' + (ok ? 'ok' : 'err');
    label.textContent = ok ? 'live' : 'offline';
}

// ── Status modal ────────────────────────────────────────────────────────────
const statusBtn   = document.getElementById('status-btn');
const statusModal = document.getElementById('status-modal');
const statusClose = document.getElementById('status-close');
let statusTimer = null;

function fmtTime(unix) {
    if (!unix) return '—';
    try {
        const d = new Date(unix * 1000);
        return d.toLocaleString();
    } catch { return String(unix); }
}

function fmtUSD(v) {
    const n = Number(v || 0);
    return '$' + n.toFixed(4);
}

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

async function loadStatus() {
    // SSE status is local, still show it even if API call fails.
    document.getElementById('st-sse').textContent = sseConnected ? 'connected' : 'disconnected';
    try {
        const res = await fetch('/api/status');
        const s = await res.json();

        document.getElementById('st-db').textContent = s.db_ok ? 'ok' : 'error';
        document.getElementById('st-otel').textContent = s.otel_receiver_listening ? `listening :${s.otel_port}` : `not responding :${s.otel_port}`;
        document.getElementById('st-last').textContent = s.last_update_unix ? fmtTime(s.last_update_unix) : '—';

        document.getElementById('st-otel-endpoint').textContent = `http://localhost:${s.otel_port}`;
        document.getElementById('st-web-endpoint').textContent  = `http://localhost:${s.web_port}`;
        // Also add small context like SSE clients if present.
        const sseLine = sseConnected ? `connected · ${s.sse_clients ?? 0} clients` : 'disconnected';
        document.getElementById('st-sse').textContent = sseLine;
    } catch (e) {
        console.error('status:', e);
        document.getElementById('st-db').textContent = '—';
        document.getElementById('st-otel').textContent = '—';
        document.getElementById('st-last').textContent = '—';
    }
}

function openStatusModal() {
    openPopover(statusModal, statusBtn);
    loadStatus();
    clearInterval(statusTimer);
    statusTimer = setInterval(loadStatus, 4000);
}

function closeStatusModal() {
    closePopover(statusModal);
    clearInterval(statusTimer);
    statusTimer = null;
}

statusBtn?.addEventListener('click', openStatusModal);
statusClose?.addEventListener('click', closeStatusModal);
statusModal?.addEventListener('click', (e) => {
    if (e.target === statusModal) closeStatusModal();
});
document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && statusModal?.style.display !== 'none') closeStatusModal();
});

window.addEventListener('resize', () => {
    if (statusModal?.style.display !== 'none') openPopover(statusModal, statusBtn);
});

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

// ── KPI: Cost by model ──────────────────────────────────────────────────────
const costBtn   = document.getElementById('kpi-total-cost');
const inputBtn  = document.getElementById('kpi-input');
const outputBtn = document.getElementById('kpi-output');
const cacheBtn  = document.getElementById('kpi-cache-hit');
const reqBtn    = document.getElementById('kpi-requests');
const costModal = document.getElementById('cost-modal');
const costClose = document.getElementById('cost-close');
const costColValue = document.getElementById('cost-col-value');
const breakdownChartEl = document.getElementById('breakdown-chart');
const breakdownTableEl = document.getElementById('breakdown-table');
const breakdownSelectedEl = document.getElementById('breakdown-selected');
const breakdownViewPieBtn = document.getElementById('breakdown-view-pie');
const breakdownViewListBtn = document.getElementById('breakdown-view-list');

let breakdownChart = null;
let breakdownLast = null; // { kind, cfg, items, total, from, to }

function closeCostModal() {
    closePopover(costModal);
}

function fmtPct(p) {
    if (!Number.isFinite(p)) return '—';
    return p.toFixed(1) + '%';
}

function fmtTokens(n) {
    return fmtNum(n);
}

function getBreakdownView(kind) {
    // Cache hit is not additive; a pie chart would be misleading.
    if (kind === 'cache_hit') return 'list';
    const v = (localStorage.getItem('cc-otel-breakdown-view') || '').trim();
    return (v === 'list' || v === 'pie') ? v : 'pie';
}

function setBreakdownView(kind, view) {
    if (kind === 'cache_hit') view = 'list';
    localStorage.setItem('cc-otel-breakdown-view', view);
    applyBreakdownView(kind, view);
}

function applyBreakdownView(kind, view) {
    if (!breakdownViewPieBtn || !breakdownViewListBtn || !breakdownChartEl || !breakdownTableEl) return;
    const v = (kind === 'cache_hit') ? 'list' : view;

    breakdownViewPieBtn.disabled = kind === 'cache_hit';
    breakdownViewPieBtn.classList.toggle('active', v === 'pie');
    breakdownViewPieBtn.setAttribute('aria-selected', v === 'pie' ? 'true' : 'false');
    breakdownViewListBtn.classList.toggle('active', v === 'list');
    breakdownViewListBtn.setAttribute('aria-selected', v === 'list' ? 'true' : 'false');

    breakdownChartEl.style.display = v === 'pie' ? '' : 'none';
    breakdownChartEl.setAttribute('aria-hidden', v === 'pie' ? 'false' : 'true');
    breakdownTableEl.style.display = v === 'list' ? '' : 'none';

    if (v === 'pie') {
        ensureBreakdownChart();
        // ECharts needs resize after being unhidden.
        try { breakdownChart?.resize?.(); } catch {}
    }
}

function ensureBreakdownChart() {
    if (!breakdownChartEl || typeof echarts === 'undefined') return null;
    if (!breakdownChart) {
        breakdownChart = echarts.init(breakdownChartEl, null, { renderer: 'canvas' });
        breakdownChart.on('mouseover', (params) => {
            if (!params || params.componentType !== 'series') return;
            const it = params.data;
            if (it && breakdownSelectedEl) {
                breakdownSelectedEl.textContent = `${it.name} · ${it.valueText} · ${it.shareText}`;
            }
        });
        breakdownChart.on('mouseout', () => {
            if (!breakdownSelectedEl) return;
            if (!breakdownLast) { breakdownSelectedEl.textContent = ''; return; }
            breakdownSelectedEl.textContent = breakdownLast.selectedText || '';
        });
        breakdownChart.on('click', (params) => {
            if (!params || params.componentType !== 'series') return;
            const it = params.data;
            if (it && breakdownSelectedEl) {
                breakdownLast = breakdownLast || {};
                breakdownLast.selectedText = `${it.name} · ${it.valueText} · ${it.shareText}`;
                breakdownSelectedEl.textContent = breakdownLast.selectedText;
            }
        });
    }
    return breakdownChart;
}

function renderBreakdownPie(kind, cfg, items, total, from, to) {
    const chart = ensureBreakdownChart();
    if (!chart) return;

    const c = chartColors();

    // Color assignment: top model accent, next two secondary, rest muted.
    const tiers = isDark ? COLOR_TIERS_DARK : COLOR_TIERS_LIGHT;
    function colorForRank(i) {
        return tiers[Math.min(i, tiers.length - 1)];
    }

    const data = items.map((it, idx) => {
        const share = (kind !== 'cache_hit' && total > 0) ? (it.v / total) * 100 : NaN;
        const valueText = cfg.fmt(it.v);
        const shareText = Number.isFinite(share) ? `${share.toFixed(1)}%` : '—';
        return {
            name: it.model,
            value: it.v,
            valueText,
            shareText,
            itemStyle: { color: colorForRank(idx) },
        };
    });

    const totalText =
        kind === 'cost' ? fmtUSD(total)
        : kind === 'cache_hit' ? '—'
        : fmtTokens(total);

    chart.setOption({
        backgroundColor: 'transparent',
        tooltip: {
            trigger: 'item',
            backgroundColor: c.tooltipBg,
            borderColor: c.tooltipBorder,
            borderWidth: 1,
            textStyle: { color: c.tooltipText },
            extraCssText: `box-shadow:${c.shadow};border-radius:10px;padding:10px 12px;`,
            formatter(params) {
                const d = params?.data;
                if (!d) return '';
                const shareLine = (kind === 'cache_hit')
                    ? `<div style="opacity:.72;margin-top:2px">Share: —</div>`
                    : `<div style="opacity:.72;margin-top:2px">Share: ${escapeHtml(d.shareText)}</div>`;
                return `
                    <div style="font-weight:700;margin-bottom:4px">${escapeHtml(d.name)}</div>
                    <div>Value: <span style="font-family:var(--font-mono)">${escapeHtml(d.valueText)}</span></div>
                    ${shareLine}
                `;
            },
        },
        series: [{
            name: cfg.title,
            type: 'pie',
            radius: '72%',
            center: ['50%', '54%'],
            avoidLabelOverlap: true,
            label: {
                show: true,
                color: c.tooltipText,
                fontSize: 11,
                formatter(params) {
                    // Show percent on-chart; hide very small slices to reduce clutter.
                    const p = Number(params?.percent);
                    if (!Number.isFinite(p) || p < 3) return '';
                    const name = String(params?.name || '').trim();
                    // Keep labels compact so they don't overwhelm the chart.
                    const short = name.length > 14 ? (name.slice(0, 12) + '…') : name;
                    return `${short}\n${p.toFixed(1)}%`;
                },
            },
            labelLine: {
                show: true,
                length: 10,
                length2: 8,
                lineStyle: { color: c.axisLine },
            },
            emphasis: {
                // NOTE: Do not focus whole series; keep it per-slice.
                scale: true,
                scaleSize: 6,
            },
            data,
        }],
        graphic: [
            {
                type: 'text',
                left: 'center',
                top: 10,
                style: {
                    text: `total ${totalText}`,
                    fill: c.mutedText,
                    fontSize: 12,
                    fontFamily: 'var(--font-mono)',
                },
            },
        ],
    }, { notMerge: true });
}

async function openBreakdownModal(kind, anchorEl) {
    openPopover(costModal, anchorEl);

    const titleEl = document.getElementById('cost-title');
    const metaEl  = document.getElementById('cost-meta');
    const tbody  = document.getElementById('cost-tbody');
    metaEl.textContent = 'loading…';
    tbody.innerHTML = '';
    if (breakdownSelectedEl) breakdownSelectedEl.textContent = '';

    const { from, to } = rangeToFromTo(currentRange);
    try {
        // Use /api/daily as source of truth, aggregate cost by model.
        const q = new URLSearchParams({
            from,
            to,
            page: '1',
            page_size: '2000',
            granularity: chartGranularity || 'day',
        });
        const res = await fetch(`/api/daily?${q}`);
        if (!res.ok) {
            const errBody = await res.text();
            throw new Error(errBody || `HTTP ${res.status}`);
        }
        const json = await res.json();
        const raw = json.data != null ? json.data : json;
        const rows = Array.isArray(raw) ? raw : [];

        const byModel = new Map();
        let total = 0;
        for (const r of rows) {
            const model = r.model || 'unknown';
            let val = 0;
            if (kind === 'cost') val = Number(r.cost_usd || 0);
            else if (kind === 'input') {
                val = Number(r.input_tokens || 0) + Number(r.cache_read_tokens || 0) + Number(r.cache_creation_tokens || 0);
            }
            else if (kind === 'output') val = Number(r.output_tokens || 0);
            else if (kind === 'requests') val = Number(r.request_count || 0);
            else if (kind === 'cache_hit') {
                const cacheRead = Number(r.cache_read_tokens || 0);
                const cacheCreate = Number(r.cache_creation_tokens || 0);
                // Same definition as backend: cache_read / (cache_read + cache_creation)
                const denom = cacheRead + cacheCreate;
                val = denom > 0 ? cacheRead / denom : 0;
            }
            total += val;
            byModel.set(model, (byModel.get(model) || 0) + val);
        }

        const items = [...byModel.entries()]
            .map(([model, v]) => ({ model, v }))
            .sort((a, b) => b.v - a.v);

        const cfg = {
            cost:      { title: 'Cost by Model',     col: 'Cost',      fmt: v => fmtUSD(v) },
            input:     { title: 'Input by Model (input-side)',  col: 'Input', fmt: v => fmtTokens(v) },
            output:    { title: 'Output Tokens by Model', col: 'Output',fmt: v => fmtTokens(v) },
            requests:  { title: 'Requests by Model', col: 'Requests',  fmt: v => String(Math.round(v)) },
            cache_hit: { title: 'Cache Hit by Model',col: 'Cache Hit', fmt: v => fmtPct(v * 100) },
        }[kind] || { title: 'Breakdown by Model', col: 'Value', fmt: v => String(v) };

        titleEl.textContent = cfg.title;
        if (costColValue) costColValue.textContent = cfg.col;

        const totalText =
            kind === 'cost' ? fmtUSD(total)
            : kind === 'cache_hit' ? '—'
            : fmtTokens(total);
        metaEl.textContent = `${from} → ${to} · ${items.length} models` + (kind === 'cache_hit' ? '' : ` · total ${totalText}`);

        if (!items.length) {
            if (breakdownChartEl) breakdownChartEl.style.display = 'none';
            if (breakdownTableEl) breakdownTableEl.style.display = '';
            tbody.innerHTML = `<tr><td colspan="3" style="color:var(--text-muted)">No data</td></tr>`;
            return;
        }

        tbody.innerHTML = items.map(it => {
            // Share is meaningful for additive metrics; for cache_hit it is not.
            const share = (kind !== 'cache_hit' && total > 0) ? (it.v / total) * 100 : NaN;
            return `<tr>
                <td class="mono">${escapeHtml(it.model)}</td>
                <td class="mono">${escapeHtml(cfg.fmt(it.v))}</td>
                <td class="mono">${Number.isFinite(share) ? share.toFixed(1) + '%' : '—'}</td>
            </tr>`;
        }).join('');

        // Store last breakdown data for resize / rerender.
        breakdownLast = { kind, cfg, items, total, from, to, selectedText: '' };

        // Apply view preference and render pie if visible.
        const view = getBreakdownView(kind);
        applyBreakdownView(kind, view);
        if (view === 'pie' && kind !== 'cache_hit') {
            try {
                renderBreakdownPie(kind, cfg, items, total, from, to);
                if (breakdownSelectedEl) {
                    const totalLine = kind === 'cost' ? fmtUSD(total) : fmtTokens(total);
                    breakdownLast.selectedText = `Total · ${totalLine} · 100%`;
                    breakdownSelectedEl.textContent = breakdownLast.selectedText;
                }
            } catch (pieErr) {
                console.error('breakdown pie:', pieErr);
                setBreakdownView(kind, 'list');
            }
        }
    } catch (e) {
        console.error('cost breakdown:', e);
        metaEl.textContent = 'failed to load';
        if (breakdownChartEl) breakdownChartEl.style.display = 'none';
        if (breakdownTableEl) breakdownTableEl.style.display = '';
        tbody.innerHTML = `<tr><td colspan="3" style="color:var(--text-muted)">Failed to load</td></tr>`;
    }
}

costBtn?.addEventListener('click', (e) => openBreakdownModal('cost', e.currentTarget));
inputBtn?.addEventListener('click', (e) => openBreakdownModal('input', e.currentTarget));
outputBtn?.addEventListener('click', (e) => openBreakdownModal('output', e.currentTarget));
cacheBtn?.addEventListener('click', (e) => openBreakdownModal('cache_hit', e.currentTarget));
reqBtn?.addEventListener('click', (e) => openBreakdownModal('requests', e.currentTarget));
costClose?.addEventListener('click', closeCostModal);
costModal?.addEventListener('click', (e) => {
    if (e.target === costModal) closeCostModal();
});

document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && costModal?.style.display !== 'none') closeCostModal();
});

window.addEventListener('resize', () => {
    if (costModal?.style.display !== 'none') openPopover(costModal, document.activeElement);
});

breakdownViewPieBtn?.addEventListener('click', () => {
    const k = breakdownLast?.kind || 'cost';
    setBreakdownView(k, 'pie');
    if (breakdownLast && breakdownLast.kind !== 'cache_hit') {
        renderBreakdownPie(breakdownLast.kind, breakdownLast.cfg, breakdownLast.items, breakdownLast.total, breakdownLast.from, breakdownLast.to);
    }
});
breakdownViewListBtn?.addEventListener('click', () => {
    const k = breakdownLast?.kind || 'cost';
    setBreakdownView(k, 'list');
});

// ── Dashboard cards ─────────────────────────────────────────────────────────
async function loadDashboard() {
    try {
        const { from, to } = rangeToFromTo(currentRange);
        const res = await fetch(`/api/dashboard?from=${from}&to=${to}`);
        const d = await res.json();
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

function ensureInsightsBar() {
    if (document.getElementById('insights-bar')) return;
    const anchor = document.querySelector('.kpi-row');
    if (!anchor) return;
    const bar = document.createElement('div');
    bar.id = 'insights-bar';
    bar.className = 'insights-bar';
    bar.style.display = 'none';
    bar.innerHTML = `
        <button type="button" class="insights-main" id="insights-toggle" title="Show details">
            <span class="insights-k">Avg/day</span>
            <span class="insights-v mono" id="ins-avg-summary">—</span>
            <span class="insights-tail mono" id="ins-days">—</span>
        </button>
        <button type="button" class="insights-details-btn" id="insights-details-btn" title="Show details">Details</button>
    `;
    anchor.insertAdjacentElement('afterend', bar);

    ensureInsightsModal();
    const open = () => openInsightsModal();
    document.getElementById('insights-toggle')?.addEventListener('click', open);
    document.getElementById('insights-details-btn')?.addEventListener('click', open);
}

function setInsightsVisible(show) {
    ensureInsightsBar();
    const bar = document.getElementById('insights-bar');
    if (!bar) return;
    bar.style.display = show ? '' : 'none';
    if (!show) closeInsightsModal();
}

let insightsReqId = 0;
let insightsData = null; // { from, to, activeDays, byModel, byDayModel, dayTotals, models, dates }

let insightsModal = null;
function ensureInsightsModal() {
    if (insightsModal) return;
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.id = 'insights-modal';
    backdrop.style.display = 'none';
    backdrop.setAttribute('aria-hidden', 'true');
    backdrop.innerHTML = `
        <div class="modal insights-modal" role="dialog" aria-modal="true" aria-labelledby="insights-title">
            <div class="modal-header">
                <div class="modal-title" id="insights-title">Insights</div>
                <div class="insights-controls" role="group" aria-label="Insights controls">
                    <select class="insights-select" id="insights-metric">
                        <option value="tokens">Tokens</option>
                        <option value="cost">Cost</option>
                        <option value="reqs">Reqs</option>
                    </select>
                    <select class="insights-select" id="insights-model">
                        <option value="">All models</option>
                    </select>
                </div>
                <button type="button" class="modal-close" id="insights-close" title="Close">×</button>
            </div>
            <div class="modal-body">
                <div class="insights-grid">
                    <div class="insights-card">
                        <div class="insights-card-k">Top model (range total, current metric)</div>
                        <div class="insights-card-v mono" id="ins-top-summary">—</div>
                        <div class="insights-card-sub mono" id="ins-top-alt">—</div>
                    </div>
                    <div class="insights-card">
                        <div class="insights-card-k">Active days</div>
                        <div class="insights-card-v mono" id="ins-active-days-summary">—</div>
                        <details class="insights-days-details">
                            <summary class="insights-days-summary">Show dates</summary>
                            <div class="mono insights-days-list" id="ins-active-days-list">—</div>
                        </details>
                        <div class="insights-card-sub">Only days with data are counted in Avg/day.</div>
                    </div>
                </div>
                <div class="insights-table-wrap">
                    <div class="insights-table-title">Daily ranking</div>
                    <table class="insights-table">
                        <thead>
                            <tr>
                                <th>Date</th>
                                <th>Top1</th>
                                <th>Selected model rank</th>
                                <th>Selected model value</th>
                                <th>Selected model share</th>
                            </tr>
                        </thead>
                        <tbody id="ins-daily-rank-tbody"></tbody>
                    </table>
                </div>
                <div class="insights-table-wrap">
                    <div class="insights-table-title">Model share (Top 10)</div>
                    <table class="insights-table">
                        <thead>
                            <tr>
                                <th>#</th>
                                <th>Model</th>
                                <th>Value</th>
                                <th>Share</th>
                            </tr>
                        </thead>
                        <tbody id="ins-model-share-tbody"></tbody>
                    </table>
                </div>
            </div>
        </div>
    `;
    document.body.appendChild(backdrop);
    insightsModal = backdrop;

    // Close behaviors
    document.getElementById('insights-close')?.addEventListener('click', closeInsightsModal);
    backdrop.addEventListener('click', (e) => {
        if (e.target === backdrop) closeInsightsModal();
    });
    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && insightsModal?.style.display !== 'none') closeInsightsModal();
    });
}

function openInsightsModal() {
    ensureInsightsModal();
    openPopover(insightsModal, null); // centered on mobile; popover logic handles null anchor by centering
    if (insightsData) renderInsightsDetails();
}

function closeInsightsModal() {
    if (!insightsModal) return;
    closePopover(insightsModal);
}

/** Coerce per-model aggregate field to a finite number (avoids NaN in UI when data is missing). */
function insightMetricScalar(v, key) {
    if (v == null || typeof v !== 'object') return 0;
    const n = Number(v[key]);
    return Number.isFinite(n) ? n : 0;
}

function fmtMetricValue(metric, v) {
    const n = Number(v);
    const safe = Number.isFinite(n) ? n : 0;
    if (metric === 'reqs' && safe === 0 && v) {
        console.debug('insights reqs coerced to 0', { v, n });
    }
    if (metric === 'cost') return fmtUSD(safe);
    if (metric === 'reqs') return String(Math.round(safe));
    return fmtNum(Math.round(safe));
}

function metricKey(metric) {
    if (metric === 'cost') return 'cost';
    if (metric === 'reqs' || metric === 'requests') return 'reqs';
    return 'tokens';
}

function topEntry(map, key) {
    let bestM = '—';
    let bestV = -Infinity;
    for (const [m, v] of map.entries()) {
        const val = insightMetricScalar(v, key);
        if (val > bestV) { bestV = val; bestM = m; }
    }
    if (bestV === -Infinity || !Number.isFinite(bestV)) {
        return { model: '—', value: 0 };
    }
    return { model: bestM, value: bestV };
}

function renderInsightsDetails() {
    if (!insightsData) return;
    const metricSel = document.getElementById('insights-metric');
    const modelSel = document.getElementById('insights-model');
    const metric = metricSel?.value || 'tokens';
    const key = metricKey(metric);
    const selectedModel = modelSel?.value || '';

    // Top model summary: focus on current metric to reduce clutter.
    const top = topEntry(insightsData.byModel, key);
    const total = Number(insightsData.totals[key]);
    const totalSafe = Number.isFinite(total) ? total : 0;
    const share = totalSafe > 0 ? (top.value / totalSafe) * 100 : NaN;
    const topSummary = document.getElementById('ins-top-summary');
    const topAlt = document.getElementById('ins-top-alt');
    if (topSummary) {
        topSummary.textContent = `${metric} ${top.model}`;
    }
    if (topAlt) {
        topAlt.textContent = `total=${fmtMetricValue(metric, top.value)} · share=${Number.isFinite(share) ? share.toFixed(1) + '%' : '—'}`;
    }

    // Active days: show compact summary + expandable list.
    const activeEl = document.getElementById('ins-active-days-list');
    if (activeEl) activeEl.textContent = insightsData.activeDates.join(', ') || '—';
    const activeSummaryEl = document.getElementById('ins-active-days-summary');
    if (activeSummaryEl) {
        const n = insightsData.activeDates.length;
        const first = insightsData.activeDates[0];
        const last = insightsData.activeDates[n - 1];
        activeSummaryEl.textContent = n ? `${n} days · ${first} → ${last}` : '—';
    }

    // Daily ranking table
    const dailyTbody = document.getElementById('ins-daily-rank-tbody');
    if (dailyTbody) {
        dailyTbody.innerHTML = insightsData.dates.map(date => {
            const dm = insightsData.byDayModel.get(date) || new Map();
            // rank models by selected metric
            const entries = [...dm.entries()]
                .map(([m, v]) => [m, insightMetricScalar(v, key)])
                .sort((a, b) => b[1] - a[1]);
            const top1 = entries[0]?.[0] || '—';
            const dayTotal = insightMetricScalar(insightsData.dayTotals.get(date) || {}, key);

            let rankText = '—';
            let valueText = '—';
            let shareText = '—';
            if (selectedModel) {
                const idx = entries.findIndex(e => e[0] === selectedModel);
                if (idx >= 0) {
                    const v = entries[idx][1] || 0;
                    rankText = `#${idx + 1}/${entries.length}`;
                    valueText = fmtMetricValue(metric, v);
                    const share = dayTotal > 0 ? (Number(v) / dayTotal) * 100 : NaN;
                    shareText = Number.isFinite(share) ? share.toFixed(1) + '%' : '—';
                } else {
                    rankText = `—/${entries.length}`;
                    valueText = fmtMetricValue(metric, 0);
                    shareText = '0.0%';
                }
            } else {
                // no model filter -> show Top1 value/share
                const v = entries[0]?.[1] || 0;
                rankText = '#1';
                valueText = fmtMetricValue(metric, v);
                const share = dayTotal > 0 ? (Number(v) / dayTotal) * 100 : NaN;
                shareText = Number.isFinite(share) ? share.toFixed(1) + '%' : '—';
            }

            return `<tr>
                <td class="mono">${escapeHtml(date)}</td>
                <td class="mono">${escapeHtml(top1)}</td>
                <td class="mono">${escapeHtml(rankText)}</td>
                <td class="mono">${escapeHtml(valueText)}</td>
                <td class="mono">${escapeHtml(shareText)}</td>
            </tr>`;
        }).join('');
    }

    // Model share table (Top 10) for selected metric
    const shareTbody = document.getElementById('ins-model-share-tbody');
    if (shareTbody) {
        const total = Number.isFinite(Number(insightsData.totals[key]))
            ? Number(insightsData.totals[key])
            : 0;
        const ranked = [...insightsData.byModel.entries()]
            .map(([m, v]) => ({ model: m, value: insightMetricScalar(v, key) }))
            .sort((a, b) => b.value - a.value)
            .slice(0, 10);
        shareTbody.innerHTML = ranked.map((it, i) => {
            const share = total > 0 ? (it.value / total) * 100 : NaN;
            return `<tr>
                <td class="mono">${i + 1}</td>
                <td class="mono">${escapeHtml(it.model)}</td>
                <td class="mono">${escapeHtml(fmtMetricValue(metric, it.value))}</td>
                <td class="mono">${Number.isFinite(share) ? share.toFixed(1) + '%' : '—'}</td>
            </tr>`;
        }).join('');
    }
}

async function loadInsights(from, to) {
    ensureInsightsBar();
    const myId = ++insightsReqId;
    try {
        const res = await fetch(`/api/daily?from=${from}&to=${to}&page=1&page_size=2000&granularity=day`);
        const json = await res.json();
        const rows = (json.data || json) || [];
        if (myId !== insightsReqId) return;

        if (!rows.length) {
            setInsightsVisible(false);
            return;
        }

        const byDate = new Map();   // date => true (active days)
        const byModel = new Map();  // model => { tokens, cost, reqs }
        const byDayModel = new Map(); // date => Map(model => { tokens, cost, reqs })
        const dayTotals = new Map();  // date => { tokens, cost, reqs }

        for (const r of rows) {
            const date = r.date;
            const model = r.model || 'unknown';
            const cost = Number(r.cost_usd || 0);
            const reqs = Number(r.request_count || 0);
            const inputSide = Number(r.input_tokens || 0) + Number(r.cache_read_tokens || 0) + Number(r.cache_creation_tokens || 0);
            const tokensTotal = inputSide + Number(r.output_tokens || 0);

            // active days: dates present in aggregation
            byDate.set(date, true);

            const acc = byModel.get(model) || { tokens: 0, cost: 0, reqs: 0 };
            acc.tokens += tokensTotal;
            acc.cost += cost;
            acc.reqs += reqs;
            byModel.set(model, acc);

            let dm = byDayModel.get(date);
            if (!dm) { dm = new Map(); byDayModel.set(date, dm); }
            const dAcc = dm.get(model) || { tokens: 0, cost: 0, reqs: 0 };
            dAcc.tokens += tokensTotal;
            dAcc.cost += cost;
            dAcc.reqs += reqs;
            dm.set(model, dAcc);

            const dt = dayTotals.get(date) || { tokens: 0, cost: 0, reqs: 0 };
            dt.tokens += tokensTotal;
            dt.cost += cost;
            dt.reqs += reqs;
            dayTotals.set(date, dt);
        }

        const activeDays = byDate.size || 1;
        let totalTokens = 0, totalCost = 0, totalReqs = 0;
        for (const v of byModel.values()) {
            totalTokens += v.tokens;
            totalCost += v.cost;
            totalReqs += v.reqs;
        }

        // Summary: show only Avg/day by current chart metric (default: tokens)
        const avgTokens = Math.round(totalTokens / activeDays);
        const avgCost = totalCost / activeDays;
        const avgReqs = Math.round(totalReqs / activeDays);

        const metric = chartMetric; // 'tokens' | 'cost' | 'requests'
        let avgText = '';
        if (metric === 'cost') avgText = `cost ${fmtUSD(avgCost)}`;
        else if (metric === 'requests') avgText = `reqs ${avgReqs}`;
        else avgText = `tokens ${fmtNum(avgTokens)}`;

        const avgEl = document.getElementById('ins-avg-summary');
        if (avgEl) avgEl.textContent = avgText;

        const daysEl = document.getElementById('ins-days');
        if (daysEl) daysEl.textContent = `· active days ${activeDays}`;

        // Store dataset for interactive details
        const models = [...byModel.keys()].sort();
        const dates = [...byDayModel.keys()].sort().reverse();
        insightsData = {
            from, to,
            activeDays,
            activeDates: [...byDate.keys()].sort(),
            totals: { tokens: totalTokens, cost: totalCost, reqs: totalReqs },
            byModel,
            byDayModel,
            dayTotals,
            models,
            dates,
        };

        // Populate selects and render details
        const metricSel = document.getElementById('insights-metric');
        const modelSel = document.getElementById('insights-model');
        if (metricSel) metricSel.value = (chartMetric === 'requests') ? 'reqs' : (chartMetric === 'cost' ? 'cost' : 'tokens');

        if (modelSel) {
            const current = modelSel.value;
            modelSel.innerHTML = '<option value=\"\">All models</option>' +
                models.map(m => `<option value=\"${escapeHtml(m)}\">${escapeHtml(m)}</option>`).join('');
            if (models.includes(current)) modelSel.value = current;
        }

        // Bind once per element lifetime.
        if (metricSel && !metricSel.dataset.bound) {
            metricSel.addEventListener('change', renderInsightsDetails);
            metricSel.dataset.bound = '1';
        }
        if (modelSel && !modelSel.dataset.bound) {
            modelSel.addEventListener('change', renderInsightsDetails);
            modelSel.dataset.bound = '1';
        }

        renderInsightsDetails();

        setInsightsVisible(true);
    } catch (e) {
        console.error('insights:', e);
        if (myId === insightsReqId) setInsightsVisible(false);
    }
}

// ── Chart: grouped bars per model, metric-switchable ────────────────────────
async function loadChart() {
    const { from, to } = rangeToFromTo(currentRange);
    try {
        const res = await fetch(`/api/daily?from=${from}&to=${to}&page=1&page_size=1000&granularity=${chartGranularity}`);
        const json = await res.json();
        const rows = (json.data || json) || [];

        const dates  = [...new Set(rows.map(r => r.date))].sort().reverse();
        const models = [...new Set(rows.map(r => r.model))].sort();

        // Build O(1) lookup map: "date|model" → row  (replaces O(n²) rows.find)
        const rowIndex = new Map();
        for (const r of rows) rowIndex.set(r.date + '|' + r.model, r);

        const c = chartColors();

        // Metric configuration
        const isCost = chartMetric === 'cost';
        const isReqs = chartMetric === 'requests';
        function getVal(r) {
            if (!r) return 0;
            if (isCost) return r.cost_usd;
            if (isReqs) return r.request_count;
            // Tokens: full bar = all input-side (uncached + cache read + cache create) + output
            const inputSide =
                (r.input_tokens || 0) + (r.cache_read_tokens || 0) + (r.cache_creation_tokens || 0);
            return inputSide + (r.output_tokens || 0);
        }
        function fmtVal(v) {
            if (isCost) return '$' + v.toFixed(4);
            return fmtNum(v);
        }

        // Update title
        const titleEl = document.getElementById('chart-title');
        if (titleEl) titleEl.textContent = isCost ? 'Cost' : isReqs ? 'Requests' : 'Token Usage';

        const series = models.map(model => ({
            name: model,
            type: 'bar',
            barMaxWidth: 44,
            itemStyle: {
                color(params) {
                    // For non-token metrics, keep solid color.
                    if (isCost || isReqs) return getModelColor(model);
                    const raw = params?.data?.raw;
                    if (!raw) return getModelColor(model);
                    const uncachedIn = Number(raw.input_tokens || 0);
                    const cacheRead = Number(raw.cache_read_tokens || 0);
                    const cacheCreate = Number(raw.cache_creation_tokens || 0);
                    const output = Number(raw.output_tokens || 0);
                    const inputSide = uncachedIn + cacheRead + cacheCreate;
                    const total = inputSide + output;
                    const base = getModelColor(model);
                    if (!(total > 0)) return base;
                    // One bar: bottom = all input-side tokens; top = output (light). Same split as before 3-band experiment.
                    if (!(output > 0)) return base;
                    const light = mixHex(base, '#ffffff', isDark ? 0.28 : 0.35);
                    if (!(inputSide > 0)) return light;
                    const exactRatio = output / total;
                    const minVis = 0.06;
                    const outputRatio = exactRatio < minVis ? minVis : exactRatio;
                    return new echarts.graphic.LinearGradient(0, 0, 0, 1, [
                        { offset: 0, color: light },
                        { offset: outputRatio, color: light },
                        { offset: outputRatio, color: base },
                        { offset: 1, color: base },
                    ]);
                },
            },
            data: dates.map(d => {
                const r = rowIndex.get(d + '|' + model);
                return r ? { value: getVal(r), raw: r } : 0;
            }),
        }));

        const visibleDates = 14;
        const zoomEnd = dates.length > visibleDates
            ? Math.round(visibleDates / dates.length * 100)
            : 100;

        // Adaptive chart height
        const chartEl = document.getElementById('main-chart');
        const dataCount = dates.length;
        chartEl.style.height = dataCount <= 3 ? '200px' : dataCount <= 7 ? '260px' : '300px';

        const option = {
            backgroundColor: c.bg,
            tooltip: {
                trigger: 'item',
                axisPointer: {
                    type: 'shadow',
                    shadowStyle: { color: isDark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.03)' },
                },
                backgroundColor: c.tooltipBg,
                borderColor: c.tooltipBorder,
                borderWidth: 1,
                borderRadius: 10,
                padding: [12, 14],
                textStyle: { color: c.tooltipText, fontSize: 12 },
                extraCssText: `box-shadow: ${c.shadow};`,
                formatter(params) {
                    return buildBarTooltip(params, c);
                },
            },
            legend: {
                data: models.map(m => ({
                    name: m,
                    icon: 'roundRect',
                    itemStyle: { color: getModelColor(m) },
                })),
                textStyle: { color: c.legendText, fontSize: 11 },
                top: 0,
                itemGap: 16,
            },
            grid: { left: 60, right: 20, top: 40, bottom: dates.length > visibleDates ? 44 : 20 },
            dataZoom: dates.length > visibleDates ? [
                {
                    type: 'inside',
                    xAxisIndex: 0,
                    start: 0,
                    end: zoomEnd,
                    zoomLock: true,
                },
                {
                    type: 'slider',
                    xAxisIndex: 0,
                    start: 0,
                    end: zoomEnd,
                    height: 16,
                    bottom: 4,
                    borderColor: c.dzBorder,
                    backgroundColor: c.dzBg,
                    fillerColor: c.dzFill,
                    handleStyle: { color: c.dzHandle, borderColor: c.dzHandle },
                    moveHandleStyle: { color: c.dzHandle },
                    textStyle: { color: c.legendText, fontSize: 10 },
                    dataBackground: {
                        lineStyle: { color: c.dzBgLine },
                        areaStyle: { color: c.dzBgArea },
                    },
                    selectedDataBackground: {
                        lineStyle: { color: c.dzSelLine },
                        areaStyle: { color: c.dzSelArea },
                    },
                },
            ] : [],
            xAxis: {
                type: 'category',
                data: dates,
                axisLabel: { color: c.axisLabel, fontSize: 11 },
                axisLine: { lineStyle: { color: c.axisLine } },
                splitLine: { show: false },
            },
            yAxis: {
                name: isCost ? 'USD' : isReqs ? 'Reqs' : 'Tokens',
                nameTextStyle: { color: c.axisLabel, fontSize: 11 },
                axisLabel: { color: c.axisLabel, fontSize: 11, formatter: v => fmtVal(v) },
                axisLine: { show: false },
                splitLine: { lineStyle: { color: c.splitLine } },
            },
            series,
        };

        if (!mainChart) {
            mainChart = echarts.init(chartEl, null, { renderer: 'canvas' });
            window.addEventListener('resize', () => mainChart.resize());
        }
        mainChart.setOption(option, true);
    } catch (e) { console.error('chart:', e); }
}

// ── Daily detail table ─────────────────────────────────────────────────────
let dailyDetailView = 'day'; // 'day' | 'hour'
let hourlyChart = null;

function isSingleDayRangeSelected() {
    const { from, to } = rangeToFromTo(currentRange);
    return !!from && from === to;
}

function updateDailyViewControls() {
    const dayBtn = document.getElementById('daily-view-day');
    const hourBtn = document.getElementById('daily-view-hour');
    if (!dayBtn || !hourBtn) return;

    const canHour = isSingleDayRangeSelected();
    hourBtn.disabled = !canHour;

    if (!canHour && dailyDetailView === 'hour') dailyDetailView = 'day';

    dayBtn.classList.toggle('active', dailyDetailView === 'day');
    hourBtn.classList.toggle('active', dailyDetailView === 'hour');

    const byDay = document.getElementById('daily-byday');
    const byHour = document.getElementById('daily-byhour');
    if (byDay) byDay.style.display = dailyDetailView === 'day' ? '' : 'none';
    if (byHour) byHour.style.display = dailyDetailView === 'hour' ? '' : 'none';
}

function setDailyDetailView(view) {
    dailyDetailView = view === 'hour' ? 'hour' : 'day';
    updateDailyViewControls();
    refreshDailyPanel();
}

function refreshDailyPanel() {
    updateDailyViewControls();
    if (dailyDetailView === 'hour' && isSingleDayRangeSelected()) {
        loadHourly();
    } else {
        loadDailyTable();
    }
}

function fmtHourRange(h) {
    const hh = String(h).padStart(2, '0');
    const hh2 = String((h + 1) % 24).padStart(2, '0');
    return `${hh}:00\u2013${hh2}:00`;
}

function metricLabel() {
    if (chartMetric === 'cost') return 'USD';
    if (chartMetric === 'requests') return 'Reqs';
    return 'Tokens';
}

function metricValueFromRow(r) {
    if (chartMetric === 'cost') return Number(r.cost_usd || 0);
    if (chartMetric === 'requests') return Number(r.request_count || 0);
    return Number(r.input_tokens || 0)
        + Number(r.cache_read_tokens || 0)
        + Number(r.cache_creation_tokens || 0)
        + Number(r.output_tokens || 0);
}

function fmtChartMetricValue(v) {
    if (chartMetric === 'cost') return '$' + Number(v || 0).toFixed(4);
    if (chartMetric === 'requests') return String(Math.round(Number(v || 0)));
    return fmtNum(Number(v || 0));
}

async function loadHourly() {
    const { from, to } = rangeToFromTo(currentRange);
    const date = from === to ? from : '';
    const tbody = document.getElementById('hourly-tbody');
    const chartEl = document.getElementById('hourly-chart');
    if (!tbody || !chartEl) return;

    if (!date) {
        tbody.innerHTML = '<tr><td colspan="9" style="text-align:center;color:var(--text-muted);padding:24px">Hourly view is available for a single day only.</td></tr>';
        return;
    }

    try {
        const res = await fetch(`/api/hourly?date=${date}`);
        const json = await res.json();
        const rows = json.data || [];

        const byHourModel = new Map(); // hour -> Map(model -> row)
        const modelsSet = new Set();
        for (let h = 0; h < 24; h++) byHourModel.set(h, new Map());

        for (const r of rows) {
            const h = Number(r.hour);
            const model = r.model || 'Unknown';
            if (!byHourModel.has(h)) continue;
            byHourModel.get(h).set(model, r);
            modelsSet.add(model);
        }

        // Render table
        const hasAny = rows.length > 0;
        if (!hasAny) {
            tbody.innerHTML = '<tr><td colspan="9" style="text-align:center;color:var(--text-muted);padding:24px">No data</td></tr>';
        } else {
            const sortedRows = [...rows].sort((a, b) => {
                const hourDiff = Number(a.hour) - Number(b.hour);
                if (hourDiff !== 0) return hourDiff;
                return metricValueFromRow(b) - metricValueFromRow(a);
            });
            tbody.innerHTML = sortedRows.map(r => {
                const totalTokens =
                    Number(r.input_tokens || 0)
                    + Number(r.cache_read_tokens || 0)
                    + Number(r.cache_creation_tokens || 0)
                    + Number(r.output_tokens || 0);
                return `<tr>
                    <td class="mono">${fmtHourRange(Number(r.hour))}</td>
                    <td><span class="badge">${escapeHtml(r.model || 'Unknown')}</span></td>
                    <td class="mono">${fmtNum(totalTokens)}</td>
                    <td class="mono">${fmtNum(Number(r.input_tokens || 0) + Number(r.cache_read_tokens || 0) + Number(r.cache_creation_tokens || 0))}</td>
                    <td class="mono">${fmtNum(r.input_tokens)}</td>
                    <td class="mono">${fmtNum(r.cache_read_tokens)}</td>
                    <td class="mono">${fmtNum(r.cache_creation_tokens)}</td>
                    <td class="mono">${fmtNum(r.output_tokens)}</td>
                    <td class="cost-val">$${Number(r.cost_usd || 0).toFixed(4)}</td>
                    <td class="mono">${r.request_count || 0}</td>
                </tr>`;
            }).join('');
        }

        // Render chart (grouped bars: one bar per hour+model)
        const x = Array.from({ length: 24 }).map((_, h) => String(h).padStart(2, '0'));
        const isCost = chartMetric === 'cost';
        const models = Array.from(modelsSet).sort((a, b) => a.localeCompare(b));

        // Reuse shared color system instead of hardcoding
        const c = chartColors();

        const option = {
            grid: { left: 44, right: 18, top: 18, bottom: 32 },
            tooltip: {
                trigger: 'item',
                axisPointer: {
                    type: 'shadow',
                    shadowStyle: { color: isDark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.03)' },
                },
                backgroundColor: c.tooltipBg,
                borderColor: c.tooltipBorder,
                borderWidth: 1,
                borderRadius: 10,
                padding: [12, 14],
                textStyle: { color: c.tooltipText, fontSize: 12 },
                extraCssText: `box-shadow: ${c.shadow};`,
                formatter(params) {
                    return buildBarTooltip(params, c);
                },
            },
            legend: {
                type: 'scroll',
                top: 0,
                textStyle: { color: c.legendText, fontSize: 11 },
            },
            xAxis: {
                type: 'category',
                data: x,
                axisLabel: { color: c.axisLabel, fontSize: 11 },
                axisLine: { lineStyle: { color: c.axisLine } },
            },
            yAxis: {
                type: 'value',
                name: metricLabel(),
                nameTextStyle: { color: c.axisLabel, fontSize: 11 },
                axisLabel: {
                    color: c.axisLabel,
                    fontSize: 11,
                    formatter: v => isCost ? ('$' + Number(v || 0).toFixed(2)) : fmtNum(v),
                },
                splitLine: { lineStyle: { color: c.splitLine } },
            },
            series: models.map(model => ({
                name: model,
                type: 'bar',
                barMaxWidth: 18,
                itemStyle: { color: getModelColor(model) },
                data: Array.from({ length: 24 }).map((_, h) => {
                    const row = byHourModel.get(h).get(model);
                    return row ? { value: metricValueFromRow(row), raw: row } : 0;
                }),
            })),
        };

        if (!hourlyChart) {
            hourlyChart = echarts.init(chartEl, null, { renderer: 'canvas' });
            window.addEventListener('resize', () => hourlyChart && hourlyChart.resize());
        }
        hourlyChart.setOption(option, true);
    } catch (e) {
        console.error('hourly:', e);
        tbody.innerHTML = '<tr><td colspan="9" style="text-align:center;color:var(--text-muted);padding:24px">Failed to load hourly data</td></tr>';
    }
}

async function loadDailyTable() {
    const { from, to } = rangeToFromTo(currentRange);
    const { page, pageSize } = paging.daily;
    try {
        const res = await fetch(`/api/daily?from=${from}&to=${to}&page=${page}&page_size=${pageSize}&granularity=${chartGranularity}`);
        const json = await res.json();
        paging.daily.total = json.total || 0;
        const rows = json.data || [];
        const tbody = document.getElementById('daily-tbody');
        if (rows.length === 0) {
            tbody.innerHTML = '<tr><td colspan="8" style="text-align:center;color:var(--text-muted);padding:32px">No data</td></tr>';
            renderPagination('daily-pagination', paging.daily, loadDailyTable);
            return;
        }
        tbody.innerHTML = rows.map(r => `<tr>
            <td class="mono">${escapeHtml(r.date)}</td>
            <td><span class="badge">${escapeHtml(r.model)}</span></td>
            <td class="mono">${r.request_count}</td>
            <td class="mono">${fmtNum(r.input_tokens)}</td>
            <td class="mono">${fmtNum(r.cache_read_tokens)}</td>
            <td class="mono">${fmtNum(r.cache_creation_tokens)}</td>
            <td class="mono">${fmtNum(r.output_tokens)}</td>
            <td class="cost-val">$${r.cost_usd.toFixed(4)}</td>
        </tr>`).join('');
        renderPagination('daily-pagination', paging.daily, loadDailyTable);
    } catch (e) { console.error('daily table:', e); }
}

// ── Sessions (fixed: now uses from/to like dashboard) ─────────────────────
async function loadSessions() {
    const { from, to } = rangeToFromTo(currentRange);
    const { page, pageSize } = paging.sessions;
    try {
        const res = await fetch(`/api/sessions?from=${from}&to=${to}&page=${page}&page_size=${pageSize}`);
        const json = await res.json();
        paging.sessions.total = json.total || 0;
        const rows = json.data || [];
        const tbody = document.getElementById('sessions-tbody');
        if (rows.length === 0) {
            tbody.innerHTML = '<tr><td colspan="7" style="text-align:center;color:var(--text-muted);padding:32px">No data</td></tr>';
            renderPagination('sessions-pagination', paging.sessions, loadSessions);
            return;
        }
        tbody.innerHTML = rows.map(s => `<tr>
            <td><span class="badge">${escapeHtml(truncate(s.session_id, 16))}</span></td>
            <td class="mono">${formatUserCell(s.user_id)}</td>
            <td class="mono">${new Date(s.start_time).toLocaleString()}</td>
            <td class="mono">${s.request_count}</td>
            <td class="mono">${fmtNum(s.input_tokens)}</td>
            <td class="mono">${fmtNum(s.output_tokens)}</td>
            <td class="cost-val">$${s.cost_usd.toFixed(4)}</td>
        </tr>`).join('');
        renderPagination('sessions-pagination', paging.sessions, loadSessions);
    } catch (e) { console.error('sessions:', e); }
}

// ── Request log (fixed: now uses from/to for time range) ───────────────────
async function loadRequests() {
    const { from, to } = rangeToFromTo(currentRange);
    const { page, pageSize } = paging.requests;
    try {
        const model = document.getElementById('model-filter').value;
        const res = await fetch(`/api/requests?from=${from}&to=${to}&page=${page}&page_size=${pageSize}&model=${encodeURIComponent(model)}`);
        const json = await res.json();
        paging.requests.total = json.total || 0;
        const data = json.data || [];

        const tbody = document.getElementById('request-tbody');
        if (data.length === 0) {
            tbody.innerHTML = '<tr><td colspan="9" style="text-align:center;color:var(--text-muted);padding:32px">No data</td></tr>';
            renderPagination('requests-pagination', paging.requests, loadRequests);
            return;
        }
        tbody.innerHTML = data.map(r => `<tr>
            <td class="mono">${new Date(r.timestamp).toLocaleString()}</td>
            <td><span class="badge">${escapeHtml(r.model)}</span></td>
            <td class="mono">${formatUserCell(r.user_id)}</td>
            <td class="mono">${fmtNum(r.input_tokens)}</td>
            <td class="mono">${fmtNum(r.output_tokens)}</td>
            <td class="mono">${fmtNum(r.cache_read_tokens)}</td>
            <td class="mono">${fmtNum(r.cache_creation_tokens)}</td>
            <td class="cost-val">$${r.cost_usd.toFixed(4)}</td>
            <td class="mono">${r.duration_ms ? r.duration_ms + 'ms' : '\u2014'}</td>
        </tr>`).join('');
        renderPagination('requests-pagination', paging.requests, loadRequests);
    } catch (e) { console.error('requests:', e); }
}

// ── Helpers ────────────────────────────────────────────────────────────────

async function loadModelFilter() {
    try {
        const res = await fetch('/api/models');
        const models = await res.json();
        const select = document.getElementById('model-filter');
        const current = select.value;
        select.innerHTML = '<option value="">All Models</option>';
        models.forEach(m => {
            const opt = document.createElement('option');
            opt.value = m; opt.textContent = m;
            select.appendChild(opt);
        });
        if (models.includes(current)) select.value = current;
    } catch(e) { console.error('models:', e); }
}

function renderPagination(containerId, state, reloadFn) {
    const el = document.getElementById(containerId);
    if (!el) return;
    const totalPages = Math.max(1, Math.ceil(state.total / state.pageSize));
    if (state.total <= state.pageSize) { el.innerHTML = ''; return; }

    el.innerHTML = '';

    const prev = document.createElement('button');
    prev.textContent = '\u2039 Prev';
    prev.disabled = state.page <= 1;
    prev.onclick = () => { state.page--; reloadFn(); };

    const info = document.createElement('span');
    info.textContent = `${state.page} / ${totalPages}  (${state.total} rows)`;

    const next = document.createElement('button');
    next.textContent = 'Next \u203a';
    next.disabled = state.page >= totalPages;
    next.onclick = () => { state.page++; reloadFn(); };

    el.append(prev, info, next);
}

/**
 * Shared bar-chart tooltip formatter for daily and hourly charts.
 * Accepts the echarts params object and a color-theme object (from chartColors()).
 */
function buildBarTooltip(params, c) {
    const raw = params.data?.raw;
    if (!raw) return '';
    const uncachedIn = Number(raw.input_tokens || 0);
    const cacheRead = Number(raw.cache_read_tokens || 0);
    const cacheCreate = Number(raw.cache_creation_tokens || 0);
    const totalOutput = Number(raw.output_tokens || 0);
    const inputSide = uncachedIn + cacheRead + cacheCreate;
    const total = inputSide + totalOutput;
    const sub = 'padding:2px 0 2px 16px;font-size:11px';
    // Header: either date or hour range
    const header = raw.hour != null
        ? fmtHourRange(Number(raw.hour))
        : escapeHtml(params.name);
    // Model color: use params.color for daily, compute for hourly
    const modelColor = typeof params.color === 'string' ? params.color : getModelColor(raw.model || 'Unknown');
    const modelName = raw.model || params.seriesName || 'Unknown';
    return `<div style="margin-bottom:6px;font-weight:600;color:${c.tooltipText}">${header}</div>` +
        `<div style="color:${modelColor};font-weight:600;margin-bottom:8px">${params.marker} ${escapeHtml(modelName)}</div>` +
        `<table style="width:100%;font-size:12px;border-collapse:collapse">` +
        `<tr><td style="color:${c.mutedText};padding:2px 0">Total</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0;font-weight:600">${fmtNum(total)}</td></tr>` +
        `<tr><td colspan="2" style="height:4px"></td></tr>` +
        `<tr><td style="color:${c.tooltipText};padding:2px 0;font-weight:600">Input</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0;font-weight:600">${fmtNum(inputSide)}</td></tr>` +
        `<tr><td style="color:${c.mutedText};${sub}">Uncached</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${fmtNum(uncachedIn)}</td></tr>` +
        `<tr><td style="color:${c.mutedText};${sub}">Cache Read</td><td style="font-family:var(--font-mono);text-align:right;color:var(--green);padding:2px 0">${fmtNum(cacheRead)}</td></tr>` +
        `<tr><td style="color:${c.mutedText};${sub}">Cache Create</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${fmtNum(cacheCreate)}</td></tr>` +
        `<tr><td colspan="2" style="height:4px"></td></tr>` +
        `<tr><td style="color:${c.mutedText};padding:2px 0">Output</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${fmtNum(totalOutput)}</td></tr>` +
        `<tr><td style="color:${c.mutedText};padding:2px 0">Requests</td><td style="font-family:var(--font-mono);text-align:right;padding:2px 0">${raw.request_count}</td></tr>` +
        `<tr style="border-top:1px solid ${c.axisLine}"><td style="color:${c.mutedText};padding:6px 0 0;font-weight:500">Cost</td><td style="font-family:var(--font-mono);font-weight:600;color:var(--orange);text-align:right;padding:6px 0 0">$${Number(raw.cost_usd || 0).toFixed(4)}</td></tr>` +
        `</table>`;
}

function fmtNum(n) {
    if (n == null || isNaN(n)) return '0';
    if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
    if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
    return String(n);
}
function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
function truncate(s, n) {
    return s && s.length > n ? s.slice(0, n) + '\u2026' : s;
}
/** Short user id hash for table cells; full value in title attribute */
function formatUserCell(userId) {
    if (!userId) return '\u2014';
    const short = userId.length > 14 ? userId.slice(0, 10) + '\u2026' : userId;
    return `<span class="badge" title="${escapeHtml(userId)}">${escapeHtml(short)}</span>`;
}
// SYNC: Mirrors Go handler.go rangeToFromTo(). Keep both in sync:
//   week  = today − 6 days  (inclusive → 7 days total)
//   month = today − 29 days (inclusive → 30 days total)
//   all   = 1970-01-01 → today
function rangeToFromTo(range) {
    if (range === 'custom' || range === 'single-day') return { from: customFrom, to: customTo };
    const now = new Date();
    const fmt = d => toYMD(d);
    const today = fmt(now);
    switch (range) {
        case 'week':  { const f = new Date(now); f.setDate(f.getDate() - 6);  return { from: fmt(f), to: today }; }
        case 'month': { const f = new Date(now); f.setDate(f.getDate() - 29); return { from: fmt(f), to: today }; }
        case 'all':   return { from: '1970-01-01', to: today };
        default:      return { from: today, to: today };
    }
}
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
        try { customRangeFlatpickr?.set?.('maxDate', 'today'); } catch {}

        // Refresh dropdown labels/active highlights.
        try { buildDayDropdown(); } catch {}

        // Auto-refresh only when the current view is "today" (or single-day pointing at today).
        const isTodayView =
            currentRange === 'today' ||
            (currentRange === 'single-day' && !selectedDayDate);
        if (isTodayView) {
            resetPages();
            loadAll();
        }
    }, 45 * 1000);
}

// ── Boot ───────────────────────────────────────────────────────────────────
initCustomRangePicker();
applyStateFromURL();
loadModelFilter();
loadAll();
connectSSE();
startDayRolloverWatcher();
window.addEventListener('popstate', () => {
    applyStateFromURL();
    loadAll();
});

// Daily subtabs
document.getElementById('daily-view-day')?.addEventListener('click', () => setDailyDetailView('day'));
document.getElementById('daily-view-hour')?.addEventListener('click', () => setDailyDetailView('hour'));

// Request filter — replaces inline onchange/onclick removed from index.html
document.getElementById('model-filter')?.addEventListener('change', () => loadRequests());
document.getElementById('requests-refresh-btn')?.addEventListener('click', () => loadRequests());
