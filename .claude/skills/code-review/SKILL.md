---
name: code-review
description: >
  The repo-aware code review entry point for cc-otel. When the user says "review /
  评审 / code review / 看看代码", run this — not a generic review pass. Three layers,
  in order: (1) mechanical checks (MECHANICAL-CHECKS.md — build/tests/forbidden
  chart patterns/gofmt), (2) repo-specific defect patterns (DEFECT-PATTERNS.md —
  12 patterns earned in this repo's own history, each with its incident), (3)
  hotspots sweep (HOTSPOTS.md — the files where correctness is most fragile).
  Report format: P0 (data corruption / wrong money) / P1 (feature broken) /
  P2 (edge cases, perf) / P3 (style, docs), each finding cites file:line and the
  matching pattern id. Triggers: review, 评审, code review, 审查, 帮我看看代码.
---

# Repo-aware code review (lifecycle)

## When to use

- User asks for a review of a branch/diff/PR before merge or release.
- The pre-commit skill's Step 8 refers a risky diff here.
- Post-incident: reviewing a fix against the pattern library.

## Procedure

### Step 1 — scope

Ask or infer: which commits (`main...HEAD`? a single fix?). Get
`git diff --stat <base>...HEAD` and read every changed file's hunks.
Focus depth on HOTSPOTS.md files; other files get the mechanical layer + skim.

### Step 2 — mechanical checks (MECHANICAL-CHECKS.md)

Run the full checklist there. Any red = P1 minimum.

### Step 3 — defect-pattern sweep (DEFECT-PATTERNS.md)

For each pattern DP-01..DP-12, check whether the diff could reintroduce it.
The patterns are ordered by how often this repo has been bitten.

### Step 4 — hotspot invariants (HOTSPOTS.md)

Each hotspot lists its invariants (e.g. "pricing predicate must change at all
three call sites"). Verify each one explicitly in the diff.

### Step 5 — report

```
## Findings
- [P0] <one-line> — <file>:<line> (DP-xx / HS-xx)
  evidence: <command output or reasoning>
  fix: <concrete suggestion>
## Verified-OK
- <invariant checks that passed, with evidence>
```

Never report a finding without evidence (a command, a line, a number).
For UI-affecting diffs, additionally apply CLAUDE.md's "Change verification
checklist" — rebuild, restart, API check, and say plainly when no browser
screenshot was possible.

## Not in scope

- Generic style opinions not backed by a pattern or convention file.
- Rewriting the author's design; flag design doubts as P2 questions instead.
