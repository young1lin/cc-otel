package db

import (
	"path/filepath"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
)

// The CC_OTEL_TEST_NOSYNC env lets CI matrices run the suite without fsync-ing
// throwaway temp databases: on CI Windows runners a single FlushFileBuffers can
// stall for minutes while Defender scans a fresh .db file, which blew go test's
// default 10m package timeout (v0.1.0 tag CI flake, 2026-08-16). Production
// never sets it, so its DSN stays the plain path.

func TestDSNWithoutNosyncEnvReturnsPlainPath(t *testing.T) {
	t.Setenv("CC_OTEL_TEST_NOSYNC", "")
	plain := filepath.Join("some", "dir", "test.db")
	if got := DSN(plain); got != plain {
		t.Fatalf("DSN(%q) = %q, want the plain path when env is unset", plain, got)
	}
	if got := DSN(":memory:"); got != ":memory:" {
		t.Fatalf("DSN(:memory:) = %q, want %q", got, ":memory:")
	}
}

func TestDSNWithNosyncEnvAppendsSynchronousOff(t *testing.T) {
	t.Setenv("CC_OTEL_TEST_NOSYNC", "1")
	if got := DSN(":memory:"); got != ":memory:" {
		t.Fatalf("DSN(:memory:) = %q, want %q even with env set", got, ":memory:")
	}
	got := DSN(filepath.Join("some", "dir", "test.db"))
	want := "file:some/dir/test.db?_pragma=synchronous(0)"
	if got != want {
		t.Fatalf("DSN() = %q, want %q", got, want)
	}
}

func TestInitWithNosyncEnvOpensConnectionsWithoutFsync(t *testing.T) {
	t.Setenv("CC_OTEL_TEST_NOSYNC", "1")
	d, err := Init(&config.Config{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Two round-trips so a second pooled connection is likely opened too —
	// the pragma must ride the DSN, not a one-off Exec on a single conn.
	for i := 0; i < 2; i++ {
		var mode int
		if err := d.QueryRow("PRAGMA synchronous").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != 0 {
			t.Fatalf("round %d: PRAGMA synchronous = %d, want 0 (OFF) on every connection", i+1, mode)
		}
	}
}
