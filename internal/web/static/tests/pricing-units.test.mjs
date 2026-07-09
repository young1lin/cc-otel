// Unit tests for the pricing unit converters. The pricing API exchanges
// USD-per-million-tokens on the wire; the DB stores USD-per-token. These two
// pure helpers live in utils.js (the designated home for pure helpers per the
// repo's module-layout rule) so they can be imported under Node with no DOM.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { perMtokToToken, tokenToPerMtok } from '../js/utils.js';

test('USD/Mtok <-> USD/token round trip', () => {
    assert.equal(perMtokToToken(0.6), 6e-7);
    assert.equal(tokenToPerMtok(6e-7), 0.6);
});

test('zero and large values', () => {
    assert.equal(perMtokToToken(0), 0);
    assert.equal(tokenToPerMtok(0), 0);
    assert.equal(perMtokToToken(10), 1e-5);
});
