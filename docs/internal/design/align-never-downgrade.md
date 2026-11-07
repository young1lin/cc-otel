# align-never-downgrade — dsh plugin align is upgrade-only + proactive plugin self-update
Date: 2026-11-06 / Author: agent session (incident: same-day prod downgrade) / Status: approved (2026-11-07, option B)

## Problem (user-visible)

At 2026-11-06 10:58, the stale plugin `@young1lin/dsh-cc-otel@0.1.3` in the dsh `web` profile "aligned" `~/.cc-otel/cc-otel.exe` (a calendar build manually deployed on 11-04) back down to its bundled 0.1.3 as the profile booted — the production daemon was downgraded automatically (the swapped-in exe's SHA256 matched the exe bundled in the plugin's node_modules byte for byte). Root cause: the align criterion was `npmVersion !== homeVersion` (strict inequality, direction-blind), so a plugin **older** than the installed daemon triggered `install` just the same. The user demanded a permanent fix: at minimum never downgrade again, and ideally the plugin should also upgrade itself proactively, eliminating the "plugin older than daemon" state outright.

## Facts found (file:line, each verified)

- `npm/dsh-cc-otel/lib/index.js` `ensureDaemon` (from :154), align criterion at **:175**: `if (npmVersion !== undefined && npmVersion !== homeVersion)` → `cc-otel install` (install stops the daemon, copies the binary), then `start`. The criterion is direction-blind. VERIFIED (re-checked on ba8bc31 == v0.1.7; line numbers undrifted)
- Same file :141-146, `versionOf` extracts the version via `-v`: release artifacts → `0.1.7`; local dev builds → `v0.1.6-1-gf78233e-dirty` (a git-describe suffix meaning "1 commit after v0.1.6" — **newer than the tag**). Under strict `!==` a dev build always counts as "different" → always overwritten. VERIFIED
- `commands/setup.md` :26-28: `/cc-otel:setup` skips when already installed (ALREADY_INSTALLED); only `--force` reinstalls → the Claude Code channel itself does **not** downgrade; the README.md:509 / README_en-US.md:511 claim of "checks the installed version and auto-updates to latest" was inaccurate (actual behavior: install-if-missing). VERIFIED
- Incident anchor: the same-day downgrade was triggered by the plugin @0.1.3; after `dsh plugin --profile web update` to @0.1.7, the plugin's bundled exe and the `~/.cc-otel` deployed binary hash-match (850898…), and align returned to no-op. VERIFIED
- `npm/dsh-cc-otel/` currently carries no JS tests at all (only lib/bin/cordis.patch.yml). VERIFIED
- Plugin and platform-binary packages release under the same tag and version (release skill §1.7: scripts/build-npm.mjs injects the optionalDependencies version) → "plugin older than daemon" can only arise from a manual daemon-side deploy or a long-stale plugin. VERIFIED
- Profile discovery: dsh injects `DSH_PROFILE=web` / `DSH_PROFILE_DIR` (observed live in a shell); whether they are visible **inside the plugin process is UNVERIFIED** — verify during implementation; fallback: scan `${DSH_HOME:-~/.dsh}/profiles/*/node_modules/@young1lin/dsh-cc-otel`.
- Hotspot distance: HOTSPOTS HS-01..HS-08 are all Go/static-asset hotspots; the JS plugin is not among them; nearest are HS-07 / DP-02 / DP-09 (instance topology, binary-swap ordering, PID-file traps) — this design does not change the install/start order and touches neither the Go side nor the database.

## Options considered (with tradeoffs)

| Option | Mechanism | Radius / cost | Migration compatibility | Tests |
|---|---|---|---|---|
| A. Upgrade-only guard | Version parsing + direction comparison: home missing → install; npm strictly newer than home → install; home version unparsable (dev/dirty/garbage) → skip + warn; home newer than npm → skip + hint to update the plugin | One file lib/index.js + new lib/versions.js; no blast radius beyond the tightened align behavior | No config migration; old plugin behavior = status quo (it downgrades), the new plugin only tightens | Table-driven unit tests over pure functions |
| **B. A + proactive plugin self-update (recommended)** | On top of A: at boot (24h throttle, cached in tmpdir) compare `npm view @young1lin/dsh-cc-otel dist-tags` against the running pkg version; newer available → run `dsh plugin --profile <DSH_PROFILE or scanned> update @young1lin/dsh-cc-otel` automatically (config `pluginAutoUpdate:false` turns it off, failure degrades to a warning); next boot the fresh plugin aligns the fresh binary **upward** | One extra external call + one self-mutation; failure mode = losing stats, no harm to sessions (the plugin is best-effort by design; telemetry and align never block) | Same as A; adds optional config keys | Unit tests + the same-day 0.1.3→0.1.7 facts as regression anchors |
| C. No code change | `manageHomeBinary:false` (daemon runs straight from node_modules — on Windows a plugin update is then locked out by the running exe, exactly what install-to-home exists to avoid); or keep things in sync by hand (the same day = the incident) | Zero code, but recurrence risk remains / introduces update lock-out | — | — |

C rejected: the same-day production downgrade is C's status-quo cost; `manageHomeBinary:false` trades a known update-lock-out problem for an unknown downgrade — net negative.

## Decision

Adopt **B** (superset of A). Core invariant:

**The home binary may be overwritten only when home is missing, or when its version parses and the npm-bundled version is strictly newer; every other case (unparsable version, home newer, equal) leaves it untouched, with the reason logged.**

Version-parsing details: `vX.Y.Z-N-g<sha>[-dirty]` (git-describe) parses as "N commits after base" and sorts **above** base but below the next patch (`0.1.6 < 0.1.6-1-g… < 0.1.7`); plain semver prerelease segments follow the standard order; parse failure (`dev`, empty, garbage) = unmanaged = never overwritten.

## Task list

1. New `npm/dsh-cc-otel/lib/versions.js`: `parseDaemonVersion(raw)`, `isNewer(a, b)` pure functions (comments in English).
2. Rework the `ensureDaemon` align block onto 1's comparison: home missing → install; `isNewer(npm, home)` → install; home unparsable → warn + skip; `isNewer(home, npm)` → info hint `dsh plugin --profile X update …` then skip; equal → keep today's no-op.
3. Self-update `maybeSelfUpdate(logger, config)`: 24h throttle (tmpdir marker file); `npm view` (10s timeout, silent-fail info, retried next boot); newer version available → spawn `dsh plugin --profile <DSH_PROFILE || scanned profile> update @young1lin/dsh-cc-otel`; with `pluginAutoUpdate:false` only warn and print the command. No failure may affect telemetry installation.
4. Tests `npm/dsh-cc-otel/test/versions.test.mjs` (node --test, patterned on internal/web/static/tests): parse table (`0.1.7` / `v0.1.7` / `v0.1.6-1-gf78233e-dirty` / `dev` / empty); ordering table (`0.1.6 < 0.1.6-1-g… < 0.1.7`; equal must not trigger); align decision table (the same-day incident shape: npm 0.1.3 vs home 0.1.7 → no install).
5. Docs: reword the README.md / README_en-US.md "Update the dsh plugin (npm)" sections to describe "align upwards only"; correct the inaccurate :509/:511 statements about `/cc-otel:setup` (install-if-missing + `--force`); bilingual CHANGELOG entries at release time (document skill sync).
6. Release (release skill Track A: tag → CI → npm); afterwards `dsh plugin --profile web update` pulls the fresh plugin. (Actually shipped as v0.1.9: 0.1.8 was already taken by another line of work — Cursor CSV import / Clients drill-down — which did not carry this fix.)
   The daemon has no Go-side changes from this fix; the new binary differs from 0.1.7 only in its version string (plus the v0.1.8-line Cursor/pricing changes) — production Track B may skip the swap and wait for the next release that carries Go changes.

## Verification plan

- `node --test npm/dsh-cc-otel/test/versions.test.mjs` fully green (incl. the decision table for the same-day incident shape).
- Manual e2e: temp profile with the @0.1.3 platform package + home at 0.1.7 → after boot home is still 0.1.7 and the log carries the "plugin older than installed daemon" hint; boot with equal versions → no install call (no-op).
- Self-update path: delete the throttle marker to force "due", boot once, confirm `dsh plugin update` is invoked (or only warns when disabled).
- Regression: telemetry.js untouched; `dsh plugin list --profile web` dependency tree unchanged; `node --check` every changed file.

## Rollback

A single-commit `git revert` returns to the status quo (the old, downgrading behavior); already-published npm versions cannot be retracted, so the fix can only land in ≥0.1.9; after rollback nothing reads the new config keys — harmless.

## Change history

- 2026-11-06 first draft (based on f78233e + local WIP).
- 2026-11-06 per user instruction the local WIP was dropped and, after `git pull`, re-verified on **ba8bc31 (== v0.1.7)**: `f78233e..ba8bc31` shows zero diff on `npm/`, `commands/`, and both READMEs — align code, criterion, README line numbers (:499/:509/:511) all unchanged, spec conclusions and task list stand; the align criterion's exact line is :175. The discarded WIP and upstream 40b57ef were two drafts of the same fix (the upstream implementation is the better one); no content was lost; backup at `bin/wip-discarded-20261106.patch`.
