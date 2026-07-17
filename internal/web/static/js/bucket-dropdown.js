// Shared time-bucket control, used by the Token Rate panel and the Intraday
// chart. Both offer the same 5/10/15/30/60-minute choices, so the constants and
// the dropdown factory live here rather than being duplicated per panel.

export const BUCKET_CHOICES = new Set([5, 10, 15, 30, 60]);

export function normalizeBucket(n) {
    return BUCKET_CHOICES.has(n) ? n : 30;
}

// Fine buckets are only readable over a short span: a 5-minute bucket across
// 7 days is ~2000 slots per model.
export function recommendedBucketForSpan(spanDays) {
    return spanDays <= 1 ? 5 : 30;
}

// makeBucketDropdown wires a custom .select-wrap dropdown — a trigger button
// plus a .rate-menu popover — and returns { setValue, setOpen }. Native
// <select> option popups can't be rounded on Windows Chromium, so the open
// menu is a styled list. setValue updates the trigger label and the selected
// item WITHOUT firing onPick (used to sync the control to state, e.g. when a
// loader normalizes the bucket).
export function makeBucketDropdown(wrap, onPick) {
    if (!wrap) return null;
    const trigger = wrap.querySelector('[data-trigger]');
    const label = wrap.querySelector('[data-label]');
    const items = wrap.querySelectorAll('.rate-item');
    if (!trigger || !label) return null;

    const setOpen = (open) => {
        wrap.classList.toggle('open', open);
        trigger.setAttribute('aria-expanded', open ? 'true' : 'false');
    };

    trigger.addEventListener('click', () => {
        setOpen(!wrap.classList.contains('open'));
    });
    items.forEach((item) => {
        item.addEventListener('click', () => {
            setValue(item.dataset.value);
            setOpen(false);
            if (onPick) onPick(item.dataset.value);
        });
    });
    // Close when clicking outside this dropdown, or on Escape.
    document.addEventListener('click', (e) => { if (!wrap.contains(e.target)) setOpen(false); });
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape') setOpen(false); });

    function setValue(value) {
        let matched = null;
        items.forEach((item) => {
            const sel = item.dataset.value === String(value);
            item.setAttribute('aria-selected', sel ? 'true' : 'false');
            if (sel) matched = item;
        });
        if (matched) label.textContent = matched.textContent;
    }
    return { setValue, setOpen };
}
