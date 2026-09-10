#!/usr/bin/env node
/**
 * cc-otel CLI shim for the @young1lin/dsh-cc-otel npm package.
 *
 * Forwards every argument to the platform binary selected at install time
 * (CC_OTEL_BIN > the @young1lin/cc-otel-<os>-<arch> optional dependency >
 * ~/.cc-otel/cc-otel). Lets a dsh profile run e.g.
 *   node_modules/.bin/cc-otel status
 * without knowing where the real executable lives.
 */
import { spawnSync } from 'node:child_process'
import { resolveCcOtelBinary } from '../lib/index.js'

const binary = resolveCcOtelBinary()
if (binary === undefined) {
  console.error('cc-otel: no binary for this platform — install the package '
    + '(@young1lin/cc-otel-' + process.platform + '-' + (process.arch === 'x64' ? 'x64' : process.arch)
    + '), or set CC_OTEL_BIN to an existing cc-otel executable')
  process.exit(127)
}
const result = spawnSync(binary, process.argv.slice(2), { stdio: 'inherit', windowsHide: true })
if (result.error !== undefined) {
  console.error('cc-otel: failed to run ' + binary + ': ' + String(result.error))
  process.exit(127)
}
process.exit(result.status ?? 1)
