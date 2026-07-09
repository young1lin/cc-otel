import { state } from './state.js';

// Stable model-family palettes: same family => same color system across all charts.
const FAMILY_BASE_DARK = {
    claude: '#ff9f0a',
    glm: '#5e5ce6',
    step: '#30d158',
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
    gpt: '#007aff',
    qwen: '#af52de',
    deepseek: '#ff2d55',
    kimi: '#ffcc00',
    other: '#8e8e93',
};

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

export function mixHex(a, b, t) {
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

export function hashModelName(name) {
    const s = String(name || '').trim().toLowerCase();
    let h = 0;
    for (let i = 0; i < s.length; i++) {
        h = ((h * 31) + s.charCodeAt(i)) >>> 0;
    }
    return h >>> 0;
}

export function getModelFamily(name) {
    const s = String(name || '').trim().toLowerCase();
    if (!s) return 'other';
    if (s.startsWith('claude')) return 'claude';
    if (s.startsWith('glm')) return 'glm';
    if (s.startsWith('step')) return 'step';
    if (s.startsWith('gpt') || s.startsWith('o1') || s.startsWith('o3') || s.startsWith('o4')) return 'gpt';
    if (s.startsWith('qwen')) return 'qwen';
    if (s.startsWith('deepseek')) return 'deepseek';
    if (s.startsWith('kimi')) return 'kimi';
    return 'other';
}

export function getModelColor(name) {
    const model = String(name || '').trim().toLowerCase();
    const family = getModelFamily(name);
    const base = (state.isDark ? FAMILY_BASE_DARK : FAMILY_BASE_LIGHT)[family]
        || (state.isDark ? FAMILY_BASE_DARK.other : FAMILY_BASE_LIGHT.other);

    // Claude family: anchor each tier at a distinct hue inside the warm range
    // (red-orange → orange → amber-yellow) so haiku/sonnet/opus stay
    // distinguishable even at 4-px bar widths or in a 11-line chart.
    if (family === 'claude') {
        if (model.includes('opus')) {
            return state.isDark ? '#ff9f0a' : '#ff8a00'; // bright orange (anchor)
        }
        if (model.includes('sonnet')) {
            return state.isDark ? '#ff6a3d' : '#e85a1a'; // red-orange
        }
        if (model.includes('haiku')) {
            return state.isDark ? '#f5b73c' : '#cf9000'; // amber/goldenrod
        }
        const h = hashModelName(name);
        const variant = h % 3;
        if (state.isDark) {
            const claudeShades = ['#ffb066', '#d97a2b', '#ffcc73'];
            return claudeShades[variant];
        }
        const claudeShades = ['#e88a33', '#bd6a14', '#e8a44c'];
        return claudeShades[variant];
    }
    if (family === 'glm') {
        // More separated shades so multiple GLM models stay readable on the same chart.
        const h = hashModelName(name);
        const variant = h % 5;
        if (state.isDark) {
            const glmShades = ['#4f7cff', '#22a6f2', '#6b5cff', '#00b8d9', '#7c4dff'];
            return glmShades[variant];
        }
        const glmShades = ['#3f6fff', '#168fe0', '#5b50e6', '#00a0c7', '#6d45e0'];
        return glmShades[variant];
    }
    if (family === 'step') {
        const h = hashModelName(name);
        const variant = h % 4;
        if (state.isDark) {
            const stepShades = ['#30d158', '#1fbf75', '#49e37f', '#00c27a'];
            return stepShades[variant];
        }
        const stepShades = ['#34c759', '#1fb36d', '#42d47a', '#00b36b'];
        return stepShades[variant];
    }

    // Same family, different model => slight shade variation within the family color system.
    const h = hashModelName(name);
    const variant = h % 5;
    if (variant === 0) return base;

    if (state.isDark) {
        const steps = [0.14, 0.26, 0.10, 0.34];
        return mixHex(base, '#ffffff', steps[variant - 1]);
    }
    const steps = [0.10, 0.18, 0.26, 0.08];
    return mixHex(base, '#000000', steps[variant - 1]);
}

/**
 * Per-slice color for **pie** charts: same model family = same hue; adjacent slices
 * in the same family (after grouping) use palette slots 0,1,2… (not a hash) so
 * they read as a band of related shades. Does not use opus special-casing, so
 * two Opus versions never share an identical color.
 */
export function getPieSliceColor(modelName, indexWithinFamily) {
    const family = getModelFamily(modelName);
    const i = Math.max(0, Math.floor(Number(indexWithinFamily) || 0));
    if (family === 'claude') {
        const n = i % 4;
        if (state.isDark) {
            const claudeShades = ['#ff9f0a', '#ffb340', '#ffcc73', '#e8902a'];
            return claudeShades[n];
        }
        const claudeShades = ['#ff8a00', '#ffad33', '#ffc266', '#e88600'];
        return claudeShades[n];
    }
    if (family === 'glm') {
        const n = i % 5;
        if (state.isDark) {
            const glmShades = ['#5e5ce6', '#4f7cff', '#6b5cff', '#22a6f2', '#7c4dff'];
            return glmShades[n];
        }
        const glmShades = ['#5856d6', '#3f6fff', '#5b50e6', '#168fe0', '#6d45e0'];
        return glmShades[n];
    }
    if (family === 'step') {
        const n = i % 4;
        if (state.isDark) {
            const stepShades = ['#30d158', '#1fbf75', '#49e37f', '#00c27a'];
            return stepShades[n];
        }
        const stepShades = ['#34c759', '#1fb36d', '#42d47a', '#00b36b'];
        return stepShades[n];
    }
    const base = (state.isDark ? FAMILY_BASE_DARK : FAMILY_BASE_LIGHT)[family]
        || (state.isDark ? FAMILY_BASE_DARK.other : FAMILY_BASE_LIGHT.other);
    const n = (i % 4) + 1;
    if (state.isDark) {
        const steps = [0.14, 0.26, 0.10, 0.34];
        return mixHex(base, '#ffffff', steps[n - 1]);
    }
    const steps = [0.10, 0.18, 0.26, 0.08];
    return mixHex(base, '#000000', steps[n - 1]);
}

export function chartColors() {
    return state.isDark ? {
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

let onThemeChange = null;

export function applyTheme(dark) {
    state.isDark = dark;
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
    document.getElementById('theme-icon-sun').style.display  = dark ? 'none' : '';
    document.getElementById('theme-icon-moon').style.display = dark ? '' : 'none';
    localStorage.setItem('cc-otel-theme', dark ? 'dark' : 'light');
    if (typeof onThemeChange === 'function') onThemeChange();
}

export function initTheme(opts = {}) {
    onThemeChange = opts.onThemeChange || null;

    const saved = localStorage.getItem('cc-otel-theme');
    const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
    applyTheme(saved ? saved === 'dark' : prefersDark);

    document.getElementById('theme-toggle').addEventListener('click', () => {
        applyTheme(!state.isDark);
    });
}
