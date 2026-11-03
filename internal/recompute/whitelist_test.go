package recompute

import (
	"context"
	"strings"
	"testing"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/pricing"
)

// R3 review A1-F3: the table whitelist inside Run is the fail-closed gate —
// pin it with a test so a refactor cannot drop it silently.
func TestRun_RejectsUnknownTable(t *testing.T) {
	testDB := newDB(t)
	reg, err := pricing.NewRegistry(context.Background(), testDB, &config.Config{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	_, err = Run(context.Background(), testDB, reg, Options{Tables: []string{"sessions"}}, true, nil)
	if err == nil {
		t.Fatal("Run must reject table names outside the closed whitelist")
	}
	if got := err.Error(); !strings.Contains(got, "not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
