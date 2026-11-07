/**
 * @young1lin/dsh-cc-otel — DeepSeek Harness profile-bundle plugin.
 *
 * What this plugin does when the loader mounts it (one row per boot):
 *
 * 1. Resolve the cc-otel binary for this platform:
 *      CC_OTEL_BIN  >  @young1lin/cc-otel-<platform>-<arch> (installed by
 *      pnpm through this package's optionalDependencies — the package manager
 *      picks exactly one)  >  ~/.cc-otel/cc-otel(.exe).
 * 2. When the npm binary resolves and ~/.cc-otel carries no binary or an
 *    older version, run `cc-otel install`: the Go side stops any running
 *    daemon, copies the binary into ~/.cc-otel, and the daemon afterwards
 *    runs from that stable path. Running from node_modules would work too,
 *    but Windows cannot replace an executable while it runs — `dsh plugin
 *    update/remove` would fail until the daemon was stopped by hand.
 *    Direction matters: a strict-inequality check here once aligned
 *    production DOWN to the bundled 0.1.3 (2026-11-06), so since v0.1.9 the
 *    copy is upgrade-only (alignDecision in lib/versions.js).
 * 2b. Keep the bundle itself fresh: once a day the plugin checks npm for a
 *    newer @young1lin/dsh-cc-otel and runs the official updater when one
 *    exists (lib/self-update.js), so "plugin older than daemon" self-heals.
 * 3. Ensure the daemon is running (`cc-otel start` is idempotent: exit 1 +
 *    "already running" is success for our purposes). The daemon is shared
 *    with the Claude Code / Codex / opencode clients, so this plugin never
 *    stops it on unload unless stopOnUnload: true is set.
 *
 * 4. Report every provider call to cc-otel (lib/telemetry.js): a transparent
 *    `llm/stream` listener maps each settled attempt onto one OTLP/HTTP JSON
 *    `api_request` log record — Claude Code's vocabulary, which cc-otel's
 *    generic ingestion path stores natively, `ttft_ms` included. The native
 *    session-telemetry row is disabled by the bundle patch (dsh 0.1.5 removed
 *    FULL; its feedback-only export would double-count beside this adapter),
 *    so this plugin IS the telemetry source wherever the bundle is installed.
 */

import { spawn } from 'node:child_process'
import { existsSync } from 'node:fs'
import { homedir } from 'node:os'
import { dirname, join } from 'node:path'
import { createRequire } from 'node:module'

import { DEFAULT_OTLP_URL, installTelemetry } from './telemetry.js'
import { maybeSelfUpdate } from './self-update.js'
import { alignDecision } from './versions.js'

/** Stable Cordis plugin name (the loader row id in cordis.patch.yml matches). */
export const name = 'dsh-cc-otel'

/** Binary file name on this platform. */
const EXE = process.platform === 'win32' ? 'cc-otel.exe' : 'cc-otel'

/** Canonical daemon home shared by every install channel: ~/.cc-otel */
function homeBinary(env = process.env) {
  // os.homedir() honours USERPROFILE on Windows; tests override it.
  const home = env.CC_OTEL_HOME ?? homedir()
  return join(home, '.cc-otel', EXE)
}

/**
 * Resolve the npm platform package's binary for the running platform.
 * @returns {string|undefined} absolute path, or undefined when the optional
 * dependency was not installed (unsupported platform / partial install).
 */
function npmPlatformBinary() {
  const supported = new Set(['linux', 'darwin', 'win32'])
  const arch = process.arch === 'x64' ? 'x64' : process.arch === 'arm64' ? 'arm64' : undefined
  if (!supported.has(process.platform) || arch === undefined) return undefined
  const packageName = '@young1lin/cc-otel-' + process.platform + '-' + arch
  try {
    // No `exports` map on the platform packages, so the package.json subpath
    // stays resolvable; the binary sits at the package root (NOT inside a
    // bin/ directory — cc-otel treats an exe located in a directory literally
    // named "bin" as a dev build and would put its database there).
    const require = createRequire(import.meta.url)
    const root = dirname(require.resolve(packageName + '/package.json'))
    return join(root, EXE)
  } catch {
    return undefined
  }
}

/**
 * Locate the best cc-otel binary for this process.
 * Order: CC_OTEL_BIN env > npm platform package > ~/.cc-otel install.
 * @param {{CC_OTEL_BIN?: string, CC_OTEL_HOME?: string}} [env] environment to consult.
 * @returns {string|undefined} absolute path, or undefined when nothing usable exists.
 */
export function resolveCcOtelBinary(env = process.env) {
  if (env.CC_OTEL_BIN && env.CC_OTEL_BIN.trim() !== '') return env.CC_OTEL_BIN
  const fromNpm = npmPlatformBinary()
  if (fromNpm !== undefined && existsSync(fromNpm)) return fromNpm
  const home = homeBinary(env)
  if (existsSync(home)) return home
  return undefined
}

/** Run one short-lived cc-otel subcommand, resolving with its trimmed output. */
function run(bin, args, timeoutMillis, env = process.env) {
  return new Promise((resolveRun) => {
    let child
    try {
      child = spawn(bin, args, { stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true, env })
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

/**
 * Environment for child cc-otel processes. CC_OTEL_HOME (a test seam) moves
 * the JS-side homeBinary, but the Go binary resolves ~/.cc-otel from the OS
 * home — without translating it into USERPROFILE/HOME for children, install
 * and start would target different directories and split-brain the state.
 * Unset in production, so children simply inherit the parent environment.
 */
function childEnv() {
  const override = process.env.CC_OTEL_HOME
  if (override === undefined || override.trim() === '') return process.env
  const env = { ...process.env, CC_OTEL_HOME: override }
  if (process.platform === 'win32') env.USERPROFILE = override
  else {
    env.HOME = override
    // Node keeps a derived cache; the child never sees ours, but keep it honest.
    delete env.HOMEPATH
  }
  return env
}

/** Read a binary's self-reported version, or undefined when it cannot run. */
async function versionOf(bin) {
  const result = await run(bin, ['-v'], 5000)
  if (result.status !== 0) return undefined
  // "cc-otel 0.2.0" -> "0.2.0" (cosmetic; comparisons use the same extractor)
  return result.stdout.split('\n').pop()?.trim().replace(/^cc-otel\s+/, '') ?? undefined
}

/**
 * Ensure the ~/.cc-otel daemon matches the npm-shipped version, then start it.
 * @param {{ info: Function, warn: Function }} logger logger sink.
 * @param {{autoStart?: boolean, manageHomeBinary?: boolean}} config plugin config.
 * @returns {Promise<void>}
 */
async function ensureDaemon(logger, config) {
  const env = childEnv()
  const resolved = resolveCcOtelBinary()
  if (resolved === undefined) {
    logger.warn(
      'dsh-cc-otel: no binary found — expected the optionalDependency '
      + '@young1lin/cc-otel-' + process.platform + '-' + (process.arch === 'x64' ? 'x64' : process.arch)
      + ' (or a copy in ~/.cc-otel); set CC_OTEL_BIN to point at one manually',
    )
    return
  }

  let daemonBin = resolved
  const npmBin = npmPlatformBinary()
  if (npmBin !== undefined && existsSync(npmBin)) {
    if (config.manageHomeBinary === false) {
      daemonBin = npmBin
    } else {
      const home = homeBinary()
      const npmVersion = await versionOf(npmBin)
      const homeVersion = existsSync(home) ? await versionOf(home) : undefined
      const decision = alignDecision(npmVersion, homeVersion, existsSync(home))
      if (decision === 'install') {
        // `install` stops any running daemon, copies the binary, and leaves
        // it stopped — the start below brings the new version up.
        const installed = await run(npmBin, ['install'], 120000, env)
        if (installed.status === 0) {
          logger.info('dsh-cc-otel: installed cc-otel ' + npmVersion + ' to ' + dirname(home))
        } else {
          logger.warn('dsh-cc-otel: cc-otel install failed: ' + (installed.stderr || installed.stdout))
          if (!existsSync(home)) daemonBin = npmBin
        }
      } else if (decision === 'skip-stale-plugin') {
        // Never downgrade the shared daemon: the installed binary is newer
        // than the plugin bundle (e.g. a manual dev deploy). The old strict
        // inequality check once rolled production back to 0.1.3 (2026-11-06).
        logger.warn(
          'dsh-cc-otel: installed cc-otel ' + homeVersion + ' is newer than the plugin bundle '
          + npmVersion + ' — leaving ~/.cc-otel alone; to follow along update the plugin: '
          + 'dsh plugin --profile <name> update @young1lin/dsh-cc-otel',
        )
      } else if (decision === 'skip-unmanaged-home') {
        logger.warn(
          'dsh-cc-otel: cannot parse installed cc-otel version "' + homeVersion + '" — '
          + 'refusing to touch ~/.cc-otel (expected X.Y.Z or vX.Y.Z-N-g<sha>[-dirty])',
        )
      }
      if (existsSync(home)) daemonBin = home
    }
  }

  if (config.autoStart === false) {
    logger.info('dsh-cc-otel: autoStart disabled — manage the daemon yourself (' + daemonBin + ')')
    return
  }

  const started = await run(daemonBin, ['start'], 30000, env)
  if (started.status === 0) {
    const url = /Web UI: (\S+)/.exec(started.stdout)?.[1] ?? 'http://localhost:8899'
    logger.info('dsh-cc-otel: daemon started — dashboard ' + url)
  } else if (/already running/.test(started.stdout + ' ' + started.stderr)) {
    logger.info('dsh-cc-otel: daemon already running — dashboard http://localhost:8899')
  } else {
    logger.warn('dsh-cc-otel: daemon start failed: ' + (started.stderr || started.stdout || 'exit ' + started.status))
  }
}

/**
 * Plugin entry: the loader mounts this with the row's (usually empty) config.
 * Everything is best-effort — a telemetry helper must never fail the host.
 * @param {{ logger?: { info: Function, warn: Function }, effect: Function, on: Function }} ctx Cordis context.
 * @param {{ autoStart?: boolean, manageHomeBinary?: boolean, stopOnUnload?: boolean, pluginAutoUpdate?: boolean, updateCheckHours?: number, telemetry?: { url?: string, enabled?: boolean } }} [config] row config.
 */
export function apply(ctx, config = {}) {
  const logger = ctx.logger ?? console
  void ensureDaemon(logger, config).catch((error) => {
    logger.warn('dsh-cc-otel: ' + String(error))
  })
  // Self-update runs in parallel: it swaps the bundle on disk for the NEXT
  // boot — the align step above is upgrade-only, so a stale plugin would
  // otherwise pin the daemon below latest forever.
  void maybeSelfUpdate(logger, config).catch((error) => {
    logger.warn('dsh-cc-otel: self-update check failed: ' + String(error))
  })
  // Telemetry adapter: on by default — installing this package IS the choice
  // to report. CC_OTEL_OTLP_URL redirects (e.g. a dev receiver on 14318).
  const telemetry = config.telemetry ?? {}
  const url = telemetry.url ?? process.env.CC_OTEL_OTLP_URL ?? DEFAULT_OTLP_URL
  let disposeTelemetry = undefined
  if (telemetry.enabled !== false) {
    try {
      disposeTelemetry = installTelemetry(ctx, { url, logger })
    } catch (error) {
      logger.warn('dsh-cc-otel: telemetry adapter failed to install — calls run unreported: ' + String(error))
    }
  }
  if (config.stopOnUnload === true || disposeTelemetry !== undefined) {
    ctx.effect(() => () => {
      disposeTelemetry?.()
      if (config.stopOnUnload === true) {
        // Opt-in: normally the daemon outlives dsh because other clients share it.
        void run(homeBinary(), ['stop'], 15000)
      }
    })
  }
}
