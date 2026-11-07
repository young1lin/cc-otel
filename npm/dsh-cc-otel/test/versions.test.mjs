import test from 'node:test'
import assert from 'node:assert/strict'

import { alignDecision, isNewer, parseDaemonVersion } from '../lib/versions.js'
import { isCheckDue } from '../lib/self-update.js'

test('parseDaemonVersion recognizes release, describe, and prerelease shapes', () => {
  assert.deepEqual(parseDaemonVersion('0.1.7'), { major: 0, minor: 1, patch: 7, kind: 'release', ahead: 0, suffix: '' })
  assert.equal(parseDaemonVersion('v0.1.7').kind, 'release')
  const dev = parseDaemonVersion('v0.1.6-1-gf78233e-dirty')
  assert.equal(dev.kind, 'describe')
  assert.equal(dev.ahead, 1)
  assert.deepEqual([dev.major, dev.minor, dev.patch], [0, 1, 6])
  assert.equal(parseDaemonVersion('0.1.8-rc.1').kind, 'prerelease')
})

test('parseDaemonVersion rejects unmanaged shapes (never overwrite what we cannot read)', () => {
  assert.equal(parseDaemonVersion('dev'), undefined)
  assert.equal(parseDaemonVersion(''), undefined)
  assert.equal(parseDaemonVersion(undefined), undefined)
  // describe-like junk without the -g<sha> marker must not parse as prerelease
  assert.equal(parseDaemonVersion('0.1.6-1-abc123'), undefined)
})

test('isNewer orders release < describe-on-release < next release', () => {
  assert.equal(isNewer('0.1.7', '0.1.6'), true)
  assert.equal(isNewer('0.1.6', '0.1.7'), false)
  assert.equal(isNewer('0.1.7', '0.1.7'), false)
  // git-describe = base tag PLUS N commits: above the tag, below next release
  assert.equal(isNewer('v0.1.6-1-gf78233e-dirty', '0.1.6'), true)
  assert.equal(isNewer('0.1.7', 'v0.1.6-1-gf78233e-dirty'), true)
  assert.equal(isNewer('0.1.6', 'v0.1.6-1-gf78233e-dirty'), false)
  assert.equal(isNewer('v0.1.6-2-gabc123', 'v0.1.6-1-gf78233e'), true)
  // plain prerelease ranks below its release; numeric segments order among themselves
  assert.equal(isNewer('0.1.8', '0.1.8-rc.1'), true)
  assert.equal(isNewer('0.1.8-rc.2', '0.1.8-rc.1'), true)
  // unparsable operand never wins
  assert.equal(isNewer('dev', '0.1.3'), false)
})

test('alignDecision never returns install for a downgrade (incident 2026-11-06: plugin 0.1.3 vs home 0.1.7)', () => {
  assert.equal(alignDecision('0.1.3', '0.1.7', true), 'skip-stale-plugin')
  assert.equal(alignDecision('0.1.8', '0.1.7', true), 'install')
  assert.equal(alignDecision('0.1.7', '0.1.7', true), 'noop')
  assert.equal(alignDecision('0.1.8', undefined, false), 'install')
  // a manual dev deploy ahead of the tag is preserved against that same release
  assert.equal(alignDecision('0.1.7', 'v0.1.7-2-gabc123-dirty', true), 'skip-stale-plugin')
  // but a real newer release still upgrades over it
  assert.equal(alignDecision('0.1.8', 'v0.1.7-2-gabc123-dirty', true), 'install')
  // unmanaged home is never touched, even by a newer bundle
  assert.equal(alignDecision('0.1.8', 'dev', true), 'skip-unmanaged-home')
  // home binary exists but -v failed: same protection
  assert.equal(alignDecision('0.1.8', undefined, true), 'skip-unmanaged-home')
  // unmanaged npm bundle never installs
  assert.equal(alignDecision(undefined, '0.1.7', true), 'skip-unmanaged-npm')
})

test('isCheckDue throttles by marker age and config', () => {
  const now = Date.now()
  assert.equal(isCheckDue('', now, 24), true)
  assert.equal(isCheckDue('{"checkedAt":' + (now - 23 * 3600e3) + '}', now, 24), false)
  assert.equal(isCheckDue('{"checkedAt":' + (now - 25 * 3600e3) + '}', now, 24), true)
  assert.equal(isCheckDue('garbage', now, 24), true)
  assert.equal(isCheckDue('{"checkedAt":' + now + '}', now, 0), false)
})
