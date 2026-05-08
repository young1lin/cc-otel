package main

import "testing"

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
