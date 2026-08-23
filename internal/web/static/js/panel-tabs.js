// Shared primary-panel navigation. Keeping activation and its data-refresh hook in
// one place prevents hidden panels from showing stale rows when reopened.
export function activatePanel(panelName, { onActivate = () => {}, notify = true, doc = document } = {}) {
    const buttons = [...doc.querySelectorAll('.panel-btn')];
    const panels = [...doc.querySelectorAll('.panel')];
    const btn = buttons.find((item) => item.dataset.panel === panelName);
    const panel = doc.getElementById('panel-' + panelName);
    if (!btn || !panel) return false;

    buttons.forEach((button) => button.classList.remove('active'));
    panels.forEach((item) => item.classList.remove('active'));
    btn.classList.add('active');
    panel.classList.add('active');

    const filterEl = doc.getElementById('request-filter');
    if (filterEl) filterEl.classList.toggle('visible', panelName === 'requests');

    if (notify) onActivate(panelName);
    return true;
}

export function initPanelTabs({ onActivate = () => {}, doc = document } = {}) {
    [...doc.querySelectorAll('.panel-btn')].forEach((btn) => {
        btn.addEventListener('click', () => {
            // Refresh after the panel becomes visible so charts can measure their
            // container and tables always fetch data accumulated while hidden.
            activatePanel(btn.dataset.panel, { onActivate, doc });
        });
    });
}
