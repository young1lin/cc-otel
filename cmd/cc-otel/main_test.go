package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

type legacyCodexEventsCleanerStub struct {
	deleted []int64
	errs    []error
	calls   int
}

func (s *legacyCodexEventsCleanerStub) CleanupLegacyCodexEventsBatch(context.Context, int) (int64, error) {
	i := s.calls
	s.calls++
	if i < len(s.errs) && s.errs[i] != nil {
		return 0, s.errs[i]
	}
	return s.deleted[i], nil
}

func TestCmdServeSchedulesLegacyCodexEventsCleanup(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "cmdServe" {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "startLegacyCodexEventsCleanup" {
				found = true
			}
			return true
		})
		if !found {
			t.Fatal("cmdServe must schedule legacy Codex events cleanup")
		}
		return
	}
	t.Fatal("cmdServe function not found")
}

func TestCleanupLegacyCodexEventsRunsUntilEmpty(t *testing.T) {
	cleaner := &legacyCodexEventsCleanerStub{deleted: []int64{10_000, 3, 0}}
	total, err := cleanupLegacyCodexEvents(context.Background(), cleaner)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if total != 10_003 || cleaner.calls != 3 {
		t.Fatalf("cleanup returned total=%d calls=%d, want total=10003 calls=3", total, cleaner.calls)
	}
}

func TestCleanupLegacyCodexEventsStopsOnError(t *testing.T) {
	wantErr := errors.New("database busy")
	cleaner := &legacyCodexEventsCleanerStub{
		deleted: []int64{7, 0},
		errs:    []error{nil, wantErr},
	}
	total, err := cleanupLegacyCodexEvents(context.Background(), cleaner)
	if !errors.Is(err, wantErr) {
		t.Fatalf("cleanup error = %v, want %v", err, wantErr)
	}
	if total != 7 || cleaner.calls != 2 {
		t.Fatalf("cleanup returned total=%d calls=%d, want total=7 calls=2", total, cleaner.calls)
	}
}

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1.0K"},
		{1500, "1.5K"},
		{999999, "1000.0K"},
		{1000000, "1.0M"},
		{1500000, "1.5M"},
		{12345678, "12.3M"},
	}
	for _, tt := range tests {
		got := formatTokens(tt.input)
		if got != tt.expected {
			t.Errorf("formatTokens(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
