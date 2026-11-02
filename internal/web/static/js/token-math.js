// Since the AllClient merge every row — Claude Code, OpenCode, Codex, … —
// comes from the unified api_requests pool with the same token semantics:
// input_tokens is UNCACHED input, cache_read/cache_creation are disjoint
// categories, and total_tokens (when > 0) is the client-reported raw total.

export function tokenParts(row) {
    const input = Number(row?.input_tokens || 0);
    const output = Number(row?.output_tokens || 0);
    const cacheRead = Number(row?.cache_read_tokens || 0);
    const cacheCreate = Number(row?.cache_creation_tokens || 0);
    const reportedTotal = Number(row?.total_tokens || 0);

    const inputSide = input + cacheRead + cacheCreate;
    return {
        inputSide,
        uncachedInput: input,
        cacheRead,
        cacheCreate,
        output,
        total: reportedTotal > 0 ? reportedTotal : inputSide + output,
    };
}

export function cacheHitParts(row) {
    const cacheRead = Number(row?.cache_read_tokens || 0);
    // Denominator is the full input side (uncached + cache read + cache
    // create). Using cache_read + cache_create alone would collapse to a
    // constant 100% for reverse-proxied providers (GLM, mimo) that never
    // report cache_creation.
    return {
        numerator: cacheRead,
        denominator: Number(row?.input_tokens || 0) + cacheRead + Number(row?.cache_creation_tokens || 0),
    };
}
