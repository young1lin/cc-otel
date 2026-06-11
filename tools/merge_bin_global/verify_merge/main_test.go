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

func TestContainmentSQLCoversAllCostCheckedTables(t *testing.T) {
	sqls := containmentSQL()
	for _, c := range simpleTableChecks() {
		if !c.CheckCost {
			continue
		}
		q, ok := sqls[c.Name]
		if !ok {
			t.Fatalf("containmentSQL() missing %s: cost-checked request tables need per-row containment", c.Name)
		}
		if !strings.Contains(q, "NOT EXISTS") || !strings.Contains(q, "src."+c.Name) {
			t.Fatalf("containmentSQL(%s) malformed: %q", c.Name, q)
		}
		if strings.Contains(q, "cost_usd") {
			t.Fatalf("containmentSQL(%s) must not match on cost_usd (recompute_cost rewrites it)", c.Name)
		}
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
			// Cost divergence is warn-only: recompute_cost can reprice one
			// side without any rows being lost.
			name: "global lower cost but counts ok",
			bin:  tableStats{Count: 10, CostUnits: 100},
			glob: tableStats{Count: 12, CostUnits: 90},
			want: true,
		},
		{
			name: "stat error fails",
			bin:  tableStats{Count: 10},
			glob: tableStats{Count: 12, Err: "boom"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containedAtLeast(tt.bin, tt.glob)
			if got != tt.want {
				t.Fatalf("containedAtLeast() = %v, want %v", got, tt.want)
			}
		})
	}
}
