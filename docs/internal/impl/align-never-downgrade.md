# align-never-downgrade — implementation record

Spec: docs/internal/design/align-never-downgrade.md (approved 2026-11-07, option B)
Base: ba8bc31 (== v0.1.7)

## Tasks

1. ✅ `npm/dsh-cc-otel/lib/versions.js` — parseDaemonVersion / isNewer / alignDecision pure functions
2. ✅ `index.js` align block (formerly :175 strict inequality) now dispatches on alignDecision: install / skip-stale-plugin (with hint) / skip-unmanaged-home (with warning); header comment and @param updated in step
3. ✅ `npm/dsh-cc-otel/lib/self-update.js` — maybeSelfUpdate: 24h throttle (tmpdir marker, written before the check to prevent a failure-retry storm), `npm view … version --json` for latest, DSH_PROFILE-first profile discovery (fallback: scan the profiles dir), pluginAutoUpdate=false warns only, mounted in apply() parallel to ensureDaemon
4. ✅ `npm/dsh-cc-otel/test/versions.test.mjs` — node --test, 5 cases (incl. the 2026-11-06 incident shape 0.1.3-vs-0.1.7 → skip-stale-plugin)
5. ✅ Documentation four-piece set (bilingual README align-semantics fix + `/cc-otel:setup` inaccuracy fix + bilingual CHANGELOG Unreleased); CLAUDE.md memory backfill; spec status → approved

## Evidence

- `node --check` ×4 (versions / self-update / index / test) all pass
- `node --test npm/dsh-cc-otel/test/versions.test.mjs` → **5 pass / 0 fail** (re-run after the spawn patch too)
- Driver smoke (bin/selfupdate-smoke.mjs, temporary, since deleted) → **6/6 passed**:
  ① same version silent + marker written ② newer version triggers the updater exactly once (profile=web) ③ pluginAutoUpdate=false warns only ④ throttle short-circuits (injecting 9.9.9 does not trigger) ⑤ hours=0 fully off ⑥ **real npm.cmd probe silent success**
- `go build ./...` → exit=0 (this change carries zero Go code; pre-commit gate pre-verified)
- Bilingual parity: pluginAutoUpdate appears once in each of both READMEs + both CHANGELOGs (README.md:505 / README_en-US.md:507 / CHANGELOG.md:14 / CHANGELOG_EN.md:14); the zh "只升不降" and en "upwards only" phrasings sit on lines 505/507 respectively
- Web suite not run: zero changes under internal/web/static/ (no UI touched; pre-commit's static-files trigger condition not met)

Final driver output (cmd.exe routing, zero deprecation warnings, 6/6):
```
case6 real npm probe: silent success (cmd.exe routing works)
SMOKE OK - all 6 cases passed; DSH_PROFILE=web; updater got: ["web"]
```

Historical output (intermediate shell:true version, with DEP0190, abandoned):
```
(node:75080) [DEP0190] DeprecationWarning: Passing args to a child process with shell option true can lead to security vulnerabilities, as the arguments are not escaped, only concatenated.
(Use `node --trace-deprecation ...` to show where the warning was created)
case6 real npm probe: silent success (npm.cmd spawn works)
SMOKE OK - all 6 cases passed; DSH_PROFILE=web; updater got: ["web"]
```

## Deviations (vs spec)

1. Option A read "single file lib/index.js"; self-update actually lives in its own `lib/self-update.js` (avoids an index.js↔self-update circular import; the spawn helper stays a local copy with a comment). Decision semantics match the spec exactly.
2. Windows spawn targets are npm.cmd / dsh.cmd; **Node ≥20.12 throws EINVAL spawning a .cmd directly without a shell** (CVE-2024-27980 mitigation; local node v24.20.0 — smoke case 6 hit it on first run). Patch evolution: `shell:true` works but raises the DEP0190 deprecation warning (which would pollute the dsh log on every 24h check) → final approach explicitly routes `.cmd/.bat` through `cmd.exe /d /s /c <cmd> <args…>` (spawns a real exe; no shell parsing, no warning; every argument is a plain token without metacharacters; smoke re-run confirmed zero warnings). Failure path = one warning + the manual command.
3. pluginAutoUpdate:false still performs the check and warns (per spec task 3's literal semantics); only updateCheckHours:0 turns everything off.
4. PRERELEASE_RE requires the suffix to start with a letter: a mangled describe shape ("0.1.6-1-abc123") does not enter prerelease but counts as "unparsable = unmanaged" (stricter than the spec's Decision section).
5. Run straight from the source tree, ownPackageVersion()=0.0.0 → self-update would raise an installed profile to latest (it never writes back into the source tree; acceptable).
6. AGENTS.md on this machine's checkout is a **17-byte pointer file** (contents exactly ".claude/CLAUDE.md"), not the hardlinked full mirror the v0.1.6 changelog described — nothing to sync; unequal hashes are by design. The CLAUDE.md backfill is in effect.

## Follow-ups

- Release goes through the release skill (shipped as v0.1.9: 0.1.8 was taken by the Cursor import / Clients drill-down line on another machine and did not carry this fix; this fix was rebased after that line and released as v0.1.9; the daemon has no Go changes related to this fix, so production Track B may skip the binary swap)
