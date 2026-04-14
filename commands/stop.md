---
description: "Stop the cc-otel background service"
---

# /cc-otel:stop

## Instructions

1. **Locate cc-otel binary:**
   - Primary: `~/.claude/cc-otel/cc-otel` (Unix) or `~/.claude/cc-otel/cc-otel.exe` (Windows)
   - Fallback: check if `cc-otel` is in PATH
   - If not found, tell user to run `/cc-otel:setup` first

2. Run `<bin>/cc-otel stop`
3. Report stopped PID or "not running" if already stopped
