"""
cc-otel database cleanup script.

Usage:
    python tools/cleanup_db.py               # Clean bin/cc-otel.db (dev)
    python tools/cleanup_db.py --production  # Clean ~/.claude/cc-otel/cc-otel.db (requires service stop)
    python tools/cleanup_db.py --db PATH     # Clean a specific DB file
    python tools/cleanup_db.py --dry-run     # Show what would be deleted without modifying

Removes:
    - raw_otlp_events, codex_raw_otlp_events tables (full OTLP JSON, already parsed into structured tables)
    - otel_metric_points table (redundant with api_requests data, never used by dashboard)
    - import_ledger table (merge dedup records, only needed during cross-machine import)
    - codex.websocket_event rows from codex_events (real-time data consumed by tracker, stale after 10min)

After cleanup, runs VACUUM to reclaim disk space.
"""

import argparse
import os
import sqlite3
import sys
import time
from datetime import datetime


def human_size(n_bytes):
    for unit in ["B", "KB", "MB", "GB"]:
        if n_bytes < 1024:
            return f"{n_bytes:.1f} {unit}"
        n_bytes /= 1024
    return f"{n_bytes:.1f} TB"


def get_db_size(path):
    return os.path.getsize(path)


def print_header(msg):
    print(f"\n{'='*60}")
    print(f"  {msg}")
    print(f"{'='*60}")


def cleanup_db(db_path, dry_run=False):
    if not os.path.exists(db_path):
        print(f"ERROR: DB not found: {db_path}")
        sys.exit(1)

    before = get_db_size(db_path)
    print(f"DB: {db_path}")
    print(f"Size before: {human_size(before)}")

    conn = sqlite3.connect(db_path)
    c = conn.cursor()

    # Verify integrity
    c.execute("PRAGMA integrity_check(1)")
    result = c.fetchone()[0]
    if result != "ok":
        print(f"ERROR: DB integrity check failed: {result}")
        conn.close()
        sys.exit(1)
    print("Integrity: ok")

    total_deleted = 0

    # 1. Drop raw_otlp_events
    print_header("1. Drop raw_otlp_events")
    for tbl in ["raw_otlp_events", "codex_raw_otlp_events"]:
        c.execute(f"SELECT COUNT(*) FROM [{tbl}]")
        cnt = c.fetchone()[0]
        if cnt > 0:
            print(f"  {tbl}: {cnt:,} rows")
        if not dry_run:
            c.execute(f"DROP TABLE IF EXISTS [{tbl}]")
            print(f"  {tbl}: DROPPED")
        else:
            print(f"  {tbl}: would DROP")

    # 2. Drop otel_metric_points
    print_header("2. Drop otel_metric_points")
    c.execute("SELECT COUNT(*) FROM otel_metric_points")
    cnt = c.fetchone()[0]
    print(f"  otel_metric_points: {cnt:,} rows")
    if not dry_run:
        c.execute("DROP TABLE IF EXISTS otel_metric_points")
        print(f"  otel_metric_points: DROPPED")
    else:
        print(f"  otel_metric_points: would DROP")

    # 3. Drop import_ledger
    print_header("3. Drop import_ledger")
    c.execute("SELECT COUNT(*) FROM import_ledger")
    cnt = c.fetchone()[0]
    print(f"  import_ledger: {cnt:,} rows")
    if not dry_run:
        c.execute("DROP TABLE IF EXISTS import_ledger")
        print(f"  import_ledger: DROPPED")
    else:
        print(f"  import_ledger: would DROP")

    # 4. Clean stale codex.websocket_event (>10 min old)
    print_header("4. Clean stale codex.websocket_event")
    cutoff = int(time.time()) - 600
    c.execute(
        "SELECT COUNT(*) FROM codex_events WHERE event_name = 'codex.websocket_event' AND timestamp < ?",
        (cutoff,),
    )
    cnt = c.fetchone()[0]
    print(f"  codex.websocket_event older than 10min: {cnt:,} rows")
    if not dry_run and cnt > 0:
        c.execute(
            "DELETE FROM codex_events WHERE event_name = 'codex.websocket_event' AND timestamp < ?",
            (cutoff,),
        )
        print(f"  deleted {c.rowcount:,} rows")

    # 5. Clean processed pending_ttft_spans
    print_header("5. Clean processed pending_ttft_spans")
    c.execute("SELECT COUNT(*) FROM pending_ttft_spans WHERE processed = 1")
    cnt = c.fetchone()[0]
    print(f"  processed spans: {cnt:,} rows")
    if not dry_run and cnt > 0:
        c.execute("DELETE FROM pending_ttft_spans WHERE processed = 1")
        print(f"  deleted {c.rowcount:,} rows")

    # 6. Remove orphaned indexes
    print_header("6. Remove orphaned indexes")
    dropped_indexes = [
        "idx_raw_otlp_time",
        "idx_codex_raw_time",
        "idx_metric_points_time",
        "idx_metric_points_name",
        "idx_metric_points_user",
        "idx_import_ledger_table_time",
    ]
    for idx in dropped_indexes:
        if not dry_run:
            c.execute(f"DROP INDEX IF EXISTS [{idx}]")
        else:
            print(f"  would drop index {idx}")

    if dry_run:
        conn.close()
        print(f"\n[dry-run] Size: {human_size(before)} (no changes made)")
        return

    conn.commit()

    # VACUUM
    print_header("7. VACUUM")
    print("  Running VACUUM (may take a few minutes)...")
    t0 = time.time()
    conn.execute("VACUUM")
    conn.close()
    elapsed = time.time() - t0
    print(f"  VACUUM done in {elapsed:.0f}s")

    after = get_db_size(db_path)
    saved = before - after
    print(f"\nSize after:  {human_size(after)}")
    print(f"Saved:       {human_size(saved)} ({saved/before*100:.1f}% reduction)")


def main():
    parser = argparse.ArgumentParser(description="cc-otel database cleanup")
    parser.add_argument("--production", action="store_true", help="Clean production DB (~/.claude/cc-otel/cc-otel.db)")
    parser.add_argument("--db", type=str, help="Path to a specific DB file")
    parser.add_argument("--dry-run", action="store_true", help="Show what would be deleted without modifying")
    args = parser.parse_args()

    if args.db:
        db_path = args.db
    elif args.production:
        db_path = os.path.join(os.path.expanduser("~"), ".claude", "cc-otel", "cc-otel.db")
    else:
        db_path = os.path.join(os.path.dirname(os.path.dirname(__file__)), "bin", "cc-otel.db")

    if args.production and not args.dry_run:
        print("WARNING: This will modify the PRODUCTION database.")
        print("Make sure cc-otel is stopped before proceeding.")
        resp = input("Continue? [y/N] ")
        if resp.lower() != "y":
            print("Aborted.")
            return

    cleanup_db(db_path, dry_run=args.dry_run)


if __name__ == "__main__":
    main()
