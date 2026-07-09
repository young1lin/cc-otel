---
description: "Set up cc-otel: download/build binary, configure Claude Code OTEL telemetry, and start the dashboard service"
argument-hint: "[--force]"
---

# /cc-otel:setup

## Purpose

One-command setup for cc-otel telemetry dashboard. Downloads the latest binary (or builds from source), configures Claude Code's OTEL environment variables, and starts the background service.

## Contract

**Inputs:**
- `--force` — re-download/rebuild binary even if already installed

**Outputs:**
`STATUS=<INSTALLED|UPDATED|ALREADY_INSTALLED|FAIL>`

## Instructions

1. **Detect platform and architecture:**
   - OS: `uname -s` → `darwin`, `linux`, or check for Windows (`GOOS`)
   - Arch: `uname -m` → `amd64` / `arm64`

2. **Check if cc-otel is already installed:**
   - Check `~/.claude/cc-otel/cc-otel` (Unix) or `~/.claude/cc-otel/cc-otel.exe` (Windows)
   - Run `<bin>/cc-otel -v` — if it works and `--force` was not passed, skip to step 4.

3. **Download the latest release and install:**
   - Query GitHub API to get the actual download URL (release assets include version in filename):
     ```bash
     curl -sL https://api.github.com/repos/young1lin/cc-otel/releases/latest | jq -r '.assets[] | .browser_download_url'
     ```
   - Match the URL for the current platform: `{os}_{arch}` where os=`windows`/`darwin`/`linux`, arch=`amd64`/`arm64`.
     Windows uses `.zip`, others use `.tar.gz`.
   - If `curl`/`jq` are not available, or API fails, fallback to building from source:
     ```bash
     git clone https://github.com/young1lin/cc-otel.git /tmp/cc-otel-build && cd /tmp/cc-otel-build && go build -o cc-otel ./cmd/cc-otel/
     ```
   - Download the matched asset to a temp dir, extract it.
   - Run `./cc-otel install` — copies the binary to `~/.claude/cc-otel/cc-otel(.exe)`.
   - **No need to add to PATH** — all commands use absolute path to `~/.claude/cc-otel/cc-otel`.

4. **Configure Claude Code OTEL environment (merge, not replace):**
   - First, read cc-otel config to get the actual OTEL port:
     - Run `<bin>/cc-otel status` or read `~/.claude/cc-otel/cc-otel.yaml` to get `otel_port` (default: 4317)
   - Read `~/.claude/settings.json` (if not exists, start with `{}`)
   - Parse as JSON object
   - If top-level `"env"` key does not exist, create it as `{}`
   - **Only add/update** these keys inside `"env"` (do NOT delete any existing keys):
     ```
     CLAUDE_CODE_ENABLE_TELEMETRY = "1"
     OTEL_EXPORTER_OTLP_PROTOCOL = "grpc"
     OTEL_EXPORTER_OTLP_ENDPOINT = "http://localhost:<otel_port>"
     OTEL_METRICS_EXPORTER       = "otlp"
     OTEL_LOGS_EXPORTER          = "otlp"
     ```
     其中 `<otel_port>` 取自 cc-otel.yaml 的 `otel_port`，默认 4317
   - Preserve all other top-level keys (`"permissions"`, `"hooks"`, etc.) and all other `"env"` entries untouched
   - Write back with 2-space indent JSON formatting
   - **Example:** if settings.json is:
     ```json
     {
       "permissions": { "allow": ["Bash(npm run *)"] },
       "env": { "MY_VAR": "123" }
     }
     ```
     Result (assuming otel_port=4317):
     ```json
     {
       "permissions": { "allow": ["Bash(npm run *)"] },
       "env": {
         "MY_VAR": "123",
         "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
         "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
         "OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
         "OTEL_METRICS_EXPORTER": "otlp",
         "OTEL_LOGS_EXPORTER": "otlp"
       }
     }
     ```

5. **Initialize config if not exists:**
   - Run `<bin>/cc-otel init` to generate default config

6. **Start the service:**
   - Run `<bin>/cc-otel start`
   - Verify with `<bin>/cc-otel status`

7. **Report result:**
   - Print version, binary path, config path, DB path, dashboard URL
   - Remind user: "Restart Claude Code for OTEL env vars to take effect"

## Important Notes

- settings.json 操作是 **merge**，不是 replace。只在 `env` 内添加/更新上述 5 个 key，绝不删除任何已有 key
- 如果 settings.json 不存在，创建最小 JSON：`{"env": {...}}`
- 如果 settings.json 存在但无 `env` 字段，添加 `env` 字段，保留其他所有字段
- If download fails, suggest manual download from GitHub releases page
- Binary does NOT need to be in PATH — all slash commands use absolute path
