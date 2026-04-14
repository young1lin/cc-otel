---
description: "Open cc-otel Web dashboard in your default browser"
---

# /cc-otel:open

## Instructions

1. **Determine web port** (default 8899):
   - Try `~/.claude/cc-otel/cc-otel status` to check if running and get port
   - Or use default 8899

2. **If service not running, start it first:**
   - Run `~/.claude/cc-otel/cc-otel start` (Unix) or `~/.claude/cc-otel/cc-otel.exe start` (Windows)

3. **Open browser:**
   - macOS: `open http://localhost:8899`
   - Linux: `xdg-open http://localhost:8899`
   - Windows: `start http://localhost:8899`
