# cc-otel Agent Skill Suite

Lifecycle skills covering `spec -> implement -> document -> commit -> review -> release`,
custom-built for this repository (Go + SQLite + embedded web UI, dual-instance topology).
Not a generic template: every gate, hotspot, and defect pattern below was earned in this
repo's own history (five 25-agent review rounds, a production rollout, and one privacy leak).

## Layout

| Skill | Role | Entry words |
|---|---|---|
| `design/` | Requirement triage -> fact check -> options -> spec file -> user approval gate (no code before approval) | design, spec, 方案, triage |
| `implement/` | Execute an approved spec task-by-task under `CONVENTIONS.md` guards; writes the impl record | implement, 按方案做, build it |
| `document/` | Bilingual doc parity gates (README/CHANGELOG zh<->en), CLAUDE.md memory backfill, skills sync | document, docs, 文档 |
| `pre-commit/` | Outgoing-diff gates (scope, privacy scan, build, tests) then executes the commit itself | commit, 提交, push |
| `code-review/` | Mechanical checks + repo-specific defect patterns + hotspots | review, 评审, code review |
| `release/` | npm/GitHub release checklist **plus** the production-instance rollout runbook (rehearsal -> hash-verified swap -> smoke matrix) | release, 发版, deploy prod |

## Where artifacts land

```
docs/internal/design/<slug>.md   # design specs (tracked - cross-machine)
docs/internal/impl/<slug>.md     # implementation records (tracked)
.claude/docs/plans/              # throwaway task plans (local only, gitignored)
```

## Authoritative sources these skills reference (never duplicate)

- `.claude/CLAUDE.md` — project memory: ports, topology, wire contracts, pricing rules, UI rules
- `.claude/rules/db-copy-no-stop.md` — iron rule: never stop a daemon to copy its DB
- `.claude/harness/FRONTEND_TASTE.md` — visual standards for every UI change
- `docs/MERGE_AND_RECOMPUTE.md` — DB merge + cost-recompute procedure

These skills add *process* on top; when a skill and CLAUDE.md disagree, CLAUDE.md wins.
