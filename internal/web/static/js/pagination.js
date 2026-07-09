export function renderPagination(containerId, pageState, reloadFn) {
    const el = document.getElementById(containerId);
    if (!el) return;
    const totalPages = Math.max(1, Math.ceil(pageState.total / pageState.pageSize));
    if (pageState.total <= pageState.pageSize) { el.innerHTML = ''; return; }

    el.innerHTML = '';

    const prev = document.createElement('button');
    prev.textContent = '‹ Prev';
    prev.disabled = pageState.page <= 1;
    prev.onclick = () => { pageState.page--; reloadFn(); };

    const info = document.createElement('span');
    info.textContent = `${pageState.page} / ${totalPages}  (${pageState.total} rows)`;

    const next = document.createElement('button');
    next.textContent = 'Next ›';
    next.disabled = pageState.page >= totalPages;
    next.onclick = () => { pageState.page++; reloadFn(); };

    el.append(prev, info, next);
}
