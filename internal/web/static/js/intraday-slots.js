// Dense-slot builder for the Intraday bar chart.
//
// The intraday SQL omits empty buckets. On a category axis that would place
// 01:55 directly beside 09:00 when nothing ran in between, so the axis would
// misstate time. fillIntradaySlots reinstates the missing instants.
//
// The fill spans the returned data's extent, not the requested date range:
// padding to the full range would append empty future slots, so viewing Today
// at 14:00 with 5-minute buckets would render ten hours of blank axis.

function pad2(n) {
    return String(n).padStart(2, '0');
}

// Mirrors the server's BucketLabel format ("MM-DD HH:MM", local time). Used
// only for synthesized gap slots; slots carrying data keep the server's label.
export function slotLabel(unixSec) {
    const d = new Date(unixSec * 1000);
    return `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

// The default zoom window spans a working day rather than a calendar one, and
// is anchored on the newest bucket. For a day whose data runs to midnight this
// lands on 08:00-24:00; the early hours stay one pan away instead of spending
// the opening view on time nobody worked.
export const DEFAULT_WINDOW_MINUTES = 16 * 60;

// defaultVisibleSlots converts that duration into a slot count for the current
// bucket size. Sizing the window in slots instead would make its wall-clock
// width swing with the bucket — 48 slots is 4h at 5min but a full day at 30min.
export function defaultVisibleSlots(bucketMinutes, totalSlots) {
    const bucket = Number(bucketMinutes);
    const total = Number(totalSlots);
    if (!(bucket > 0) || !(total > 0)) return 0;
    return Math.min(total, Math.ceil(DEFAULT_WINDOW_MINUTES / bucket));
}

export function fillIntradaySlots(rows, bucketMinutes) {
    const empty = { slots: [], rowAt: new Map() };
    const step = Number(bucketMinutes) * 60;
    if (!Array.isArray(rows) || rows.length === 0) return empty;
    if (!Number.isFinite(step) || step <= 0) return empty;

    const labelAt = new Map();
    const rowAt = new Map();
    let first = Infinity;
    let last = -Infinity;

    for (const r of rows) {
        const u = Number(r?.bucket_start_unix);
        if (!Number.isFinite(u)) continue;
        if (u < first) first = u;
        if (u > last) last = u;
        if (!labelAt.has(u)) {
            const lbl = r?.bucket_label;
            labelAt.set(u, lbl != null && String(lbl) !== '' ? String(lbl) : slotLabel(u));
        }
        rowAt.set(u + '|' + r.model, r);
    }
    if (!Number.isFinite(first) || !Number.isFinite(last)) return empty;

    const slots = [];
    for (let u = first; u <= last; u += step) {
        slots.push({ unix: u, label: labelAt.get(u) ?? slotLabel(u) });
    }
    return { slots, rowAt };
}
