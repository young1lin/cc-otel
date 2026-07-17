package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/young1lin/cc-otel/internal/dbmerge"
)

func TestCountQueryEmitsCostSumOnlyWhenChecked(t *testing.T) {
	withCost := countQuery("codex_api_requests", true)
	if !strings.Contains(withCost, "SUM(cost_usd)") || !strings.Contains(withCost, "FROM codex_api_requests") {
		t.Fatalf("countQuery(checkCost=true) missing cost sum or table: %q", withCost)
	}
	noCost := countQuery("user_prompt_events", false)
	if strings.Contains(noCost, "SUM(cost_usd)") {
		t.Fatalf("countQuery(checkCost=false) should not sum cost: %q", noCost)
	}
}

func TestTableChecksCoverSharedImportRegistry(t *testing.T) {
	var got []string
	for _, check := range simpleTableChecks() {
		got = append(got, check.Name)
	}
	for _, spec := range dbmerge.ImportSpecs() {
		if spec.Name == "pending_ttft_spans" {
			continue
		}
		if !slices.Contains(got, spec.Name) {
			t.Fatalf("shared import table %s is missing from CLI diagnostics", spec.Name)
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
