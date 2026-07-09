---
description: "Start cc-otel background service (OTLP receiver + Web dashboard)"
---

# /cc-otel:start

## Purpose

Start the cc-otel daemon process in the background.

## Instructions

1. **Locate cc-otel binary:**
   - Primary: `~/.claude/cc-otel/cc-otel` (Unix) or `~/.claude/cc-otel/cc-otel.exe` (Windows)
   - Fallback: check if `cc-otel` is in PATH
   - If not found, tell user to run `/cc-otel:setup` first

2. **Check if already running:**
   - Run `<bin>/cc-otel status`
   - If running, report current status and exit

3. **Start the daemon:**
   - Run `<bin>/cc-otel start`
   - Wait for confirmation output

4. **Verify:**
   - Run `<bin>/cc-otel status` to confirm startup
   - Report: version, PID, OTEL port, Web UI URL, config path

5. **Output example:**
   ```
   cc-otel started (PID 12345)
     Version:       a55be87
     Config:        ~/.claude/cc-otel/cc-otel.yaml
     DB:            ~/.claude/cc-otel/cc-otel.db
     OTEL receiver: localhost:4317
     Web dashboard: http://localhost:8899
   ```
