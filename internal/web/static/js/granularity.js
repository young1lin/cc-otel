export function syncGranularityButtons(buttons, granularity) {
    const selected = granularity === 'month' ? 'month' : 'day';
    buttons.forEach((button) => {
        const active = button.dataset.gran === selected;
        button.classList.toggle('active', active);
        button.setAttribute('aria-pressed', active ? 'true' : 'false');
    });
}
