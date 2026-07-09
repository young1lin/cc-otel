// Pricing Table admin panel: CRUD over /api/pricing + suggest + recompute.
// Prices are shown/edited in USD/Mtok; the API converts to/from USD/token.
// Recompute status lives server-side; this UI only reads it on open and never
// triggers a job on page load/refresh (the ↻ button is the only POST).
// Claude rows are read-only — Claude is priced upstream, never by this table.

import { escapeHtml, fmtTime, perMtokToToken, tokenToPerMtok, quantBadge } from './utils.js';
import {
    loadPricingPage, upsertPricing, deletePricing, suggestPricing,
    startRecompute, recomputeStatus,
} from './api.js';
import { renderPagination } from './pagination.js';

// Converters are re-exported so callers can reach them through this module if
// needed; the canonical home is utils.js (see the pricing-units test).
export { perMtokToToken, tokenToPerMtok };

// SF-Symbol-style line icons (stroke=currentColor, no emoji). Clicks land on the
// inner <svg>/<path>, so the row delegation uses closest('[data-act]').
const IC = {
    pencil:   '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 20.5 L4.8 16.8 L15.5 6.1 L18.9 9.5 L8.2 20.2 L4.5 21 Z"/><path d="M13.5 8 L17 11.5"/></svg>',
    sparkles: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3 L13.4 9.2 L19.5 10.6 L13.4 12 L12 18.2 L10.6 12 L4.5 10.6 L10.6 9.2 Z"/><path d="M18.5 14.5 L19 16.5 L21 17 L19 17.5 L18.5 19.5 L18 17.5 L16 17 L18 16.5 Z"/></svg>',
    external: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M9.5 6 H7 a2 2 0 0 0 -2 2 v9 a2 2 0 0 0 2 2 h9 a2 2 0 0 0 2 -2 v-2.5"/><path d="M14 4.5 H20 V10.5"/><path d="M20 4.5 L12.5 12"/></svg>',
    trash:    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4.5 7 H19.5"/><path d="M9 7 V5 a1 1 0 0 1 1 -1 h4 a1 1 0 0 1 1 1 V7"/><path d="M6.5 7 L7.3 19 a1.5 1.5 0 0 0 1.5 1.4 h6.4 a1.5 1.5 0 0 0 1.5 -1.4 L17.5 7"/><path d="M10.5 11 V16.5"/><path d="M13.5 11 V16.5"/></svg>',
    lock:     '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M7.5 11 V8.5 a4.5 4.5 0 0 1 9 0 V11"/><rect x="5" y="11" width="14" height="9" rx="2.5"/></svg>',
};

let recomputeTimer = null;

export function initPanelPricing({ openPopover, closePopover }) {
    const btn = document.getElementById('pricing-btn');
    const backdrop = document.getElementById('pricing-modal');
    const closeBtn = document.getElementById('pricing-close');
    if (!btn || !backdrop) return;

    const tbody = backdrop.querySelector('#pricing-tbody');
    const search = backdrop.querySelector('#pricing-search');
    const sourceBar = backdrop.querySelector('#pricing-source');
    const addBtn = backdrop.querySelector('#pricing-add');
    const recomputeBtn = backdrop.querySelector('#pricing-recompute');
    const recomputeLbl = backdrop.querySelector('#pricing-recompute-lbl');
    const pageState = { page: 1, pageSize: 50, total: 0 };
    let searchTimer = null;

    const activeSource = () => sourceBar?.querySelector('button.active')?.dataset.src || 'all';

    const open = () => {
        openPopover(backdrop, btn);
        load(1);
        syncRecompute(); // GET only — never starts a job
    };

    btn.addEventListener('click', open);
    closeBtn?.addEventListener('click', () => closePopover(backdrop));
    backdrop.addEventListener('click', (e) => { if (e.target === backdrop) closePopover(backdrop); });

    search?.addEventListener('input', () => {
        clearTimeout(searchTimer);
        searchTimer = setTimeout(() => load(1), 250);
    });
    sourceBar?.addEventListener('click', (ev) => {
        const b = ev.target.closest('button[data-src]');
        if (!b) return;
        sourceBar.querySelectorAll('button').forEach(x => x.classList.remove('active'));
        b.classList.add('active');
        load(1);
    });

    addBtn?.addEventListener('click', () => {
        tbody.prepend(renderRow({ model: '', input: '', output: '', cache_read: '', cache_create: '', aliases: '', source: 'manual', isNew: true }, true));
    });

    recomputeBtn?.addEventListener('click', async () => {
        recomputeBtn.disabled = true;
        try {
            await startRecompute();
            pollRecompute();
        } catch (e) {
            toast(e.message);
            recomputeBtn.disabled = false;
        }
    });

    async function load(page) {
        pageState.page = page;
        try {
            const src = activeSource();
            const { entries, total } = await loadPricingPage({
                q: search?.value || '', source: src === 'all' ? '' : src,
                page, pageSize: pageState.pageSize,
            });
            pageState.total = total;
            tbody.innerHTML = '';
            if (!entries.length) {
                tbody.innerHTML = `<tr><td colspan="8" class="pf-upd" style="text-align:center">No models</td></tr>`;
            } else {
                for (const e of entries) tbody.append(renderRow(e, false));
            }
            const countEl = backdrop.querySelector('#pricing-count');
            if (countEl) countEl.textContent = `${total}`;
            renderPagination('pricing-pagination', pageState, () => load(pageState.page));
        } catch (e) {
            toast('Load failed: ' + e.message);
        }
    }

    function renderRow(e, editable) {
        const tr = document.createElement('tr');
        tr.dataset.model = e.model;
        if (e.is_local) tr.classList.add('pf-local');
        tr._variants = Array.isArray(e.variants) && e.variants.length ? e.variants : null;
        tr.append(...cells(e, editable));
        return tr;
    }

    function cells(e, editable) {
        const isClaude = String(e.model || '').toLowerCase().startsWith('claude-');
        const fld = (name, val, wide = false) =>
            `<input class="pf-input${wide ? ' wide' : ''}" data-k="${name}" value="${escapeHtml(String(val ?? ''))}"${name === 'aliases' ? ' placeholder="Aliases (comma-separated)"' : ''}${name === 'model' && !e.isNew ? ' readonly' : ''}>`;
        const numCell = (v) => el('td', (v == null || v === 0)
            ? '<span class="pf-num dim">—</span>'
            : `<span class="pf-num">${num(v)}</span>`);
        const srcBadge = () => {
            if (isClaude) {
                // Read-only; price (when present) is a reference from the
                // OpenRouter catalog, so badge it accordingly.
                return e.input > 0
                    ? `<span class="pf-badge openrouter">OpenRouter</span>`
                    : `<span class="pf-badge upstream">${IC.lock} Upstream</span>`;
            }
            let cls = 'manual', label = escapeHtml(e.source || 'manual');
            if (e.source === 'user') { cls = 'user'; label = 'YAML'; }
            else if (e.source === 'seed') { cls = 'seed'; label = 'seed'; }
            else if (e.source === 'openrouter') { cls = 'openrouter'; label = 'OpenRouter'; }
            return `<span class="pf-badge ${cls}">${label}</span>`;
        };

        if (isClaude) {
            // Anthropic prompt caching has two write tiers — 5m = 1.25x and
            // 1h = 2x the input price. Show both in the Cache Create cell.
            const cc = (m) => num(e.input * m);
            return [
                el('td', `<span class="pf-model">${escapeHtml(e.model)}</span>`),
                numCell(e.input), numCell(e.output), numCell(e.cache_read),
                el('td', `<div class="pf-cc"><span class="pf-cc-row"><span class="pf-cc-k">5m</span><span class="pf-num">${cc(1.25)}</span></span><span class="pf-cc-row"><span class="pf-cc-k">1h</span><span class="pf-num">${cc(2)}</span></span></div>`),
                el('td', srcBadge()),
                el('td', '<span class="pf-upd">—</span>'),
                el('td', `<span class="pf-lock">${IC.lock} Read-only</span>`),
            ];
        }
        if (!editable) {
            return [
                el('td', `<span class="pf-model">${escapeHtml(e.model)}</span>${e.overridden_by_yaml ? '<span style="margin-left:5px;color:var(--orange);font-size:10px" title="Shadowed by YAML pricing:">⚙</span>' : ''}${variantsBtn(e)}`),
                numCell(e.input), numCell(e.output), numCell(e.cache_read), numCell(e.cache_create),
                el('td', srcBadge()),
                el('td', e.updated_at ? `<span class="pf-upd">${fmtTime(e.updated_at)}</span>` : '<span class="pf-upd">—</span>'),
                el('td', `<div class="pf-act"><button title="Edit" data-act="edit">${IC.pencil}</button><button title="Fill from OpenRouter" data-act="suggest">${IC.sparkles}</button><button title="Open in OpenRouter" data-act="or">${IC.external}</button><button class="del" title="Delete" data-act="del">${IC.trash}</button></div>`),
            ];
        }
        return [
            // Aliases are not user-editable in the manual form (the auto-matcher
            // already covers provider/name, date suffixes, basename). Keep a
            // hidden input carrying the existing value so save() round-trips it
            // unchanged instead of wiping a seed entry's aliases.
            el('td', fld('model', e.model) + `<input type="hidden" data-k="aliases" value="${escapeHtml((e.aliases || []).join(', '))}">`),
            el('td', fld('input', e.input)), el('td', fld('output', e.output)),
            el('td', fld('cache_read', e.cache_read)), el('td', fld('cache_create', e.cache_create)),
            el('td', `<span class="pf-badge pf-edit-src manual" title="Manual = hand-typed · OpenRouter = accepted from the picker">Manual</span>`),
            el('td', '<span class="pf-stamp">Editing</span>'),
            el('td', `<button class="pf-ghost" title="Fill from OpenRouter" data-act="suggest">${IC.sparkles}</button> <button class="pf-save" data-act="save">Save</button> <button class="pf-cancel" data-act="cancel">✕</button>`),
        ];
    }

    // Event delegation for all row actions. closest('[data-act]') so clicks on the
    // inner <svg>/<path> still resolve to the owning action button.
    tbody?.addEventListener('click', async (ev) => {
        const btn = ev.target?.closest('[data-act]');
        const act = btn?.dataset?.act;
        let tr = ev.target?.closest('tr');
        if (!act || !tr) return;
        // Provider-picker rows live in a sibling <tr class="pf-pickrow">; the
        // model row (with _providers + the input cells) is its previous sibling.
        if (tr.classList.contains('pf-pickrow')) tr = tr.previousElementSibling;
        const model = tr.dataset.model;
        if (act === 'edit') { renderInto(tr, model, true); }
        else if (act === 'toggle') { toggleVariants(tr); }
        else if (act === 'pick') { openProviderPicker(tr); }
        else if (act === 'pickrow') { pickProvider(tr, Number(btn.dataset.pidx)); }
        else if (act === 'cancel') { model ? renderInto(tr, model, false) : tr.remove(); }
        else if (act === 'save') { await save(tr); }
        else if (act === 'del') { await del(model, tr); }
        else if (act === 'suggest') { await suggest(tr); }
        else if (act === 'or') {
            const model0 = tr?.dataset?.model || tr?.querySelector('[data-k="model"]')?.value || '';
            window.open(orHref(model0, tr?.dataset?.orId || ''), '_blank', 'noopener');
        }
    });

    async function renderInto(tr, model, editable) {
        // re-read current values server-side to avoid clobbering
        try {
            const { entries } = await loadPricingPage({ q: model, pageSize: 50 });
            const e = entries.find(x => x.model === model) || { model };
            tr._variants = Array.isArray(e.variants) && e.variants.length ? e.variants : null;
            tr._providers = null;
            tr._providersTotal = 0;
            // collapse any expanded variant / provider-picker rows before re-rendering
            tr.classList.remove('pf-open');
            let sib = tr.nextElementSibling;
            while (sib && (sib.classList.contains('pf-variant') || sib.classList.contains('pf-pickrow'))) {
                const nx = sib.nextElementSibling; sib.remove(); sib = nx;
            }
            tr.replaceChildren(...cells(e, editable));
            if (editable) wireEditSource(tr, e.source);
        } catch { /* keep */ }
    }

    // wireEditSource tracks whether the row's prices are OpenRouter-sourced:
    // the provider picker / 💡 mark it openrouter; any hand-edit of a price
    // field flips it back to manual. The edit-mode badge reflects the state.
    function wireEditSource(tr, savedSource) {
        tr._orSourced = savedSource === 'openrouter';
        tr.querySelectorAll('[data-k="input"],[data-k="output"],[data-k="cache_read"],[data-k="cache_create"]')
            .forEach(inp => inp.addEventListener('input', () => { tr._orSourced = false; paintEditSource(tr); }));
        paintEditSource(tr);
    }
    function paintEditSource(tr) {
        const badge = tr.querySelector('.pf-edit-src');
        if (!badge) return;
        const or = !!tr._orSourced;
        badge.className = 'pf-badge pf-edit-src ' + (or ? 'openrouter' : 'manual');
        badge.textContent = or ? 'OpenRouter' : 'Manual';
    }

    async function save(tr) {
        const get = (k) => tr.querySelector(`[data-k="${k}"]`)?.value;
        const entry = {
            model: get('model'), aliases: (get('aliases') || '').split(',').map(s => s.trim()).filter(Boolean),
            input: Number(get('input')), output: Number(get('output')),
            cache_read: Number(get('cache_read') || 0), cache_create: Number(get('cache_create') || 0),
            source: tr._orSourced ? 'openrouter' : 'manual',
        };
        if (!entry.model || !(entry.input > 0) || !(entry.output > 0)) {
            toast('Model, Input, and Output are required and > 0'); return;
        }
        try { await upsertPricing(entry); toast(`Saved ${entry.model}`); await load(pageState.page); }
        catch (e) { toast('Save failed: ' + e.message); }
    }

    async function del(model, tr) {
        if (!confirm(`Delete the price for ${model}?`)) return;
        try { await deletePricing(model); tr.remove(); toast(`Deleted ${model}`); }
        catch (e) { toast('Delete failed: ' + e.message); }
    }

    async function suggest(tr) {
        let model = tr.querySelector('[data-k="model"]')?.value || tr.dataset.model;
        if (!model) { toast('Enter a model name first'); return; }
        // Read-only rows have no inputs to fill — enter edit mode first so the
        // one-click fill + provider picker have somewhere to write.
        if (!tr.querySelector('[data-k="input"]')) {
            await renderInto(tr, model, true);
        }
        model = tr.querySelector('[data-k="model"]')?.value || tr.dataset.model;
        toast('Querying OpenRouter…');
        try {
            const s = await suggestPricing(model);
            if (!s.matched) { toast('No match on OpenRouter'); return; }
            tr.dataset.orId = s.model;
            tr._providers = Array.isArray(s.providers) && s.providers.length ? s.providers : null;
            tr._providersTotal = s.providers_total || 0;
            fillInputs(tr, s.input, s.output, s.cache_read, s.cache_creation);
            renderProviderHint(tr);
            tr._orSourced = true;
            paintEditSource(tr);
            const tail = tr._providers ? ` · ${tr._providersTotal} providers` : '';
            toast(`Filled from OpenRouter (${s.model})${tail}`);
        } catch (e) { toast('OpenRouter unreachable: ' + e.message); }
    }

    // fillInputs writes the four price fields. cache_read/create fall back to ''
    // so an OpenRouter value of 0 (absent) clears the cell instead of showing 0.
    function fillInputs(tr, input, output, cacheRead, cacheCreate) {
        const set = (k, v) => { const inp = tr.querySelector(`[data-k="${k}"]`); if (inp) inp.value = v ? v : ''; };
        set('input', input); set('output', output);
        set('cache_read', cacheRead); set('cache_create', cacheCreate);
    }

    // renderProviderHint turns the edit-mode stamp cell into a clickable summary
    // of the OpenRouter providers, with badges flagging any promo / quantized quote.
    function renderProviderHint(tr) {
        const stamp = tr.querySelector('.pf-stamp');
        if (!stamp) return;
        const provs = tr._providers;
        if (!provs || !provs.length) { stamp.className = 'pf-stamp'; stamp.textContent = 'Editing'; return; }
        const badges = [];
        if (provs.some(p => p.discount > 0)) badges.push('<span class="pf-dbadge">promo</span>');
        if (provs.some(p => quantBadge(p.quant) === 'quantized')) badges.push('<span class="pf-qbadge">quant</span>');
        stamp.className = 'pf-stamp pf-pick';
        stamp.dataset.act = 'pick';
        stamp.innerHTML = `${tr._providersTotal} providers ${badges.join(' ')} · Choose`;
    }

    // openProviderPicker toggles a grouped mini-table of EVERY provider for the
    // model (no cap). Prices are column-aligned so they scan vertically; the
    // Official row is tinted. Clicking a row fills its price and closes the picker.
    function openProviderPicker(tr) {
        if (!tr._providers || !tr._providers.length) return;
        const existing = tr.nextElementSibling;
        if (existing && existing.classList.contains('pf-pickrow')) { existing.remove(); return; }
        const rows = tr._providers.map((p, i) => {
            const tag = p.official
                ? '<span class="pf-off">Official</span>'
                : p.discount > 0
                    ? `<span class="pf-disc">−${Math.round(p.discount * 100)}%</span>`
                    : quantBadge(p.quant) === 'quantized'
                        ? `<span class="pf-quant">${escapeHtml(p.quant)}</span>`
                        : '';
            return `<div class="pf-pr${p.official ? ' is-off' : ''}" data-act="pickrow" data-pidx="${i}">
                <span class="pf-pc-name">${escapeHtml(p.provider)}</span>
                <span class="pf-pc num">${num(p.input)}</span>
                <span class="pf-pc num">${num(p.output)}</span>
                <span class="pf-pc num">${p.cache_read ? num(p.cache_read) : '—'}</span>
                <span class="pf-pc num">${p.cache_creation ? num(p.cache_creation) : '—'}</span>
                <span class="pf-pc-tag">${tag}</span>
            </div>`;
        }).join('');
        const wrap = document.createElement('tr');
        wrap.className = 'pf-pickrow';
        const td = document.createElement('td');
        td.colSpan = 8;
        td.innerHTML = `<div class="pf-ptable">
            <div class="pf-ph">
                <span class="pf-pc-name">Provider</span>
                <span class="pf-pc num">In</span>
                <span class="pf-pc num">Out</span>
                <span class="pf-pc num">Cache</span>
                <span class="pf-pc num">Create</span>
                <span class="pf-pc-tag"></span>
            </div>${rows}</div>`;
        wrap.append(td);
        tr.after(wrap);
    }

    // pickProvider fills the inputs from the chosen provider and closes the picker.
    function pickProvider(tr, idx) {
        const p = tr._providers?.[idx];
        if (!p) return;
        fillInputs(tr, p.input, p.output, p.cache_read, p.cache_creation);
        tr._orSourced = true;
        paintEditSource(tr);
        const pickrow = tr.nextElementSibling;
        if (pickrow?.classList.contains('pf-pickrow')) pickrow.remove();
        toast(`Filled ${p.provider}: ${num(p.input)} / ${num(p.output)}`);
    }

    function orHref(model, orId) {
        // OpenRouter's model search lives at /models?q= (the bare /?q= root does
        // not search). Prefer the matched OpenRouter id when we have one; it
        // lands the search on the exact model.
        const q = orId || model || '';
        return `https://openrouter.ai/models?q=${encodeURIComponent(q)}`;
    }

    async function syncRecompute() {
        try {
            const s = await recomputeStatus();
            if (s.running) pollRecompute();
            else paintRecompute(s);
        } catch { /* ignore */ }
    }
    function pollRecompute() {
        clearTimeout(recomputeTimer);
        const tick = async () => {
            try {
                const s = await recomputeStatus();
                paintRecompute(s);
                if (s.running) recomputeTimer = setTimeout(tick, 1000);
            } catch { recomputeTimer = setTimeout(tick, 2000); }
        };
        tick();
    }
    function paintRecompute(s) {
        if (!recomputeBtn || !recomputeLbl) return;
        recomputeBtn.disabled = !!s.running;
        recomputeLbl.textContent = s.running
            ? `Recomputing ${s.scanned}/${s.total || '…'}…`
            : (s.last_result ? `Last run: ${s.last_result.updated} rows` : '');
    }

    function toast(msg) {
        const t = backdrop.querySelector('#pricing-toast');
        if (!t) return;
        t.textContent = msg; t.style.opacity = '1';
        clearTimeout(t._h); t._h = setTimeout(() => t.style.opacity = '0', 2500);
    }
}

// ---- tiny DOM helpers (module-local) ----
function el(tag, html) { const e = document.createElement(tag); e.innerHTML = html; return e; }
function num(v) { return v == null || v === 0 ? '—' : (Math.round(v * 1e6) / 1e6).toString(); }

// variantsBtn is the ▸ affordance shown on a folded row that has other
// provider-prefixed entries hidden under it.
function variantsBtn(e) {
    const n = Array.isArray(e.variants) ? e.variants.length : 0;
    return n ? `<button class="pf-toggle" data-act="toggle" title="Show ${n} provider variant(s)">▸</button><span class="pf-vcount">${n}</span>` : '';
}

// toggleVariants expands/collapses the provider variants stored on the row.
function toggleVariants(tr) {
    if (!tr._variants) return;
    const open = tr.classList.toggle('pf-open');
    const chev = tr.querySelector('.pf-toggle');
    if (chev) chev.textContent = open ? '▾' : '▸';
    // remove existing variant rows immediately following this one
    let sib = tr.nextElementSibling;
    while (sib && sib.classList.contains('pf-variant')) { const nx = sib.nextElementSibling; sib.remove(); sib = nx; }
    if (open) {
        const frag = document.createDocumentFragment();
        for (const v of tr._variants) frag.append(variantRow(v));
        tr.after(frag);
    }
}

// variantRow is a read-only, indented sub-row showing one alternative provider.
function variantRow(v) {
    const tr = document.createElement('tr');
    tr.className = 'pf-variant';
    const cls = v.source === 'user' ? 'user' : (v.source === 'seed' ? 'seed' : (v.source === 'manual' ? 'manual' : ''));
    tr.innerHTML = `<td colspan="8">
        <span class="pf-vkey">${escapeHtml(v.model)}</span>
        <span class="pf-badge ${cls}">${escapeHtml(v.source || '')}</span>
        <span class="pf-vnums">in ${num(v.input)} · out ${num(v.output)} · cr ${v.cache_read ? num(v.cache_read) : '—'} · cc ${v.cache_create ? num(v.cache_create) : '—'}</span>
    </td>`;
    return tr;
}
