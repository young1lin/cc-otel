package main

import (
	"strings"
	"testing"
)

func TestSimpleTableChecksIncludeGeminiWithCost(t *testing.T) {
	var found *tableCheck
	for i, c := range simpleTableChecks() {
		if c.Name == "gemini_api_requests" {
			found = &simpleTableChecks()[i]
			break
		}
	}
	if found == nil {
		t.Fatal("simpleTableChecks() must include gemini_api_requests so merges verify Gemini carried over")
	}
	if !found.CheckCost {
		t.Fatal("gemini_api_requests check should compare cost (CheckCost=true)")
	}
}

func TestCountQueryEmitsCostSumOnlyWhenChecked(t *testing.T) {
	withCost := countQuery("gemini_api_requests", true)
	if !strings.Contains(withCost, "SUM(cost_usd)") || !strings.Contains(withCost, "FROM gemini_api_requests") {
		t.Fatalf("countQuery(checkCost=true) missing cost sum or table: %q", withCost)
	}
	noCost := countQuery("user_prompt_events", false)
	if strings.Contains(noCost, "SUM(cost_usd)") {
		t.Fatalf("countQuery(checkCost=false) should not sum cost: %q", noCost)
	}
}

func TestContainedAtLeast(t *testing.T) {
	tests := []struct {
		name string
		bin  tableStats
		glob tableStats
		want bool
	}{
		{
			name: "global has more rows and cost",
			bin:  tableStats{Count: 10, CostUnits: 100},
			glob: tableStats{Count: 12, CostUnits: 110},
			want: true,
		},
		{
			name: "global missing rows",
			bin:  tableStats{Count: 10, CostUnits: 100},
			glob: tableStats{Count: 9, CostUnits: 110},
			want: false,
		},
		{
			name: "global missing cost",
			bin:  tableStats{Count: 10, CostUnits: 100},
			glob: tableStats{Count: 12, CostUnits: 90},
			want: false,
		},
		{
			name: "cost check can be disabled",
			bin:  tableStats{Count: 10, CostUnits: 100},
			glob: tableStats{Count: 12, CostUnits: 0},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkCost := tt.name != "cost check can be disabled"
			got := containedAtLeast(tt.bin, tt.glob, checkCost)
			if got != tt.want {
				t.Fatalf("containedAtLeast() = %v, want %v", got, tt.want)
			}
		})
	}
}
