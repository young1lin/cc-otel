package db

import (
	"os"
	"path/filepath"
	"strings"
)

// DSN builds the go-sqlite3 driver DSN for a database path.
//
// With CC_OTEL_TEST_NOSYNC set (CI test matrices), synchronous=OFF is applied
// per connection via the driver's _pragma DSN parameter — test databases are
// throwaway temp files, and on CI Windows runners a single FlushFileBuffers can
// stall for minutes while Defender scans a fresh .db file, which blew go
// test's default 10m package timeout (v0.1.0 tag CI flake, 2026-08-16).
// The pragma must ride the DSN so it is in effect during Init's migrations,
// which run before Init's own PRAGMA statements.
//
// Production also carries its per-connection pragmas on the DSN: the pool
// opens up to 4 connections and a PRAGMA issued on one pooled conn never
// reaches the others, so settings had to be re-run per conn (the ncruces
// driver defaults busy_timeout to 60s on bare-path DSNs, and Init's one-shot
// "PRAGMA busy_timeout=5000" actually LOWERED it on whichever conn ran it).
// Riding the DSN makes every conn identical from its first statement —
// including during Init's migrations, which run before any Init PRAGMA.
// journal_mode and synchronous(NORMAL) are set once in Init: the first is a
// database-level property, and NORMAL must not stomp the test DSN's OFF.
// :memory: always passes through untouched — its lifetime is the connection,
// not the disk, and URI-form DSNs would break the single-shared-db assumption
// of in-memory tests.
func DSN(path string) string {
	if path == ":memory:" {
		return path
	}
	u := "file:" + filepath.ToSlash(filepath.Clean(path))
	pragmas := []string{
		"busy_timeout(5000)",
		"cache_size(-65536)",
		"mmap_size(268435456)",
		// temp_store=FILE (R4 review A3-F2): TEMP b-trees and dbmerge staging
		// tables would otherwise live in the wazero linear heap, which never
		// returns memory to the OS — every large migration permanently raised
		// the RSS floor. On-disk temp keeps the spike out of the process.
		"temp_store(FILE)",
		// WAL high-water control (R3 perf finding): neither PASSIVE
		// checkpoints nor autocheckpoint ever shrink the -wal file; after a
		// large migration it stayed frozen at 340MB forever. The limit lets a
		// RESET-ing checkpoint truncate the file back down.
		"journal_size_limit(67108864)",
	}
	if nosyncEnabled() {
		pragmas = append(pragmas, "synchronous(0)")
	}
	return u + "?_pragma=" + strings.Join(pragmas, "&_pragma=")
}

// nosyncEnabled is the single switch DSN and Init consult, so the DSN pragma
// and Init's own synchronous setting can never disagree.
func nosyncEnabled() bool {
	return os.Getenv("CC_OTEL_TEST_NOSYNC") != ""
}
