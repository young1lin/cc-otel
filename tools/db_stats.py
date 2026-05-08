"""Quick DB stats: table sizes, row counts, and column samples."""
import os, sqlite3

DB = os.path.join(os.path.expanduser("~"), ".claude", "cc-otel", "cc-otel.db")

def main():
    print(f"DB: {DB}")
    print(f"Size: {os.path.getsize(DB) / 1024 / 1024 / 1024:.2f} GB\n")

    conn = sqlite3.connect(DB)
    c = conn.cursor()

    c.execute("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
    tables = [r[0] for r in c.fetchall()]

    for t in tables:
        c.execute(f"SELECT COUNT(*) FROM [{t}]")
        cnt = c.fetchone()[0]
        c.execute(f"PRAGMA table_info([{t}])")
        cols = [r[1] for r in c.fetchall()]
        print(f"--- {t} ---")
        print(f"  Rows: {cnt:,}   Columns: {', '.join(cols)}")
        if cnt > 0:
            c.execute(f"SELECT * FROM [{t}] LIMIT 1")
            row = c.fetchone()
            print(f"  Sample: {dict(zip(cols, row))}")
        print()

    conn.close()

if __name__ == "__main__":
    main()
