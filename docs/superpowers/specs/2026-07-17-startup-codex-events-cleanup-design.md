# Startup Legacy Codex Events Cleanup

## Objective

Temporarily remove legacy rows from the compatibility-only `codex_events`
table after every daemon start. The cleanup must not delay OTLP or Web startup
and is intended to be removed in a later release after deployed databases have
had enough opportunities to clean themselves.

## Behavior

- Start one cleanup goroutine after the daemon has initialized its repository.
- Treat every row in `codex_events` as legacy because the receiver no longer
  writes this table and database imports ignore it.
- Delete at most 10,000 rows per transaction, then continue until the table is
  empty.
- Log the final deleted-row count in English when at least one row was removed.
- On any database error, log the error in English and stop the goroutine. The
  next daemon start retries the remaining rows.
- Do not run `VACUUM`; normal SQLite page reuse and existing maintenance remain
  unchanged.

## Repository API

Add a repository method that deletes one bounded batch and returns the number
of affected rows. The SQL must select row IDs with `LIMIT ?` and delete only
those rows in a single statement/transaction boundary supplied by SQLite.

The daemon owns the loop so startup scheduling stays visible in `main.go` and
the repository method remains independently testable.

## Tests

- Repository test: more than one batch is removed across repeated calls and an
  empty table returns zero.
- Daemon source test: startup schedules the new cleanup function in a
  goroutine, replacing the previous assertion that automatic cleanup is
  forbidden.
- Full Go, race-sensitive database, frontend, build, and development-port
  verification remain required before the final commit.

## Non-goals

- No configuration flag.
- No retention cutoff for `codex_events`.
- No cleanup of other structured Codex tables.
- No schema migration and no historical cost recomputation.
- No automatic `VACUUM` or database-file compaction.
