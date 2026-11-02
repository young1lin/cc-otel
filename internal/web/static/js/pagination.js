// Shared pagination bar: Prev / page X of Y (N rows) / Next / jump-to-page.
// The jump box exists for long tables (Request Log over months of data):
// type a page number (or scroll it) and hit Go / Enter to land there directly
// instead of clicking Next dozens of times. Out-of-range input snaps to the
// nearest bound, so it can never navigate to an empty page.
//
// Blur-race note: clicking Go blurs the input FIRST, and the cosmetic blur
// handler below resets input.value to the current page before the click
// handler runs. doJump therefore reads `entered` (the last typed value, kept
// up to date by input events) — never the live input.value. Reading the live
// value made every Go click a silent no-op (caught by pagination.test.mjs).
export function renderPagination(containerId, pageState, reloadFn) {
    const el = document.getElementById(containerId);
    if (!el) return;
    const totalPages = Math.max(1, Math.ceil(pageState.total / pageState.pageSize));
    if (pageState.total <= pageState.pageSize) { el.innerHTML = ''; return; }

    el.innerHTML = '';

    const prev = document.createElement('button');
    prev.textContent = '\u2039 Prev';
    prev.disabled = pageState.page <= 1;
    prev.onclick = () => { pageState.page--; reloadFn(); };

    const info = document.createElement('span');
    info.textContent = `${pageState.page} / ${totalPages}  (${pageState.total} rows)`;

    const next = document.createElement('button');
    next.textContent = 'Next \u203a';
    next.disabled = pageState.page >= totalPages;
    next.onclick = () => { pageState.page++; reloadFn(); };

    const jump = document.createElement('span');
    jump.className = 'pg-jump';
    const input = document.createElement('input');
    input.type = 'number';
    input.min = '1';
    input.max = String(totalPages);
    input.value = String(pageState.page);
    input.title = `Jump to page (1\u2013${totalPages})`;
    input.setAttribute('aria-label', 'Jump to page number');
    const go = document.createElement('button');
    go.textContent = 'Go';

    let entered = pageState.page;
    input.addEventListener('input', () => { entered = Number(input.value); });
    const doJump = () => {
        const n = Math.floor(Number(entered));
        if (!Number.isFinite(n)) { input.value = String(pageState.page); return; }
        const target = Math.min(totalPages, Math.max(1, n));
        if (target === pageState.page) { input.value = String(pageState.page); return; }
        pageState.page = target;
        reloadFn();
    };
    go.onclick = doJump;
    input.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') { e.preventDefault(); doJump(); }
    });
    // Cosmetic only: put the box back in sync when focus leaves without a jump.
    input.addEventListener('blur', () => { input.value = String(pageState.page); });
    jump.append(input, go);

    el.append(prev, info, next, jump);
}
