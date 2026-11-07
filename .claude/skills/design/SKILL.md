---
name: design
description: >
  Requirement triage and spec-writing for cc-otel changes. When the user brings a
  new requirement ("I want X / add Y / 能不能 Z") and code will follow, run this
  skill FIRST — it triages (small-fix / feature / refactor / data-layer), checks
  facts against CLAUDE.md and the actual code, drafts options with tradeoffs,
  writes the spec to docs/internal/design/<slug>.md, and STOPS at the user
  approval gate: no implementation code before explicit approval. Data-layer
  changes additionally force a rehearsal plan (the production DB is 500MB+).
  Triggers: design, spec, 方案, triage, how should we, 设计, 先出方案.
---

# Design (lifecycle)

## Step 1 — triage

| Class | Meaning | Extra requirements |
|---|---|---|
| small-fix | One file, no semantics change to money/data | straight to implement, spec optional (one-paragraph in the PR) |
| feature | New panel / endpoint / config | full spec + document-skill sync plan |
| refactor | Moves code, keeps behavior | spec lists invariants to prove unchanged (tests before/after) |
| **data-layer** | Touches schema, views, ingestion, pricing, migration | **mandatory**: rehearsal plan (snapshot + 25xxx ports + boot/memory budget per release skill 3.1) + number-reconciliation expectation |

## Step 2 — fact check (never design from memory)

- Grep the real code for the modules involved; read the relevant CLAUDE.md
  sections (topology, wire contracts, pricing rules, UI rules) and
  `.claude/rules/db-copy-no-stop.md` when DB handling is involved.
- List every fact with file:line. If a "fact" cannot be located, mark it
  UNVERIFIED — do not silently assume.

## Step 3 — options

Two or three genuine alternatives (including "don't do it / do less"),
each with: mechanism, blast radius (which hotspots from
`.claude/skills/code-review/HOTSPOTS.md` are nearby), migration/compat story,
test plan. State the recommendation and why.

## Step 4 — spec file

Write `docs/internal/design/<slug>.md` **in English** — author the spec (and
the matching `docs/internal/impl/<slug>.md` record) in English directly, never
zh-CN. Chinese is reserved for the zh halves of the bilingual pairs only
(README.md, CHANGELOG.md); quoting a zh phrase from those files as a grep
anchor is acceptable, prose is English (user directive 2026-11-11).

```markdown
# <slug> — <title>
Date / Author / Status: draft|approved
## Problem (user-visible)
## Facts found (file:line, each verified)
## Options considered (with the tradeoff table)
## Decision (which option, why)
## Task list (numbered, each independently verifiable)
## Verification plan (tests + smoke + numbers to reconcile)
## Rollback
```

## Step 5 — approval gate

Present the spec summary to the user and STOP. Implementation starts only on
explicit approval ("approved / 可以 / 就这么干"). If the user amends, update the
spec file (keep the change history in the doc) and re-present.
