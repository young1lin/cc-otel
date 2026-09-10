# DeepSeek Harness (DSH) Telemetry Setup

[DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) exports its **session ledger** — every user message, assistant message, tool call and tool result — rather than Claude Code's event vocabulary. cc-otel maps the one record that carries token accounting into an `api_request`, and drops the rest.

DSH speaks **OTLP/HTTP (`:4318`)**, not gRPC, and its endpoint must be the **full logs path** `/v1/logs`.

## Prerequisites

- cc-otel **v0.1.0 or newer** — DSH's shipped exporter sets `compression: gzip`, and earlier builds do not decompress, so every export is rejected as malformed
- cc-otel is running with `otlp_http_port: 4318` (the default)

## Configuration

DSH's `dsh-base` bundle already mounts a `session-telemetry-otel` row. It defaults to `DISABLED` and points at DeepSeek's own collector, so you only need to redirect it.

> **dsh 0.1.5+**: the `FULL` mode was removed upstream (2026-09) — a config carrying it fails validation and crashes the boot. The only accepted values are `FEEDBACK_ONLY` (default below) and `DISABLED`. Under `FEEDBACK_ONLY` ordinary conversation uploads **nothing**; an explicit like/dislike or `/feedback` uploads that session's full prefix once.

**Option A — environment variables (one shell, good for a first test):**

```powershell
$env:DSH_TELEMETRY_MODE = "FEEDBACK_ONLY"
$env:DSH_TELEMETRY_OTLP_URL = "http://localhost:4318/v1/logs"
npx @deepseek-ai/dsh web
```

**Option B — `$DSH_HOME/cordis.patch.yml` (permanent; `$DSH_HOME` defaults to `~/.dsh`):**

Append this as a **top-level** list item. Do not nest it inside an existing `insert:` block — it overrides the bundle's existing row by `id` rather than adding a new one.

```yaml
- id: session-telemetry-otel
  name: '@deepseek-ai/dsh-session-telemetry-otel'
  config:
    mode: !!js process.env.DSH_TELEMETRY_MODE || 'FEEDBACK_ONLY'
    shutdownTimeoutMillis: 3000
    exporter:
      url: !!js process.env.DSH_TELEMETRY_OTLP_URL ?? 'http://localhost:4318/v1/logs'
      compression: gzip
      timeoutMillis: 1000
    processor:
      scheduledDelayMillis: 10000
      maxQueueSize: 2048
      maxExportBatchSize: 2048
      exportTimeoutMillis: 1500
```

> A patch **replaces the row's whole `config`** rather than merging into it, so every field the bundle set has to be restated — omitting one silently reverts it to the plugin default, not to the bundle's value. Keep `name`: on a package rename the patch is skipped with a warning instead of silently sending telemetry back upstream. The two `!!js` expressions preserve env-var override (e.g. aim one shell at a dev instance on `14318`).

Verify the composed tree before booting:

```bash
npx @deepseek-ai/dsh web --dump-config
```

Exactly **one** `id: session-telemetry-otel` row should appear, carrying the local URL.

## Verify

1. Start DSH and complete one exchange — only an assembled `assistant/message` carries `usage`, so a cancelled turn records nothing
2. Trigger the export: like/dislike the reply or run `/feedback` (under `FEEDBACK_ONLY` this is the only thing that uploads — the session's full prefix, once)
3. Wait up to 10s (`scheduledDelayMillis`)
4. Open `http://localhost:8899`; rows appear with **Source** = `deepseek-harness`

For **continuous statistics** (no feedback clicks needed), backfill from the local session logs instead: `go run ./tools/dsh_ingest -db ~/.cc-otel/cc-otel.db` — idempotent, safe to re-run.

## Differences from Claude Code

- **No cost reported.** DSH sends no `cost_usd`, so cc-otel always recomputes from the local pricing table.
- **Only usage-bearing assistant messages are stored.** `step/start`, `assistant/chunk` and `step/end` are consumed to reconstruct `duration_ms` and `ttft_ms`; every other ledger record is dropped without being written. Note the v3 session format (dsh 0.1.5) no longer emits `assistant/chunk`, so v3-era uploads leave `ttft_ms` at 0.
- **History is not backfilled over OTLP.** DSH hands off events from its live boundary forward, so a session that existed before telemetry was enabled exports only what happens after — `tools/dsh_ingest` backfills the rest from local logs.
- **`request_id` is `dsh-v<session.format_version>-<session.id>-<event.seq>`** (falling back to the versionless `dsh-<session.id>-<event.seq>` when the attribute is missing), so retries of the same batch collapse onto one row and the renumbered v3 generation cannot collide with stored v0 rows.

## How cc-otel Identifies DSH Data

By the instrumentation scope `@deepseek-ai/dsh-session-telemetry-otel` (the `/ops` channel suffixes it), with Resource `service.name: deepseek-harness` as a fallback signal.

## Troubleshooting

| Symptom | Cause |
|---|---|
| No data at all | `mode` defaults to `DISABLED`; config is read at plugin load, so **restart DSH** after changing it |
| No data, config looks right | `DSH_TELEMETRY_DISABLED` vetoes everything when set to any non-empty value — including `0` and `false` |
| Nothing after a few seconds | Batch delay is 10s by default; that is not a failure |
| Only recent turns appear | Expected — events before telemetry was enabled are never handed off |
| Server logs `malformed OTLP payload` | cc-otel is older than v0.1.0 and cannot decompress DSH's gzipped exports |
| Cost is $0 | No price entry for that model (e.g. `glm-5.x` is not in the upstream catalog). Add it in the Pricing Table modal, then ↻ 重算历史 |

## Privacy

The feedback-triggered upload puts the **entire session prefix** on the wire — prompts, tool output, file contents, and the system prompt. cc-otel reads token counts from it and stores nothing else (`raw_otlp_events` stays empty; no prompt or tool-result rows are written for DSH sessions), but the content does cross the connection. Keep the endpoint on localhost.

`FEEDBACK_ONLY` (the default) exports only when the user records feedback; `DISABLED` keeps everything local. `FULL` no longer exists (dsh 0.1.5 removed it). Continuous statistics via `tools/dsh_ingest` never leave the machine.
