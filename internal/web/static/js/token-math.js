import { state } from './state.js';

export function tokenParts(row) {
    const input = Number(row?.input_tokens || 0);
    const output = Number(row?.output_tokens || 0);
    const cacheRead = Number(row?.cache_read_tokens || 0);
    const cacheCreate = Number(row?.cache_creation_tokens || 0);
    const reportedTotal = Number(row?.total_tokens || 0);

    if (state.source === 'codex') {
        const uncachedInput = Math.max(input - cacheRead - cacheCreate, 0);
        return {
            inputSide: input,
            uncachedInput,
            cacheRead,
            cacheCreate,
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
        // Codex input_tokens already includes the cached portion.
        return { numerator: cacheRead, denominator: Number(row?.input_tokens || 0) };
    }
    // Claude path: denominator is the full input side (uncached + cache read + cache
    // create). Using cache_read + cache_create alone would collapse to a constant 100%
    // for reverse-proxied providers (GLM, mimo) that never report cache_creation.
    return {
        numerator: cacheRead,
        denominator: Number(row?.input_tokens || 0) + cacheRead + Number(row?.cache_creation_tokens || 0),
    };
}
