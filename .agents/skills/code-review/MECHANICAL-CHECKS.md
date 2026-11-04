# Mechanical checks — run before reading a single hunk

Cheap, automatable, and catch a large fraction of this repo's historical defects.

## Build & tests

```bash
gofmt -l . | grep -v bin/          # must be empty
go build ./...                     # silent success
go test ./... -count=1             # full suite for release/merge reviews;
                                    # scoped (./pkg/...) for fix-sized diffs
node --test internal/web/static/tests/*.test.mjs   # when any static file changed
```

## Forbidden UI patterns (pinned in CLAUDE.md — grep the diff)

```
stack: 'total'            # stacked bars — forbidden
trigger: 'axis'           # axis tooltip — forbidden (must be 'item')
emphasis: { focus: 'series' }  # forbidden
```

Also grep: `console.log` added in shipped static modules; `import.*echarts`
(modules read window.echarts); missing `?v=` bump in index.html when CSS/app.js
changed (and the pinned version assertion in tests/intraday-markup.test.mjs).

## Repository hygiene

- No `http.Error` in internal/api (use `writeAPIError`); no new endpoints without
  `rejectInvalidDateParams` where dates are accepted.
- No raw `db.Exec("PRAGMA ...")` outside dsn.go.
- No new writer to ttft_ms/duration_ms without a precedence comment (DP-11).
- Error wrapping: `fmt.Errorf("...: %w", err)`.
- Test companion rule: new pure helper in js/utils.js or theme.js gets a
  `.test.mjs`; new Go behavior gets a regression test naming its incident.

## Docs parity (when the diff is feat/fix scope)

- README.md and README_en-US.md both updated, same sections.
- CHANGELOG.md and CHANGELOG_EN.md both carry the entry.
- If ingest vocabulary changed: CLAUDE.md's wire-contract sections updated.
- If ports/config changed: cc-otel.yaml.example and the Ports table updated.

## Git hygiene

- Subject: Conventional Commits, English, imperative.
- No secrets/paths in the message (the privacy scan applies to commit text too).
