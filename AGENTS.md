# AGENTS.md

## Cursor Cloud specific instructions

`cc-otel` is a single self-contained Go binary: it runs an OTLP gRPC receiver (`:4317`)
and a web dashboard + REST API (`:8899`) in one process, backed by an embedded pure-Go
SQLite database (no external DB/cache/broker to start). See `README_en-US.md` and the
`Makefile` for the canonical commands.

### Build / lint / test / run
- Build: `make build` → produces `bin/cc-otel`.
- Lint: `make lint` (runs `go vet ./...`).
- Test: `make test` (or `go test ./...`).
- Run (dev, foreground): `./bin/cc-otel serve`. This starts both the OTLP receiver
  and the dashboard. Open `http://localhost:8899`. Other subcommands: `start`/`stop`/
  `restart`/`status` (daemon mode), `init`, `version`.

### Non-obvious caveats
- Data dir is location-dependent: when the binary lives in a `bin/` directory it uses
  `bin/` as its data dir (config `cc-otel.yaml`, DB `cc-otel.db`, pid/log). Elsewhere it
  defaults to `~/.claude/cc-otel/`. Override with `CC_OTEL_DB_PATH`, `CC_OTEL_WEB_PORT`,
  `CC_OTEL_OTEL_PORT`. `bin/` and `*.db` are gitignored, so runtime state is never committed.
- To iterate on the web UI without recompiling, set `CC_OTEL_STATIC_DIR` to
  `internal/web/static` (assets are otherwise `go:embed`-bundled).
- Ingestion is log-driven, not metric-driven: the OTLP **metrics** service is accepted
  but intentionally NOT persisted (`internal/receiver/receiver.go`). Persisted rows come
  from OTLP **log** records whose `event.name` is `api_request` (plus `user_prompt`,
  `tool_decision`, etc.). To seed test data, send OTLP *logs* to `:4317`, e.g. point a
  telemetry producer at it with `CLAUDE_CODE_ENABLE_TELEMETRY=1` and
  `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317`, or craft an OTLP LogsService client.
- On startup a background pricing refresh fetches model prices over HTTPS
  (LiteLLM + OpenRouter). It is optional — an embedded seed (`internal/pricing/embed/seed.json`)
  makes the app work fully offline, and it can be disabled via `pricing_refresh.enabled: false`
  in `cc-otel.yaml`.
