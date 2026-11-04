---
name: implement
description: >
  Executes an approved design spec task-by-task under the repo's coding guards.
  Enter when the user says "implement / 按方案做 / go ahead / 开工" referencing a
  spec at docs/internal/design/<slug>.md (or approving a small-fix verbally).
  Reads CONVENTIONS.md in this directory for the repo's Go/web conventions,
  executes tasks in order with per-task verification (build + scoped tests +
  dev-instance smoke when UI-facing), records deviations and evidence into
  docs/internal/impl/<slug>.md, and hands the finished diff to the pre-commit
  skill — it never commits past the gates itself. Triggers: implement, 按方案做,
  开工, build it, do the tasks.
---

# Implement (lifecycle)

## Step 1 — load context

- Read the spec (docs/internal/design/<slug>.md). If none exists and the change
  is more than a small-fix, stop and route through the design skill.
- Read CONVENTIONS.md here; read CLAUDE.md sections for every module the spec touches.

## Step 2 — execute tasks in order

For each numbered task in the spec:

1. Implement exactly the task; resist scope creep (new discoveries go into the
   impl record as follow-ups, or back to design if they change the approach).
2. Verify: gofmt + scoped go test (+ web tests when static files changed);
   UI-facing tasks additionally rebuild + restart the dev instance (:18899) and
   check the API/panel per CLAUDE.md's verification checklist.
3. Record in docs/internal/impl/<slug>.md: task id, what deviated and why,
   evidence (commands + numbers). An impl record with zero deviations is fine;
   a missing impl record is not.

## Step 3 — cross-cutting verification

- Full go test ./... + web suite once after the last task.
- Data-layer specs: run the rehearsal from release skill 3.1 before declaring done.
- Any new writer to ttft/duration/cost: state its precedence tier (DP-11) and
  add the pairwise test.

## Step 4 — handoff

Summarize: tasks done, verification evidence, follow-ups. Then route the diff
into the **pre-commit** skill for gated commit — this skill never commits itself.
If the user wants review first, route to **code-review**.
