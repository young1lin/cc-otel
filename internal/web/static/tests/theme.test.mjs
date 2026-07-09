import test from 'node:test';
import assert from 'node:assert/strict';
import {
    getModelFamily, getModelColor, hashModelName, mixHex,
} from '../js/theme.js';
import { state } from '../js/state.js';

test('getModelFamily classifies known families', () => {
    assert.equal(getModelFamily('claude-3-5-sonnet-20241022'), 'claude');
    assert.equal(getModelFamily('claude-opus-4-7'), 'claude');
    assert.equal(getModelFamily('glm-4-flash'), 'glm');
    assert.equal(getModelFamily('gpt-4o'), 'gpt');
    assert.equal(getModelFamily('o3-mini'), 'gpt');
    assert.equal(getModelFamily('qwen2.5-72b'), 'qwen');
    assert.equal(getModelFamily('deepseek-coder'), 'deepseek');
    assert.equal(getModelFamily('kimi-k2'), 'kimi');
    assert.equal(getModelFamily('something-unknown-xyz'), 'other');
    assert.equal(getModelFamily(''), 'other');
    assert.equal(getModelFamily(null), 'other');
});

test('getModelColor is a hex string and stable per model', () => {
    state.isDark = true;
    const a1 = getModelColor('claude-3-5-sonnet-20241022');
    const a2 = getModelColor('claude-3-5-sonnet-20241022');
    assert.equal(a1, a2);
    assert.match(a1, /^#[0-9a-fA-F]{6}$/);
});

test('Opus gets the eye-catching orange', () => {
    state.isDark = true;
    assert.equal(getModelColor('claude-opus-4-7'), '#ff9f0a');
    state.isDark = false;
    assert.equal(getModelColor('claude-opus-4-7'), '#ff8a00');
});

test('hashModelName returns a non-negative integer', () => {
    const h = hashModelName('foo-bar');
    assert.ok(Number.isInteger(h) && h >= 0);
    assert.equal(hashModelName('foo-bar'), hashModelName('FOO-BAR')); // case-insensitive
});

test('mixHex blends two hex colors', () => {
    assert.equal(mixHex('#000000', '#ffffff', 0.5), '#808080');
    assert.equal(mixHex('#000000', '#ffffff', 0), '#000000');
    assert.equal(mixHex('#000000', '#ffffff', 1), '#ffffff');
});
