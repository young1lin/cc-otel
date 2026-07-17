package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDaemonDoesNotScheduleCodexEventsDeletion(t *testing.T) {
	packages, err := parser.ParseDir(token.NewFileSet(), ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && selector.Sel.Name == "CleanupCodexWebsocketEvents" {
					t.Fatal("daemon must not automatically delete compatibility codex_events rows")
				}
				return true
			})
		}
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
