---
name: pre-commit
description: >
  The sole entry point for committing in cc-otel. When the user says "commit / 提交 /
  push / 可以提交了", run this skill immediately — do NOT run a bare `git commit` or a
  generic commit command: they skip this repo's gates. Gates (in order): forbidden-path
  scan on the outgoing diff, privacy scan (usernames, absolute home paths, API keys),
  gofmt, go build, scoped go test, web tests when static files changed, bilingual
  CHANGELOG reminder for feat/fix. All green -> this skill itself performs the
  git add/commit (Conventional Commits, English subject). Gate red -> stop and report,
  never commit past a red light. Triggers: commit, 提交, 提交代码, 可以提交了, push, 推送,
  推一下, 帮我提交, pre-commit.
---

# Pre-commit gates + commit (lifecycle)

Only check what the **outgoing diff actually touches** — never blind full-repo scans.
A red gate stops the commit. After all gates pass, the commit step is done **by this
skill**; do not hand the user a "checked, now you commit".

## Gate 1 — scope: forbidden paths in the outgoing diff

`git status --short` + `git diff --stat` (+ `--cached` if staged). Reject if any hunk touches:

| Path | Why |
|---|---|
| `bin/`, `*.db`, `*.db-wal`, `*.db-shm`, `*.exe` | build artifacts / live databases |
| `.cursor/` | private editor rules — #L1: one leaked here and had to be history-rewritten off GitHub |
| `.claude/settings.json`, `.claude/settings.local.json`, `.claude/docs/` | local-only config/memory |
| `.agents/settings*`, `.agents/docs/` | mirrored local-only paths through the junction view |
| `skills.zip`, `bin/skills-review/` | imported reference material, never tracked |
| `*`-pre-rewrite.bundle`, `*.snap` | rollback artifacts that contain pre-scrub history |

If the diff contains a new file type never seen before, ask before staging it.

## Gate 2 — privacy scan on the diff content

`git diff` text must not contain:

- `C:\Users\` or any real home-directory path (use `~` or `%USERPROFILE%` in docs/scripts)
- the owner's real personal name (the one behind the Windows profile / git identity)
- key-shaped strings: `sk-`, `zai-`, `ghp_`, `AKIA`, long hex blobs
- internal hostnames/ports presented as production facts when they are machine-local

Run `git diff | Select-String -Pattern 'C:\\Users|sk-|zai-|ghp_'` (or ripgrep equivalent).
One hit = red. This gate exists because Gate-1's `.cursor/` row was learned the hard way.

## Gate 3 — format & build

- `gofmt -l .` (excluding `bin/`, `node_modules/`) -> must be empty; else `gofmt -w` the listed files.
- `go build ./...` -> zero output.

## Gate 4 — tests scoped to the change

- Go packages touched by the diff: `go test ./<pkg>/... -count=1` green.
- Any file under `internal/web/static/`: `node --test internal/web/static/tests/*.test.mjs` green
  (or `node internal/web/static/tests/run-all.mjs`).
- Static-file changes additionally remind: embedded `go:embed` snapshot means the daemon
  must be rebuilt+restarted to serve them (CLAUDE.md "How frontend changes take effect").

## Gate 5 — CHANGELOG parity reminder (non-blocking unless user asks)

If the diff contains `feat`/`fix` scope and neither `CHANGELOG.md` nor `CHANGELOG_EN.md`
is in the diff, remind: both files must gain the entry (zh and en, same semantics).
Blocking only when the user asked for release-readiness.

## Step 6 — execute the commit

- Stage exactly the reviewed paths (never `git add -A` unless every untracked file passed Gate 1).
- Conventional Commit subject in **English**, imperative, <= 72 chars; body may carry the
  why + evidence. Reference specs via `docs/internal/design/<slug>.md` when one exists.
- After committing, print the hash + one-line diffstat. If the user also said push,
  push the current branch; **never force-push** without an explicit user instruction
  (history rewrites require the user's own sign-off).
