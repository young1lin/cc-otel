/**
 * Version parsing and ordering for cc-otel daemon binaries.
 *
 * Two version shapes arrive from `cc-otel -v`:
 *   - release artifacts (goreleaser injects main.version={{.Version}}): "0.1.7"
 *   - local builds with git-describe injection: "v0.1.6-1-gf78233e-dirty",
 *     meaning "1 commit AFTER tag v0.1.6" — newer than the tag itself.
 *
 * Ordering (docs/internal/design/align-never-downgrade.md):
 *   0.1.6  <  0.1.6-1-g<sha>[-dirty]  <  0.1.7
 * A git-describe suffix ranks ABOVE its base tag but BELOW the next release;
 * a plain semver prerelease (0.1.8-rc.1) ranks BELOW its release (0.1.8).
 * Anything unparsable ("dev", "", garbage) is unmanaged: callers must refuse
 * to touch a home binary whose version does not parse.
 */

const RELEASE_RE = /^v?(\d+)\.(\d+)\.(\d+)$/
const DESCRIBE_RE = /^v?(\d+)\.(\d+)\.(\d+)-(\d+)-g[0-9a-f]+(?:-dirty)?$/
// Prerelease suffix must start with a letter so describe-like junk without
// the -g<sha> marker ("0.1.6-1-abc123") stays unparsable = unmanaged.
const PRERELEASE_RE = /^v?(\d+)\.(\d+)\.(\d+)-([A-Za-z][0-9A-Za-z.-]*)$/

// Kind rank when the core triple is equal: prerelease < release < describe.
const KIND_RANK = { prerelease: 0, release: 1, describe: 2 }

/**
 * Parse a self-reported daemon version.
 * @param {string|undefined} raw version as printed by `cc-otel -v` (the
 *   "cc-otel " prefix is already stripped by versionOf in index.js).
 * @returns {{major:number, minor:number, patch:number, kind:'release'|'describe'|'prerelease', ahead:number, suffix:string}|undefined}
 *   undefined when the shape is not recognized — the caller must treat the
 *   binary as unmanaged and never overwrite it.
 */
export function parseDaemonVersion(raw) {
  if (typeof raw !== 'string') return undefined
  const s = raw.trim()
  let m = DESCRIBE_RE.exec(s)
  if (m) {
    return { major: +m[1], minor: +m[2], patch: +m[3], kind: 'describe', ahead: +m[4], suffix: '' }
  }
  m = RELEASE_RE.exec(s)
  if (m) {
    return { major: +m[1], minor: +m[2], patch: +m[3], kind: 'release', ahead: 0, suffix: '' }
  }
  m = PRERELEASE_RE.exec(s)
  if (m) {
    return { major: +m[1], minor: +m[2], patch: +m[3], kind: 'prerelease', ahead: 0, suffix: m[4] }
  }
  return undefined
}

/**
 * Semver-ish prerelease segment comparison, simplified: numeric segments
 * compare numerically and rank below alphanumeric ones; otherwise lexical;
 * a shorter field list ranks lower when the shared prefix is equal.
 */
function compareSuffix(a, b) {
  const sa = a.split('.')
  const sb = b.split('.')
  const n = Math.max(sa.length, sb.length)
  for (let i = 0; i < n; i++) {
    const x = sa[i]
    const y = sb[i]
    if (x === undefined) return -1
    if (y === undefined) return 1
    const nx = /^\d+$/.test(x) ? Number(x) : null
    const ny = /^\d+$/.test(y) ? Number(y) : null
    if (nx !== null && ny !== null) {
      if (nx !== ny) return nx < ny ? -1 : 1
    } else if (nx !== null) return -1
    else if (ny !== null) return 1
    else if (x !== y) return x < y ? -1 : 1
  }
  return 0
}

/**
 * Strict "a > b" comparison on daemon version strings.
 * @returns {boolean} true only when a is strictly newer; false on equal,
 *   older, or either side unparsable (callers pre-check parseability).
 */
export function isNewer(a, b) {
  const pa = parseDaemonVersion(a)
  const pb = parseDaemonVersion(b)
  if (pa === undefined || pb === undefined) return false
  const ca = [pa.major, pa.minor, pa.patch]
  const cb = [pb.major, pb.minor, pb.patch]
  for (let i = 0; i < 3; i++) {
    if (ca[i] !== cb[i]) return ca[i] > cb[i]
  }
  const rank = KIND_RANK[pa.kind] - KIND_RANK[pb.kind]
  if (rank !== 0) return rank > 0
  if (pa.kind === 'describe') return pa.ahead > pb.ahead
  if (pa.kind === 'prerelease') return compareSuffix(pa.suffix, pb.suffix) > 0
  return false
}

/**
 * Decide whether the align step may overwrite the ~/.cc-otel binary.
 * Invariant from the 2026-11-06 incident: the shared daemon may only be
 * replaced when the plugin bundle is strictly newer — never on mere
 * inequality, which is what downgraded production to 0.1.3 back then.
 * @param {string|undefined} npmVersion version of the bundled binary.
 * @param {string|undefined} homeVersion version of the ~/.cc-otel binary
 *   (undefined also covers "exists but -v failed").
 * @param {boolean} homeExists whether the home binary exists on disk.
 * @returns {'install'|'skip-stale-plugin'|'skip-unmanaged-home'|'skip-unmanaged-npm'|'noop'}
 */
export function alignDecision(npmVersion, homeVersion, homeExists) {
  if (!homeExists) return 'install'
  if (parseDaemonVersion(homeVersion) === undefined) return 'skip-unmanaged-home'
  if (parseDaemonVersion(npmVersion) === undefined) return 'skip-unmanaged-npm'
  if (isNewer(npmVersion, homeVersion)) return 'install'
  if (isNewer(homeVersion, npmVersion)) return 'skip-stale-plugin'
  return 'noop'
}
