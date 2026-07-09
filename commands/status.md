---
description: "Show cc-otel service status, version, config path, today's token usage and cost summary"
---

# /cc-otel:status

## Purpose

Display current service status, version info, and today's token/cost summary inline in the terminal.

## Instructions

1. **Locate cc-otel binary:**
   - Primary: `~/.claude/cc-otel/cc-otel` (Unix) or `~/.claude/cc-otel/cc-otel.exe` (Windows)
   - Fallback: check if `cc-otel` is in PATH
   - If not found, tell user to run `/cc-otel:setup` first

2. **Show version:**
   - Run `<bin>/cc-otel -v` to get version

3. **Check service status:**
   - Run `<bin>/cc-otel status` and capture output
   - This shows: version, config path, DB path, PID, ports, today's stats
   - If not running, suggest `/cc-otel:start`

4. **Fetch dashboard data via API (if running):**
   - GET `http://localhost:8899/api/dashboard` (default, today's data)
   - Parse JSON response

5. **Display summary:**
   ```
   === cc-otel ===
   Version:    a55be87
   Config:     ~/.claude/cc-otel/cc-otel.yaml
   DB:         ~/.claude/cc-otel/cc-otel.db
   Service:    running (PID 12345)
   Dashboard:  http://localhost:8899

   --- Today ---
   Cost:       $12.34
   Input:      2.1M tokens
   Output:     45.6K tokens
   Cache Hit:  97.2%
   Requests:   156
   DB Size:    8.3 MB
   ```

6. **If API returns per-model breakdown, show top 3 models by cost.**
