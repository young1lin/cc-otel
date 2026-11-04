---
name: document
description: >
  Bilingual documentation maintenance for cc-otel. Enter when the user says
  "docs / 文档 / update README / changelog" or when a diff changes features,
  endpoints, config, or wire contracts. Owns four artifacts and their parity:
  README.md <-> README_en-US.md (same sections, same facts), CHANGELOG.md <->
  CHANGELOG_EN.md (same semantics), .claude/CLAUDE.md (project memory — backfill
  after behavior changes, prune after removals), and the skills/ docs under
  .claude/skills. Never edits one half of a bilingual pair alone; never invents
  facts — every doc claim traces to code. Triggers: docs, 文档, 文档同步,
  update readme, changelog, 文档更新.
---

# Document (lifecycle)

## Pairs and their parity gates

| Pair | Gate |
|---|---|
| README.md / README_en-US.md | same sections exist in both; feature lists, panel tables, config tables, and cc-otel.yaml.example agree |
| CHANGELOG.md / CHANGELOG_EN.md | entry exists in both, same semantics, same version heading and date |
| .claude/CLAUDE.md | single source (no translation); structure preserved when editing |
| .claude/skills/** | English only, per suite convention |

## When CLAUDE.md gets backfilled

- New endpoint family / config key / env var -> Commands, Configuration, Ports tables.
- Ingest behavior change -> the wire-contract sections (Codex / DSH / opencode),
  written from the parser code, not from memory.
- Pricing semantics -> the pricing section, all three call sites mentioned.
- Retired behavior -> prune the section; do not leave "legacy" prose that
  contradicts code.

## Prose rules

- Every behavioral claim traces to a file or endpoint; if you cannot cite it,
  verify in code first or mark it clearly as planned.
- Numbers (port lists, default values, table names) come from code/config —
  never from a previous doc revision (docs drift; code is truth).
- Upgrade/compat notes are mandatory in changelog entries for anything touching
  schema, ports, or defaults.

## Suggested flow

1. `git diff` (or the feature description) -> list which of the four artifacts
   are affected.
2. Edit the authoritative side first (usually zh for README/CHANGELOG), then
   mirror; then cc-otel.yaml.example; then CLAUDE.md memory.
3. Self-check: grep both halves for the feature keyword — a hit in exactly one
   language is a parity failure.
