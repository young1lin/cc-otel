import { state } from './state.js';
import { fmtUSD, fmtPct, fmtTokens, escapeHtml, rangeToFromTo } from './utils.js';
import { chartColors, getModelFamily, getPieSliceColor } from './theme.js';
import { loadDailyData } from './api.js';
import { cacheHitParts, tokenParts } from './token-math.js';

let openPopoverFn = () => {};
let closePopoverFn = () => {};

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

function closeCostModal() {
    closePopoverFn(costModal);
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
        try { state.breakdownChart?.resize?.(); } catch {}
    }
}

function ensureBreakdownChart() {
    if (!breakdownChartEl || typeof echarts === 'undefined') return null;
    if (!state.breakdownChart) {
        state.breakdownChart = echarts.init(breakdownChartEl, null, { renderer: 'canvas' });
        state.breakdownChart.on('mouseover', (params) => {
            if (!params || params.componentType !== 'series') return;
            const it = params.data;
            if (it && breakdownSelectedEl) {
                breakdownSelectedEl.textContent = `${it.name} · ${it.valueText} · ${it.shareText}`;
            }
        });
        state.breakdownChart.on('mouseout', () => {
            if (!breakdownSelectedEl) return;
            if (!state.breakdownLast) { breakdownSelectedEl.textContent = ''; return; }
            breakdownSelectedEl.textContent = state.breakdownLast.selectedText || '';
        });
        state.breakdownChart.on('click', (params) => {
            if (!params || params.componentType !== 'series') return;
            const it = params.data;
            if (it && breakdownSelectedEl) {
                state.breakdownLast = state.breakdownLast || {};
                state.breakdownLast.selectedText = `${it.name} · ${it.valueText} · ${it.shareText}`;
                breakdownSelectedEl.textContent = state.breakdownLast.selectedText;
            }
        });
    }
    return state.breakdownChart;
}

/** Pie: group slices by model family (adjacent), then value desc within family; order families by total share. */
function orderPieItemsByFamily(items) {
    const byFam = new Map();
    for (const it of items) {
        const f = getModelFamily(it.model);
        if (!byFam.has(f)) byFam.set(f, []);
        byFam.get(f).push(it);
    }
    for (const arr of byFam.values()) {
        arr.sort((a, b) => b.v - a.v);
    }
    const famKeys = [...byFam.keys()];
    famKeys.sort((a, b) => {
        const sa = byFam.get(a).reduce((s, x) => s + x.v, 0);
        const sb = byFam.get(b).reduce((s, x) => s + x.v, 0);
        return sb - sa;
    });
    return famKeys.flatMap((f) => byFam.get(f));
}

function renderBreakdownPie(kind, cfg, items, total, from, to) {
    const chart = ensureBreakdownChart();
    if (!chart) return;

    const c = chartColors();
    const ordered = orderPieItemsByFamily(items);
    const famIndex = new Map();
    const data = ordered.map((it) => {
        const f = getModelFamily(it.model);
        const idx = famIndex.get(f) || 0;
        famIndex.set(f, idx + 1);
        const share = (kind !== 'cache_hit' && total > 0) ? (it.v / total) * 100 : NaN;
        const valueText = cfg.fmt(it.v);
        const shareText = Number.isFinite(share) ? `${share.toFixed(1)}%` : '—';
        return {
            name: it.model,
            value: it.v,
            valueText,
            shareText,
            itemStyle: { color: getPieSliceColor(it.model, idx) },
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
                    const p = Number(params?.percent);
                    if (!Number.isFinite(p) || p < 3) return '';
                    const name = String(params?.name || '').trim();
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
            // Keep our family-grouped `data` order; ECharts may sort by value otherwise.
            sort: 'none',
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
    try {
        chart.resize();
    } catch {
        /* empty */
    }
}

async function openBreakdownModal(kind, anchorEl) {
    openPopoverFn(costModal, anchorEl);

    const titleEl = document.getElementById('cost-title');
    const metaEl  = document.getElementById('cost-meta');
    const tbody  = document.getElementById('cost-tbody');
    metaEl.textContent = 'loading…';
    tbody.innerHTML = '';
    if (breakdownSelectedEl) breakdownSelectedEl.textContent = '';

    const { from, to } = rangeToFromTo(state.currentRange);
    try {
        const json = await loadDailyData({
            from, to, page: 1, pageSize: 2000,
            granularity: state.chartGranularity || 'day',
        });
        const raw = json.data != null ? json.data : json;
        const rows = Array.isArray(raw) ? raw : [];

        const byModel = new Map();
        const cacheByModel = new Map();
        let total = 0;
        for (const r of rows) {
            const model = r.model || 'unknown';
            let val = 0;
            if (kind === 'cost') val = Number(r.cost_usd || 0);
            else if (kind === 'input') {
                val = tokenParts(r).inputSide;
            }
            else if (kind === 'output') val = Number(r.output_tokens || 0);
            else if (kind === 'requests') val = Number(r.request_count || 0);
            else if (kind === 'cache_hit') {
                const parts = cacheHitParts(r);
                const acc = cacheByModel.get(model) || { numerator: 0, denominator: 0 };
                acc.numerator += parts.numerator;
                acc.denominator += parts.denominator;
                cacheByModel.set(model, acc);
                continue;
            }
            total += val;
            byModel.set(model, (byModel.get(model) || 0) + val);
        }

        if (kind === 'cache_hit') {
            for (const [model, acc] of cacheByModel.entries()) {
                byModel.set(model, acc.denominator > 0 ? acc.numerator / acc.denominator : 0);
            }
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
            const share = (kind !== 'cache_hit' && total > 0) ? (it.v / total) * 100 : NaN;
            return `<tr>
                <td class="mono">${escapeHtml(it.model)}</td>
                <td class="mono">${escapeHtml(cfg.fmt(it.v))}</td>
                <td class="mono">${Number.isFinite(share) ? share.toFixed(1) + '%' : '—'}</td>
            </tr>`;
        }).join('');

        state.breakdownLast = { kind, cfg, items, total, from, to, selectedText: '' };

        const view = getBreakdownView(kind);
        applyBreakdownView(kind, view);
        if (view === 'pie' && kind !== 'cache_hit') {
            try {
                renderBreakdownPie(kind, cfg, items, total, from, to);
                if (breakdownSelectedEl) {
                    const totalLine = kind === 'cost' ? fmtUSD(total) : fmtTokens(total);
                    state.breakdownLast.selectedText = `Total · ${totalLine} · 100%`;
                    breakdownSelectedEl.textContent = state.breakdownLast.selectedText;
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

export function initBreakdownModal(opts = {}) {
    openPopoverFn  = opts.openPopover  || (() => {});
    closePopoverFn = opts.closePopover || (() => {});

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
        if (costModal?.style.display !== 'none') openPopoverFn(costModal, document.activeElement);
    });

    breakdownViewPieBtn?.addEventListener('click', () => {
        const k = state.breakdownLast?.kind || 'cost';
        setBreakdownView(k, 'pie');
        if (state.breakdownLast && state.breakdownLast.kind !== 'cache_hit') {
            const bl = state.breakdownLast;
            renderBreakdownPie(bl.kind, bl.cfg, bl.items, bl.total, bl.from, bl.to);
        }
    });
    breakdownViewListBtn?.addEventListener('click', () => {
        const k = state.breakdownLast?.kind || 'cost';
        setBreakdownView(k, 'list');
    });
}
