/**
 * Best-effort self-update for the dsh plugin bundle.
 *
 * Once per updateCheckHours (default 24; 0 disables) the plugin asks npm
 * whether @young1lin/dsh-cc-otel has a newer `latest` than the running copy.
 * When it does — and pluginAutoUpdate is not false — the official updater
 * (`dsh plugin --profile <p> update @young1lin/dsh-cc-otel`) runs for every
 * profile that carries this package, so the NEXT boot mounts a fresh bundle
 * whose binary the align step (lib/versions.js) then installs — upgrades
 * only, never downgrades.
 *
 * Everything here is advisory: any failure degrades to a single log line and
 * never blocks telemetry or the daemon.
 */
import { spawn } from 'node:child_process'
import { existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { join } from 'node:path'
import { createRequire } from 'node:module'

import { isNewer, parseDaemonVersion } from './versions.js'

const NPM = process.platform === 'win32' ? 'npm.cmd' : 'npm'
const DSH = process.platform === 'win32' ? 'dsh.cmd' : 'dsh'
const PACKAGE = '@young1lin/dsh-cc-otel'

/** Throttle marker path (one small JSON file in the OS temp dir). */
export function updateMarkerPath() {
  return join(tmpdir(), 'dsh-cc-otel-update-check.json')
}

/**
 * Pure throttle decision, split out for tests.
 * @param {string} markerRaw raw marker file content ('' when missing/corrupt).
 * @param {number} nowMs current epoch millis.
 * @param {number} hours check interval in hours; <= 0 disables checks.
 * @returns {boolean} true when a registry check is due.
 */
export function isCheckDue(markerRaw, nowMs, hours) {
  if (!Number.isFinite(hours) || hours <= 0) return false
  if (typeof markerRaw !== 'string' || markerRaw === '') return true
  try {
    const t = Number(JSON.parse(markerRaw).checkedAt)
    if (!Number.isFinite(t)) return true
    return nowMs - t >= hours * 3600 * 1000
  } catch {
    return true
  }
}

/** Version of the running plugin package ("0.0.0" in a source checkout). */
export function ownPackageVersion() {
  const require = createRequire(import.meta.url)
  return require('../package.json').version
}

/** Profiles under the dsh home that carry this package. */
export function findProfiles(dshHome = process.env.DSH_HOME ?? join(homedir(), '.dsh')) {
  const root = join(dshHome, 'profiles')
  try {
    return readdirSync(root, { withFileTypes: true })
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name)
      .filter((name) => existsSync(join(root, name, 'node_modules', '@young1lin', 'dsh-cc-otel')))
  } catch {
    return []
  }
}

/**
 * Minimal spawn helper — same semantics as run() in index.js, kept local to
 * avoid a circular import between the two modules.
 */
function run(bin, args, timeoutMillis) {
  return new Promise((resolveRun) => {
    let child
    // Node >= 20.12 refuses to spawn .cmd/.bat directly (CVE-2024-27980:
    // EINVAL without a shell), and shell:true with an args array is
    // deprecated (DEP0190, warns on every daily check). Route .cmd/.bat
    // through cmd.exe explicitly — a real executable, no shell parsing of
    // our plain-token arguments, no warning.
    let finalBin = bin
    let finalArgs = args
    if (process.platform === 'win32' && /\.(cmd|bat)$/i.test(bin)) {
      finalBin = process.env.ComSpec ?? 'cmd.exe'
      finalArgs = ['/d', '/s', '/c', bin, ...args]
    }
    try {
      child = spawn(finalBin, finalArgs, { stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true })
    } catch (error) {
      resolveRun({ status: null, stdout: '', stderr: String(error) })
      return
    }
    let stdout = ''
    let stderr = ''
    const timer = setTimeout(() => {
      stdout += '\n(timeout)'
      child.kill()
    }, timeoutMillis)
    child.stdout.on('data', (chunk) => { stdout += chunk })
    child.stderr.on('data', (chunk) => { stderr += chunk })
    child.on('error', (error) => {
      clearTimeout(timer)
      resolveRun({ status: null, stdout, stderr: stderr + String(error) })
    })
    child.on('close', (code) => {
      clearTimeout(timer)
      resolveRun({ status: code, stdout: stdout.trim(), stderr: stderr.trim() })
    })
  })
}

/** npm `latest` for this package, or undefined when the query fails. */
async function npmViewLatest() {
  const r = await run(NPM, ['view', PACKAGE, 'version', '--json'], 30000)
  if (r.status !== 0) return undefined
  try {
    const parsed = JSON.parse(r.stdout)
    return typeof parsed === 'string' ? parsed : undefined
  } catch {
    return undefined
  }
}

/** Run the official updater for one profile. @returns {Promise<boolean>} success. */
async function runDshUpdate(profile) {
  const r = await run(DSH, ['plugin', '--profile', profile, 'update', PACKAGE], 180000)
  return r.status === 0
}

/**
 * Entry point, called from apply() in parallel with ensureDaemon.
 * @param {{ info: Function, warn: Function }} logger logger sink.
 * @param {{ pluginAutoUpdate?: boolean, updateCheckHours?: number }} config row config.
 * @param {{ markerPath?: string, ownVersion?: string, latest?: string, runUpdate?: Function }} [deps] test seams.
 */
export async function maybeSelfUpdate(logger, config = {}, deps = {}) {
  const hours = config.updateCheckHours === undefined ? 24 : Number(config.updateCheckHours)
  if (!Number.isFinite(hours) || hours <= 0) return
  const markerPath = deps.markerPath ?? updateMarkerPath()
  let markerRaw = ''
  try {
    markerRaw = readFileSync(markerPath, 'utf8')
  } catch {
    // missing marker = first run
  }
  if (!isCheckDue(markerRaw, Date.now(), hours)) return
  try {
    writeFileSync(markerPath, JSON.stringify({ checkedAt: Date.now() }) + '\n', 'utf8')
  } catch {
    // a read-only temp dir only costs a redundant check; not worth failing on
  }
  // The marker is written BEFORE the slow steps on purpose: a failed check
  // must not retry on every boot.
  const own = deps.ownVersion ?? ownPackageVersion()
  const latest = deps.latest ?? (await npmViewLatest())
  if (latest === undefined || parseDaemonVersion(latest) === undefined) {
    logger.info('dsh-cc-otel: self-update registry check failed — next attempt in ' + hours + 'h')
    return
  }
  if (!isNewer(latest, own)) return
  const profiles = process.env.DSH_PROFILE ? [process.env.DSH_PROFILE] : findProfiles()
  if (profiles.length === 0) {
    logger.warn(
      'dsh-cc-otel: ' + PACKAGE + ' ' + latest + ' is available (running ' + own + '); '
      + 'no dsh profile found — run: dsh plugin --profile <name> update ' + PACKAGE,
    )
    return
  }
  if (config.pluginAutoUpdate === false) {
    logger.warn(
      'dsh-cc-otel: ' + latest + ' available (running ' + own + '), pluginAutoUpdate=false — '
      + 'run: dsh plugin --profile ' + profiles[0] + ' update ' + PACKAGE,
    )
    return
  }
  const runUpdate = deps.runUpdate ?? runDshUpdate
  for (const profile of profiles) {
    const ok = await runUpdate(profile)
    if (ok) {
      logger.info('dsh-cc-otel: self-updated to ' + latest + ' in profile "' + profile + '" — effective next boot')
    } else {
      logger.warn('dsh-cc-otel: self-update failed for profile "' + profile + '" — run: dsh plugin --profile ' + profile + ' update ' + PACKAGE)
    }
  }
}
