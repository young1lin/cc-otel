package db

import (
	"context"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
)

func newIntradayBucketRepo(t *testing.T) *Repository {
	t.Helper()
	d, err := Init(&config.Config{DBPath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return NewRepository(d)
}

func TestGetIntradayStatsByModel_BucketWhitelist(t *testing.T) {
	repo := newIntradayBucketRepo(t)
	ctx := context.Background()

	for _, n := range []int{5, 10, 15, 30, 60} {
		if _, err := repo.GetIntradayStatsByModel(ctx, "2026-01-01", "2026-01-01", n, ""); err != nil {
			t.Errorf("bucket %d should be accepted, got error: %v", n, err)
		}
	}
	for _, n := range []int{0, 7, 45, -5} {
		if _, err := repo.GetIntradayStatsByModel(ctx, "2026-01-01", "2026-01-01", n, ""); err == nil {
			t.Errorf("bucket %d should be rejected, got nil error", n)
		}
	}
}

func TestGetCodexIntradayStatsByModel_BucketWhitelist(t *testing.T) {
	repo := newIntradayBucketRepo(t)
	ctx := context.Background()

	for _, n := range []int{5, 10, 15, 30, 60} {
		if _, err := repo.GetCodexIntradayStatsByModel(ctx, "2026-01-01", "2026-01-01", n, ""); err != nil {
			t.Errorf("codex bucket %d should be accepted, got error: %v", n, err)
		}
	}
	for _, n := range []int{0, 7, 45, -5} {
		if _, err := repo.GetCodexIntradayStatsByModel(ctx, "2026-01-01", "2026-01-01", n, ""); err == nil {
			t.Errorf("codex bucket %d should be rejected, got nil error", n)
		}
	}
}
