import { state } from './state.js';

export function fmtNum(n) {
    if (n == null || isNaN(n)) return '0';
    if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
    if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
    return String(n);
}

export function fmtUSD(v) {
    const n = Number(v || 0);
    return '$' + n.toFixed(4);
}

export function fmtPct(p) {
    if (!Number.isFinite(p)) return '—';
    return p.toFixed(1) + '%';
}

export function fmtTokens(n) {
    return fmtNum(n);
}

// Local-time, 24-hour, locale-independent "YYYY-MM-DD HH:mm:ss".
// Built from local getters so midnight renders as 00:xx (never the 12-hour "12:xx AM").
function fmtDate24(d) {
    const p = n => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} `
        + `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

// Accepts an ISO string (e.g. "2026-06-11T00:06:29+08:00") or a Date.
export function fmtDateTime(input) {
    if (input == null || input === '') return '—';
    const d = input instanceof Date ? input : new Date(input);
    if (isNaN(d.getTime())) return typeof input === 'string' ? input : '—';
    return fmtDate24(d);
}

export function fmtTime(unix) {
    if (!unix) return '—';
    try {
        return fmtDate24(new Date(unix * 1000));
    } catch { return String(unix); }
}

export function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

export function truncate(s, n) {
    return s && s.length > n ? s.slice(0, n) + '…' : s;
}

export function formatUserCell(userId) {
    if (!userId) return '—';
    const short = userId.length > 14 ? userId.slice(0, 10) + '…' : userId;
    return `<span class="badge" title="${escapeHtml(userId)}">${escapeHtml(short)}</span>`;
}

export function toYMD(d) {
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${y}-${m}-${day}`;
}

export function getTodayYMD() {
    return toYMD(new Date());
}

export function isValidYMD(s) {
    if (!s || typeof s !== 'string') return false;
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s.trim());
    if (!m) return false;
    const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 12, 0, 0, 0);
    if (isNaN(d.getTime())) return false;
    return toYMD(d) === s.trim();
}

export function fmtHourRange(h) {
    const hh = String(h).padStart(2, '0');
    const hh2 = String((h + 1) % 24).padStart(2, '0');
    return `${hh}:00–${hh2}:00`;
}

// SYNC: Mirrors Go handler.go rangeToFromTo(). Keep both in sync:
//   week  = today - 6 days  (inclusive -> 7 days total)
//   month = today - 29 days (inclusive -> 30 days total)
//   all   = 1970-01-01 -> today
export function rangeToFromTo(range) {
    if (range === 'custom' || range === 'single-day') {
        return { from: state.customFrom, to: state.customTo };
    }
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
