import test from 'node:test';
import assert from 'node:assert/strict';
import { beginLatestRequest, invalidateRequestContext } from '../js/latest-request.js';

test('only the newest request generation may commit per render target', () => {
    const first = beginLatestRequest('test-target');
    assert.equal(first(), true);

    const second = beginLatestRequest('test-target');
    assert.equal(first(), false);
    assert.equal(second(), true);

    const independent = beginLatestRequest('other-target');
    assert.equal(second(), true);
    assert.equal(independent(), true);

    invalidateRequestContext();
    assert.equal(second(), false);
    assert.equal(independent(), false);

    const contextIndependent = beginLatestRequest('model-like-target', { trackContext: false });
    invalidateRequestContext();
    assert.equal(contextIndependent(), true);
});
