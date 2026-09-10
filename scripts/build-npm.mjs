#!/usr/bin/env node
/**
 * Assemble the npm distribution of cc-otel from goreleaser release artifacts.
 *
 * Produces under --out:
 *   dsh-cc-otel/              the dsh plugin bundle (copied from npm/dsh-cc-otel,
 *                             version injected, optionalDependencies pinned)
 *   cc-otel-<os>-<cpu>/       6 platform packages, binary at the package ROOT
 *                             (never inside bin/ — cc-otel's data-dir rule treats
 *                             an exe in a directory named "bin" as a dev build
 *                             and would store its database next to the binary)
 *
 * goreleaser artifacts are named cc-otel_<version>_<goos>_<goarch>.tar.gz|zip
 * and contain the bare binary. The in-repo manifest's 0.0.0 placeholders are
 * replaced at assembly time so the repository never carries release versions.
 *
 * Usage:
 *   node scripts/build-npm.mjs --version 0.2.0 --artifacts dist/release --out dist/npm
 *
 * Extraction uses tar (bsdtar handles both formats on Windows/macOS) and falls
 * back to unzip for .zip on Linux, where GNU tar cannot read zip archives.
 */

import { spawnSync } from 'node:child_process'
import {
  chmodSync,
  copyFileSync,
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const MAIN_SOURCE = join(REPO_ROOT, 'npm', 'dsh-cc-otel')

/** goreleaser target -> npm platform package name parts. */
const PLATFORMS = [
  { goos: 'linux', goarch: 'amd64', os: 'linux', cpu: 'x64' },
  { goos: 'linux', goarch: 'arm64', os: 'linux', cpu: 'arm64' },
  { goos: 'darwin', goarch: 'amd64', os: 'darwin', cpu: 'x64' },
  { goos: 'darwin', goarch: 'arm64', os: 'darwin', cpu: 'arm64' },
  { goos: 'windows', goarch: 'amd64', os: 'win32', cpu: 'x64' },
  { goos: 'windows', goarch: 'arm64', os: 'win32', cpu: 'arm64' },
]

function parseArgs(argv) {
  const args = { dryRun: false }
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]
    if (arg === '--version') args.version = argv[++i]
    else if (arg === '--artifacts') args.artifacts = argv[++i]
    else if (arg === '--out') args.out = argv[++i]
    else if (arg === '--dry-run') args.dryRun = true
    else {
      console.error('build-npm: unknown argument ' + arg)
      process.exit(2)
    }
  }
  for (const key of ['version', 'artifacts', 'out']) {
    if (!args[key]) {
      console.error('build-npm: missing --' + key)
      console.error('usage: node scripts/build-npm.mjs --version <semver> --artifacts <dir> --out <dir> [--dry-run]')
      process.exit(2)
    }
  }
  if (!/^\d+\.\d+\.\d+(-[\w.]+)?$/.test(args.version)) {
    console.error('build-npm: --version must be plain semver without a leading v, got ' + args.version)
    process.exit(2)
  }
  return args
}

function run(tool, toolArgs, cwd) {
  // Windows resolves tar/unzip through .cmd/.exe shims picky about spawning.
  const result = spawnSync(tool, toolArgs, { encoding: 'utf8', cwd, shell: process.platform === 'win32' })
  if (result.status !== 0) {
    throw new Error(tool + ' ' + toolArgs.join(' ') + ' failed: ' + (result.stderr || result.stdout || 'exit ' + result.status))
  }
}

function extract(archive, dest) {
  mkdirSync(dest, { recursive: true })
  if (archive.endsWith('.zip')) {
    // bsdtar reads zip; GNU tar (plain Linux CI images) does not.
    if (process.platform === 'linux') run('unzip', ['-o', '-q', archive, '-d', dest])
    else run('tar', ['-xf', archive, '-C', dest])
    return
  }
  run('tar', ['-xzf', archive, '-C', dest])
}

function humanSize(bytes) {
  return (bytes / 1024 / 1024).toFixed(1) + ' MB'
}

function assembleMain(outDir, version) {
  const dest = join(outDir, 'dsh-cc-otel')
  rmSync(dest, { recursive: true, force: true })
  mkdirSync(dest, { recursive: true })
  const manifest = JSON.parse(readFileSync(join(MAIN_SOURCE, 'package.json'), 'utf8'))
  manifest.version = version
  for (const name of Object.keys(manifest.optionalDependencies ?? {})) {
    manifest.optionalDependencies[name] = version
  }
  if (manifest.dsh?.bundle?.patch === undefined) {
    throw new Error('npm/dsh-cc-otel/package.json lost its dsh.bundle.patch declaration')
  }
  writeFileSync(join(dest, 'package.json'), JSON.stringify(manifest, null, 2) + '\n')
  for (const file of ['cordis.patch.yml', 'README.md', 'LICENSE']) {
    copyFileSync(join(MAIN_SOURCE, file), join(dest, file))
  }
  cpSync(join(MAIN_SOURCE, 'lib'), join(dest, 'lib'), { recursive: true })
  cpSync(join(MAIN_SOURCE, 'bin'), join(dest, 'bin'), { recursive: true })
  return dest
}

function assemblePlatform(outDir, target, version, artifactsDir) {
  const exe = target.goos === 'windows' ? 'cc-otel.exe' : 'cc-otel'
  const archiveExt = target.goos === 'windows' ? 'zip' : 'tar.gz'
  const archive = join(artifactsDir, 'cc-otel_' + version + '_' + target.goos + '_' + target.goarch + '.' + archiveExt)
  if (!existsSync(archive)) throw new Error('missing goreleaser artifact ' + archive)
  const pkgName = '@young1lin/cc-otel-' + target.os + '-' + target.cpu
  const dest = join(outDir, 'cc-otel-' + target.os + '-' + target.cpu)
  rmSync(dest, { recursive: true, force: true })
  const extractDir = join(outDir, '.extract', target.goos + '-' + target.goarch)
  rmSync(extractDir, { recursive: true, force: true })
  extract(archive, extractDir)
  const extracted = join(extractDir, exe)
  if (!existsSync(extracted)) {
    throw new Error('archive ' + archive + ' did not contain ' + exe + ' (found: ' + readdirSync(extractDir).join(', ') + ')')
  }
  mkdirSync(dest, { recursive: true })
  copyFileSync(extracted, join(dest, exe))
  if (target.goos !== 'windows') chmodSync(join(dest, exe), 0o755)
  writeFileSync(join(dest, 'package.json'), JSON.stringify({
    name: pkgName,
    version,
    description: 'cc-otel binary for ' + target.os + ' ' + target.cpu + ' — installed via @young1lin/dsh-cc-otel',
    license: 'MIT',
    repository: { type: 'git', url: 'git+https://github.com/young1lin/cc-otel.git' },
    os: [target.os],
    cpu: [target.cpu],
    files: [exe],
  }, null, 2) + '\n')
  writeFileSync(join(dest, 'README.md'),
    '# ' + pkgName + '\n\n'
    + 'The cc-otel ' + target.os + '/' + target.cpu + ' binary (v' + version + ').\n'
    + 'Installed automatically as an optionalDependency of @young1lin/dsh-cc-otel;\n'
    + 'the npm os/cpu fields keep it from landing on other platforms.\n')
  return { pkgName, dest, size: statSync(join(dest, exe)).size }
}

const args = parseArgs(process.argv.slice(2))
const outDir = resolve(args.out)
const artifactsDir = resolve(args.artifacts)
mkdirSync(outDir, { recursive: true })

const lines = []
for (const target of PLATFORMS) {
  const { pkgName, dest, size } = assemblePlatform(outDir, target, args.version, artifactsDir)
  lines.push(pkgName + '  ' + humanSize(size) + '  -> ' + dest)
}
const mainDest = assembleMain(outDir, args.version)
lines.push('@young1lin/dsh-cc-otel'.padEnd(32) + '  -> ' + mainDest)

rmSync(join(outDir, '.extract'), { recursive: true, force: true })
console.log('build-npm: assembled ' + lines.length + ' packages for cc-otel v' + args.version)
for (const line of lines) console.log('  ' + line)
console.log('build-npm: publish with:  for d in ' + JSON.stringify(outDir) + '/*/; do (cd "$d" && npm publish --access public); done')
