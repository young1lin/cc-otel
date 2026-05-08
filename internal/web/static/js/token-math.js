import { state } from './state.js';

export function tokenParts(row) {
    const input = Number(row?.input_tokens || 0);
    const output = Number(row?.output_tokens || 0);
    const cacheRead = Number(row?.cache_read_tokens || 0);
    const cacheCreate = Number(row?.cache_creation_tokens || 0);
    const reportedTotal = Number(row?.total_tokens || 0);

    if (state.source === 'codex') {
        const uncachedInput = Math.max(input - cacheRead, 0);
        return {
            inputSide: input,
            uncachedInput,
            cacheRead,
            cacheCreate: 0,
            output,
            total: reportedTotal > 0 ? reportedTotal : input + output,
        };
    }

    const inputSide = input + cacheRead + cacheCreate;
    return {
        inputSide,
        uncachedInput: input,
        cacheRead,
        cacheCreate,
        output,
        total: inputSide + output,
    };
}

export function cacheHitParts(row) {
    const cacheRead = Number(row?.cache_read_tokens || 0);
    if (state.source === 'codex') {
        return { numerator: cacheRead, denominator: Number(row?.input_tokens || 0) };
    }
    return {
        numerator: cacheRead,
        denominator: cacheRead + Number(row?.cache_creation_tokens || 0),
    };
}
