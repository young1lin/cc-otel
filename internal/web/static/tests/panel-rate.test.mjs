import test from 'node:test';
import assert from 'node:assert/strict';

// Since the AllClient merge there is a single Rate view over the unified
// api_requests pool (Claude Code, OpenCode, Codex, …). The old "Rate is
// rejected for the Codex source" guard was removed together with the Codex
// tab; rendering itself is covered by the bucket/legend hover tests, so this
// file only pins the module's import surface now.
import * as rate from '../js/panel-rate.js';

test('panel-rate exports its loaders for the unified pool', () => {
    assert.equal(typeof rate.loadRate, 'function');
    assert.equal(typeof rate.initPanelRate, 'function');
});
