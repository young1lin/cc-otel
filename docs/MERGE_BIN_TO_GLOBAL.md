# Merge bin DB into global DB (insert-only)

Goal: merge a time window of rows from:

- **Source (read-only)**: `D:\goProject\cc-otel\bin\cc-otel.db`
- **Destination (insert-only)**: `C:\Users\Administrator\.claude\cc-otel\cc-otel.db`

Time window (your case):

- From: **UTC+8** `2026-04-23 00:00:00`
- To: **now**

This uses two Go tools:

- `tools/merge_bin_global/export_bin/main.go` (export JSONL)
- `tools/merge_bin_global/import_global/main.go` (import JSONL + ledger + daily agg delta)

## 0) Stop both cc-otel instances (PowerShell)

Stop **global**:

```powershell
Set-Location "C:\Users\Administrator\.claude\cc-otel"
cc-otel stop
```

Stop **bin**:

```powershell
Set-Location "D:\goProject\cc-otel\bin"
cc-otel stop
```

If you started `bin\cc-otel.exe serve` manually, make sure that process is stopped as well.

## 1) Backup the global DB (PowerShell)

```powershell
$globalDb = "C:\Users\Administrator\.claude\cc-otel\cc-otel.db"
$backup   = "C:\Users\Administrator\.claude\cc-otel\cc-otel.db.bak-" + (Get-Date -Format "yyyyMMdd-HHmmss")
Copy-Item -LiteralPath $globalDb -Destination $backup -Force
$backup
```

## 2) Export from bin DB to JSONL (PowerShell)

```powershell
Set-Location "D:\goProject\cc-otel"

$src = "D:\goProject\cc-otel\bin\cc-otel.db"
$out = "D:\goProject\cc-otel\merge-bin-20260423-to-now.jsonl"
$from = "2026-04-23T00:00:00+08:00"

go run .\tools\merge_bin_global\export_bin\main.go -src $src -out $out -from $from
```

## 3) Import into global DB (insert-only) + update `daily_model_agg` (PowerShell)

This will create (if missing) a new table in global DB:

- `import_ledger(uuid TEXT PRIMARY KEY, imported_at INTEGER, source_db TEXT, table_name TEXT)`

```powershell
Set-Location "D:\goProject\cc-otel"

$dst = "C:\Users\Administrator\.claude\cc-otel\cc-otel.db"
$in  = "D:\goProject\cc-otel\merge-bin-20260423-to-now.jsonl"

go run .\tools\merge_bin_global\import_global\main.go -dst $dst -in $in -source "bin-14317" -batch 1000
```

Re-running the same import is safe (idempotent) because:

- `api_requests` also uses `INSERT OR IGNORE` on `request_id` unique index (when `request_id != ''`)
- all tables are guarded by `import_ledger` with stable UUIDs from export

## 4) Quick verification (PowerShell)

If you have `sqlite3.exe` available, you can sanity-check counts/cost in the target window:

```powershell
$dst = "C:\Users\Administrator\.claude\cc-otel\cc-otel.db"
$fromUnix = [int][DateTimeOffset]::Parse("2026-04-23T00:00:00+08:00").ToUnixTimeSeconds()
$toUnix = [int][DateTimeOffset]::Now.ToUnixTimeSeconds()

sqlite3 $dst "SELECT COUNT(*) AS api_requests_rows, SUM(cost_usd) AS cost_units FROM api_requests WHERE timestamp BETWEEN $fromUnix AND $toUnix;"
sqlite3 $dst "SELECT COUNT(*) AS ledger_rows FROM import_ledger WHERE imported_at BETWEEN $fromUnix AND $toUnix;"
```

## 5) Rollback (if needed)

If the result is not what you expect, restore the backup file you made in step (1):

```powershell
$globalDir = "C:\Users\Administrator\.claude\cc-otel"
Set-Location $globalDir

# pick the right backup file manually, then:
Copy-Item -LiteralPath ".\cc-otel.db.bak-YYYYMMDD-HHMMSS" -Destination ".\cc-otel.db" -Force
```

