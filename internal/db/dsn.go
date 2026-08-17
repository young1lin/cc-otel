package db

import (
	"os"
	"path/filepath"
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
// Production never sets the env, so its DSN is the plain path unchanged.
// :memory: always passes through untouched — its lifetime is the connection,
// not the disk, and URI-form DSNs would break the single-shared-db assumption
// of in-memory tests.
func DSN(path string) string {
	if path == ":memory:" || !nosyncEnabled() {
		return path
	}
	return "file:" + filepath.ToSlash(filepath.Clean(path)) + "?_pragma=synchronous(0)"
}

// nosyncEnabled is the single switch DSN and Init consult, so the DSN pragma
// and Init's own synchronous setting can never disagree.
func nosyncEnabled() bool {
	return os.Getenv("CC_OTEL_TEST_NOSYNC") != ""
}
