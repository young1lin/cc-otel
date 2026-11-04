---
name: release
description: >
  The sole entry point for releasing cc-otel — both the public npm/GitHub release
  (tag push -> goreleaser -> npm Trusted Publishing) and the production-instance
  rollout (~/.cc-otel binary upgrade: rehearsal -> hash-verified swap -> smoke
  matrix -> rollback). When the user says "release / 发版 / deploy prod / patch /
  publish vX.Y.Z", run this skill end to end; do NOT push tags or swap production
  binaries directly. Bare triggers like "deploy 一下" without qualifiers still
  enter here — the skill decides which track applies. Triggers: release, 发版,
  publish, tag release, deploy production, patch release, can we release.
---

# Release (lifecycle)

Two tracks, both owned by this skill:
- **Track A — public release**: tag -> GitHub Actions -> goreleaser -> npm.
- **Track B — production rollout**: upgrading the running ~/.cc-otel instance.

## 0. Release mechanics (background, do not change)

- Pushing a vX.Y.Z tag triggers .github/workflows/publish.yml: goreleaser
  (.goreleaser.yml, version injected via ldflags main.version) builds all
  platform artifacts and creates the GitHub Release; npm Trusted Publishing then
  publishes @young1lin/dsh-cc-otel plus platform binary packages (no token).
- Prerelease tags (v1.2.3-rc.1) publish to the npm next dist-tag, never over latest.
- CI unit tests run in .github/workflows/test.yml (go test + race; web tests run locally).

## 1. Pre-release checklist (every time, no skips)

### 1.1 Content sanity

- git status --short clean; git log accounts for every feature/fix being released.
- go test ./... -count=1 green (optionally CC_OTEL_TEST_NOSYNC=1).
- Web suite: node --test internal/web/static/tests/*.test.mjs green.
- go build ./... succeeds; dev-instance smoke recommended: bin/cc-otel.exe
  on :18899, open the panels/endpoints this change touched.

### 1.1b UI changes are included (mandatory)

- Read .claude/harness/FRONTEND_TASTE.md completely first, plus CLAUDE.md's
  Frontend Development Rules / Web UI Data and Display Rules.
- The three chart commandments, before AND after touching chart code: no
  stack: 'total', no trigger: 'axis', no emphasis: { focus: 'series' };
  minimum bar granularity is time x model.
- Input semantics: Input = Uncached + Cache Read + Cache Create (all three),
  Output is not Input; header/tooltip/KPI agree everywhere.
- Verification standard: web tests green + relevant Go tests + rebuild/restart the
  dev instance (14317/18899) + open the page with cache-busting; without a browser
  tool, at least verify JSON vs display logic via API and say plainly that no
  screenshot was taken.
- CSS or root-module changes bump ?v= in index.html AND the pinned assertion in
  tests/intraday-markup.test.mjs.

### 1.2 CHANGELOG, both languages

- CHANGELOG.md and CHANGELOG_EN.md both carry the entry with identical semantics.
- Rename "## [Unreleased]" to "## [vX.Y.Z] - YYYY-MM-DD" (create the section if absent).
- Entries answer: what / why / how (key tradeoffs, files, behavior changes) — not one-liners.

### 1.3 README, both languages

- New features in the Features section; new panels in the Panels section; new
  config keys/env vars in the Configuration tables AND cc-otel.yaml.example.
- README.md and README_en-US.md updated together, never just one.
- Offer to refresh stale UI screenshots.

### 1.4 Version number (patch by default)

| Situation | Action |
|---|---|
| Bug fixes / internal polish only | patch (0.2.0 -> 0.2.1; default) |
| New backward-compatible capability (panel/API/config) | minor (0.1.x -> 0.2.0) |
| Breaking change (config schema / API behavior / incompatible DB) | major, with migration notes in CHANGELOG |

The version lives only in the git tag and CHANGELOG headings (goreleaser injects it);
never hardcode it in source.

### 1.5 Pricing seed refresh (rerun at release)

- go run ./tools/dump_pricing_snapshot regenerates
  internal/pricing/embed/seed.json (offline fallback prices) from
  BerriAI/litellm; then go test ./internal/pricing/. Claude entries are
  intentionally absent — do not add them by hand. If offline, skip it and note
  "seed not refreshed" in the release notes; the existing seed remains the fallback.

### 1.6 Privacy check (public repo — mandatory before pushing)

- Scan git diff <previous-tag>..HEAD for: personal paths (C:\Users\...),
  secrets (sk- / api[_-]?key / password / Bearer), real personal usage
  numbers (your token counts / costs).
- Confirm .gitignore covers bin/, *.db, *.exe, *.bak, cc-otel.yaml, .env;
  git ls-files bin/ must be empty.
- Commit messages are subject to the same scan. (#L1: a .cursor/ rules file once
  leaked and required a filter-repo history rewrite + force push.)

### 1.7 npm Trusted Publishing pitfalls (bought with three v0.1.4 failures)

Pipeline: goreleaser (assets) -> npm job (scripts/build-npm.mjs grouping 7
packages -> npm publish). Auth is OIDC trusted publishing; there is no NPM_TOKEN.

1. Register a Trusted Publisher per package on npmjs (one-time, only the
   account owner can do it): package Settings -> Trusted publishing -> GitHub repo/workflow/filename.
2. Strip setup-node's injected token in CI: setup-node writes
   _authToken=$NODE_AUTH_TOKEN into the runner .npmrc; the workflow removes
   it — keep that step.
3. npm publish --provenance: provenance consumes the OIDC token (same ride).
   Already in the workflow; do not remove.

Diagnosis order when the npm job fails: 404 PUT = condition 2 broken; ENEEDAUTH =
exchange failed (usually condition 1 or a workflow filename mismatch);
E409 "Cannot publish over" = version already published.

## 2. Release steps (Track A)

```powershell
# 1) After 1.x all pass, commit the release prep (CHANGELOG finalized + README updates)
git add CHANGELOG.md CHANGELOG_EN.md README.md README_en-US.md
git commit -m "chore(release): prepare vX.Y.Z"

# 2) Tag (version decided by 1.4)
git tag vX.Y.Z

# 3) Push (this triggers the public release: goreleaser + npm)
git push origin main --tags

# 4) Watch until done
#    https://github.com/young1lin/cc-otel/actions  Release workflow green
#    Release page shows vX.Y.Z assets; npm view @young1lin/dsh-cc-otel version
```

## 3. Production rollout (Track B — the ~/.cc-otel upgrade runbook)

The iron rule forbids stopping the production daemon **to copy its DB** (use
VACUUM INTO snapshots, see .claude/rules/db-copy-no-stop.md). A **user-directed
binary upgrade** may stop it briefly — and then only in this exact order.

Incident anchor **#R1** (DP-02): copying over a running exe fails *silently* on
Windows; the daemon then restarts on the old binary and /api/status.version
comes back empty. Order and hash verification below exist because of it.

### 3.1 Rehearse on a snapshot first (never gamble on production)

```powershell
# online snapshot; production keeps running
go run ./tools/snapshot_db "$env:USERPROFILE\.cc-otel\cc-otel.db" bin\prod-rehearsal.db.snap
# temp config on rehearsal ports (25317/25318/25899), rename .snap -> .db
# foreground instance (PID-file trap: DP-09 — 'start' would refuse)
./bin/cc-otel.exe serve -config bin/prod-rehearsal.yaml   # background job
```

Measure and record: **boot-to-ready seconds** (poll /api/status), **peak working
set MB** (sample during boot), and **number reconciliation** (requests/cost before
vs after migration; deltas must be explainable — e.g. +30,730 autoimported archive
rows). Budget: boot < 60s and peak < 300MB for a ~500MB DB; exceeding either =
investigate before touching production.

### 3.2 Build and swap (stop -> copy -> hash-verify -> start)

```powershell
go build -ldflags '-s -w -X main.version=vX.Y.Z' -o bin/cc-otel-new.exe ./cmd/cc-otel/
$dir = "$env:USERPROFILE\.cc-otel"
Copy-Item "$dir\cc-otel.exe" "$dir\cc-otel.exe.bak-prev" -Force   # rollback copy
& "$dir\cc-otel.exe" stop        # STOP first — #R1
Copy-Item bin\cc-otel-new.exe "$dir\cc-otel.exe" -Force
# hash compare source vs destination — never skip
(Get-FileHash bin\cc-otel-new.exe -Algorithm SHA256).Hash -eq
  (Get-FileHash "$dir\cc-otel.exe" -Algorithm SHA256).Hash
& "$dir\cc-otel.exe" start
```

### 3.3 Smoke matrix (all green or roll back)

- GET /api/status -> version matches, db_ok true; **empty version = wrong binary (#R1)**.
- Base endpoints 200: dashboard, daily, calendar, sessions, requests, durations,
  models, hours, rate, clients.
- Mirrors 200: /api/codex/{dashboard,daily,calendar,sessions,requests,durations,models,intraday}.
- Envelope: ?from=2026-99-99 -> 400 with {"error":{"code":"invalid_date"}}.
- Static: ETag present, If-None-Match -> 304 (headers are case-insensitive — DP-04).
- Live traffic lands: run one Codex round (your local CODEX_HOME pointing at
  :4317) and confirm the row appears with ttft > 0 and ttft <= duration (DP-06
  invariant).
- Number reconciliation vs the pre-upgrade baseline.

### 3.4 Rollback

Stop; restore cc-otel.exe.bak-prev; start; re-run the smoke matrix. The DB
schema is additive-only (ensureColumn + view rebuild), so a rollback of the binary
with the migrated DB is safe — new columns simply sit unused. For data-level
disasters, restore from the rehearsal snapshot taken in 3.1.

### 3.5 Post-rollout

- Clean rehearsal artifacts (bin/prod-rehearsal.*).
- Track A reminder: if the tag was created locally but only main was pushed,
  push the tag itself (git push origin vX.Y.Z) — a missing tag push once left
  the public release behind the running production binary.

## 4. Release-note quality bar (written for future readers)

Every CHANGELOG entry answers:
1. **What**: the user-visible change.
2. **Why**: motivation / the bug fixed (reproduction cues when useful).
3. **How**: key technical points (tradeoffs, data sources, endpoints/tables/files).
4. **Upgrade notes**: migrations needed, old-data compatibility, new config defaults.
