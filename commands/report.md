---
description: "Generate a cost report for a specific date range from cc-otel data"
argument-hint: "[today|7d|30d|all]"
---

# /cc-otel:report

## Purpose

Generate a formatted cost and usage report from cc-otel data, useful for tracking spending.

## Contract

**Inputs:**
- `$1` — Date range: `today` (default), `7d`, `30d`, `all`

**Outputs:**
Formatted cost report with per-model breakdown.

## Instructions

1. **Determine date range from argument:**
   - `today`: `?from=<today>&to=<today>`
   - `7d`: `?from=<7 days ago>&to=<today>`
   - `30d`: `?from=<30 days ago>&to=<today>`
   - `all`: no date filter

2. **Fetch data:**
   - GET `http://localhost:8899/api/dashboard?from={from}&to={to}`
   - GET `http://localhost:8899/api/daily-detail?from={from}&to={to}`

3. **Format report:**
   ```
   ╔══════════════════════════════════════╗
   ║   CC-OTEL Cost Report (7 Days)      ║
   ╠══════════════════════════════════════╣
   ║ Total Cost:    $155.06              ║
   ║ Total Input:   144.5M tokens       ║
   ║ Total Output:  828.8K tokens       ║
   ║ Cache Hit:     99.4%               ║
   ║ Requests:      2,053               ║
   ╠══════════════════════════════════════╣
   ║ Model Breakdown:                    ║
   ║  glm-5v-turbo   $63.40  774 req    ║
   ║  glm-5-turbo    $56.18  783 req    ║
   ║  step-3.5-flash $14.00   43 req    ║
   ║  claude-opus-4-6 $11.96  179 req    ║
   ║  ...                                ║
   ╚══════════════════════════════════════╝
   ```

4. **If service is not running, report error and suggest `/cc-otel:start`.**
