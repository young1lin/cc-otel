import { test } from 'node:test';
import assert from 'node:assert/strict';
import { state } from '../js/state.js';
import { cacheHitParts, tokenParts } from '../js/token-math.js';

// Helper: cache hit ratio from parts.
const ratio = (row) => {
    const p = cacheHitParts(row);
    return p.denominator > 0 ? p.numerator / p.denominator : 0;
};

test('claude cache hit uses full input-side as denominator', () => {
    state.source = 'claude';
    // input(uncached)=100, cache_read=800, cache_creation=200
    // ratio = 800 / (100 + 800 + 200) = 800/1100
    const row = { input_tokens: 100, cache_read_tokens: 800, cache_creation_tokens: 200 };
    assert.ok(Math.abs(ratio(row) - 800 / 1100) < 1e-9);
});

test('claude cache hit does NOT collapse to 100% when cache_creation is 0 (GLM/mimo)', () => {
    state.source = 'claude';
    // Reverse-proxied GLM/mimo: cache_read>0, cache_creation=0.
    // ratio = 900 / (100 + 900 + 0) = 0.9, NOT 1.0.
    const row = { input_tokens: 100, cache_read_tokens: 900, cache_creation_tokens: 0 };
    assert.ok(Math.abs(ratio(row) - 0.9) < 1e-9, `expected 0.9, got ${ratio(row)}`);
});

test('codex-shaped row uses the unified denominator (input pre-normalized to uncached)', () => {
    // Since the AllClient merge, Codex rows arrive NORMALIZED: input_tokens is
    // the uncached portion, cache read/create are disjoint categories.
    const row = { input_tokens: 600, cache_read_tokens: 400, cache_creation_tokens: 0 };
    assert.ok(Math.abs(ratio(row) - 0.4) < 1e-9);
});

test('codex token parts keep disjoint categories and prefer the reported total', () => {
    const parts = tokenParts({
        input_tokens: 100,
        cache_read_tokens: 40,
        cache_creation_tokens: 20,
        output_tokens: 10,
        total_tokens: 110,
    });
    assert.deepEqual(parts, {
        inputSide: 160,
        uncachedInput: 100,
        cacheRead: 40,
        cacheCreate: 20,
        output: 10,
        total: 110,
    });
});
