package db

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
)

// The CC_OTEL_TEST_NOSYNC env lets CI matrices run the suite without fsync-ing
// throwaway temp databases: on CI Windows runners a single FlushFileBuffers can
// stall for minutes while Defender scans a fresh .db file, which blew go test's
// default 10m package timeout (v0.1.0 tag CI flake, 2026-08-16). With the env
// set, DSN appends synchronous(0) after the pool pragmas; without it, DSN
// carries the pool pragmas only and Init sets synchronous=NORMAL itself.

func TestDSNWithoutNosyncEnvCarriesPoolPragmas(t *testing.T) {
	t.Setenv("CC_OTEL_TEST_NOSYNC", "")
	if got := DSN(":memory:"); got != ":memory:" {
		t.Fatalf("DSN(:memory:) = %q, want %q", got, ":memory:")
	}
	got := DSN(filepath.Join("some", "dir", "test.db"))
	want := "file:some/dir/test.db" +
		"?_pragma=busy_timeout(5000)&_pragma=cache_size(-65536)" +
		"&_pragma=mmap_size(268435456)&_pragma=temp_store(MEMORY)"
	if got != want {
		t.Fatalf("DSN() = %q, want %q", got, want)
	}
	if strings.Contains(got, "synchronous") {
		t.Fatalf("DSN() must not pin synchronous when the nosync env is unset: %q", got)
	}
}

func TestDSNWithNosyncEnvAppendsSynchronousOff(t *testing.T) {
	t.Setenv("CC_OTEL_TEST_NOSYNC", "1")
	if got := DSN(":memory:"); got != ":memory:" {
		t.Fatalf("DSN(:memory:) = %q, want %q even with env set", got, ":memory:")
	}
	got := DSN(filepath.Join("some", "dir", "test.db"))
	want := "file:some/dir/test.db" +
		"?_pragma=busy_timeout(5000)&_pragma=cache_size(-65536)" +
		"&_pragma=mmap_size(268435456)&_pragma=temp_store(MEMORY)" +
		"&_pragma=synchronous(0)"
	if got != want {
		t.Fatalf("DSN() = %q, want %q", got, want)
	}
}

// TestInitPoolPragmasCoverEveryConnection pins the R3 error-recovery fix:
// per-connection pragmas must reach EVERY pooled connection, not just the one
// that happened to execute Init's one-off statements (the ncruces driver
// defaults busy_timeout to 60s on bare-path DSNs — Init used to lower it on
// one conn while the rest kept 60s).
func TestInitPoolPragmasCoverEveryConnection(t *testing.T) {
	d, err := Init(&config.Config{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var wg sync.WaitGroup
	res := make(chan int64, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var timeout int64
			if err := d.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
				res <- -1
				return
			}
			res <- timeout
		}()
	}
	wg.Wait()
	close(res)
	for got := range res {
		if got != 5000 {
			t.Fatalf("PRAGMA busy_timeout = %d on a pooled conn, want 5000 on every connection", got)
		}
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
