---
event: SessionStart
description: "Check if cc-otel is running and remind the user if not"
---

# cc-otel Session Start Check

On session start, silently check if cc-otel is running by testing TCP connectivity to port 4317.

## Logic

```bash
# Quick TCP check — no output if running, gentle reminder if not
if ! (echo > /dev/tcp/localhost/4317) 2>/dev/null; then
  echo "💡 cc-otel is not running. Use /cc-otel:start to enable telemetry dashboard."
fi
```

## Behavior

- **If running:** Do nothing. No output. Zero cost.
- **If not running:** Print a one-line reminder. Non-blocking, informational only.
- **If cc-otel is not installed:** Do nothing (the TCP check will simply fail silently).

This hook must be lightweight — no HTTP calls, no binary invocations, just a TCP port check.
