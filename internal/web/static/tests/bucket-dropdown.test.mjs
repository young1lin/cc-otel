import test from 'node:test';
import assert from 'node:assert/strict';
import {
    BUCKET_CHOICES,
    normalizeBucket,
    recommendedBucketForSpan,
} from '../js/bucket-dropdown.js';

test('BUCKET_CHOICES is exactly 5/10/15/30/60', () => {
    assert.deepEqual([...BUCKET_CHOICES].sort((a, b) => a - b), [5, 10, 15, 30, 60]);
});

test('normalizeBucket passes valid buckets through', () => {
    for (const n of [5, 10, 15, 30, 60]) assert.equal(normalizeBucket(n), n);
});

test('normalizeBucket falls back to 30 for anything else', () => {
    for (const n of [0, 7, 45, -5, 3600, NaN, null, undefined, '5']) {
        assert.equal(normalizeBucket(n), 30);
    }
});

test('recommendedBucketForSpan: 5 for a single day, 30 beyond', () => {
    assert.equal(recommendedBucketForSpan(0), 5);
    assert.equal(recommendedBucketForSpan(1), 5);
    assert.equal(recommendedBucketForSpan(2), 30);
    assert.equal(recommendedBucketForSpan(7), 30);
});
